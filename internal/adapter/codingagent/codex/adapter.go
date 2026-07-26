package codex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/protocol"
)

// Adapter is the in-process NeuroForge coding-agent adapter for the Codex CLI
// (spec §12.2; AC-5). It implements [codingagent.Adapter]. Construct it with
// [New]; it does not self-register.
type Adapter struct {
	opts   Options
	runner Runner

	// Cached detection (populated lazily by the first Detect/Version/Health/
	// Capabilities call). Detection runs "codex --version" once.
	detMu      sync.Mutex
	detDone    bool
	det        protocol.DetectionResult
	detStderr  string
	detVersion parsedVersion

	// Live runs, keyed by run id, for Cancel.
	mu   sync.Mutex
	runs map[string]*runState
}

// New returns a Codex adapter configured by opts. It never registers itself; the
// daemon wires it into a [codingagent.Registry] at startup.
func New(opts Options) *Adapter {
	opts = opts.resolve()
	r := opts.runner
	if r == nil {
		r = newProctreeRunner()
	}
	return &Adapter{opts: opts, runner: r, runs: map[string]*runState{}}
}

// ID implements [codingagent.Adapter]. It is the stable engine id "codex"
// (spec §12.1), independent of any model name.
func (a *Adapter) ID() string { return "codex" }

// Detect implements [codingagent.Adapter]. It resolves the Codex binary (via
// exec.LookPath, honouring PATHEXT/.exe/.cmd/.bat and npm shims on Windows) and
// runs "codex --version". The result is cached for the life of the adapter.
func (a *Adapter) Detect(ctx context.Context) protocol.DetectionResult {
	return a.detect(ctx)
}

// Version implements [codingagent.Adapter].
func (a *Adapter) Version(ctx context.Context) protocol.VersionResult {
	d := a.detect(ctx)
	pv := a.cachedVersion()
	return protocol.VersionResult{
		AdapterVersion:  adapterVersion,
		EngineVersion:   pv.raw,
		ProtocolVersion: protocol.ProtocolVersion,
		Error: func() string {
			if d.Installed && !pv.valid {
				return "codex version string could not be parsed; capabilities are conservative"
			}
			return ""
		}(),
	}
}

// Health implements [codingagent.Adapter]. It is an offline, no-paid-call probe
// (rule §36.5): installed-but-not-authenticated is distinguished only when the
// version probe or a configured probe surfaces an auth signal. A genuine
// authenticated-state check is deferred to the opt-in smoke test
// (docs/adapters/codex.md).
func (a *Adapter) Health(ctx context.Context, _ protocol.Account) protocol.HealthResult {
	d := a.detect(ctx)
	if !d.Installed {
		return protocol.HealthResult{Status: protocol.HealthDown, Detail: d.Detail}
	}
	low := strings.ToLower(a.detStderr + " " + d.Detail + " " + d.Version)
	switch {
	case containsAny(low, "not logged in", "codex login", "no api key", "missing api key", "unauthorized", "login required", "not authenticated"):
		return protocol.HealthResult{Status: protocol.HealthDegraded, Detail: "codex installed but not authenticated; run `codex login`"}
	}
	return protocol.HealthResult{Status: protocol.HealthOK, Detail: "codex installed (version " + d.Version + ")"}
}

// Capabilities implements [codingagent.Adapter]. It derives a version-gated
// profile (spec §12.3) from the detected Codex version; unknown future Codex
// fields are tolerated, not fatal.
func (a *Adapter) Capabilities(ctx context.Context) protocol.AgentCapabilities {
	a.detect(ctx)
	return deriveCapabilities(a.cachedVersion())
}

// ListModels implements [codingagent.Adapter]. The Codex model catalogue is
// account/provider-dependent and cannot be enumerated offline without a paid
// call (rule §36.5) or hard-coding model names (rule §36.8). The adapter
// therefore returns no descriptors: the router uses provider-configured model
// ids via [protocol.AgentRunRequest.Model].
func (a *Adapter) ListModels(context.Context, protocol.Account) ([]protocol.ModelDescriptor, error) {
	return nil, nil
}

