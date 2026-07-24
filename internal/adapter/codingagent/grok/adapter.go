package grok

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/proctree"
	"neuroforge/internal/adapter/codingagent/protocol"
)

// errGrokTimeout is attached as the cause when a run exceeds its wall-clock
// budget so the supervisor loop can distinguish timeout from user cancellation
// (spec §32: TIMEOUT vs CANCELLED are distinct classes).
var errGrokTimeout = errors.New("grok: run exceeded its timeout")

// ErrLiveMessagesUnsupported is returned by SendMessage. Grok's headless `-p`
// mode has no stdin message channel, so live user messages are not supported
// (capability LiveUserMessages is false). Marked unimplemented per rule §36.25.
var ErrLiveMessagesUnsupported = errors.New("grok: live user messages are not supported in headless mode")

// Adapter is the in-process Grok Build coding-agent adapter (spec §12.2,
// AC-5). It implements the full [codingagent.Adapter] interface at coding-agent
// protocol version 1 and never modifies the shared protocol package.
//
// Construct with [New]; do not self-register (rule: the daemon wires adapters
// explicitly via the registry).
type Adapter struct {
	opts Options

	mu               sync.Mutex
	runs             map[string]*runState
	cachedVersion    versionInfo
	cachedVersionRaw string
}

// runState tracks one live run for cancellation and process-tree cleanup.
type runState struct {
	cmd       *exec.Cmd
	runCtx    context.Context
	cancelRun context.CancelFunc
}

// New returns a Grok adapter configured with opts.
func New(opts Options) *Adapter {
	return &Adapter{opts: opts, runs: map[string]*runState{}}
}

// ID implements codingagent.Adapter (spec §12.1: the engine id, not a model).
func (a *Adapter) ID() string { return defaultBinaryName }

// Health implements codingagent.Adapter. It distinguishes ok / degraded / down /
// unknown by resolving and probing the binary. No paid call is made: only
// `grok --version` is invoked.
func (a *Adapter) Health(ctx context.Context, _ protocol.Account) protocol.HealthResult {
	bin := a.resolveBinary()
	if _, err := lookPath(bin); err != nil {
		return protocol.HealthResult{Status: protocol.HealthDown, Detail: "grok binary not found: " + err.Error()}
	}
	start := time.Now()
	out, exit, err := runVersionProbe(ctx, bin)
	latency := time.Since(start)
	if ctx.Err() != nil {
		return protocol.HealthResult{Status: protocol.HealthUnknown, Detail: "health probe cancelled", Latency: latency}
	}
	if err != nil && out == "" {
		return protocol.HealthResult{Status: protocol.HealthDegraded, Detail: "grok --version failed: " + err.Error(), Latency: latency}
	}
	if exit != 0 {
		return protocol.HealthResult{Status: protocol.HealthDegraded, Detail: "grok --version exited non-zero (" + fmt.Sprintf("exit %d", exit) + ")", Latency: latency}
	}
	detail := "grok reachable"
	if v := parseVersion(out); v.known {
		detail = "grok " + v.String() + " reachable"
	}
	return protocol.HealthResult{Status: protocol.HealthOK, Detail: detail, Latency: latency}
}

// ListModels implements codingagent.Adapter. Grok has no confirmed offline
// model-listing surface, so a single opaque placeholder descriptor is returned
// (no real model names are hard-coded, rule §36.8). Replace once a `grok models`
// command is confirmed (rule §36.25).
func (a *Adapter) ListModels(context.Context, protocol.Account) ([]protocol.ModelDescriptor, error) {
	return []protocol.ModelDescriptor{{
		ID:     a.ID() + "/default",
		Engine: a.ID(),
		Kind:   protocol.ModelKindCoding,
	}}, nil
}

// InspectQuota implements codingagent.Adapter. Grok exposes no authoritative
// quota probe, so the snapshot is UNKNOWN (rule §36.10: never overstate quota
// precision).
func (a *Adapter) InspectQuota(context.Context, protocol.Account) protocol.QuotaSnapshot {
	return protocol.QuotaSnapshot{
		Confidence: protocol.QuotaConfUnknown,
		State:      protocol.QuotaStateUnknown,
		Reason:     "grok exposes no authoritative quota probe; usage is reported via run events",
	}
}

// Start implements codingagent.Adapter.
func (a *Adapter) Start(ctx context.Context, req protocol.AgentRunRequest, sink codingagent.EventSink) (protocol.RunHandle, error) {
	return a.startRun(ctx, req, sink, false)
}

