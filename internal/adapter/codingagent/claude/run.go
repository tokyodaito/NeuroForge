package claude

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/proctree"
	"neuroforge/internal/adapter/codingagent/protocol"
)

// cancelCause distinguishes why a run's context was cancelled so the supervisor
// can emit the correct terminal event (run.cancelled vs run.failed(TIMEOUT)).
type cancelCause int

const (
	cancelNone cancelCause = iota
	cancelUser
	cancelTimeout
)

// Adapter is the in-process Claude Code coding-agent adapter. Construct one
// with [New]; it is safe for concurrent use across independent runs.
type Adapter struct {
	opts Options

	// version probe cache (detectedVersion).
	vonce     sync.Once
	cachedVer parsedVersion

	mu   sync.Mutex
	runs map[string]*runState
}

// runState tracks one live run for cancellation and session-id capture.
type runState struct {
	proc   process
	cancel context.CancelFunc
	done   chan struct{}

	causeMu sync.Mutex
	cause   cancelCause

	timer *time.Timer // natural req.Timeout

	stderr *bytes.Buffer

	sessMu  sync.Mutex
	session string
}

func (s *runState) markCause(c cancelCause) {
	s.causeMu.Lock()
	if s.cause == cancelNone {
		s.cause = c
	}
	s.causeMu.Unlock()
}

func (s *runState) currentCause() cancelCause {
	s.causeMu.Lock()
	defer s.causeMu.Unlock()
	return s.cause
}

func (s *runState) setSession(id string) {
	if id == "" {
		return
	}
	s.sessMu.Lock()
	if s.session == "" {
		s.session = id
	}
	s.sessMu.Unlock()
}

func (s *runState) sessionID() string {
	s.sessMu.Lock()
	defer s.sessMu.Unlock()
	return s.session
}

// ID implements codingagent.Adapter. Always "claude" (spec §12.1 engine id).
func (a *Adapter) ID() string { return EngineID }

// ---- process seam (production: proctree; tests: recorded byte stream) ----

// process abstracts a spawned agent process group. The supervisor only needs a
// stdout reader, a start/wait lifecycle and a whole-group kill. The production
// implementation ([execProcess]) is built by [proctreeSpawner] and uses
// [proctree.NewGroupCommand] + [proctree.KillGroup], so cancellation always
// terminates the entire process tree (setpgid + negative-pgid signal).
type process interface {
	// Stdout returns the stream of newline-delimited Claude SDK messages.
	Stdout() io.Reader
	// Start begins the process; Stdout/Stderr/Dir/Env/Stdin must be wired first.
	Start() error
	// Wait blocks until the process exits and returns its wait error (nil on
	// exit 0). It must be callable after the stdout stream reaches EOF.
	Wait() error
	// Kill terminates the whole process group, best-effort and idempotent.
	Kill() error
}

// spawner builds a long-lived agent process for a run. argv[0] is the resolved
// binary; dir/env/stdin/stderr are applied to the child.
type spawner func(argv []string, dir string, env []string, stdin io.Reader, stderr io.Writer) (process, error)

// execProcess is the production [process], backed by a proctree group command.
type execProcess struct {
	cmd    *exec.Cmd
	stdout io.ReadCloser
}

func (e *execProcess) Stdout() io.Reader { return e.stdout }
func (e *execProcess) Start() error      { return e.cmd.Start() }
func (e *execProcess) Wait() error       { return e.cmd.Wait() }
func (e *execProcess) Kill() error       { return proctree.KillGroup(e.cmd, proctree.SigKill) }

// proctreeSpawner is the default [spawner]: it builds a new process group via
// [proctree.NewGroupCommand] so [execProcess.Kill] reaches every descendant.
func proctreeSpawner(argv []string, dir string, env []string, stdin io.Reader, stderr io.Writer) (process, error) {
	if len(argv) == 0 {
		return nil, errors.New("claude: empty argv")
	}
	cmd := proctree.NewGroupCommand(argv[0], argv[1:]...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = env
	cmd.Stdin = stdin
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("claude: stdout pipe: %w", err)
	}
	return &execProcess{cmd: cmd, stdout: stdout}, nil
}

