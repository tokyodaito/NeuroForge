package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/fake"
	"neuroforge/internal/adapter/codingagent/protocol"
)

// This file holds deterministic test infrastructure only: a recorded
// byte-stream "replay" process + spawner, the fake.Scenario → Claude stream
// fixture mapping, and sink/adapter helpers. No real Claude Code install and no
// paid calls are ever required (rule §36.5).

// replaySpec describes a recorded Claude Code stream-json run used in place of
// the real CLI. lines are emitted verbatim (each with a trailing '\n'); hang
// keeps the stream open (no EOF) after the lines until Kill; exitCode is
// returned by Wait; stderr is written to the stderr sink up front.
type replaySpec struct {
	lines    []string
	hang     bool
	exitCode int
	stderr   string
}

// replayProc is a [process] backed by an in-memory pipe + goroutine. It is the
// deterministic stand-in for the real `claude` process group in tests.
type replayProc struct {
	pr       *io.PipeReader
	pw       *io.PipeWriter
	killCh   chan struct{}
	doneCh   chan struct{}
	exitCode int
	once     sync.Once
}

func newReplayProc(spec replaySpec, stderr io.Writer) *replayProc {
	pr, pw := io.Pipe()
	rp := &replayProc{pr: pr, pw: pw, killCh: make(chan struct{}), doneCh: make(chan struct{}), exitCode: spec.exitCode}
	if stderr != nil && spec.stderr != "" {
		_, _ = stderr.Write([]byte(spec.stderr))
	}
	go func() {
		defer close(rp.doneCh)
		defer pr.Close() // unblock any pending reader on EOF/kill
		for _, line := range spec.lines {
			select {
			case <-rp.killCh:
				return
			default:
			}
			if _, err := pw.Write(append([]byte(line), '\n')); err != nil {
				return
			}
		}
		if spec.hang {
			<-rp.killCh
			return
		}
		_ = pw.Close()
	}()
	return rp
}

func (rp *replayProc) Stdout() io.Reader { return rp.pr }
func (rp *replayProc) Start() error      { return nil }
func (rp *replayProc) Kill() error {
	rp.once.Do(func() { close(rp.killCh) })
	_ = rp.pr.Close() // ensure a blocked reader unblocks even if the writer is mid-select
	return nil
}

func (rp *replayProc) Wait() error {
	<-rp.doneCh
	if rp.exitCode == 0 {
		return nil
	}
	return replayExitErr{code: rp.exitCode}
}

// replayExitErr carries a deterministic exit code so production exitCodeFrom can
// decode it via the ExitCoder interface.
type replayExitErr struct{ code int }

func (e replayExitErr) Error() string { return "replay exited " + strconv.Itoa(e.code) }
func (e replayExitErr) ExitCode() int { return e.code }

// replaySpawner returns a [spawner] that always produces the same recorded run.
func replaySpawner(spec replaySpec) spawner {
	return func(argv []string, dir string, env []string, stdin io.Reader, stderr io.Writer) (process, error) {
		_ = argv
		_ = dir
		_ = env
		_ = stdin
		return newReplayProc(spec, stderr), nil
	}
}

// ---- recorded Claude stream-json fixtures ----

// claudeLine builds a compact JSON line from a map (deterministic key order is
// not required for parsing, only for equality-free assertions).
func claudeLine(m map[string]any) string {
	b, _ := json.Marshal(m)
	return string(b)
}

