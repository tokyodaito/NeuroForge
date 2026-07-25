package fake

import (
	"context"
	"encoding/json"
	"io"
	"os/signal"
	"syscall"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// RunParams builds the runtime parameters for the executable modes
// (command/jsonrpc). It is the exported constructor used by
// cmd/fake-coding-agent so that the internal runParams layout can evolve
// without churning the binary.
func RunParams(workspace, engine, model, runID, sessionID string, scenario Scenario, isResume bool) runParams {
	return runParams{
		workspace:     workspace,
		engine:        engine,
		model:         model,
		runID:         runID,
		sessionID:     sessionID,
		scenario:      scenario,
		startIsResume: isResume,
	}
}

// jsonlEmitter writes normalized events as JSONL (one NormalizedEvent per line)
// to stdout, matching the declarative adapter --output jsonl contract (spec
// §13.1). Raw malformed lines are written verbatim. It is the executable's
// command-mode emitter.
type jsonlEmitter struct {
	w         io.Writer
	workspace string
}

func (e *jsonlEmitter) emit(_ context.Context, ev protocol.NormalizedEvent) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = e.w.Write(b)
	return err
}

func (e *jsonlEmitter) emitRaw(_ context.Context, line string) error {
	_, err := io.WriteString(e.w, line+"\n")
	return err
}

func (e *jsonlEmitter) write(_ context.Context, rel, content string) error {
	return fileWrite(e.workspace, rel, content)
}

// gitAddAll runs `git add -A` inside the executable's workspace. Used by the
// write-commit scenario.
func (e *jsonlEmitter) gitAddAll(_ context.Context) error {
	if e.workspace == "" {
		return nil
	}
	return gitInWorkspace(e.workspace, "add", "-A")
}

// gitCommit runs `git commit` inside the executable's workspace.
func (e *jsonlEmitter) gitCommit(_ context.Context, msg string) error {
	if e.workspace == "" {
		return nil
	}
	return gitInWorkspace(e.workspace, "commit", "-m", msg,
		"--author=NeuroForge Fake <fake@neuroforge.local>")
}

// RunCommand runs the fake agent in declarative command (JSONL) mode: it replays
// the scenario, writing normalized events as JSONL to stdout, malformed lines
// verbatim, and stderr for failures. It returns the process exit code. This is
// the entry point invoked by cmd/fake-coding-agent --mode command and exercised
// by the declarative command adapter.
//
// The replay context is cancelled on SIGINT/SIGTERM so hang scenarios (timeout,
// cancellation) block until the supervisor kills the process group, rather than
// tripping Go's all-goroutines-asleep deadlock detector.
func RunCommand(stdout, stderr io.Writer, p runParams) int {
	em := &jsonlEmitter{w: stdout, workspace: p.workspace}
	sc := resolveScenario(p.scenario, p)
	// Resume forces the first event to run.resumed.
	if p.startIsResume && len(sc.steps) > 0 && sc.steps[0].event != nil {
		sc.steps[0].event.kind = "run.resumed"
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	out, _ := replay(ctx, sc, p, em)
	if out.stderr != "" {
		_, _ = io.WriteString(stderr, out.stderr)
	}
	return out.exitCode
}
