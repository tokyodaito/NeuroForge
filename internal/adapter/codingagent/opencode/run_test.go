package opencode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/fake"
	"neuroforge/internal/adapter/codingagent/protocol"
)

func waitTerminal(t *testing.T, s *codingagent.SliceSink, timeout time.Duration) []protocol.NormalizedEvent {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		evs := s.Events()
		if len(evs) > 0 && evs[len(evs)-1].Type.IsTerminal() {
			return evs
		}
		time.Sleep(3 * time.Millisecond)
	}
	return s.Events()
}

func lastType(evs []protocol.NormalizedEvent) protocol.EventType {
	if len(evs) == 0 {
		return ""
	}
	return evs[len(evs)-1].Type
}

func startRun(t *testing.T, a *Adapter, req protocol.AgentRunRequest) (*codingagent.SliceSink, protocol.RunHandle) {
	t.Helper()
	sink := &codingagent.SliceSink{}
	h, err := a.Start(context.Background(), req, sink)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return sink, h
}

func baseReq() protocol.AgentRunRequest {
	return protocol.AgentRunRequest{RunID: "r1", Engine: "opencode", Model: "p/m", Workspace: os.TempDir(), Prompt: "hi"}
}

func TestRunSuccessOrderingAndCompletion(t *testing.T) {
	stream, stderr, code, hang := scenarioStream(scenarioSuccess())
	a := stubAdapter(stream, stderr, code, hang, t.TempDir())
	sink, _ := startRun(t, a, baseReq())
	evs := waitTerminal(t, sink, 3*time.Second)
	if len(evs) == 0 || evs[0].Type != protocol.EventRunStarted {
		t.Fatalf("first event = %v, want run.started", typesOf(evs))
	}
	if lastType(evs) != protocol.EventRunCompleted {
		t.Fatalf("last = %s, want run.completed", lastType(evs))
	}
	// Usage event should be present and normalised (no EXACT over-claim).
	hasUsage := false
	for _, e := range evs {
		if e.Type == protocol.EventUsageUpdated && e.Usage != nil {
			hasUsage = true
			if e.Usage.Confidence == protocol.QuotaConfExact {
				t.Error("usage over-claimed EXACT")
			}
		}
	}
	if !hasUsage {
		t.Error("no usage event forwarded")
	}
}

func TestRunMalformedSavedAndWarned(t *testing.T) {
	artDir := t.TempDir()
	stream, stderr, code, hang := scenarioStream(scenarioMalformed())
	a := stubAdapter(stream, stderr, code, hang, artDir)
	sink, _ := startRun(t, a, baseReq())
	evs := waitTerminal(t, sink, 3*time.Second)
	if lastType(evs) != protocol.EventRunCompleted {
		t.Fatalf("malformed broke the run; last = %s", lastType(evs))
	}
	hasWarn := false
	for _, e := range evs {
		if e.Type == protocol.EventWarning {
			hasWarn = true
		}
	}
	if !hasWarn {
		t.Error("no warning emitted for malformed line")
	}
	entries, _ := os.ReadDir(artDir)
	if len(entries) == 0 {
		t.Error("no malformed artifact saved")
	}
}

func TestRunMalformedArtifactRedacted(t *testing.T) {
	artDir := t.TempDir()
	// A malformed JSON line containing a credential fragment.
	secret := "token=sk-1234567890abcdefghij"
	mal := `{"type":"weird","data":"` + secret + `"` // missing closing brace
	stream := mal + "\n" + jline(`{"type":"run.completed"}`)
	a := stubAdapter(stream, "", 0, false, artDir)
	sink, _ := startRun(t, a, baseReq())
	evs := waitTerminal(t, sink, 3*time.Second)
	// The forwarded warning event must not leak the secret via Raw.
	for _, e := range evs {
		if e.Type == protocol.EventWarning && len(e.Raw) > 0 {
			if strings.Contains(string(e.Raw), secret) {
				t.Errorf("warning event Raw leaked secret: %q", e.Raw)
			}
		}
	}
	// The persisted artifact must not contain the secret.
	_ = filepath.Walk(artDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), secret) {
			t.Errorf("artifact %s leaked secret", path)
		}
		return nil
	})
}

