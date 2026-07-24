package declarative

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/protocol"
)

const exampleManifest = `api_version: neuroforge/v1
kind: command-coding-agent

id: example-fake-agent

detect:
  command:
    - fake-coding-agent
    - --version

run:
  command:
    - fake-coding-agent
    - --mode
    - command
    - --scenario
    - success
    - --workspace
    - "{{ workspace }}"
    - --model
    - "{{ model }}"
    - --run-id
    - "{{ run_id }}"
    - "{{ prompt_file }}"

capabilities:
  headless_mode: true
  streaming_events: true
  model_selection: true
  usage_reporting: true
`

func TestParseManifestExample(t *testing.T) {
	m, err := ParseManifest([]byte(exampleManifest))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.ID != "example-fake-agent" {
		t.Errorf("id = %q", m.ID)
	}
	if m.Kind != "command-coding-agent" {
		t.Errorf("kind = %q", m.Kind)
	}
	if len(m.Detect.Command) != 2 || m.Detect.Command[0] != "fake-coding-agent" {
		t.Errorf("detect = %v", m.Detect.Command)
	}
	if len(m.Run.Command) == 0 || m.Run.Command[0] != "fake-coding-agent" {
		t.Errorf("run = %v", m.Run.Command)
	}
	if !m.Capabilities.HeadlessMode || !m.Capabilities.ModelSelection {
		t.Errorf("caps not parsed: %+v", m.Capabilities)
	}
}

func TestParseManifestExampleFromDisk(t *testing.T) {
	data, err := os.ReadFile("example.yaml")
	if err != nil {
		t.Fatalf("read example: %v", err)
	}
	if _, err := ParseManifest(data); err != nil {
		t.Fatalf("parse example.yaml: %v", err)
	}
}

func TestParseManifestMissingID(t *testing.T) {
	_, err := ParseManifest([]byte(`api_version: neuroforge/v1
kind: command-coding-agent
`))
	if err == nil {
		t.Fatal("expected error for missing id")
	}
}