// InspectQuota implements [codingagent.Adapter]. Codex exposes no offline quota
// signal, so the snapshot is UNKNOWN (spec §20.1, rule §36.10 — never overstate
// precision).
func (a *Adapter) InspectQuota(context.Context, protocol.Account) protocol.QuotaSnapshot {
	return protocol.QuotaSnapshot{
		Confidence: protocol.QuotaConfUnknown,
		State:      protocol.QuotaStateUnknown,
		Reason:     "codex exposes no offline quota signal; quota is observed via usage events during runs",
	}
}

// Start implements [codingagent.Adapter]. It builds the headless "codex exec"
// argv, spawns Codex in a new process group with an allowlisted environment,
// and streams normalized events to sink. When session resume is supported it
// captures Codex's session id before returning (bounded by bootstrapTimeout).
func (a *Adapter) Start(ctx context.Context, req protocol.AgentRunRequest, sink codingagent.EventSink) (protocol.RunHandle, error) {
	prompt, err := resolvePrompt(req.Prompt, req.PromptFile)
	if err != nil {
		return protocol.RunHandle{}, err
	}
	binary, err := a.resolveBinary(ctx)
	if err != nil {
		return protocol.RunHandle{}, err
	}
	argv, err := buildExecArgv(binary, req.Model, prompt, a.opts.ExecArgs, false, "")
	if err != nil {
		return protocol.RunHandle{}, err
	}
	return a.run(ctx, runParams{
		runID: req.RunID, model: req.Model, account: req.Account,
		workspace: req.Workspace, argv: argv, allowlist: req.AllowlistEnv,
		timeout: req.Timeout, isResume: false,
	}, sink)
}

// Resume implements [codingagent.Adapter]. It re-attaches to an existing Codex
// session via the version-appropriate resume flag and streams events. The
// session id is taken from the resume request; no new prompt is required (Codex
// continues the prior turn).
func (a *Adapter) Resume(ctx context.Context, req protocol.ResumeRequest, sink codingagent.EventSink) (protocol.RunHandle, error) {
	if strings.TrimSpace(req.SessionID) == "" {
		return protocol.RunHandle{}, errors.New("codex: resume requires a SessionID")
	}
	binary, err := a.resolveBinary(ctx)
	if err != nil {
		return protocol.RunHandle{}, err
	}
	argv, err := buildExecArgv(binary, req.Model, "", a.opts.ExecArgs, true, req.SessionID)
	if err != nil {
		return protocol.RunHandle{}, err
	}
	return a.run(ctx, runParams{
		runID: req.RunID, model: req.Model, account: req.Account,
		workspace: req.Workspace, argv: argv, allowlist: req.AllowlistEnv,
		timeout: req.Timeout, isResume: true, sessionID: req.SessionID,
	}, sink)
}

// SendMessage implements [codingagent.Adapter]. A headless "codex exec" run is
// autonomous: there is no live chat channel (Capabilities.LiveUserMessages is
// false), so sending a message is unsupported.
func (a *Adapter) SendMessage(context.Context, protocol.RunHandle, protocol.AgentMessage) error {
	return errLiveMessagesUnsupported
}

// Cancel implements [codingagent.Adapter]. It terminates the entire Codex
// process group (spec: cancellation ends the whole group) via [Proc.Kill] and
// cancels the run context so the supervisor emits run.cancelled.
func (a *Adapter) Cancel(_ context.Context, handle protocol.RunHandle) error {
	a.mu.Lock()
	rs, ok := a.runs[handle.RunID]
	a.mu.Unlock()
	if !ok {
		return fmt.Errorf("codex: unknown run %q", handle.RunID)
	}
	rs.setReason(reasonUser)
	rs.cancel()
	return rs.proc.Kill()
}

// ClassifyFailure implements [codingagent.Adapter].
func (a *Adapter) ClassifyFailure(exitCode int, events []protocol.NormalizedEvent, stderr string) protocol.FailureClassification {
	return classifyFailure(exitCode, events, stderr)
}

// adapterVersion is this implementation's version (independent of the engine
// version and of [protocol.ProtocolVersion]).
const adapterVersion = "codex-adapter-v1"

var errLiveMessagesUnsupported = errors.New("codex: live messages are not supported in headless exec mode")

// runParams is the fully-resolved input to a single supervised run.
type runParams struct {
	runID     string
	model     string
	account   protocol.Account
	workspace string
	argv      []string
	allowlist []string
	timeout   time.Duration
	isResume  bool
	sessionID string
}

