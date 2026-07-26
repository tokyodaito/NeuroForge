package gemini

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/protocol"
)

// ErrSessionResumeNotSupported is returned by Resume. The Gemini CLI exposes
// index-based session resume (`--resume latest|N`), which cannot be reliably
// mapped to NeuroForge's continuation-pack / arbitrary-session-id contract
// without a paid call or fragile `--list-sessions` parsing. Resume is therefore
// explicitly not implemented (spec §36.25), and SessionResume is false.
var ErrSessionResumeNotSupported = errors.New("gemini: session resume is not supported (see docs/adapters/gemini.md)")

// ErrLiveMessagesNotSupported is returned by SendMessage. Headless `-p` mode has
// no live message channel, so injecting a user message into a running run is not
// possible (LiveUserMessages is false).
var ErrLiveMessagesNotSupported = errors.New("gemini: live messages are not supported in headless mode")

// Start implements [codingagent.Adapter]. It resolves the CLI, builds the
// deterministic headless argv, spawns the agent in a new process group with an
// allowlisted environment, and streams normalized events to sink. Start returns
// the live handle immediately; the run supervises in a background goroutine.
//
// The request never carries credentials (AC-28); the account is name-only and
// resolved by the CLI from its own configured auth.
func (a *Adapter) Start(ctx context.Context, req protocol.AgentRunRequest, sink codingagent.EventSink) (protocol.RunHandle, error) {
	return a.startRun(ctx, req, sink, false)
}

// Resume implements [codingagent.Adapter]. Not implemented: see
// [ErrSessionResumeNotSupported]. Capabilities correctly reports
// SessionResume=false so callers do not attempt it.
func (a *Adapter) Resume(_ context.Context, _ protocol.ResumeRequest, _ codingagent.EventSink) (protocol.RunHandle, error) {
	return protocol.RunHandle{}, ErrSessionResumeNotSupported
}

// SendMessage implements [codingagent.Adapter]. Not implemented in headless
// mode: see [ErrLiveMessagesNotSupported].
func (a *Adapter) SendMessage(_ context.Context, _ protocol.RunHandle, _ protocol.AgentMessage) error {
	return ErrLiveMessagesNotSupported
}

// Cancel implements [codingagent.Adapter]. It records the cancel intent, then
// terminates the whole agent process group (spec: cancellation ends the whole
// process group, never orphaning descendants) and signals the run's context so
// the supervise loop emits run.cancelled. The intent is recorded before the
// kill (see [runState.requestCancel]) so a kill-induced EOF can never produce a
// non-cancelled terminal (KF-09 / invariant I.9).
func (a *Adapter) Cancel(_ context.Context, handle protocol.RunHandle) error {
	a.mu.Lock()
	st, ok := a.runs[handle.RunID]
	a.mu.Unlock()
	if !ok {
		return fmt.Errorf("gemini: unknown run %q", handle.RunID)
	}
	st.requestCancel()
	return nil
}

func (a *Adapter) startRun(ctx context.Context, req protocol.AgentRunRequest, sink codingagent.EventSink, isResume bool) (protocol.RunHandle, error) {
	binary, err := a.host.lookPath(a.opts.Binary)
	if err != nil {
		return protocol.RunHandle{}, fmt.Errorf("gemini: CLI not found: %w", err)
	}

	runID := req.RunID
	if runID == "" {
		runID = "gemini-run"
	}
	engine := req.Engine
	if engine == "" {
		engine = a.ID()
	}

	spec := buildRunSpec(binary, req, a.opts.ExtraArgs)
	env := buildEnv(req.AllowlistEnv)

	// When a prompt file is configured, pipe it to the child's stdin so large
	// context packs never overflow argv and never touch a shell. The file is
	// closed after the process is reaped; the OS keeps the fd open for the child.
	var stdin io.Reader
	if spec.promptFile != "" {
		f, openErr := os.Open(spec.promptFile)
		if openErr != nil {
			return protocol.RunHandle{}, fmt.Errorf("gemini: open prompt file: %w", openErr)
		}
		defer f.Close()
		stdin = f
	}

	proc, launchErr := a.host.launch(spec.argv0(), req.Workspace, env, stdin)
	if launchErr != nil {
		return protocol.RunHandle{}, fmt.Errorf("gemini: launch agent: %w", launchErr)
	}

	runCtx, cancel := a.deriveRunCtx(ctx, req.Timeout)
	st := &runState{proc: proc, cancel: cancel}
	a.mu.Lock()
	a.runs[runID] = st
	a.mu.Unlock()

	handle := protocol.RunHandle{
		RunID:   runID,
		Engine:  engine,
		Model:   req.Model,
		Account: req.Account,
	}

	md := frameMeta{runID: runID, engine: engine, model: req.Model, isResume: isResume}
	go a.supervise(runCtx, st, md, sink)
	return handle, nil
}

