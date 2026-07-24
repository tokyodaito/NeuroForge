package claude

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/fake"
	"neuroforge/internal/adapter/codingagent/protocol"
)

func TestRunSuccessOrdering(t *testing.T) {
	a := newTestAdapter(t, fake.ScenarioSuccess)
	handle, sink := startTestRun(t, a, "ok")
	_ = handle
	evs := waitTerminal(sink, 3*time.Second)
	if len(evs) == 0 {
		t.Fatal("no events")
	}
	if evs[0].Type != protocol.EventRunStarted {
		t.Errorf("first = %s, want run.started", evs[0].Type)
	}
	if evs[len(evs)-1].Type != protocol.EventRunCompleted {
		t.Errorf("last = %s, want run.completed", evs[len(evs)-1].Type)
	}
	// usage.updated must precede run.completed.
	hasUsage := false
	for _, e := range evs {
		if e.Type == protocol.EventUsageUpdated {
			hasUsage = true
		}
	}
	if !hasUsage {
		t.Errorf("usage.updated missing: %v", typesOf(evs))
	}
}

func TestRunMalformedSavedAndWarning(t *testing.T) {
	artDir := t.TempDir()
	a := newTestAdapter(t, fake.ScenarioMalformedJSON, func(o *Options) { o.ArtifactsDir = artDir })
	_, sink := startTestRun(t, a, "mal")
	evs := waitTerminal(sink, 3*time.Second)
	if len(evs) == 0 || evs[len(evs)-1].Type != protocol.EventRunCompleted {
		t.Fatalf("malformed broke the run; last = %v", typesOf(evs))
	}
	if !hasWarning(evs) {
		t.Errorf("no warning emitted for malformed line: %v", typesOf(evs))
	}
	// The raw malformed line must be persisted as an artifact.
	entries, _ := filepathGlob(artDir)
	if len(entries) == 0 {
		t.Errorf("no malformed artifact saved in %s", artDir)
	}
}

func filepathGlob(dir string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*"))
	return matches, err
}

