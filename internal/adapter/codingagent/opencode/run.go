package opencode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/proctree"
	"neuroforge/internal/adapter/codingagent/protocol"
)

// runProcess abstracts the spawned agent process group so the run pipeline
// (stream → parse → supervise → cancel) can be exercised offline with recorded
// byte streams. The production implementation wraps a
// [proctree.NewGroupCommand] *exec.Cmd; tests inject a stub. Kill() must
// terminate the ENTIRE process group (spec: cancellation ends the whole process
// group).
type runProcess interface {
	// Stdout returns the JSONL event stream.
	Stdout() io.Reader
	// Stderr returns captured (already-redacted) stderr.
	Stderr() string
	// Wait blocks until the process exits and returns its error (nil on exit 0).
	Wait() error
	// Kill terminates the whole process group, best-effort and idempotent.
	Kill() error
}

// spawnFunc builds and starts an agent process group from a fully-resolved argv,
// working directory and allowlisted environment, returning a [runProcess]. The
// production implementation uses [proctree.NewGroupCommand].
type spawnFunc func(argv []string, dir string, env []string) (runProcess, error)

// proctreeRun is the production runProcess, wrapping a proctree *exec.Cmd.
type proctreeRun struct {
	cmd    *exec.Cmd
	stdout io.ReadCloser
	stderr *bytes.Buffer
}

func (r *proctreeRun) Stdout() io.Reader { return r.stdout }
func (r *proctreeRun) Stderr() string    { return r.stderr.String() }
func (r *proctreeRun) Wait() error       { return r.cmd.Wait() }
func (r *proctreeRun) Kill() error       { return proctree.KillGroup(r.cmd, proctree.SigKill) }

// defaultSpawn is the production spawn hook: it configures a new process group,
// pins the working directory to the workspace, applies the allowlisted
// environment and pipes stdout/stderr. It never starts a long-running server.
func defaultSpawn(argv []string, dir string, env []string) (runProcess, error) {
	if len(argv) == 0 {
		return nil, errors.New("opencode: empty argv")
	}
	cmd := proctree.NewGroupCommand(argv[0], argv[1:]...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("opencode: stdout pipe: %w", err)
	}
	cmd.Stderr = &bytes.Buffer{}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("opencode: start %q: %w", argv[0], err)
	}
	return &proctreeRun{cmd: cmd, stdout: stdout, stderr: cmd.Stderr.(*bytes.Buffer)}, nil
}

// runOptions are the per-run inputs shared by Start and Resume.
type runOptions struct {
	runID     string
	engine    string
	model     string
	workspace string
	sessionID string
	timeout   time.Duration
	isResume  bool
}

// runCommon drives one headless run: spawns the process, streams JSONL, parses
// each line, persists malformed lines as artifacts, forwards typed events to the
// sink and guarantees a terminal run.* event. It honours req.Timeout and
// caller cancellation, terminating the whole process group on either (spec:
// cancellation ends the whole process group; never block forever on a stdout
// read).
func (a *Adapter) runCommon(ctx context.Context, req protocol.AgentRunRequest, sink codingagent.EventSink, isResume bool) (protocol.RunHandle, error) {
	runID := req.RunID
	if runID == "" {
		runID = "opencode-run"
	}
	engine := req.Engine
	if engine == "" {
		engine = a.ID()
	}
	argv := a.buildArgv(req, isResume)
	env := buildEnv(req.AllowlistEnv)

	proc, err := a.spawn(argv, req.Workspace, env)
	if err != nil {
		return protocol.RunHandle{}, err
	}

	runCtx, cancel := context.WithCancel(ctx)
	st := &runState{
		proc:     proc,
		cancel:   cancel,
		runID:    runID,
		engine:   engine,
		model:    req.Model,
		timeout:  req.Timeout,
		isResume: isResume,
	}
	a.mu.Lock()
	a.runs[runID] = st
	a.mu.Unlock()

	handle := protocol.RunHandle{
		RunID:     runID,
		Engine:    engine,
		Model:     req.Model,
		Account:   req.Account,
		SessionID: req.SessionID,
	}

	go a.supervise(runCtx, st, sink)
	return handle, nil
}

// readResult carries one scanned line from the reader goroutine.
type readResult struct {
	line    []byte
	hasMore bool
}