// run spawns the process and supervises it.
func (a *Adapter) run(parent context.Context, p runParams, sink codingagent.EventSink) (protocol.RunHandle, error) {
	if sink == nil {
		return protocol.RunHandle{}, errors.New("codex: nil event sink")
	}
	env := buildAgentEnv(p.allowlist)
	proc, err := a.runner.Start(p.argv, p.workspace, env)
	if err != nil {
		return protocol.RunHandle{}, fmt.Errorf("codex: start agent: %w", err)
	}

	runCtx, cancel := context.WithCancel(parent)
	rs := &runState{proc: proc, cancel: cancel, bootstrap: make(chan struct{})}
	if p.timeout > 0 {
		t := p.timeout
		rs.timer = time.AfterFunc(t, func() {
			rs.setReason(reasonTimeout)
			cancel()
		})
	}

	runID := p.runID
	if runID == "" {
		runID = "codex-run"
	}
	a.mu.Lock()
	a.runs[runID] = rs
	a.mu.Unlock()

	go a.supervise(superviseInput{
		ctx: runCtx, rs: rs, proc: proc, sink: sink, runID: runID, engine: a.ID(), now: a.opts.now,
	})

	handle := protocol.RunHandle{RunID: runID, Engine: a.ID(), Model: p.model, Account: p.account}
	if p.isResume {
		handle.SessionID = p.sessionID
	}

	// When session resume is supported, capture the session id Codex emits at
	// start time before returning (bounded; never blocks forever).
	if !p.isResume && a.Capabilities(parent).SessionResume {
		select {
		case <-rs.bootstrap:
		case <-time.After(bootstrapTimeout):
		case <-runCtx.Done():
		}
		if sid := rs.session(); sid != "" {
			handle.SessionID = sid
		}
	}
	return handle, nil
}

// superviseInput bundles the supervisor goroutine's dependencies.
type superviseInput struct {
	ctx    context.Context
	rs     *runState
	proc   Proc
	sink   codingagent.EventSink
	runID  string
	engine string
	now    func() time.Time
}

// supervise reads JSONL from Codex until EOF, parses each line, forwards
// normalized events to sink, persists malformed lines as artifacts, and ensures a
// terminal event is always emitted. It is responsive to cancellation: the
// blocking stdout read happens in a goroutine so ctx cancellation preempts it
// and terminates the whole process group (spec).
func (a *Adapter) supervise(in superviseInput) {
	defer func() {
		if in.rs.timer != nil {
			in.rs.timer.Stop()
		}
		a.mu.Lock()
		delete(a.runs, in.runID)
		a.mu.Unlock()
	}()

	scanner := newLineScanner(in.proc.Stdout())
	type readResult struct {
		line    []byte
		hasMore bool
	}
	ch := make(chan readResult, 1)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			line, hasMore := scanner.next()
			select {
			case ch <- readResult{line: line, hasMore: hasMore}:
			case <-in.ctx.Done():
				return
			}
			if !hasMore && line == nil {
				return
			}
		}
	}()

	sawTerminal := false
	var events []protocol.NormalizedEvent

	emit := func(ev protocol.NormalizedEvent) bool {
		if ev.RunID == "" {
			ev.RunID = in.runID
		}
		if ev.Engine == "" {
			ev.Engine = in.engine
		}
		if err := in.sink.OnEvent(in.ctx, ev); err != nil {
			// Consumer aborted: tear down the group and stop.
			_ = in.proc.Kill()
			<-readerDone
			return false
		}
		return true
	}
	emitBG := func(ev protocol.NormalizedEvent) {
		if ev.RunID == "" {
			ev.RunID = in.runID
		}
		if ev.Engine == "" {
			ev.Engine = in.engine
		}
		_ = in.sink.OnEvent(context.Background(), ev)
	}

	readEOF := false
	for !readEOF && !sawTerminal {
		select {
		case <-in.ctx.Done():
			_ = in.proc.Kill()
			<-readerDone
			if !sawTerminal {
				emitBG(a.synthesizeTermination(in.runID, in.engine, in.rs.reasonOf()))
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
			if sid := extractSessionID(res.line); sid != "" {
				in.rs.setSession(sid)
			}
			ev, perr := parseCodexLine(res.line, in.now())
			in.rs.signalBootstrap()
			if perr != nil {
				// Malformed/unknown line: persist raw as artifact, emit the
				// accompanying warning (never fatal). perr != nil with empty
				// event type only for ErrEmptyLine, which we skip silently.
				if ev.Type != "" {
					events = append(events, ev)
					if !emit(ev) {
						return
					}
				}
				if !errors.Is(perr, protocol.ErrEmptyLine) {
					a.saveMalformed(in.runID, res.line)
				}
				continue
			}
			events = append(events, ev)
			if ev.Type.IsTerminal() {
				sawTerminal = true
			}
			if !emit(ev) {
				return
			}
		}
	}

	// Process exited (EOF reached). Collect exit code + redacted stderr.
	exitCode, stderr := in.proc.Wait()
	if sawTerminal {
		return
	}
	// KF-09 / invariant I.9: a cancel/timeout cancelled the run context BEFORE
	// the kill that induced this EOF, so honour the recorded reason here — never
	// synthesize a non-cancelled terminal from the SIGKILL exit code. ctx.Err()
	// distinguishes a real cancellation (Cancel sets the reason + cancels ctx
	// before the kill) from a natural exit (ctx still alive).
	if in.ctx.Err() != nil {
		emitBG(a.synthesizeTermination(in.runID, in.engine, in.rs.reasonOf()))
		return
	}
	fc := classifyFailure(exitCode, events, stderr)
	term := protocol.EventRunCompleted
	if exitCode != 0 {
		term = protocol.EventRunFailed
	}
	ev := protocol.NormalizedEvent{Type: term, Timestamp: in.now(), RunID: in.runID, Engine: in.engine}
	if term == protocol.EventRunFailed {
		ev.Failure = &protocol.FailurePayload{Class: fc.Class, Reason: fc.Reason, ExitCode: exitCode}
	}
	emitBG(ev)
}