func TestRunCancellationEmitsCancelled(t *testing.T) {
	a := newTestAdapter(t, fake.ScenarioCancellation)
	handle, sink := startTestRun(t, a, "can")
	time.Sleep(120 * time.Millisecond) // let it reach the hang
	if err := a.Cancel(context.Background(), handle); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	evs := waitTerminal(sink, 3*time.Second)
	if len(evs) == 0 || evs[len(evs)-1].Type != protocol.EventRunCancelled {
		t.Fatalf("last = %v, want run.cancelled", typesOf(evs))
	}
	// The run must be untracked after cancel completes.
	a.mu.Lock()
	_, stillTracked := a.runs[handle.RunID]
	a.mu.Unlock()
	// Give supervise's defer a moment to delete.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		_, stillTracked = a.runs[handle.RunID]
		a.mu.Unlock()
		if !stillTracked {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if stillTracked {
		t.Errorf("run still tracked after cancel")
	}
}

func TestRunTimeoutEmitsFailedTimeout(t *testing.T) {
	a := newTestAdapter(t, fake.ScenarioTimeout)
	sink := &codingagent.SliceSink{}
	_, err := a.Start(context.Background(), protocol.AgentRunRequest{
		RunID: "to", Engine: a.ID(), Model: "sonnet", Workspace: tempDir(),
		Timeout: 120 * time.Millisecond,
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	evs := waitTerminal(sink, 3*time.Second)
	if len(evs) == 0 {
		t.Fatal("no events")
	}
	last := evs[len(evs)-1]
	if last.Type != protocol.EventRunFailed || last.Failure == nil {
		t.Fatalf("last = %+v, want run.failed", last)
	}
	if last.Failure.Class != protocol.FailureTimeout {
		t.Errorf("class = %s, want TIMEOUT", last.Failure.Class)
	}
}

func TestRunCrashSynthesizesEngineCrash(t *testing.T) {
	a := newTestAdapter(t, fake.ScenarioCrash)
	_, sink := startTestRun(t, a, "crash")
	evs := waitTerminal(sink, 3*time.Second)
	last := evs[len(evs)-1]
	if last.Type != protocol.EventRunFailed || last.Failure == nil {
		t.Fatalf("last = %+v, want run.failed", last)
	}
	if last.Failure.Class != protocol.FailureEngineCrash {
		t.Errorf("class = %s, want ENGINE_CRASH", last.Failure.Class)
	}
	fc := a.ClassifyFailure(last.Failure.ExitCode, evs, "")
	if fc.Class != protocol.FailureEngineCrash {
		t.Errorf("ClassifyFailure = %s, want ENGINE_CRASH", fc.Class)
	}
}

func TestRunPartialOutputSynthesizesFailure(t *testing.T) {
	a := newTestAdapter(t, fake.ScenarioPartialOutput)
	_, sink := startTestRun(t, a, "partial")
	evs := waitTerminal(sink, 3*time.Second)
	if evs[len(evs)-1].Type != protocol.EventRunFailed {
		t.Errorf("last = %s, want run.failed", evs[len(evs)-1].Type)
	}
}

func TestRunQuotaClassificationFailover(t *testing.T) {
	a := newTestAdapter(t, fake.ScenarioQuotaBeforeEdits)
	_, sink := startTestRun(t, a, "quota")
	evs := waitTerminal(sink, 3*time.Second)
	last := evs[len(evs)-1]
	if last.Type != protocol.EventRunFailed || last.Failure == nil {
		t.Fatalf("last = %+v, want run.failed", last)
	}
	fc := a.ClassifyFailure(last.Failure.ExitCode, evs, "")
	if fc.Class != protocol.FailureProviderQuota {
		t.Errorf("class = %s, want PROVIDER_QUOTA", fc.Class)
	}
	if !fc.Failover {
		t.Errorf("quota failure should suggest failover")
	}
}

func TestRunSessionExtractionWhileActive(t *testing.T) {
	a := newTestAdapter(t, fake.ScenarioCancellation) // hangs after init
	handle, sink := startTestRun(t, a, "sess")
	time.Sleep(150 * time.Millisecond) // let init line be processed
	got := a.SessionID(handle.RunID)
	if got != "claude-session-test" {
		t.Errorf("SessionID = %q, want claude-session-test", got)
	}
	_ = a.Cancel(context.Background(), handle)
	waitTerminal(sink, 2*time.Second)
}

func TestRunResumeEmitsResumed(t *testing.T) {
	a := newTestAdapter(t, fake.ScenarioResume)
	sink := &codingagent.SliceSink{}
	_, err := a.Resume(context.Background(), protocol.ResumeRequest{
		RunID: "res", Engine: a.ID(), Model: "sonnet", Workspace: tempDir(),
		SessionID: "claude-session-test",
	}, sink)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	evs := waitTerminal(sink, 3*time.Second)
	if len(evs) == 0 || evs[0].Type != protocol.EventRunResumed {
		t.Fatalf("first = %v, want run.resumed", typesOf(evs))
	}
	if !evs[len(evs)-1].Type.IsTerminal() {
		t.Errorf("resume did not reach a terminal: %v", typesOf(evs))
	}
}

func TestRunConcurrent(t *testing.T) {
	a := newTestAdapter(t, fake.ScenarioSuccess)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sink := &codingagent.SliceSink{}
			_, err := a.Start(context.Background(), protocol.AgentRunRequest{
				RunID: "c" + tempSuffix(i), Engine: a.ID(), Model: "sonnet", Workspace: tempDir(),
			}, sink)
			if err != nil {
				t.Errorf("Start %d: %v", i, err)
				return
			}
			evs := waitTerminal(sink, 3*time.Second)
			if len(evs) == 0 || !evs[len(evs)-1].Type.IsTerminal() {
				t.Errorf("run %d did not terminate: %v", i, typesOf(evs))
			}
		}(i)
	}
	wg.Wait()
}

func TestSendMessageUnsupported(t *testing.T) {
	a := newTestAdapter(t, fake.ScenarioSuccess)
	err := a.SendMessage(context.Background(), protocol.RunHandle{RunID: "x"}, protocol.AgentMessage{Text: "hi"})
	if err == nil {
		t.Fatal("expected SendMessage to be unsupported")
	}
}

func TestCancelUnknownRun(t *testing.T) {
	a := newTestAdapter(t, fake.ScenarioSuccess)
	if err := a.Cancel(context.Background(), protocol.RunHandle{RunID: "nope"}); err == nil {
		t.Fatal("expected error cancelling unknown run")
	}
}

func TestStartNilSink(t *testing.T) {
	a := newTestAdapter(t, fake.ScenarioSuccess)
	_, err := a.Start(context.Background(), protocol.AgentRunRequest{RunID: "x", Workspace: tempDir()}, nil)
	if err == nil {
		t.Fatal("expected error for nil sink")
	}
}

func TestStartMissingBinary(t *testing.T) {
	a, _ := New(Options{LookPath: func(string) (string, error) { return "", errNotInstalled }})
	_, err := a.Start(context.Background(), protocol.AgentRunRequest{RunID: "x"}, &codingagent.SliceSink{})
	if err == nil {
		t.Fatal("expected error when binary missing")
	}
}

// TestRunNoSecretLeak verifies a crash whose stderr carries a fake provider key
// does not leak the key into the synthesized failure event or ClassifyFailure.
func TestRunNoSecretLeak(t *testing.T) {
	const secret = "sk-ant-api03-SECRETKEY0123456789abcdef"
	a, _ := New(Options{
		BinaryPath: "claude",
		Spawn: replaySpawner(replaySpec{
			lines:    []string{claudeLine(map[string]any{"type": "system", "subtype": "init", "session_id": "s", "model": "sonnet"})},
			exitCode: 139,
			stderr:   "claude crashed with key " + secret + " in context",
		}),
		ArtifactsDir: t.TempDir(),
	})
	sink := &codingagent.SliceSink{}
	_, err := a.Start(context.Background(), protocol.AgentRunRequest{RunID: "sec", Workspace: tempDir()}, sink)
	if err != nil {
		t.Fatal(err)
	}
	evs := waitTerminal(sink, 3*time.Second)
	for _, e := range evs {
		b := dumpEvent(e)
		if strings.Contains(b, secret) {
			t.Errorf("secret leaked into event: %s", b)
		}
	}
}

func dumpEvent(e protocol.NormalizedEvent) string {
	var b strings.Builder
	b.WriteString(string(e.Type))
	if e.Failure != nil {
		b.WriteString(" " + e.Failure.Reason)
	}
	if e.Warning != nil {
		b.WriteString(" " + e.Warning.Message)
	}
	if e.Message != nil {
		b.WriteString(" " + e.Message.Text + " " + e.Message.Delta)
	}
	return b.String()
}

// TestProctreeSpawnerKillsRealProcess is a real integration test of the
// production kill path: it spawns a long-lived OS process via proctreeSpawner
// and verifies Kill() (proctree.KillGroup) terminates it promptly. This covers
// "cancellation kills the group" against the real proctree implementation
// (Windows: CREATE_NEW_PROCESS_GROUP + taskkill /T /F; unix: setpgid).
func TestProctreeSpawnerKillsRealProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("real process kill test skipped in -short mode")
	}
	name, args := sleepCommand()
	stderr := &bytes.Buffer{}
	proc, err := proctreeSpawner(append([]string{name}, args...), "", nil, nil, stderr)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if err := proc.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	// Drain stdout so the child is not blocked on a full pipe.
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := proc.Stdout().Read(buf); err != nil {
				return
			}
		}
	}()
	time.Sleep(200 * time.Millisecond) // let it be running
	if err := proc.Kill(); err != nil {
		t.Logf("Kill returned %v (acceptable)", err)
	}
	done := make(chan error, 1)
	go func() { done <- proc.Wait() }()
	select {
	case err := <-done:
		_ = err // process exited (possibly with an error code) — expected after Kill
	case <-time.After(5 * time.Second):
		t.Fatal("proctree Kill did not terminate the process group within 5s")
	}
}

func sleepCommand() (name string, args []string) {
	if runtime.GOOS == "windows" {
		return "ping", []string{"-n", "31", "-w", "1000", "127.0.0.1"}
	}
	return "sleep", []string{"30"}
}

// helpers
func tempDir() string { return osTempDir() }

func osTempDir() string { return os.TempDir() }

func tempSuffix(i int) string { return string(rune('a' + i)) }