// Resume implements codingagent.Adapter. Resume is honoured only when the
// version-gated SessionResume capability is true (spec §21). When unsupported,
// Resume returns an error rather than silently degrading (rule §36.25).
func (a *Adapter) Resume(ctx context.Context, req protocol.ResumeRequest, sink codingagent.EventSink) (protocol.RunHandle, error) {
	if !a.deriveCapabilities().SessionResume {
		return protocol.RunHandle{}, errors.New("grok: session resume is not supported by the detected version")
	}
	startReq := protocol.AgentRunRequest{
		RunID:        req.RunID,
		Engine:       req.Engine,
		Model:        req.Model,
		Account:      req.Account,
		Workspace:    req.Workspace,
		PromptFile:   req.CheckpointPath,
		Scope:        req.Scope,
		AllowlistEnv: req.AllowlistEnv,
		TurnLimit:    req.TurnLimit,
		Timeout:      req.Timeout,
		SessionID:    req.SessionID,
	}
	return a.startRun(ctx, startReq, sink, true)
}

// SendMessage implements codingagent.Adapter. Not supported in headless mode
// (capability LiveUserMessages is false; rule §36.25).
func (a *Adapter) SendMessage(context.Context, protocol.RunHandle, protocol.AgentMessage) error {
	return ErrLiveMessagesUnsupported
}

// Cancel implements codingagent.Adapter. It cancels the run context and
// terminates the entire process group via [proctree.KillGroup] (spec:
// cancellation ends the whole process group, including descendants).
func (a *Adapter) Cancel(_ context.Context, handle protocol.RunHandle) error {
	a.mu.Lock()
	st, ok := a.runs[handle.RunID]
	a.mu.Unlock()
	if !ok {
		return fmt.Errorf("grok: unknown run %q", handle.RunID)
	}
	st.cancelRun()
	if st.cmd != nil && st.cmd.Process != nil {
		_ = proctree.KillGroup(st.cmd, proctree.SigKill)
	}
	return nil
}

// startRun is the shared Start/Resume core: build argv, spawn in a new process
// group with an allowlisted env, then supervise the streaming output.
func (a *Adapter) startRun(ctx context.Context, req protocol.AgentRunRequest, sink codingagent.EventSink, isResume bool) (protocol.RunHandle, error) {
	if sink == nil {
		return protocol.RunHandle{}, errors.New("grok: nil EventSink")
	}
	bin := a.resolveBinary()
	resolved, err := lookPath(bin)
	if err != nil {
		return protocol.RunHandle{}, fmt.Errorf("grok: binary not found: %w", err)
	}
	caps := a.deriveCapabilities()
	argv := buildArgv(resolved, req, caps, a.opts.EnableTurnLimit)

	// Run context: user-cancel handle + optional wall-clock timeout (with a
	// distinguishable cause so TIMEOUT ≠ CANCELLED).
	baseCtx, cancelBase := context.WithCancel(ctx)
	runCtx := baseCtx
	if req.Timeout > 0 {
		var cancelTO context.CancelFunc
		runCtx, cancelTO = context.WithTimeoutCause(baseCtx, req.Timeout, errGrokTimeout)
		_ = cancelTO
	}

	cmd := proctree.NewGroupCommand(argv[0], argv[1:]...)
	cmd.Dir = req.Workspace
	cmd.Env = buildEnv(req.AllowlistEnv, a.opts.ExtraEnv)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancelBase()
		return protocol.RunHandle{}, fmt.Errorf("grok: stdout pipe: %w", err)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf
	if err := cmd.Start(); err != nil {
		cancelBase()
		return protocol.RunHandle{}, fmt.Errorf("grok: start agent: %w", err)
	}

	runID := req.RunID
	if runID == "" {
		runID = "grok-run"
	}
	st := &runState{cmd: cmd, runCtx: runCtx, cancelRun: cancelBase}
	a.mu.Lock()
	a.runs[runID] = st
	a.mu.Unlock()

	handle := protocol.RunHandle{
		RunID:     runID,
		Engine:    a.ID(),
		Model:     req.Model,
		Account:   req.Account,
		SessionID: req.SessionID,
	}

	go a.supervise(runCtx, runID, a.ID(), req.Model, req.Workspace, req.Scope, cmd, stdout, &stderrBuf, sink, isResume)
	return handle, nil
}