// synthesizeTermination builds the terminal event for a ctx cancellation.
// reasonTimeout → run.failed(TIMEOUT); otherwise (user cancel / parent done) →
// run.cancelled.
func (a *Adapter) synthesizeTermination(runID, engine string, reason cancelReason) protocol.NormalizedEvent {
	if reason == reasonTimeout {
		return protocol.NormalizedEvent{
			Type: protocol.EventRunFailed, Timestamp: a.opts.now(), RunID: runID, Engine: engine,
			Failure: &protocol.FailurePayload{Class: protocol.FailureTimeout, Reason: "codex run exceeded its timeout", ExitCode: 124},
		}
	}
	return protocol.NormalizedEvent{
		Type:      protocol.EventRunCancelled,
		Timestamp: a.opts.now(),
		RunID:     runID,
		Engine:    engine,
		Failure:   &protocol.FailurePayload{Class: protocol.FailureCancelled, Reason: "cancelled by caller"},
	}
}

// resolvePrompt returns the inline prompt or reads the prompt file. At least one
// must be supplied; the command builder enforces non-emptiness.
func resolvePrompt(prompt, promptFile string) (string, error) {
	if prompt != "" {
		return prompt, nil
	}
	if promptFile == "" {
		return "", nil
	}
	b, err := os.ReadFile(promptFile)
	if err != nil {
		return "", fmt.Errorf("codex: read prompt file: %w", err)
	}
	return string(b), nil
}

// resolveBinary returns the Codex binary path, running detection if needed.
func (a *Adapter) resolveBinary(ctx context.Context) (string, error) {
	if a.opts.BinaryPath != "" {
		return a.opts.BinaryPath, nil
	}
	d := a.detect(ctx)
	if d.Installed && d.Path != "" {
		return d.Path, nil
	}
	return "", fmt.Errorf("codex: binary not found (%s)", d.Detail)
}

// saveMalformed persists a malformed/unknown Codex line to the artifacts dir so
// it is recoverable for forensics (spec: malformed event saved to artifacts).
// The raw bytes are redacted first so a quoted token can never be persisted.
func (a *Adapter) saveMalformed(runID string, line []byte) {
	dir := a.opts.ArtifactsDir
	if dir == "" {
		dir = os.TempDir()
	}
	name := fmt.Sprintf("codex-malformed-%s-%d.txt", sanitizeName(runID), a.opts.now().UnixNano())
	_ = os.WriteFile(filepath.Join(dir, name), []byte(redact(string(line))), 0o600)
}

func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '/' || r == '\\' || r == ' ' || r == ':' || r == 0 {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	if out == "" {
		return "run"
	}
	return out
}
