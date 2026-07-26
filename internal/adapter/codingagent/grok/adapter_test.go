package grok

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/protocol"
)

// startCtx returns a run context that outlives waitForTerminal. Tests must not
// cancel Start via the same short deadline used to wait for events: expiry
// synthesizes run.cancelled and falsely fails success scenarios under
// full-suite load (process-start delay consumes the shared budget).
func startCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx
}

// runStub starts a run against the given scenario and returns the collected
// events once a terminal event arrives (or the timeout elapses).
func runStub(t *testing.T, scenario string, timeout time.Duration) (protocol.RunHandle, []protocol.NormalizedEvent) {
	t.Helper()
	a := New(stubOptions(t, scenario))
	sink := &codingagent.SliceSink{}
	ws := t.TempDir()
	handle, err := a.Start(startCtx(t), protocol.AgentRunRequest{
		RunID: "t-" + scenario, Engine: a.ID(), Model: "grok/coding", Workspace: ws, Prompt: "hi",
	}, sink)
	if err != nil {
		t.Fatalf("Start(%s): %v", scenario, err)
	}
	return handle, waitForTerminal(sink, timeout)
}

func waitForTerminal(s *codingagent.SliceSink, timeout time.Duration) []protocol.NormalizedEvent {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		evs := s.Events()
		if len(evs) > 0 && evs[len(evs)-1].Type.IsTerminal() {
			return evs
		}
		time.Sleep(5 * time.Millisecond)
	}
	return s.Events()
}

func lastType(evs []protocol.NormalizedEvent) protocol.EventType {
	if len(evs) == 0 {
		return ""
	}
	return evs[len(evs)-1].Type
}

func TestRunSuccessOrdering(t *testing.T) {
	_, evs := runStub(t, "success", 6*time.Second)
	if len(evs) == 0 {
		t.Fatal("no events")
	}
	if evs[0].Type != protocol.EventRunStarted {
		t.Errorf("first event = %s, want run.started", evs[0].Type)
	}
	if lastType(evs) != protocol.EventRunCompleted {
		t.Errorf("last = %s, want run.completed", lastType(evs))
	}
	// Should include the mapped message delta and a usage event.
	hasDelta, hasUsage := false, false
	for _, e := range evs {
		if e.Type == protocol.EventMessageDelta {
			hasDelta = true
		}
		if e.Type == protocol.EventUsageUpdated {
			hasUsage = true
		}
	}
	if !hasDelta {
		t.Error("no message.delta mapped from stub")
	}
	if !hasUsage {
		t.Error("no usage.updated mapped from stub")
	}
}

func TestRunMalformedDoesNotBreakAndSavesArtifact(t *testing.T) {
	a := New(Options{
		Binary:       stubBin(t),
		ArtifactsDir: t.TempDir(),
		ExtraEnv:     []string{"GROK_STUB_SCENARIO=malformed-json"},
	})
	sink := &codingagent.SliceSink{}
	if _, err := a.Start(startCtx(t), protocol.AgentRunRequest{RunID: "mal", Engine: a.ID(), Workspace: t.TempDir(), Prompt: "x"}, sink); err != nil {
		t.Fatal(err)
	}
	evs := waitForTerminal(sink, 6*time.Second)

	if lastType(evs) != protocol.EventRunCompleted {
		t.Errorf("last = %s, want run.completed (malformed must not break)", lastType(evs))
	}
	hasWarning := false
	for _, e := range evs {
		if e.Type == protocol.EventWarning && e.Warning != nil && strings.Contains(e.Warning.Code, "malformed") {
			hasWarning = true
		}
	}
	if !hasWarning {
		t.Error("no malformed warning emitted")
	}
	// The raw malformed line must have been saved as an artifact.
	entries, _ := os.ReadDir(a.opts.ArtifactsDir)
	if len(entries) == 0 {
		t.Errorf("no malformed artifact saved in %s", a.opts.ArtifactsDir)
	}
}

func TestRunUnknownEventEmitsWarningAndCompletes(t *testing.T) {
	_, evs := runStub(t, "unknown-event", 6*time.Second)
	if lastType(evs) != protocol.EventRunCompleted {
		t.Errorf("last = %s, want run.completed", lastType(evs))
	}
	hasWarn := false
	for _, e := range evs {
		if e.Type == protocol.EventWarning && e.Warning != nil && e.Warning.Code == "unknown-item-type" {
			hasWarn = true
		}
	}
	if !hasWarn {
		t.Error("unknown future item did not produce an unknown-item-type warning")
	}
}