func TestRunCrashClassifiesEngineCrash(t *testing.T) {
	stream, stderr, code, hang := scenarioStream(scenarioCrash())
	a := stubAdapter(stream, stderr, code, hang, t.TempDir())
	sink, _ := startRun(t, a, baseReq())
	evs := waitTerminal(t, sink, 3*time.Second)
	if lastType(evs) != protocol.EventRunFailed {
		t.Fatalf("last = %s, want run.failed", lastType(evs))
	}
	last := evs[len(evs)-1]
	if last.Failure == nil {
		t.Fatal("missing failure payload")
	}
	if last.Failure.Class != protocol.FailureEngineCrash {
		t.Errorf("class = %s, want ENGINE_CRASH", last.Failure.Class)
	}
}

func TestRunPartialOutputSynthesizesFailure(t *testing.T) {
	// Stream emits run.started-equivalent deltas then exits non-zero with NO
	// terminal event (partial output / abrupt exit).
	stream := jline(`{"type":"message.delta","message":{"delta":"partial..."}}`)
	a := stubAdapter(stream, "", 1, false, t.TempDir())
	sink, _ := startRun(t, a, baseReq())
	evs := waitTerminal(t, sink, 3*time.Second)
	if lastType(evs) != protocol.EventRunFailed {
		t.Fatalf("partial output should synthesize run.failed; last = %s", lastType(evs))
	}
}

func TestRunCancellationKillsGroup(t *testing.T) {
	stream, stderr, code, hang := scenarioStream(scenarioCancellation())
	a := stubAdapter(stream, stderr, code, hang, t.TempDir())
	sink, h := startRun(t, a, baseReq())
	// Wait for the run to reach its hang point (run.started forwarded).
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if len(sink.Events()) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := a.Cancel(context.Background(), h); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	evs := waitTerminal(t, sink, 3*time.Second)
	if lastType(evs) != protocol.EventRunCancelled {
		t.Fatalf("last = %s, want run.cancelled", lastType(evs))
	}
	// The run must be untracked (process group terminated).
	a.mu.Lock()
	_, stillTracked := a.runs[h.RunID]
	a.mu.Unlock()
	if stillTracked {
		t.Error("run still tracked after cancel (process group not cleaned up)")
	}
}

func TestRunTimeoutFiresAndTerminates(t *testing.T) {
	stream, stderr, code, hang := scenarioStream(scenarioTimeout())
	a := stubAdapter(stream, stderr, code, hang, t.TempDir())
	req := baseReq()
	req.Timeout = 100 * time.Millisecond
	sink, _ := startRun(t, a, req)
	evs := waitTerminal(t, sink, 3*time.Second)
	if lastType(evs) != protocol.EventRunFailed {
		t.Fatalf("timeout should synthesize run.failed; last = %s", lastType(evs))
	}
	last := evs[len(evs)-1]
	if last.Failure == nil || last.Failure.Class != protocol.FailureTimeout {
		t.Errorf("expected TIMEOUT class, got %+v", last.Failure)
	}
}

func TestRunResumeEmitsRunResumedFirst(t *testing.T) {
	stream, stderr, code, hang := scenarioStream(scenarioResume())
	a := stubAdapter(stream, stderr, code, hang, t.TempDir())
	sink := &codingagent.SliceSink{}
	_, err := a.Resume(context.Background(), protocol.ResumeRequest{
		RunID: "r2", Engine: "opencode", Model: "p/m", Workspace: os.TempDir(),
		SessionID: "sess-1",
	}, sink)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	evs := waitTerminal(t, sink, 3*time.Second)
	if len(evs) == 0 || evs[0].Type != protocol.EventRunResumed {
		t.Fatalf("first event = %v, want run.resumed", typesOf(evs))
	}
	if !evs[len(evs)-1].Type.IsTerminal() {
		t.Fatalf("resume did not reach terminal: %v", typesOf(evs))
	}
}

