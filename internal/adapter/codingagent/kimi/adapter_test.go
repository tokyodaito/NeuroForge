package kimi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/protocol"
)

// --- shared test helpers ---

// testContext returns a context for tests. It does not impose a timeout here
// because every operation that could hang (version/flag probes, runs) applies its
// own internal bound (see detect.go captureVersion/probeFlags and req.Timeout).
func testContext() context.Context {
	return context.Background()
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("go.mod not found")
	return ""
}

var (
	stubBuildOnce sync.Once
	stubBin       string
)

// buildStub builds the kimistub binary once per package and returns its path.
// It uses os.MkdirTemp (not t.TempDir) so the cached binary survives the whole
// package run.
func buildStub(t *testing.T) string {
	t.Helper()
	stubBuildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "kimistub-*")
		if err != nil {
			t.Fatalf("mktemp: %v", err)
		}
		bin := filepath.Join(dir, "kimistub")
		cmd := exec.Command("go", "build", "-o", bin, "./internal/adapter/codingagent/kimi/internal/kimistub")
		cmd.Dir = moduleRoot(t)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build kimistub: %v\n%s", err, out)
		}
		stubBin = bin
	})
	if stubBin == "" {
		t.Fatal("kimistub was not built")
	}
	return stubBin
}

// newStubAdapter builds an adapter pointed at the kimistub binary with the given
// scenario injected via KIMI_STUB_SCENARIO.
func newStubAdapter(t *testing.T, scenario string, extra ...string) *Adapter {
	t.Helper()
	stub := buildStub(t)
	opts := Options{
		BinaryOverride: stub,
		ArtifactsDir:   t.TempDir(),
		ExtraEnv:       append([]string{"KIMI_STUB_SCENARIO=" + scenario}, extra...),
	}
	return New(opts)
}

func waitForTerminal(t *testing.T, sink *codingagent.SliceSink, timeout time.Duration) []protocol.NormalizedEvent {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		evs := sink.Events()
		if len(evs) > 0 && evs[len(evs)-1].Type.IsTerminal() {
			return evs
		}
		time.Sleep(5 * time.Millisecond)
	}
	return sink.Events()
}

func lastType(evs []protocol.NormalizedEvent) protocol.EventType {
	if len(evs) == 0 {
		return ""
	}
	return evs[len(evs)-1].Type
}

func hasWarning(evs []protocol.NormalizedEvent) bool {
	for _, e := range evs {
		if e.Type == protocol.EventWarning {
			return true
		}
	}
	return false
}

// --- metadata tests ---

func TestDetectInstalled(t *testing.T) {
	a := newStubAdapter(t, "success")
	d := a.Detect(testContext())
	if !d.Installed {
		t.Fatalf("detect should succeed: %+v", d)
	}
	if d.Version == "" {
		t.Errorf("version not captured: %+v", d)
	}
	if !strings.Contains(d.Version, "1.4.0") {
		t.Errorf("version = %q, want 1.4.0", d.Version)
	}
}

func TestDetectMissingEngine(t *testing.T) {
	a := New(Options{BinaryName: "kimi-definitely-not-on-path-xyz"})
	d := a.Detect(testContext())
	if d.Installed {
		t.Errorf("detect should report not installed: %+v", d)
	}
}

func TestVersionReportsProtocol1(t *testing.T) {
	a := newStubAdapter(t, "success")
	v := a.Version(testContext())
	if v.ProtocolVersion != protocol.ProtocolVersion {
		t.Errorf("protocol = %d, want %d", v.ProtocolVersion, protocol.ProtocolVersion)
	}
	if v.EngineVersion == "" {
		t.Errorf("engine version empty: %+v", v)
	}
	if v.AdapterVersion == "" {
		t.Errorf("adapter version empty: %+v", v)
	}
}

func TestHealthStatuses(t *testing.T) {
	// Installed + version → OK.
	a := newStubAdapter(t, "success")
	if h := a.Health(testContext(), protocol.Account{}); h.Status != protocol.HealthOK {
		t.Errorf("installed health = %s, want ok", h.Status)
	}
	// Missing → DOWN.
	a = New(Options{BinaryName: "kimi-missing-xyz"})
	if h := a.Health(testContext(), protocol.Account{}); h.Status != protocol.HealthDown {
		t.Errorf("missing health = %s, want down", h.Status)
	}
}