func TestRunQuotaFailureClassified(t *testing.T) {
	a := New(stubOptions(t, "quota-before-edits"))
	sink := &codingagent.SliceSink{}
	if _, err := a.Start(startCtx(t), protocol.AgentRunRequest{RunID: "q", Engine: a.ID(), Workspace: t.TempDir(), Prompt: "x"}, sink); err != nil {
		t.Fatal(err)
	}
	evs := waitForTerminal(sink, 6*time.Second)
	if lastType(evs) != protocol.EventRunFailed {
		t.Fatalf("last = %s, want run.failed", lastType(evs))
	}
	last := evs[len(evs)-1]
	if last.Failure == nil {
		t.Fatal("missing failure payload")
	}
	if last.Failure.Class != protocol.FailureProviderQuota {
		t.Errorf("synthesized class = %s, want PROVIDER_QUOTA", last.Failure.Class)
	}
	// ClassifyFailure (events-only stderr) must be consistent.
	fc := a.ClassifyFailure(last.Failure.ExitCode, evs, "")
	if fc.Class != protocol.FailureProviderQuota {
		t.Errorf("ClassifyFailure = %s, want PROVIDER_QUOTA", fc.Class)
	}
	if !fc.Failover {
		t.Error("quota should suggest failover")
	}
}

func TestRunCrashClassifiedAsEngineCrash(t *testing.T) {
	a := New(stubOptions(t, "crash"))
	sink := &codingagent.SliceSink{}
	if _, err := a.Start(startCtx(t), protocol.AgentRunRequest{RunID: "c", Engine: a.ID(), Workspace: t.TempDir(), Prompt: "x"}, sink); err != nil {
		t.Fatal(err)
	}
	evs := waitForTerminal(sink, 6*time.Second)
	if lastType(evs) != protocol.EventRunFailed {
		t.Fatalf("last = %s, want run.failed", lastType(evs))
	}
	last := evs[len(evs)-1]
	if last.Failure == nil {
		t.Fatal("missing failure payload")
	}
	fc := a.ClassifyFailure(last.Failure.ExitCode, evs, "")
	if fc.Class != protocol.FailureEngineCrash {
		t.Errorf("class = %s, want ENGINE_CRASH", fc.Class)
	}
}

func TestRunPartialOutputSynthesizesFailure(t *testing.T) {
	_, evs := runStub(t, "partial-output", 6*time.Second)
	if lastType(evs) != protocol.EventRunFailed {
		t.Errorf("partial output last = %s, want run.failed (synthesized)", lastType(evs))
	}
}

func TestRunResumeEmitsRunResumed(t *testing.T) {
	a := New(stubOptions(t, "resume"))
	// Force the SessionResume capability on for the stub (its version is known
	// and the adapter's default gate already enables it, but be explicit).
	a.opts.ResumeEnabled = boolPtr(true)
	sink := &codingagent.SliceSink{}
	if _, err := a.Resume(startCtx(t), protocol.ResumeRequest{
		RunID: "r", Engine: a.ID(), Workspace: t.TempDir(), SessionID: "grok-session-1",
	}, sink); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	evs := waitForTerminal(sink, 6*time.Second)
	if len(evs) == 0 || evs[0].Type != protocol.EventRunResumed {
		t.Errorf("first event = %v, want run.resumed", firstType(evs))
	}
	if !lastType(evs).IsTerminal() {
		t.Errorf("resume did not reach a terminal event; last = %s", lastType(evs))
	}
}

func TestRunResumeRejectedWhenUnsupported(t *testing.T) {
	a := New(stubOptions(t, "resume"))
	a.opts.ResumeEnabled = boolPtr(false)
	sink := &codingagent.SliceSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	_, err := a.Resume(ctx, protocol.ResumeRequest{RunID: "r2", Engine: a.ID(), Workspace: t.TempDir(), SessionID: "x"}, sink)
	if err == nil {
		t.Error("Resume should fail when SessionResume is disabled")
	}
}