// supervise is the per-run supervision loop. It is responsive to cancellation
// and timeout: the blocking stdout read runs in a goroutine so a context
// cancellation or timeout can preempt it and terminate the group.
func (a *Adapter) supervise(ctx context.Context, st *runState, sink codingagent.EventSink) {
	defer func() {
		a.mu.Lock()
		delete(a.runs, st.runID)
		a.mu.Unlock()
	}()

	// Open with a synthetic run.started/run.resumed so event ordering is
	// guaranteed regardless of whether the engine emits one itself (spec §12.4
	// event ordering). Duplicate opens are harmless.
	openType := protocol.EventRunStarted
	if st.isResume {
		openType = protocol.EventRunResumed
	}
	if err := sink.OnEvent(ctx, openEvent(openType, st)); err != nil {
		// Consumer aborted before any output; kill and stop.
		_ = st.proc.Kill()
		_ = st.proc.Wait()
		return
	}

	scanner := newJSONLScanner(st.proc.Stdout())
	ch := make(chan readResult, 1)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			line, hasMore := scanner.next()
			select {
			case ch <- readResult{line: line, hasMore: hasMore}:
			case <-ctx.Done():
				return
			}
			if !hasMore && line == nil {
				return
			}
		}
	}()

	var timerCh <-chan time.Time
	if st.timeout > 0 {
		t := time.NewTimer(st.timeout)
		defer t.Stop()
		timerCh = t.C
	}

	sawTerminal := false
	for {
		select {
		case <-ctx.Done():
			_ = st.proc.Kill()
			<-readerDone
			a.finalizeTerminal(st, sink, sawTerminal)
			return
		case <-timerCh:
			// Record the timeout intent BEFORE the kill so a kill-induced EOF
			// cannot synthesize a non-timeout terminal (KF-09 / I.9).
			st.timedOut.Store(true)
			_ = st.proc.Kill()
			<-readerDone
			a.finalizeTerminal(st, sink, sawTerminal)
			return
		case res := <-ch:
			if res.line == nil && !res.hasMore {
				// EOF: process closed stdout. Route through the single terminal
				// decision, which honours a cancel/timeout intent recorded
				// before any kill (so a kill-induced EOF never misclassifies).
				<-readerDone
				a.finalizeTerminal(st, sink, sawTerminal)
				return
			}
			if res.line == nil {
				continue
			}
			ev, hasContent, perr := parseLine(res.line)
			if !hasContent {
				continue
			}
			// Once a terminal event has been delivered, no further events are
			// expected for this run (spec §12.4). Keep draining the pipe so the
			// child cannot deadlock on a full stdout buffer, but forward nothing
			// past the terminal.
			if sawTerminal {
				continue
			}
			if perr != nil {
				// Malformed/unknown line: persist (redacted) and emit the
				// warning event produced by ParseEventLine (never fatal).
				a.saveMalformed(st.runID, res.line)
				if ev.Type != "" {
					a.forward(ctx, st, sink, ev)
				}
				continue
			}
			if ev.Type == protocol.EventUsageUpdated && ev.Usage != nil {
				mapped := *ev.Usage
				ev.Usage = ptr(mapUsage(mapped))
			}
			if ev.Type.IsTerminal() {
				sawTerminal = true
			}
			if err := a.forward(ctx, st, sink, ev); err != nil {
				_ = st.proc.Kill()
				<-readerDone
				_ = st.proc.Wait()
				return
			}
		}
	}
}

// forward stamps run/engine/model on an event (when absent), redacts
// leak-prone fields (Raw/Warning/Failure reason) and delivers it, returning the
// sink error so the loop can abort on consumer cancellation.
func (a *Adapter) forward(ctx context.Context, st *runState, sink codingagent.EventSink, ev protocol.NormalizedEvent) error {
	if ev.RunID == "" {
		ev.RunID = st.runID
	}
	if ev.Engine == "" {
		ev.Engine = st.engine
	}
	if ev.Model == "" {
		ev.Model = st.model
	}
	return sink.OnEvent(ctx, redactEvent(ev))
}