// Start implements codingagent.Adapter (spec §12.2). It spawns the headless
// `claude -p` process group, streams normalized events to sink, and returns a
// live handle. The request never carries credentials (AC-28).
func (a *Adapter) Start(ctx context.Context, req protocol.AgentRunRequest, sink codingagent.EventSink) (protocol.RunHandle, error) {
	return a.startRun(ctx, req, sink, false)
}

// Resume implements codingagent.Adapter. Session resume is supported via
// `--resume <session>` when the CLI is new enough (see capabilities.go).
func (a *Adapter) Resume(ctx context.Context, req protocol.ResumeRequest, sink codingagent.EventSink) (protocol.RunHandle, error) {
	ar := protocol.AgentRunRequest{
		RunID:        req.RunID,
		Engine:       req.Engine,
		Model:        req.Model,
		Account:      req.Account,
		Workspace:    req.Workspace,
		PromptFile:   "",
		Prompt:       "",
		Scope:        req.Scope,
		AllowlistEnv: req.AllowlistEnv,
		TurnLimit:    req.TurnLimit,
		Timeout:      req.Timeout,
		SessionID:    req.SessionID,
	}
	return a.startRun(ctx, ar, sink, true)
}

// SendMessage implements codingagent.Adapter. Headless text-mode reads the
// prompt once via stdin; live mid-run injection is not supported, so this always
// returns a terminal error (consistent with Capabilities().LiveUserMessages ==
// false). LiveUserMessages support is explicitly not implemented (rule §36.25).
func (a *Adapter) SendMessage(context.Context, protocol.RunHandle, protocol.AgentMessage) error {
	return errors.New("claude: live user messages are not supported in headless -p mode (LiveUserMessages=false)")
}

func (a *Adapter) startRun(ctx context.Context, req protocol.AgentRunRequest, sink codingagent.EventSink, isResume bool) (protocol.RunHandle, error) {
	if sink == nil {
		return protocol.RunHandle{}, errors.New("claude: nil event sink")
	}
	bin, err := a.binary()
	if err != nil {
		return protocol.RunHandle{}, errNotInstalled
	}
	runID := req.RunID
	if runID == "" {
		runID = newRunID()
	}
	argv := a.buildArgv(bin, req, isResume)
	env := buildEnv(req.AllowlistEnv)

	prompt, perr := a.resolvePrompt(req)
	if perr != nil {
		return protocol.RunHandle{}, perr
	}
	var stdin io.Reader
	if a.useStdin() {
		stdin = bytes.NewBufferString(prompt)
	}

	stderr := &bytes.Buffer{}
	proc, err := a.opts.Spawn(argv, req.Workspace, env, stdin, stderr)
	if err != nil {
		return protocol.RunHandle{}, fmt.Errorf("claude: spawn: %s", redactBytes(err.Error()))
	}
	if err := proc.Start(); err != nil {
		return protocol.RunHandle{}, fmt.Errorf("claude: start agent: %s", redactBytes(err.Error()))
	}

	runCtx, cancel := context.WithCancel(ctx)
	st := &runState{proc: proc, cancel: cancel, done: make(chan struct{}), stderr: stderr}
	if req.Timeout > 0 {
		st.timer = time.AfterFunc(req.Timeout, func() {
			st.markCause(cancelTimeout)
			cancel()
		})
	}
	// a.runs is pre-allocated in New; insert under a.mu so concurrent Starts
	// never race on the registry. Run supervision/streaming stays parallel.
	a.mu.Lock()
	a.runs[runID] = st
	a.mu.Unlock()

	engine := req.Engine
	if engine == "" {
		engine = EngineID
	}
	handle := protocol.RunHandle{
		RunID:     runID,
		Engine:    engine,
		Model:     req.Model,
		Account:   req.Account,
		SessionID: req.SessionID,
	}

	go a.supervise(runCtx, st, runID, engine, req.Model, sink, isResume)
	return handle, nil
}

// readChunk is one unit produced by the line-reader goroutine.
type readChunk struct {
	line    []byte
	hasMore bool
}