func TestRunCancellationKillsGroup(t *testing.T) {
	a := New(stubOptions(t, "cancellation"))
	sink := &codingagent.SliceSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	handle, err := a.Start(ctx, protocol.AgentRunRequest{RunID: "cancel", Engine: a.ID(), Workspace: t.TempDir(), Prompt: "x"}, sink)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // let the stub reach its hang point
	if err := a.Cancel(context.Background(), handle); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	evs := waitForTerminal(sink, 4*time.Second)
	if lastType(evs) != protocol.EventRunCancelled {
		t.Errorf("last = %s, want run.cancelled", lastType(evs))
	}
	// The process group must be cleaned up (run untracked).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		_, running := a.runs[handle.RunID]
		a.mu.Unlock()
		if !running {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	a.mu.Lock()
	_, stillRunning := a.runs[handle.RunID]
	a.mu.Unlock()
	if stillRunning {
		t.Error("run still tracked after cancel (process group not cleaned up)")
	}
}

func TestRunTimeoutEmitsFailedTimeout(t *testing.T) {
	a := New(stubOptions(t, "timeout"))
	sink := &codingagent.SliceSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := a.Start(ctx, protocol.AgentRunRequest{
		RunID: "to", Engine: a.ID(), Workspace: t.TempDir(), Prompt: "x", Timeout: 150 * time.Millisecond,
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	evs := waitForTerminal(sink, 4*time.Second)
	if lastType(evs) != protocol.EventRunFailed {
		t.Fatalf("last = %s, want run.failed (timeout)", lastType(evs))
	}
	last := evs[len(evs)-1]
	if last.Failure == nil || last.Failure.Class != protocol.FailureTimeout {
		t.Errorf("timeout class wrong: %+v", last.Failure)
	}
}

func TestRunSecretRedactionInFailureReason(t *testing.T) {
	// The auth-failure stub writes a fake key to stderr; the adapter must scrub
	// it from the failure reason.
	a := New(stubOptions(t, "auth-failure"))
	sink := &codingagent.SliceSink{}
	if _, err := a.Start(startCtx(t), protocol.AgentRunRequest{RunID: "auth", Engine: a.ID(), Workspace: t.TempDir(), Prompt: "x"}, sink); err != nil {
		t.Fatal(err)
	}
	evs := waitForTerminal(sink, 6*time.Second)
	last := evs[len(evs)-1]
	if last.Failure == nil {
		t.Fatal("missing failure payload")
	}
	if strings.Contains(last.Failure.Reason, "sk-abcd1234") {
		t.Errorf("secret leaked into failure reason: %q", last.Failure.Reason)
	}
}

func TestRunWindowsPathAndUnicodeWorkspace(t *testing.T) {
	// The workspace path with spaces/unicode must be passed to the child CWD
	// without error.
	a := New(stubOptions(t, "success"))
	ws := filepath.Join(t.TempDir(), "café ☕ dir")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	sink := &codingagent.SliceSink{}
	if _, err := a.Start(startCtx(t), protocol.AgentRunRequest{RunID: "ws", Engine: a.ID(), Workspace: ws, Prompt: "x"}, sink); err != nil {
		t.Fatalf("Start with unicode/spaces workspace: %v", err)
	}
	evs := waitForTerminal(sink, 6*time.Second)
	if lastType(evs) != protocol.EventRunCompleted {
		t.Errorf("last = %s, want run.completed", lastType(evs))
	}
}

func TestRunConcurrent(t *testing.T) {
	a := New(stubOptions(t, "success"))
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sink := &codingagent.SliceSink{}
			_, err := a.Start(startCtx(t), protocol.AgentRunRequest{RunID: "cc", Engine: a.ID(), Workspace: t.TempDir(), Prompt: "x"}, sink)
			if err != nil {
				t.Errorf("Start %d: %v", i, err)
				return
			}
			if lastType(waitForTerminal(sink, 6*time.Second)) != protocol.EventRunCompleted {
				t.Errorf("run %d did not complete", i)
			}
		}(i)
	}
	wg.Wait()
}

func TestInspectQuotaUnknown(t *testing.T) {
	a := New(stubOptions(t, "success"))
	q := a.InspectQuota(testContext(t), protocol.Account{})
	if q.Confidence != protocol.QuotaConfUnknown {
		t.Errorf("confidence = %s, want UNKNOWN", q.Confidence)
	}
}

func TestListModelsOpaqueNoRealNames(t *testing.T) {
	a := New(stubOptions(t, "success"))
	models, err := a.ListModels(testContext(t), protocol.Account{})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) == 0 {
		t.Fatal("no models")
	}
	for _, m := range models {
		if m.Engine != a.ID() {
			t.Errorf("model engine = %s", m.Engine)
		}
		low := strings.ToLower(m.ID)
		for _, bad := range []string{"grok-1", "grok-2", "grok1", "grok2"} {
			if strings.Contains(low, bad) {
				t.Errorf("model id %q contains forbidden real-model token %q (rule §36.8)", m.ID, bad)
			}
		}
	}
}

func TestSendMessageUnsupported(t *testing.T) {
	a := New(stubOptions(t, "success"))
	err := a.SendMessage(context.Background(), protocol.RunHandle{}, protocol.AgentMessage{Text: "hi"})
	if err == nil {
		t.Error("SendMessage should fail (not supported)")
	}
}

func TestStartMissingBinaryErrors(t *testing.T) {
	a := New(Options{Binary: "definitely-not-a-real-grok-binary-xyz"})
	_, err := a.Start(context.Background(), protocol.AgentRunRequest{RunID: "x", Engine: a.ID(), Workspace: t.TempDir()}, &codingagent.SliceSink{})
	if err == nil {
		t.Error("Start with missing binary should error")
	}
}

func TestCancelUnknownRunErrors(t *testing.T) {
	a := New(stubOptions(t, "success"))
	if err := a.Cancel(context.Background(), protocol.RunHandle{RunID: "nope"}); err == nil {
		t.Error("Cancel of unknown run should error")
	}
}

// helpers

func boolPtr(b bool) *bool { return &b }

func firstType(evs []protocol.NormalizedEvent) protocol.EventType {
	if len(evs) == 0 {
		return ""
	}
	return evs[0].Type
}