// deriveRunCtx wraps the caller context with the run's hard timeout when set.
// The deadline (not a manual timer) drives timeout classification: when it
// fires, supervise treats it as a TIMEOUT failure rather than a cancellation.
func (a *Adapter) deriveRunCtx(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout > 0 {
		return context.WithTimeout(ctx, timeout)
	}
	return context.WithCancel(ctx)
}

// supervise owns one run's lifecycle. It emits the frame-open event, streams the
// agent's stdout, parses it into normalized events, persists malformed output as
// artifacts, and guarantees exactly one terminal event — synthesized from the
// exit code when the agent did not emit one. It is fully responsive to
// cancellation and timeout: the blocking stdout read runs in a reader goroutine,
// and ctx cancellation kills the process group, unblocking the read.
func (a *Adapter) supervise(ctx context.Context, st *runState, md frameMeta, sink codingagent.EventSink) {
	defer a.unregister(md.runID)

	emit := func(ev protocol.NormalizedEvent) {
		_ = sink.OnEvent(ctx, ev)
	}
	// emitTerm delivers a terminal event on a fresh context so it is always
	// delivered even after the run's ctx has been cancelled (the terminal is the
	// run's result; dropping it would leave the caller waiting forever).
	emitTerm := func(ev protocol.NormalizedEvent) {
		_ = sink.OnEvent(context.Background(), ev)
	}

	// Frame-open event.
	openType := protocol.EventRunStarted
	if md.isResume {
		openType = protocol.EventRunResumed
	}
	emit(protocol.NormalizedEvent{
		Type: openType, Timestamp: time.Now(),
		RunID: md.runID, Engine: md.engine, Model: md.model,
	})

	// Stream stdout in a goroutine so ctx cancellation/timeout preempts the
	// blocking read (the group is killed, EOF unblocks the reader).
	type readOutcome struct {
		raw []byte
		err error
	}
	readCh := make(chan readOutcome, 1)
	go func() {
		raw, err := readAll(st.proc.stdout())
		readCh <- readOutcome{raw: raw, err: err}
	}()

	var raw []byte
	select {
	case <-ctx.Done():
		// Cancellation or hard deadline. Kill the group (idempotent) and drain
		// the reader so its goroutine exits cleanly. The terminal reason is
		// decided below from race-free sources — not here — so a kill-induced
		// EOF that races back into this goroutine cannot misclassify.
		_ = st.proc.kill()
		select {
		case o := <-readCh:
			raw = o.raw
		case <-time.After(2 * time.Second):
		}
	case o := <-readCh:
		raw = o.raw
	}

	// Persist malformed lines and surface them as recoverable warnings.
	res := parseStream(raw, md)
	for _, m := range res.malformed {
		a.saveMalformed(md.runID, m)
		emit(malformedWarning(md.runID, m))
	}
	for _, ev := range res.body {
		emit(ev)
	}

	// Terminal arbitration (KF-09 / invariant I.9): a SINGLE decision owned by
	// this goroutine. Priority per STATE_MACHINE §1.3:
	//   timeout (deadline) > cancellation > natural exit.
	// The deadline is baked into ctx at creation (race-free); the cancel intent
	// is recorded in st.cancelled BEFORE the kill (race-free). A kill-induced
	// EOF therefore always observes the cancel intent, so it can never leak a
	// non-cancelled terminal from synthesizedTerminal below.
	timedOut := ctx.Err() == context.DeadlineExceeded
	cancelled := !timedOut && (st.cancelled.Load() || ctx.Err() == context.Canceled)

	switch {
	case cancelled:
		emitTerm(a.cancelledTerminal(md, st))
		return
	case timedOut:
		emitTerm(a.timeoutTerminal(md, st))
		return
	}

	exitCode, stderr := st.proc.wait()
	stderr = redact(stderr)
	if res.terminal != nil {
		emitTerm(*res.terminal)
		return
	}
	emitTerm(a.synthesizedTerminal(md, exitCode, stderr, res.body))
}