// finalizeTerminal is the SINGLE terminal decision point for a run (KF-09 /
// invariant I.9). It waits for the process, then — if the engine did not emit
// its own terminal — decides exactly one terminal event with priority
// timeout > cancellation > natural exit. The cancel/timeout intents are
// recorded before any kill, so a kill-induced EOF always observes them and can
// never misclassify. Calling it more than once is harmless because the run is
// already unregistered by the time a second path could reach it.
func (a *Adapter) finalizeTerminal(st *runState, sink codingagent.EventSink, sawTerminal bool) {
	waitErr := st.proc.Wait()
	exitCode := exitCodeFrom(waitErr)
	stderr := redactSecrets(st.proc.Stderr())
	if sawTerminal {
		return
	}
	var ev protocol.NormalizedEvent
	switch {
	case st.timedOut.Load():
		ev = terminalTimeout(st)
	case st.cancelRequested.Load():
		ev = terminalCancel(st)
	default:
		ev = a.synthesizeFromExit(st, exitCode, stderr)
	}
	_ = sink.OnEvent(context.Background(), ev)
}

// synthesizeFromExit builds a terminal event from the process outcome when the
// engine did not emit one and no cancel/timeout intent was recorded. Exit 0 →
// run.completed; non-zero → run.failed classified via ClassifyFailure.
func (a *Adapter) synthesizeFromExit(st *runState, exitCode int, stderr string) protocol.NormalizedEvent {
	if exitCode == 0 {
		return openEvent(protocol.EventRunCompleted, st)
	}
	fc := a.ClassifyFailure(exitCode, nil, stderr)
	return protocol.NormalizedEvent{
		Type:      protocol.EventRunFailed,
		Timestamp: time.Now(),
		RunID:     st.runID,
		Engine:    st.engine,
		Model:     st.model,
		Failure:   &protocol.FailurePayload{Class: fc.Class, Reason: fc.Reason, ExitCode: exitCode},
	}
}

// saveMalformed persists a (redacted) malformed agent output line to the
// artifacts dir for forensics (spec: malformed event saved to artifacts).
func (a *Adapter) saveMalformed(runID string, line []byte) {
	dir := a.opts.ArtifactsDir
	if dir == "" {
		dir = os.TempDir()
	}
	name := fmt.Sprintf("opencode-malformed-%s-%d.txt", sanitize(runID), time.Now().UnixNano())
	_ = os.WriteFile(filepath.Join(dir, name), redactBytes(line), 0o600)
}

// sessionResume would select the opening event; the selection now lives inline
// in supervise via st.isResume, so this placeholder is intentionally absent.

// helpers ----------------------------------------------------------------

func openEvent(t protocol.EventType, st *runState) protocol.NormalizedEvent {
	return protocol.NormalizedEvent{
		Type:      t,
		Timestamp: time.Now(),
		RunID:     st.runID,
		Engine:    st.engine,
		Model:     st.model,
	}
}

func terminalCancel(st *runState) protocol.NormalizedEvent {
	return protocol.NormalizedEvent{
		Type: protocol.EventRunCancelled, Timestamp: time.Now(), RunID: st.runID, Engine: st.engine, Model: st.model,
		Failure: &protocol.FailurePayload{Class: protocol.FailureCancelled, Reason: "cancelled by caller"},
	}
}

func terminalTimeout(st *runState) protocol.NormalizedEvent {
	return protocol.NormalizedEvent{
		Type: protocol.EventRunFailed, Timestamp: time.Now(), RunID: st.runID, Engine: st.engine, Model: st.model,
		Failure: &protocol.FailurePayload{Class: protocol.FailureTimeout, Reason: "run exceeded its wall-clock timeout", ExitCode: 124},
	}
}

func ptr[T any](v T) *T { return &v }

// exitCoder is satisfied by *exec.ExitError (production) and by test stub
// errors, so exit codes are recovered uniformly.
type exitCoder interface{ ExitCode() int }

func exitCodeFrom(err error) int {
	if err == nil {
		return 0
	}
	var ec exitCoder
	if errors.As(err, &ec) {
		return ec.ExitCode()
	}
	return 1
}

func sanitize(s string) string {
	var b []byte
	for _, r := range s {
		if r == '/' || r == ' ' || r == ':' || r == '\\' {
			b = append(b, '_')
			continue
		}
		b = append(b, []byte(string(r))...)
	}
	if b == nil {
		return s
	}
	return string(b)
}