// supervise reads the JSONL stream, translates each Claude SDK message to
// normalized events, forwards them to sink, captures the session id, persists
// malformed lines as artifacts, and guarantees a terminal event. It is
// responsive to cancellation: the blocking pipe read happens in a goroutine so
// ctx cancellation preempts it and terminates the whole process group (spec:
// cancellation ends the whole process group). It never blocks forever on a
// stdout read.
func (a *Adapter) supervise(ctx context.Context, st *runState, runID, engine, model string, sink codingagent.EventSink, isResume bool) {
	defer func() {
		st.cancel()
		if st.timer != nil {
			st.timer.Stop()
		}
		a.mu.Lock()
		delete(a.runs, runID)
		a.mu.Unlock()
		close(st.done)
	}()

	now := a.opts.Now
	mk := func(t protocol.EventType) protocol.NormalizedEvent {
		return protocol.NormalizedEvent{Type: t, Timestamp: now(), RunID: runID, Engine: engine, Model: model}
	}

	// Emit run.started/run.resumed first to guarantee event ordering.
	firstType := protocol.EventRunStarted
	if isResume {
		firstType = protocol.EventRunResumed
	}
	if err := sink.OnEvent(ctx, mk(firstType)); err != nil {
		a.abortRun(ctx, st)
		return
	}

	reader := newLineReader(stripBOM(st.proc.Stdout()))
	ch := make(chan readChunk, 1)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			line, hasMore := reader.Next()
			select {
			case ch <- readChunk{line: line, hasMore: hasMore}:
			case <-ctx.Done():
				return
			}
			if !hasMore && line == nil {
				return
			}
		}
	}()

	tc := transCtx{runID: runID, engine: engine, model: model, now: now}
	sawTerminal := false

	readEOF := false
	for !readEOF && !sawTerminal {
		select {
		case <-ctx.Done():
			a.abortRun(ctx, st)
			cause := st.currentCause()
			term := mk(protocol.EventRunCancelled)
			if cause == cancelTimeout {
				term = mk(protocol.EventRunFailed)
				term.Failure = &protocol.FailurePayload{Class: protocol.FailureTimeout, Reason: "claude: run exceeded its wall-clock timeout", ExitCode: 0}
			} else {
				term.Failure = &protocol.FailurePayload{Class: protocol.FailureCancelled, Reason: "cancelled by caller"}
			}
			_ = sink.OnEvent(context.Background(), term)
			return
		case res := <-ch:
			if res.line == nil && !res.hasMore {
				readEOF = true
				continue
			}
			if res.line == nil {
				continue
			}
			evs := a.translate(res.line, &tc)
			// Capture the session id from any line that carried one (system/init
			// or result), even when translate produced no events.
			if tc.session != "" {
				st.setSession(tc.session)
			}
			for _, ev := range evs {
				if ev.Timestamp.IsZero() {
					ev.Timestamp = now()
				}
				if ev.RunID == "" {
					ev.RunID = runID
				}
				if ev.Engine == "" {
					ev.Engine = engine
				}
				if ev.Model == "" {
					ev.Model = model
				}
				if ev.Raw != nil {
					a.saveMalformed(runID, ev.Raw)
				}
				if ev.Type.IsTerminal() {
					sawTerminal = true
				}
				if err := sink.OnEvent(ctx, ev); err != nil {
					a.abortRun(ctx, st)
					return
				}
			}
		}
	}

	// Stream ended (EOF) before/after a terminal event. Wait for the process to
	// collect its exit code + captured stderr, then synthesize a terminal event
	// if the agent did not emit one itself (e.g. a crash).
	//
	// KF-09 / invariant I.9: a cancel/timeout cause was recorded BEFORE the kill
	// that induced this EOF, so check it first. A kill-induced EOF must never
	// synthesize a non-cancelled/non-timeout terminal from the SIGKILL exit
	// code. Priority: timeout > cancellation > natural exit.
	<-readerDone
	waitErr := st.proc.Wait()
	exitCode := exitCodeFrom(waitErr)
	if sawTerminal {
		return
	}
	switch st.currentCause() {
	case cancelTimeout:
		_ = sink.OnEvent(context.Background(), mkTimeoutTerminal(mk))
		return
	case cancelUser:
		_ = sink.OnEvent(context.Background(), mkCancelTerminal(mk))
		return
	}
	stderr := redact(st.stderr.String())
	fc := a.classifyInternal(exitCode, nil, stderr)
	term := mk(protocol.EventRunFailed)
	if exitCode == 0 && fc.Class == protocol.FailureInternalError {
		// Process exited 0 with no terminal event: treat as completed.
		term.Type = protocol.EventRunCompleted
		term.Failure = nil
	} else {
		term.Failure = &protocol.FailurePayload{Class: fc.Class, Reason: fc.Reason, ExitCode: exitCode}
	}
	_ = sink.OnEvent(context.Background(), term)
}