func TestInspectQuotaUnknown(t *testing.T) {
	a := newStubAdapter(t, "success")
	q := a.InspectQuota(testContext(), protocol.Account{})
	if q.Confidence != protocol.QuotaConfUnknown {
		t.Errorf("quota confidence = %s, want UNKNOWN", q.Confidence)
	}
}

func TestListModels(t *testing.T) {
	a := newStubAdapter(t, "success")
	ms, err := a.ListModels(testContext(), protocol.Account{})
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(ms) == 0 {
		t.Fatal("no models reported")
	}
	for _, m := range ms {
		if m.Engine != "kimi" {
			t.Errorf("model engine = %q, want kimi", m.Engine)
		}
	}
}

func TestCapabilitiesDerivedFromVersion(t *testing.T) {
	a := newStubAdapter(t, "success") // stub reports 1.4.0
	caps := a.Capabilities(testContext())
	if !caps.HeadlessMode || !caps.StreamingEvents || !caps.SessionResume || !caps.UsageReporting {
		t.Errorf("1.4.0 capabilities incomplete: %+v", caps)
	}
}

// --- run lifecycle tests ---

func TestStartSuccessEventOrdering(t *testing.T) {
	a := newStubAdapter(t, "success")
	sink := &codingagent.SliceSink{}
	handle, err := a.Start(testContext(), protocol.AgentRunRequest{
		RunID: "ok", Engine: "kimi", Model: "kimi/default", Workspace: t.TempDir(), Prompt: "hi",
	}, sink)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if handle.Engine != "kimi" {
		t.Errorf("handle engine = %s", handle.Engine)
	}
	evs := waitForTerminal(t, sink, 6*time.Second)
	if len(evs) == 0 {
		t.Fatal("no events")
	}
	if evs[0].Type != protocol.EventRunStarted {
		t.Errorf("first = %s, want run.started", evs[0].Type)
	}
	if lastType(evs) != protocol.EventRunCompleted {
		t.Errorf("last = %s, want run.completed", lastType(evs))
	}
}

func TestStartMalformedDoesNotBreakRun(t *testing.T) {
	artDir := t.TempDir()
	stub := buildStub(t)
	a := New(Options{BinaryOverride: stub, ArtifactsDir: artDir, ExtraEnv: []string{"KIMI_STUB_SCENARIO=malformed-json"}})
	sink := &codingagent.SliceSink{}
	if _, err := a.Start(testContext(), protocol.AgentRunRequest{RunID: "mal", Workspace: t.TempDir(), Prompt: "x"}, sink); err != nil {
		t.Fatalf("Start: %v", err)
	}
	evs := waitForTerminal(t, sink, 6*time.Second)
	if lastType(evs) != protocol.EventRunCompleted {
		t.Errorf("last = %s, want run.completed", lastType(evs))
	}
	if !hasWarning(evs) {
		t.Error("malformed output produced no warning event")
	}
	// The raw malformed line must have been saved as an artifact.
	entries, _ := os.ReadDir(artDir)
	if len(entries) == 0 {
		t.Errorf("no malformed artifact saved in %s", artDir)
	}
}

func TestStartQuotaFailure(t *testing.T) {
	a := newStubAdapter(t, "quota-before-edits")
	sink := &codingagent.SliceSink{}
	if _, err := a.Start(testContext(), protocol.AgentRunRequest{RunID: "q", Workspace: t.TempDir(), Prompt: "x"}, sink); err != nil {
		t.Fatalf("Start: %v", err)
	}
	evs := waitForTerminal(t, sink, 6*time.Second)
	if lastType(evs) != protocol.EventRunFailed {
		t.Fatalf("last = %s, want run.failed", lastType(evs))
	}
	last := evs[len(evs)-1]
	if last.Failure == nil {
		t.Fatal("missing failure payload")
	}
	fc := a.ClassifyFailure(last.Failure.ExitCode, evs, "")
	if fc.Class != protocol.FailureProviderQuota {
		t.Errorf("class = %s, want PROVIDER_QUOTA", fc.Class)
	}
	if !fc.Failover {
		t.Error("quota should suggest failover")
	}
}