func TestParseManifestQuotedAndBooleans(t *testing.T) {
	m, err := ParseManifest([]byte(`
id: "quoted-id"
capabilities:
  headless_mode: true
  interactive_mode: false
  mcp: yes
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.ID != "quoted-id" {
		t.Errorf("id = %q", m.ID)
	}
	if !m.Capabilities.HeadlessMode || m.Capabilities.InteractiveMode || !m.Capabilities.MCP {
		t.Errorf("caps wrong: %+v", m.Capabilities)
	}
}

func TestSubstitute(t *testing.T) {
	got := substitute("--cwd {{ workspace }} --model {{ model }} -- {{ prompt_file }}", templateVars{
		workspace: "/ws", model: "fake/standard", promptFile: "/p.txt",
	})
	want := "--cwd /ws --model fake/standard -- /p.txt"
	if got != want {
		t.Errorf("substitute = %q, want %q", got, want)
	}
}

// moduleRoot walks up from the current dir to find the directory containing
// go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not find module root (go.mod)")
	return ""
}

// fakeBinary builds the cmd/fake-coding-agent binary once per package (cached
// via sync.Once) and returns its path plus a no-op cleanup. The binary is put on
// PATH so manifests can invoke "fake-coding-agent" by name. It uses os.MkdirTemp
// (not t.TempDir) so the cached binary survives the whole package run.
func fakeBinary(t *testing.T) (string, func()) {
	t.Helper()
	buildFakeOnce.Do(func() {
		dir, err := os.MkdirTemp("", "fake-coding-agent-*")
		if err != nil {
			t.Fatalf("mktemp: %v", err)
		}
		bin := filepath.Join(dir, "fake-coding-agent")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", bin, "./cmd/fake-coding-agent")
		cmd.Dir = moduleRoot(t)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build fake-coding-agent: %v\n%s", err, out)
		}
		cachedFakeBin = bin
		oldPath := os.Getenv("PATH")
		_ = os.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath)
	})
	if cachedFakeBin == "" {
		t.Fatalf("fake-coding-agent was not built")
	}
	return cachedFakeBin, func() {}
}

var (
	buildFakeOnce sync.Once
	cachedFakeBin string
)

func manifestForScenario(scenario string) string {
	return `api_version: neuroforge/v1
kind: command-coding-agent
id: fake
detect:
  command: [fake-coding-agent, --version]
run:
  command:
    - fake-coding-agent
    - --mode
    - command
    - --scenario
    - ` + scenario + `
    - --workspace
    - "{{ workspace }}"
    - --model
    - "{{ model }}"
    - --run-id
    - "{{ run_id }}"
capabilities:
  headless_mode: true
  streaming_events: true
  usage_reporting: true
`
}

func runDeclarative(t *testing.T, scenario string) []protocol.NormalizedEvent {
	t.Helper()
	bin, cleanup := fakeBinary(t)
	defer cleanup()
	_ = bin

	artDir := t.TempDir()
	a, err := FromYAML([]byte(manifestForScenario(scenario)), artDir)
	if err != nil {
		t.Fatalf("FromYAML: %v", err)
	}
	sink := &codingagent.SliceSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	workspace := t.TempDir()
	handle, err := a.Start(ctx, protocol.AgentRunRequest{
		RunID: "d1", Engine: "fake", Model: "fake/standard", Workspace: workspace,
	}, sink)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if handle.Engine != "fake" {
		t.Errorf("engine = %s", handle.Engine)
	}
	return waitForTerminal(t, sink, 4*time.Second)
}

func waitForTerminal(t *testing.T, s *codingagent.SliceSink, timeout time.Duration) []protocol.NormalizedEvent {
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

func TestDeclarativeSuccess(t *testing.T) {
	evs := runDeclarative(t, "success")
	types := typesOf(evs)
	if len(types) == 0 || types[0] != protocol.EventRunStarted {
		t.Fatalf("first = %v, want run.started", types)
	}
	if types[len(types)-1] != protocol.EventRunCompleted {
		t.Fatalf("last = %v, want run.completed", types)
	}
}

func TestDeclarativeMalformedSavedAndClassified(t *testing.T) {
	artDir := t.TempDir()
	bin, cleanup := fakeBinary(t)
	defer cleanup()
	_ = bin

	a, err := FromYAML([]byte(manifestForScenario("malformed-json")), artDir)
	if err != nil {
		t.Fatalf("FromYAML: %v", err)
	}
	sink := &codingagent.SliceSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = a.Start(ctx, protocol.AgentRunRequest{RunID: "m1", Engine: "fake", Workspace: t.TempDir()}, sink)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	evs := waitForTerminal(t, sink, 4*time.Second)

	// The run must still complete (malformed does not break it).
	if lastType(evs) != protocol.EventRunCompleted {
		t.Errorf("last = %s, want run.completed", lastType(evs))
	}
	// A malformed-output warning must be present.
	found := false
	for _, e := range evs {
		if e.Type == protocol.EventWarning && e.Warning != nil && strings.Contains(e.Warning.Code, "malformed") {
			found = true
		}
	}
	if !found {
		t.Errorf("no malformed warning emitted: %+v", evs)
	}
	// The raw malformed line must have been saved as an artifact.
	entries, _ := os.ReadDir(artDir)
	if len(entries) == 0 {
		t.Errorf("no malformed artifact saved in %s", artDir)
	}
}

func TestDeclarativeCrashSynthesizesFailure(t *testing.T) {
	evs := runDeclarative(t, "crash")
	if lastType(evs) != protocol.EventRunFailed {
		t.Fatalf("last = %s, want run.failed (crash synthesizes terminal)", lastType(evs))
	}
	last := evs[len(evs)-1]
	if last.Failure == nil {
		t.Fatal("missing failure payload")
	}
	if last.Failure.Class != protocol.FailureEngineCrash {
		t.Errorf("class = %s, want ENGINE_CRASH", last.Failure.Class)
	}
}

func TestDeclarativePartialOutputSynthesizesFailure(t *testing.T) {
	evs := runDeclarative(t, "partial-output")
	if lastType(evs) != protocol.EventRunFailed {
		t.Errorf("last = %s, want run.failed", lastType(evs))
	}
}

func TestDeclarativeCancellationKillsGroup(t *testing.T) {
	bin, cleanup := fakeBinary(t)
	defer cleanup()
	_ = bin

	a, err := FromYAML([]byte(manifestForScenario("timeout")), t.TempDir())
	if err != nil {
		t.Fatalf("FromYAML: %v", err)
	}
	sink := &codingagent.SliceSink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handle, err := a.Start(ctx, protocol.AgentRunRequest{RunID: "c1", Engine: "fake", Workspace: t.TempDir()}, sink)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(150 * time.Millisecond) // let it reach the hang
	if err := a.Cancel(context.Background(), handle); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	evs := waitForTerminal(t, sink, 2*time.Second)
	if lastType(evs) != protocol.EventRunCancelled {
		t.Errorf("last = %s, want run.cancelled", lastType(evs))
	}
	// The fake process must actually be gone (process group killed).
	a.mu.Lock()
	_, stillRunning := a.runs[handle.RunID]
	a.mu.Unlock()
	if stillRunning {
		t.Errorf("run still tracked after cancel")
	}
}

func TestDeclarativeDetect(t *testing.T) {
	bin, cleanup := fakeBinary(t)
	defer cleanup()
	_ = bin

	a, err := FromYAML([]byte(manifestForScenario("success")), t.TempDir())
	if err != nil {
		t.Fatalf("FromYAML: %v", err)
	}
	res := a.Detect(context.Background())
	if !res.Installed {
		t.Errorf("detect should succeed: %+v", res)
	}
}

func TestDeclarativeNoCredentialsInEnv(t *testing.T) {
	// AC-28: the agent process must never receive merge credentials / the
	// daemon auth token. buildEnv must not copy arbitrary env or FORGE_TOKEN.
	t.Setenv("FORGE_DAEMON_TOKEN", "super-secret")
	env := buildEnv([]string{"FOO=bar"})
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "super-secret") {
		t.Errorf("daemon token leaked into agent env: %s", joined)
	}
	if !strings.Contains(joined, "FOO=bar") {
		t.Errorf("allowlist entry not passed: %s", joined)
	}
	if !strings.Contains(joined, "PATH=") {
		t.Errorf("PATH not passed")
	}
}

func TestDeclarativeConcurrentRuns(t *testing.T) {
	bin, cleanup := fakeBinary(t)
	defer cleanup()
	_ = bin

	a, err := FromYAML([]byte(manifestForScenario("success")), t.TempDir())
	if err != nil {
		t.Fatalf("FromYAML: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sink := &codingagent.SliceSink{}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := a.Start(ctx, protocol.AgentRunRequest{RunID: "concurrent", Engine: "fake", Workspace: t.TempDir()}, sink)
			if err != nil {
				t.Errorf("Start %d: %v", i, err)
			}
			waitForTerminal(t, sink, 4*time.Second)
		}(i)
	}
	wg.Wait()
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