// mkCancelTerminal builds the run.cancelled terminal for the post-EOF path.
func mkCancelTerminal(mk func(protocol.EventType) protocol.NormalizedEvent) protocol.NormalizedEvent {
	term := mk(protocol.EventRunCancelled)
	term.Failure = &protocol.FailurePayload{Class: protocol.FailureCancelled, Reason: "cancelled by caller"}
	return term
}

// mkTimeoutTerminal builds the run.failed(TIMEOUT) terminal for the post-EOF
// path.
func mkTimeoutTerminal(mk func(protocol.EventType) protocol.NormalizedEvent) protocol.NormalizedEvent {
	term := mk(protocol.EventRunFailed)
	term.Failure = &protocol.FailurePayload{Class: protocol.FailureTimeout, Reason: "claude: run exceeded its wall-clock timeout", ExitCode: 0}
	return term
}

// abortRun kills the process group and waits for the reader goroutine to
// detach from the pipe. Used when the consumer aborts (sink error) — no
// terminal event is emitted in that path, mirroring the declarative adapter.
func (a *Adapter) abortRun(ctx context.Context, st *runState) {
	_ = st.proc.Kill()
}

// Cancel implements codingagent.Adapter. It marks the cause, cancels the run
// context (which drives [supervise] to emit run.cancelled and kill the group),
// and directly kills the process group as a safety net. It never blocks on the
// stdout read. Unknown runs return an error.
func (a *Adapter) Cancel(_ context.Context, handle protocol.RunHandle) error {
	a.mu.Lock()
	st, ok := a.runs[handle.RunID]
	a.mu.Unlock()
	if !ok {
		return fmt.Errorf("claude: unknown run %q", handle.RunID)
	}
	st.markCause(cancelUser)
	st.cancel()
	// Direct kill as a safety net; supervise's ctx handler also kills. Both are
	// idempotent. proctree.KillGroup tolerates an already-dead group.
	_ = st.proc.Kill()
	return nil
}

// SessionID returns the provider session id captured for a run (from the
// system/init or result message), or "" if the run is unknown / has not yet
// reported one. It is an additive accessor (not part of the Adapter interface)
// used to form continuation packs; the RunHandle returned by Start/Resume
// carries the caller-supplied session id for resume.
func (a *Adapter) SessionID(runID string) string {
	a.mu.Lock()
	st, ok := a.runs[runID]
	a.mu.Unlock()
	if !ok {
		return ""
	}
	return st.sessionID()
}

// WaitForRun blocks until the run's supervise goroutine finishes or the timeout
// elapses. Returns the session id captured (best-effort). Additive helper for
// tests and continuation-pack formation; not part of the Adapter interface.
func (a *Adapter) WaitForRun(runID string, timeout time.Duration) string {
	a.mu.Lock()
	st, ok := a.runs[runID]
	a.mu.Unlock()
	if !ok {
		return ""
	}
	select {
	case <-st.done:
	case <-time.After(timeout):
	}
	return st.sessionID()
}

// saveMalformed persists a malformed/unknown agent output line to the artifacts
// dir so it is recoverable for forensics (spec: malformed event saved +
// classified). Failures are silent (best-effort).
func (a *Adapter) saveMalformed(runID string, raw []byte) {
	dir := a.opts.artifactsDir()
	name := "claude-malformed-" + sanitizeRunID(runID) + "-" + nowStamp(a.opts.Now) + ".txt"
	path := joinPath(dir, name)
	_ = osWriteFile(path, raw, 0o600)
}

// exitCoder is implemented by errors that carry a process exit code (e.g.
// *exec.ExitError and the test replayExitErr). Recognising it lets exitCodeFrom
// decode deterministic non-zero codes from both real and stub processes.
type exitCoder interface {
	ExitCode() int
}

// exitCodeFrom extracts the process exit code from a wait error.
func exitCodeFrom(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	var ec exitCoder
	if errors.As(err, &ec) {
		return ec.ExitCode()
	}
	return 1
}

// newRunID mints a random run id when the caller did not supply one.
func newRunID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "claude-run"
	}
	return "claude-" + hex.EncodeToString(b[:])
}