func TestStartCrashSynthesizesFailure(t *testing.T) {
	a := newStubAdapter(t, "crash")
	sink := &codingagent.SliceSink{}
	if _, err := a.Start(testContext(), protocol.AgentRunRequest{RunID: "crash", Workspace: t.TempDir(), Prompt: "x"}, sink); err != nil {
		t.Fatalf("Start: %v", err)
	}
	evs := waitForTerminal(t, sink, 6*time.Second)
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

func TestStartPartialOutputFails(t *testing.T) {
	a := newStubAdapter(t, "partial-output")
	sink := &codingagent.SliceSink{}
	if _, err := a.Start(testContext(), protocol.AgentRunRequest{RunID: "p", Workspace: t.TempDir(), Prompt: "x"}, sink); err != nil {
		t.Fatalf("Start: %v", err)
	}
	evs := waitForTerminal(t, sink, 6*time.Second)
	if lastType(evs) != protocol.EventRunFailed {
		t.Errorf("partial output last = %s, want run.failed", lastType(evs))
	}
}

func TestResumeEmitsRunResumed(t *testing.T) {
	a := newStubAdapter(t, "resume")
	sink := &codingagent.SliceSink{}
	if _, err := a.Resume(testContext(), protocol.ResumeRequest{
		RunID: "res", Engine: "kimi", Model: "kimi/default", Workspace: t.TempDir(), SessionID: "sess-1",
	}, sink); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	evs := waitForTerminal(t, sink, 6*time.Second)
	if len(evs) == 0 || evs[0].Type != protocol.EventRunResumed {
		t.Fatalf("first = %s, want run.resumed", firstType(evs))
	}
	if lastType(evs) != protocol.EventRunCompleted {
		t.Errorf("last = %s, want run.completed", lastType(evs))
	}
}

func TestFlagErrorDegradesGracefully(t *testing.T) {
	// A scenario where the engine rejects a flag (non-zero exit, stderr, no
	// events): the adapter must degrade to run.failed, not crash.
	a := newStubAdapter(t, "flag-error")
	sink := &codingagent.SliceSink{}
	if _, err := a.Start(testContext(), protocol.AgentRunRequest{RunID: "fe", Workspace: t.TempDir(), Prompt: "x"}, sink); err != nil {
		t.Fatalf("Start: %v", err)
	}
	evs := waitForTerminal(t, sink, 6*time.Second)
	if lastType(evs) != protocol.EventRunFailed {
		t.Fatalf("flag-error last = %s, want run.failed", lastType(evs))
	}
}

// --- cancellation & timeout ---

func TestCancellationKillsGroup(t *testing.T) {
	a := newStubAdapter(t, "cancellation")
	sink := &codingagent.SliceSink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handle, err := a.Start(ctx, protocol.AgentRunRequest{RunID: "c", Workspace: t.TempDir(), Prompt: "x"}, sink)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(150 * time.Millisecond) // let the run reach its hang point
	if err := a.Cancel(context.Background(), handle); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	evs := waitForTerminal(t, sink, 3*time.Second)
	if lastType(evs) != protocol.EventRunCancelled {
		t.Errorf("last = %s, want run.cancelled", lastType(evs))
	}
	// The run must no longer be tracked (process group gone).
	a.mu.Lock()
	_, stillRunning := a.runs[handle.RunID]
	a.mu.Unlock()
	if stillRunning {
		t.Error("run still tracked after cancel")
	}
}

func TestTimeoutTerminatesRun(t *testing.T) {
	a := newStubAdapter(t, "timeout")
	sink := &codingagent.SliceSink{}
	// A short wall-clock timeout must terminate the hanging run (classified as
	// TIMEOUT, not CANCELLED).
	if _, err := a.Start(testContext(), protocol.AgentRunRequest{
		RunID: "to", Workspace: t.TempDir(), Prompt: "x", Timeout: 200 * time.Millisecond,
	}, sink); err != nil {
		t.Fatalf("Start: %v", err)
	}
	evs := waitForTerminal(t, sink, 4*time.Second)
	if !lastType(evs).IsTerminal() {
		t.Fatalf("timeout did not terminate; last = %s", lastType(evs))
	}
	last := evs[len(evs)-1]
	// Timeout yields run.failed(TIMEOUT).
	if last.Type != protocol.EventRunFailed || last.Failure == nil || last.Failure.Class != protocol.FailureTimeout {
		t.Errorf("timeout terminal = %+v, want run.failed(TIMEOUT)", last)
	}
}

// --- encoding robustness through the real pipe ---

func TestStartNonStreamingPlainText(t *testing.T) {
	// An old engine version (0.5.0) with no stream-json flag: the adapter must
	// NOT pass --output, and the stub emits plain text, which the adapter wraps
	// into run.started -> message.completed -> run.completed.
	stub := buildStub(t)
	a := New(Options{
		BinaryOverride: stub,
		ArtifactsDir:   t.TempDir(),
		ExtraEnv:       []string{"KIMI_STUB_OLD=1", "KIMI_STUB_SCENARIO=success"},
	})
	caps := a.Capabilities(testContext())
	if caps.StreamingEvents {
		t.Fatalf("old version should report StreamingEvents=false: %+v", caps)
	}
	sink := &codingagent.SliceSink{}
	if _, err := a.Start(testContext(), protocol.AgentRunRequest{RunID: "plain", Workspace: t.TempDir(), Prompt: "x"}, sink); err != nil {
		t.Fatalf("Start: %v", err)
	}
	evs := waitForTerminal(t, sink, 6*time.Second)
	if lastType(evs) != protocol.EventRunCompleted {
		t.Fatalf("plain-text last = %s, want run.completed", lastType(evs))
	}
	// Must contain a message.completed carrying the plain text.
	found := false
	for _, e := range evs {
		if e.Type == protocol.EventMessageCompleted && e.Message != nil && strings.Contains(e.Message.Text, "Hello from Kimi") {
			found = true
		}
	}
	if !found {
		t.Errorf("plain text not wrapped into message.completed: %+v", evs)
	}
}

func TestStartHandlesBOM(t *testing.T) {
	a := newStubAdapter(t, "success", "KIMI_STUB_BOM=1")
	sink := &codingagent.SliceSink{}
	if _, err := a.Start(testContext(), protocol.AgentRunRequest{RunID: "bom", Workspace: t.TempDir(), Prompt: "x"}, sink); err != nil {
		t.Fatalf("Start: %v", err)
	}
	evs := waitForTerminal(t, sink, 6*time.Second)
	if lastType(evs) != protocol.EventRunCompleted {
		t.Errorf("BOM stream last = %s, want run.completed (BOM should not break parse)", lastType(evs))
	}
	if len(evs) > 0 && evs[0].Type != protocol.EventRunStarted {
		t.Errorf("BOM corrupted first event: %s", evs[0].Type)
	}
}

func TestStartHandlesCRLF(t *testing.T) {
	a := newStubAdapter(t, "success", "KIMI_STUB_CRLF=1")
	sink := &codingagent.SliceSink{}
	if _, err := a.Start(testContext(), protocol.AgentRunRequest{RunID: "crlf", Workspace: t.TempDir(), Prompt: "x"}, sink); err != nil {
		t.Fatalf("Start: %v", err)
	}
	evs := waitForTerminal(t, sink, 6*time.Second)
	if lastType(evs) != protocol.EventRunCompleted {
		t.Errorf("CRLF stream last = %s, want run.completed", lastType(evs))
	}
}

// --- misc interface behaviour ---

func TestSendMessageUnsupported(t *testing.T) {
	a := newStubAdapter(t, "success")
	if err := a.SendMessage(context.Background(), protocol.RunHandle{}, protocol.AgentMessage{Text: "hi"}); err == nil {
		t.Error("SendMessage should error in headless mode")
	}
}

func TestStartNotInstalledErrors(t *testing.T) {
	a := New(Options{BinaryName: "kimi-absent-xyz"})
	_, err := a.Start(testContext(), protocol.AgentRunRequest{Workspace: t.TempDir(), Prompt: "x"}, &codingagent.SliceSink{})
	if err == nil {
		t.Error("Start should error when engine not installed")
	}
}

func TestIDIsKimi(t *testing.T) {
	if New(Options{}).ID() != "kimi" {
		t.Error("ID() != kimi")
	}
}

func firstType(evs []protocol.NormalizedEvent) protocol.EventType {
	if len(evs) == 0 {
		return ""
	}
	return evs[0].Type
}