// fixtureForScenario returns a recorded Claude Code stream-json byte stream that
// exhibits the requested fake-agent behaviour. These are deterministic recorded
// fixtures (rule §36.5: no real paid calls); they mimic the real `claude -p
// --output-format stream-json` output shape documented in
// docs/adapters/claude.md.
func fixtureForScenario(s fake.Scenario) replaySpec {
	const sess = "claude-session-test"
	initLine := claudeLine(map[string]any{
		"type": "system", "subtype": "init", "session_id": sess, "model": "sonnet",
	})
	assistant := claudeLine(map[string]any{
		"type": "assistant", "session_id": sess,
		"message": map[string]any{
			"content": []map[string]any{{"type": "text", "text": "Hello from Claude"}},
		},
	})
	successResult := claudeLine(map[string]any{
		"type": "result", "subtype": "success", "is_error": false,
		"session_id": sess, "num_turns": 3, "result": "Done.",
		"total_cost_usd": 0.0123,
		"usage": map[string]any{
			"input_tokens": 1234, "output_tokens": 567,
			"cache_creation_input_tokens": 89, "cache_read_input_tokens": 10,
		},
	})
	mkErrResult := func(subtype string, errors ...string) string {
		return claudeLine(map[string]any{
			"type": "result", "subtype": subtype, "is_error": true,
			"session_id": sess, "total_cost_usd": 0.0,
			"usage":  map[string]any{},
			"errors": errors,
		})
	}

	switch s {
	case fake.ScenarioSuccess, fake.ScenarioUsageEvents, fake.ScenarioQuotaAfterEdits,
		fake.ScenarioScopeViolation, fake.ScenarioResume:
		return replaySpec{lines: []string{initLine, assistant, successResult}, exitCode: 0}
	case fake.ScenarioMalformedJSON:
		return replaySpec{lines: []string{initLine, "{not valid json", assistant, successResult}, exitCode: 0}
	case fake.ScenarioQuotaBeforeEdits:
		return replaySpec{lines: []string{initLine, mkErrResult("error_during_execution", "billing_error: subscription quota exhausted")}, exitCode: 2}
	case fake.ScenarioRateLimit:
		return replaySpec{lines: []string{initLine, mkErrResult("error_during_execution", "rate_limit: HTTP 429 too many requests")}, exitCode: 2}
	case fake.ScenarioAuthFailure:
		return replaySpec{lines: []string{initLine, mkErrResult("error_during_execution", "authentication_failed: invalid credentials")}, exitCode: 2}
	case fake.ScenarioCrash:
		return replaySpec{lines: []string{initLine}, exitCode: 134, stderr: "claude panicked (simulated crash)"}
	case fake.ScenarioPartialOutput:
		return replaySpec{lines: []string{initLine, assistant}, exitCode: 1}
	case fake.ScenarioCancellation, fake.ScenarioTimeout:
		return replaySpec{lines: []string{initLine}, hang: true, exitCode: 137}
	default:
		return replaySpec{lines: []string{initLine, assistant, successResult}, exitCode: 0}
	}
}

// ---- shared sink/run helpers ----

func waitTerminal(s *codingagent.SliceSink, timeout time.Duration) []protocol.NormalizedEvent {
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

func typesOf(evs []protocol.NormalizedEvent) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = string(e.Type)
	}
	return out
}

func hasWarning(evs []protocol.NormalizedEvent) bool {
	for _, e := range evs {
		if e.Type == protocol.EventWarning {
			return true
		}
	}
	return false
}

// newTestAdapter builds an adapter wired to recorded fixtures for a scenario:
// LookPath/Probe are stubbed so Detect/Version/Health are deterministic and
// paid-call-free.
func newTestAdapter(t requiringT, scenario fake.Scenario, extra ...func(*Options)) *Adapter {
	t.Helper()
	opts := Options{
		BinaryPath: "claude", // bypass real LookPath
		LookPath:   func(string) (string, error) { return "claude", nil },
		Probe: func(ctx context.Context, name string, args []string, env []string) ([]byte, []byte, int, error) {
			switch {
			case len(args) > 0 && args[0] == "--version":
				return []byte("2.1.205 (Claude Code)\n"), nil, 0, nil
			case len(args) >= 2 && args[0] == "auth" && args[1] == "status":
				return []byte(`{"loggedIn":true,"account":"test"}`), nil, 0, nil
			}
			return nil, nil, 1, nil
		},
		Spawn:        replaySpawner(fixtureForScenario(scenario)),
		ArtifactsDir: t.TempDir(),
	}
	for _, fn := range extra {
		fn(&opts)
	}
	a, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

type requiringT interface {
	Helper()
	TempDir() string
	Fatalf(string, ...any)
}

// startTestRun is a convenience that starts a run and returns handle + sink.
func startTestRun(t requiringT, a *Adapter, runID string) (protocol.RunHandle, *codingagent.SliceSink) {
	t.Helper()
	sink := &codingagent.SliceSink{}
	handle, err := a.Start(context.Background(), protocol.AgentRunRequest{
		RunID:     runID,
		Engine:    a.ID(),
		Model:     "sonnet",
		Workspace: os.TempDir(),
	}, sink)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return handle, sink
}

// argvContains reports whether argv contains the given token.
func argvContains(argv []string, tok string) bool {
	for _, a := range argv {
		if a == tok {
			return true
		}
	}
	return false
}

func argvJoin(argv []string) string { return strings.Join(argv, " ") }

// fmtLine is a tiny helper for readable failure messages.
func fmtLine(format string, args ...any) string { return fmt.Sprintf(format, args...) }