// supervise reads streaming-json until EOF/cancel/timeout, maps items to
// normalized events, persists malformed lines, and guarantees a terminal event.
// It is responsive to cancellation: the blocking pipe read runs in its own
// goroutine so ctx cancellation preempts it and kills the whole process group.
func (a *Adapter) supervise(runCtx context.Context, runID, engine, model, workspace string, scope []string,
	cmd *exec.Cmd, stdout interface{ Read([]byte) (int, error) }, stderr *bytes.Buffer,
	sink codingagent.EventSink, isResume bool) {

	defer func() {
		a.mu.Lock()
		delete(a.runs, runID)
		a.mu.Unlock()
	}()

	emit := func(ev protocol.NormalizedEvent) bool {
		if ev.RunID == "" {
			ev.RunID = runID
		}
		if ev.Engine == "" {
			ev.Engine = engine
		}
		if ev.Model == "" {
			ev.Model = model
		}
		if ev.Timestamp.IsZero() {
			ev.Timestamp = time.Now().UTC()
		}
		return sink.OnEvent(runCtx, ev) == nil
	}

	// Synthesize the opening event: run.started (or run.resumed for Resume) so
	// consumers always see a well-ordered stream even if Grok emits no such item.
	sawTerminal := false
	firstType := protocol.EventRunStarted
	if isResume {
		firstType = protocol.EventRunResumed
	}
	if !emit(protocol.NormalizedEvent{Type: firstType, Timestamp: time.Now().UTC()}) {
		// Consumer already gone; kill and finish.
		_ = proctree.KillGroup(cmd, proctree.SigKill)
		_ = cmd.Wait()
		return
	}

	scanner := newLineScanner(stdout)
	type readResult struct {
		line    []byte
		hasMore bool
	}
	ch := make(chan readResult, 1)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			line, hasMore := scanner.Next()
			select {
			case ch <- readResult{line: line, hasMore: hasMore}:
			case <-runCtx.Done():
				return
			}
			if !hasMore && line == nil {
				return
			}
		}
	}()

	readEOF := false
	for !readEOF && !sawTerminal {
		select {
		case <-runCtx.Done():
			_ = proctree.KillGroup(cmd, proctree.SigKill)
			<-readerDone
			if !sawTerminal {
				cause := context.Cause(runCtx)
				if errors.Is(cause, errGrokTimeout) {
					_ = sink.OnEvent(context.Background(), protocol.NormalizedEvent{
						Type: protocol.EventRunFailed, Timestamp: time.Now().UTC(),
						RunID: runID, Engine: engine, Model: model,
						Failure: &protocol.FailurePayload{Class: protocol.FailureTimeout, Reason: "run exceeded its wall-clock timeout", ExitCode: 124},
					})
				} else {
					_ = sink.OnEvent(context.Background(), protocol.NormalizedEvent{
						Type: protocol.EventRunCancelled, Timestamp: time.Now().UTC(),
						RunID: runID, Engine: engine, Model: model,
						Failure: &protocol.FailurePayload{Class: protocol.FailureCancelled, Reason: "cancelled by caller"},
					})
				}
			}
			return
		case res := <-ch:
			if res.line == nil && !res.hasMore {
				readEOF = true
				continue
			}
			if res.line == nil {
				continue
			}
			evs, terminal, status := parseGrokLine(res.line, scope)
			if status == parseMalformed || status == parseUnknown {
				a.saveMalformed(runID, res.line)
			}
			for _, ev := range evs {
				if !emit(ev) {
					_ = proctree.KillGroup(cmd, proctree.SigKill)
					<-readerDone
					return
				}
			}
			if terminal {
				sawTerminal = true
			}
		}
	}

	// Process exited; collect exit code + captured (redacted) stderr.
	waitErr := cmd.Wait()
	exitCode := exitCodeOf(waitErr)
	stderrText := redactSecrets(stderr.String())

	if sawTerminal {
		return
	}

	// Synthesize a terminal event from the outcome, classifying the exit code +
	// stderr via the Grok classifier. The synthesized run.failed carries the
	// class so a later ClassifyFailure(events...) is consistent (spec §32).
	if exitCode == 0 {
		_ = sink.OnEvent(context.Background(), protocol.NormalizedEvent{
			Type: protocol.EventRunCompleted, Timestamp: time.Now().UTC(),
			RunID: runID, Engine: engine, Model: model,
		})
		return
	}
	fc := grokClassify(exitCode, nil, stderrText)
	_ = sink.OnEvent(context.Background(), protocol.NormalizedEvent{
		Type: protocol.EventRunFailed, Timestamp: time.Now().UTC(),
		RunID: runID, Engine: engine, Model: model,
		Failure: &protocol.FailurePayload{Class: fc.Class, Reason: fc.Reason, ExitCode: exitCode},
	})
}

// saveMalformed persists a malformed/unknown agent output line to the artifacts
// dir for forensics (spec §13.1: malformed event is saved + classified, never
// fatal). The file is mode 0o600; on platforms that ignore the mode it is still
// created.
func (a *Adapter) saveMalformed(runID string, line []byte) {
	dir := a.opts.ArtifactsDir
	if dir == "" {
		dir = os.TempDir()
	}
	name := fmt.Sprintf("grok-malformed-%s-%d.txt", sanitizeName(runID), time.Now().UnixNano())
	_ = os.WriteFile(filepath.Join(dir, name), line, 0o600)
}

func sanitizeName(s string) string {
	var b []byte
	for _, r := range s {
		switch r {
		case '/', ' ', ':', '\\', '<', '>', '|', '?', '*':
			b = append(b, '_')
		default:
			b = append(b, []byte(string(r))...)
		}
	}
	if len(b) == 0 {
		return "run"
	}
	return string(b)
}
