package plugin

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/fake"
	"neuroforge/internal/adapter/codingagent/protocol"
)

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
	buildOnce sync.Once
	binPath   string
)

func fakeBin(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "fca-plugin-*")
		if err != nil {
			t.Fatal(err)
		}
		bin := filepath.Join(dir, "fake-coding-agent")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", bin, "./cmd/fake-coding-agent")
		cmd.Dir = moduleRoot(t)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build: %v\n%s", err, out)
		}
		binPath = bin
	})
	if binPath == "" {
		t.Fatal("binary not built")
	}
	return binPath
}

// dialFake spawns the fake plugin in jsonrpc mode with the given scenario.
func dialFake(t *testing.T, scenario fake.Scenario) (*Adapter, func()) {
	t.Helper()
	bin := fakeBin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Set FAKE_SCENARIO so the jsonrpc server picks the scenario.
	env := []string{"FAKE_SCENARIO=" + string(scenario)}
	if v, ok := os.LookupEnv("PATH"); ok {
		env = append(env, "PATH="+v)
	}
	ad, err := DialAdapter(ctx, bin, []string{"--mode", "jsonrpc"}, env)
	if err != nil {
		t.Fatalf("DialAdapter: %v", err)
	}
	a := ad.(*Adapter)
	return a, func() { _ = a.Close() }
}

func waitForTerminal(s *codingagent.SliceSink, timeout time.Duration) []protocol.NormalizedEvent {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		evs := s.Events()
		if len(evs) > 0 && evs[len(evs)-1].Type.IsTerminal() {
			return evs
		}
		time.Sleep(2 * time.Millisecond)
	}
	return s.Events()
}

func TestPluginHandshakeAndMetadata(t *testing.T) {
	a, cleanup := dialFake(t, fake.ScenarioSuccess)
	defer cleanup()

	if a.ID() != "fake" {
		t.Errorf("ID = %q, want fake", a.ID())
	}
	ctx := context.Background()
	d := a.Detect(ctx)
	if !d.Installed {
		t.Errorf("detect: %+v", d)
	}
	v := a.Version(ctx)
	if v.ProtocolVersion != protocol.ProtocolVersion {
		t.Errorf("protocol version = %d, want %d", v.ProtocolVersion, protocol.ProtocolVersion)
	}
	caps := a.Capabilities(ctx)
	if !caps.HeadlessMode {
		t.Errorf("caps: %+v", caps)
	}
	models, err := a.ListModels(ctx, protocol.Account{})
	if err != nil || len(models) == 0 {
		t.Errorf("ListModels: %v %v", models, err)
	}
	q := a.InspectQuota(ctx, protocol.Account{})
	if q.Confidence == "" {
		t.Error("quota snapshot empty")
	}
}

func TestPluginRunSuccessStreamsEvents(t *testing.T) {
	a, cleanup := dialFake(t, fake.ScenarioSuccess)
	defer cleanup()

	sink := &codingagent.SliceSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	handle, err := a.Start(ctx, protocol.AgentRunRequest{
		RunID: "p1", Engine: "fake", Model: "fake/standard", Workspace: t.TempDir(),
	}, sink)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if handle.RunID != "p1" || handle.Engine != "fake" {
		t.Errorf("handle: %+v", handle)
	}
	evs := waitForTerminal(sink, 3*time.Second)
	types := typesOf(evs)
	if len(types) == 0 || types[0] != protocol.EventRunStarted {
		t.Errorf("first = %v, want run.started", types)
	}
	if types[len(types)-1] != protocol.EventRunCompleted {
		t.Errorf("last = %v, want run.completed", types)
	}
}

func TestPluginQuotaFailureClassified(t *testing.T) {
	a, cleanup := dialFake(t, fake.ScenarioQuotaBeforeEdits)
	defer cleanup()

	sink := &codingagent.SliceSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := a.Start(ctx, protocol.AgentRunRequest{RunID: "q1", Engine: "fake", Model: "fake/standard"}, sink)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	evs := waitForTerminal(sink, 3*time.Second)
	if lastType(evs) != protocol.EventRunFailed {
		t.Fatalf("last = %s, want run.failed", lastType(evs))
	}
	last := evs[len(evs)-1]
	fc := a.ClassifyFailure(last.Failure.ExitCode, evs, "")
	if fc.Class != protocol.FailureProviderQuota {
		t.Errorf("class = %s, want PROVIDER_QUOTA", fc.Class)
	}
}

func TestPluginCancellation(t *testing.T) {
	a, cleanup := dialFake(t, fake.ScenarioCancellation)
	defer cleanup()

	sink := &codingagent.SliceSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	handle, err := a.Start(ctx, protocol.AgentRunRequest{RunID: "cc", Engine: "fake", Model: "fake/standard", Workspace: t.TempDir()}, sink)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(150 * time.Millisecond) // let it reach the hang
	if err := a.Cancel(context.Background(), handle); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	evs := waitForTerminal(sink, 2*time.Second)
	if lastType(evs) != protocol.EventRunCancelled {
		t.Errorf("last = %s, want run.cancelled", lastType(evs))
	}
}

func TestPluginResume(t *testing.T) {
	a, cleanup := dialFake(t, fake.ScenarioResume)
	defer cleanup()

	sink := &codingagent.SliceSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := a.Resume(ctx, protocol.ResumeRequest{
		RunID: "rr", Engine: "fake", Model: "fake/standard", Workspace: t.TempDir(), SessionID: "fake-session-1",
	}, sink)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	evs := waitForTerminal(sink, 3*time.Second)
	types := typesOf(evs)
	if len(types) == 0 || types[0] != protocol.EventRunResumed {
		t.Errorf("first = %v, want run.resumed", types)
	}
	if lastType(evs) != protocol.EventRunCompleted {
		t.Errorf("last = %s, want run.completed", lastType(evs))
	}
}

func TestPluginMalformedDoesNotBreakRun(t *testing.T) {
	a, cleanup := dialFake(t, fake.ScenarioMalformedJSON)
	defer cleanup()

	sink := &codingagent.SliceSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := a.Start(ctx, protocol.AgentRunRequest{RunID: "m1", Engine: "fake"}, sink)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	evs := waitForTerminal(sink, 3*time.Second)
	// Must still complete; the malformed line arrives as a warning event.
	if lastType(evs) != protocol.EventRunCompleted {
		t.Errorf("last = %s, want run.completed", lastType(evs))
	}
	if !hasWarning(evs) {
		t.Errorf("expected a warning for malformed output: %+v", evs)
	}
}

func TestPluginUsageEvents(t *testing.T) {
	a, cleanup := dialFake(t, fake.ScenarioUsageEvents)
	defer cleanup()

	sink := &codingagent.SliceSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := a.Start(ctx, protocol.AgentRunRequest{RunID: "u1", Engine: "fake"}, sink)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	evs := waitForTerminal(sink, 3*time.Second)
	n := 0
	for _, e := range evs {
		if e.Type == protocol.EventUsageUpdated {
			n++
		}
	}
	if n != 3 {
		t.Errorf("usage events = %d, want 3", n)
	}
}

func TestPluginCloseTerminatesProcessGroup(t *testing.T) {
	a, cleanup := dialFake(t, fake.ScenarioSuccess)
	defer cleanup()
	pid := a.Client().cmd.Process.Pid
	_ = a.Close()
	// After close the process should be gone within a moment.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("plugin process %d still alive after Close", pid)
}

// helpers

func typesOf(evs []protocol.NormalizedEvent) []protocol.EventType {
	out := make([]protocol.EventType, len(evs))
	for i, e := range evs {
		out[i] = e.Type
	}
	return out
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