func TestRunResumeUnsupportedWhenVersionTooLow(t *testing.T) {
	a := New(Options{Binary: "/fake/opencode"})
	a.lookPath = func(string) (string, error) { return "/fake/opencode", nil }
	a.runProbe = func(context.Context, string) (string, string, error) { return "", "boom", errors.New("no version") }
	// Capabilities for unknown version => SessionResume false.
	if _, err := a.Resume(context.Background(), protocol.ResumeRequest{SessionID: "s"}, &codingagent.SliceSink{}); err == nil {
		t.Fatal("Resume should fail when SessionResume unsupported (§36.25)")
	}
}

func TestRunStderrRedactedInSynthesizedFailure(t *testing.T) {
	secret := "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9abcdef"
	stream := "" // process exits immediately, no terminal event
	a := stubAdapter(stream, "auth error: "+secret+"\n", 2, false, t.TempDir())
	sink, _ := startRun(t, a, baseReq())
	evs := waitTerminal(t, sink, 3*time.Second)
	if lastType(evs) != protocol.EventRunFailed {
		t.Fatalf("last = %s", lastType(evs))
	}
	for _, e := range evs {
		if e.Failure != nil && strings.Contains(e.Failure.Reason, secret) {
			t.Errorf("synthesized failure reason leaked secret: %q", e.Failure.Reason)
		}
	}
}

func TestRunConcurrent(t *testing.T) {
	stream, stderr, code, hang := scenarioStream(scenarioSuccess())
	a := stubAdapter(stream, stderr, code, hang, t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sink := &codingagent.SliceSink{}
			req := baseReq()
			req.RunID = "concurrent"
			if _, err := a.Start(context.Background(), req, sink); err != nil {
				t.Errorf("Start %d: %v", i, err)
				return
			}
			waitTerminal(t, sink, 4*time.Second)
		}(i)
	}
	wg.Wait()
}

func TestCancelUnknownRunErrors(t *testing.T) {
	a := stubAdapter("", "", 0, false, "")
	if err := a.Cancel(context.Background(), protocol.RunHandle{RunID: "nope"}); err == nil {
		t.Error("cancelling unknown run should error")
	}
}

func TestSendMessageUnsupported(t *testing.T) {
	a := stubAdapter("", "", 0, false, "")
	if err := a.SendMessage(context.Background(), protocol.RunHandle{}, protocol.AgentMessage{Text: "x"}); err == nil {
		t.Error("SendMessage should be unsupported in headless mode")
	}
}

func TestHealthStates(t *testing.T) {
	t.Run("installed+version => ok", func(t *testing.T) {
		a := stubAdapter("", "", 0, false, "")
		if a.Health(context.Background(), protocol.Account{}).Status != protocol.HealthOK {
			t.Error("expected ok")
		}
	})
	t.Run("not installed => down", func(t *testing.T) {
		a := New(Options{})
		a.lookPath = func(string) (string, error) { return "", errors.New("nope") }
		if a.Health(context.Background(), protocol.Account{}).Status != protocol.HealthDown {
			t.Error("expected down")
		}
	})
}

func typesOf(evs []protocol.NormalizedEvent) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = string(e.Type)
	}
	return out
}

// scenario helpers return the fake.Scenario values used to build recorded
// fixtures via scenarioStream (runtest_test.go).
func scenarioSuccess() fake.Scenario      { return fake.ScenarioSuccess }
func scenarioMalformed() fake.Scenario    { return fake.ScenarioMalformedJSON }
func scenarioResume() fake.Scenario       { return fake.ScenarioResume }
func scenarioCancellation() fake.Scenario { return fake.ScenarioCancellation }
func scenarioTimeout() fake.Scenario      { return fake.ScenarioTimeout }
func scenarioCrash() fake.Scenario        { return fake.ScenarioCrash }