// synthesizedTerminal builds the terminal event from the process outcome when
// the agent did not emit one. Exit 0 → run.completed; non-zero → run.failed
// classified via ClassifyFailure.
func (a *Adapter) synthesizedTerminal(md frameMeta, exitCode int, stderr string, body []protocol.NormalizedEvent) protocol.NormalizedEvent {
	if exitCode == 0 {
		return protocol.NormalizedEvent{
			Type: protocol.EventRunCompleted, Timestamp: time.Now(),
			RunID: md.runID, Engine: md.engine, Model: md.model,
		}
	}
	fc := a.ClassifyFailure(exitCode, body, stderr)
	return protocol.NormalizedEvent{
		Type: protocol.EventRunFailed, Timestamp: time.Now(),
		RunID: md.runID, Engine: md.engine, Model: md.model,
		Failure: &protocol.FailurePayload{
			Class: fc.Class, Reason: fc.Reason, ExitCode: exitCode,
		},
	}
}

func (a *Adapter) cancelledTerminal(md frameMeta, st *runState) protocol.NormalizedEvent {
	exitCode, _ := st.proc.wait()
	return protocol.NormalizedEvent{
		Type: protocol.EventRunCancelled, Timestamp: time.Now(),
		RunID: md.runID, Engine: md.engine, Model: md.model,
		Failure: &protocol.FailurePayload{
			Class: protocol.FailureCancelled, Reason: "cancelled by caller", ExitCode: exitCode,
		},
	}
}

func (a *Adapter) timeoutTerminal(md frameMeta, st *runState) protocol.NormalizedEvent {
	exitCode, _ := st.proc.wait()
	fc := protocol.DefaultPolicy(protocol.FailureTimeout)
	fc.ExitCode = exitCode
	return protocol.NormalizedEvent{
		Type: protocol.EventRunFailed, Timestamp: time.Now(),
		RunID: md.runID, Engine: md.engine, Model: md.model,
		Failure: &protocol.FailurePayload{
			Class: protocol.FailureTimeout, Reason: "run exceeded its hard timeout", ExitCode: exitCode,
		},
	}
}

// malformedWarning builds a recoverable warning event for a malformed output
// line. The raw bytes are persisted separately as an artifact.
func malformedWarning(runID string, raw []byte) protocol.NormalizedEvent {
	return protocol.NormalizedEvent{
		Type: protocol.EventWarning, Timestamp: time.Now(), RunID: runID,
		Warning: &protocol.WarningPayload{
			Code: "malformed.output", Message: "unparseable agent output; saved to artifacts",
			Recoverable: true,
		},
		Raw: append([]byte(nil), raw...),
	}
}

// saveMalformed persists a malformed agent output line to the artifacts dir so
// it is recoverable for forensics (spec: malformed event saved to artifacts).
// Bytes are redacted before writing so no credential leaks into the artifact.
func (a *Adapter) saveMalformed(runID string, raw []byte) {
	dir := a.artDir
	if dir == "" {
		dir = os.TempDir()
	}
	name := fmt.Sprintf("gemini-malformed-%s-%d.json", sanitize(runID), time.Now().UnixNano())
	_ = os.WriteFile(filepath.Join(dir, name), redactBytes(raw), 0o600)
}

func (a *Adapter) unregister(runID string) {
	a.mu.Lock()
	delete(a.runs, runID)
	a.mu.Unlock()
}

// sanitize makes a run id safe for use in an artifact filename.
func sanitize(s string) string {
	var b []byte
	for _, c := range []byte(s) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-' || c == '_':
			b = append(b, c)
		default:
			b = append(b, '_')
		}
	}
	if len(b) == 0 {
		return "run"
	}
	return string(b)
}
