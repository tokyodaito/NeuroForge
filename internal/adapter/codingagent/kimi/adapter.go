package kimi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/proctree"
	"neuroforge/internal/adapter/codingagent/protocol"
)

// defaultModels is the minimal, opaque model catalogue the adapter reports when
// the caller does not supply one. Identifiers are deliberately non-specific so
// the core never depends on a real model name (rule §36.8). Dynamic model
// discovery from the engine is NOT YET IMPLEMENTED (spec §36.25): see
// ListModels.
var defaultModels = []protocol.ModelDescriptor{
	{ID: "kimi/default", Engine: "kimi", Kind: protocol.ModelKindCoding, ContextWindow: 128000, MaxOutput: 8192, CachedUsage: true},
}

// Adapter is the in-process Kimi Code coding-agent adapter (spec §12.2, §13
// "Path 3"). It wraps the `kimi` CLI: detect → probe version/flags → build a
// headless argv → spawn in a new process group inside an isolated home → parse
// stream-json into normalized events → classify failures.
//
// Adapter implements [codingagent.Adapter]. It does not self-register; obtain
// one via [New] and register it explicitly if desired.
type Adapter struct {
	opts Options

	probeOnce sync.Once
	probeVal  probe
	probeErr  error

	mu   sync.Mutex
	runs map[string]*runState
}

// runState tracks one live run for cancellation and process-tree cleanup.
//
// Terminal arbitration (KF-09 / invariant I.9): cancel/timeout intents are
// recorded before the process group is killed, so a kill-induced EOF can never
// be observed before the intent is visible.
type runState struct {
	cmd      *exec.Cmd
	cancel   context.CancelFunc
	timedOut *bool // shared flag: true when the run ended due to req.Timeout
	mu       sync.Mutex

	cancelOnce      sync.Once
	cancelRequested atomic.Bool
}

// requestCancel records the cancel intent, cancels the run context, and kills
// the process group — once, idempotently. Intent is set BEFORE the kill.
func (st *runState) requestCancel() {
	st.cancelOnce.Do(func() {
		st.cancelRequested.Store(true)
		if st.cancel != nil {
			st.cancel()
		}
		_ = proctree.KillGroup(st.cmd, proctree.SigKill)
	})
}

// isTimedOut reports whether the hard timeout fired, under the state lock.
func (st *runState) isTimedOut() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.timedOut != nil && *st.timedOut
}

// New returns a Kimi adapter configured by opts.
func New(opts Options) *Adapter {
	return &Adapter{opts: opts, runs: map[string]*runState{}}
}

// ensureProbe computes the detection probe once and caches it. The installed
// engine is assumed stable for the life of the adapter.
func (a *Adapter) ensureProbe(ctx context.Context) probe {
	a.probeOnce.Do(func() {
		a.probeVal = runProbe(ctx, &a.opts)
		if !a.probeVal.installed {
			a.probeErr = errNotDetected
		}
	})
	return a.probeVal
}

// ID implements codingagent.Adapter. The engine id is independent of any model
// name (spec §12.1).
func (a *Adapter) ID() string { return "kimi" }

// Detect implements codingagent.Adapter. It resolves the `kimi` binary (via
// exec.LookPath, honouring PATHEXT so .exe/.cmd/.bat and npm shims are found)
// and runs `kimi --version` to confirm it is usable.
func (a *Adapter) Detect(ctx context.Context) protocol.DetectionResult {
	pr := a.ensureProbe(ctx)
	return protocol.DetectionResult{
		Installed: pr.installed,
		Path:      pr.path,
		Version:   pr.versionStr,
		Detail:    pr.detail,
	}
}

// Version implements codingagent.Adapter.
func (a *Adapter) Version(ctx context.Context) protocol.VersionResult {
	pr := a.ensureProbe(ctx)
	return protocol.VersionResult{
		AdapterVersion:  a.opts.adapterVersion(),
		EngineVersion:   pr.versionStr,
		ProtocolVersion: protocol.ProtocolVersion,
		Error:           pr.probeErrToMsg(),
	}
}

// Health implements codingagent.Adapter. A found binary with a working
// --version is OK; a found binary whose --version failed is DEGRADED; a missing
// binary is DOWN; never UNKNOWN (we always attempt a probe).
func (a *Adapter) Health(ctx context.Context, _ protocol.Account) protocol.HealthResult {
	pr := a.ensureProbe(ctx)
	if !pr.installed {
		return protocol.HealthResult{Status: protocol.HealthDown, Detail: pr.detail}
	}
	if pr.versionStr == "" {
		return protocol.HealthResult{Status: protocol.HealthDegraded, Detail: pr.detail}
	}
	return protocol.HealthResult{Status: protocol.HealthOK, Detail: pr.detail}
}

// Capabilities implements codingagent.Adapter. The profile is derived from the
// detected version unless [Options.Capabilities] overrides it (merged so a
// caller can only ADD capability, never silently remove a real one).
func (a *Adapter) Capabilities(ctx context.Context) protocol.AgentCapabilities {
	if a.opts.Capabilities != nil {
		base := *a.opts.Capabilities
		// Merge with version-derived profile so the daemon's declared caps and
		// the probed caps combine (capability union, spec §12.3 Merge).
		return base.Merge(a.profileOf(ctx).caps)
	}
	return a.profileOf(ctx).caps
}

func (a *Adapter) profileOf(ctx context.Context) versionProfile {
	pr := a.ensureProbe(ctx)
	return pr.profile
}

// ListModels implements codingagent.Adapter. Dynamic discovery from the engine
// is NOT YET IMPLEMENTED (spec §36.25): the adapter reports the configured (or
// default opaque) catalogue. Model names are never hard-coded in the core
// (rule §36.8).
func (a *Adapter) ListModels(context.Context, protocol.Account) ([]protocol.ModelDescriptor, error) {
	if len(a.opts.Models) > 0 {
		out := make([]protocol.ModelDescriptor, len(a.opts.Models))
		copy(out, a.opts.Models)
		return out, nil
	}
	out := make([]protocol.ModelDescriptor, len(defaultModels))
	copy(out, defaultModels)
	return out, nil
}

// InspectQuota implements codingagent.Adapter. Kimi exposes no quota probe the
// adapter can call offline, so it reports UNKNOWN rather than a fabricated
// figure (rule §36.10: never overstate precision).
func (a *Adapter) InspectQuota(context.Context, protocol.Account) protocol.QuotaSnapshot {
	return protocol.QuotaSnapshot{
		Confidence: protocol.QuotaConfUnknown,
		State:      protocol.QuotaStateUnknown,
		Reason:     "kimi: no offline quota probe; quota is reported per-run via usage events",
	}
}

// Start implements codingagent.Adapter.
func (a *Adapter) Start(ctx context.Context, req protocol.AgentRunRequest, sink codingagent.EventSink) (protocol.RunHandle, error) {
	return a.run(ctx, req, sink, false)
}

// Resume implements codingagent.Adapter. Resume is honoured only when the
// detected version supports --continue (gated via capabilities); otherwise the
// run still starts but the session id is not forwarded to the engine.
func (a *Adapter) Resume(ctx context.Context, req protocol.ResumeRequest, sink codingagent.EventSink) (protocol.RunHandle, error) {
	return a.run(ctx, protocol.AgentRunRequest{
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
	}, sink, true)
}

// SendMessage implements codingagent.Adapter. Headless `-p` mode has no live
// message channel; this is explicitly unsupported (not faked). The capability
// profile reports LiveUserMessages=false.
func (a *Adapter) SendMessage(context.Context, protocol.RunHandle, protocol.AgentMessage) error {
	return errors.New("kimi: live messages are not supported in headless mode")
}

// Cancel implements codingagent.Adapter. It terminates the entire process group
// (spec: cancellation ends the whole process group) via proctree and emits
// run.cancelled.
func (a *Adapter) Cancel(_ context.Context, handle protocol.RunHandle) error {
	a.mu.Lock()
	st, ok := a.runs[handle.RunID]
	a.mu.Unlock()
	if !ok {
		return fmt.Errorf("kimi: unknown run %q", handle.RunID)
	}
	// Record the cancel intent BEFORE the kill so a kill-induced EOF cannot
	// produce a non-cancelled terminal (KF-09 / invariant I.9).
	st.requestCancel()
	return nil
}

// ClassifyFailure implements codingagent.Adapter.
func (a *Adapter) ClassifyFailure(exitCode int, events []protocol.NormalizedEvent, stderr string) protocol.FailureClassification {
	return a.classifyFailure(exitCode, events, stderr)
}

// run is the shared Start/Resume body. It resolves the prompt, builds the
// deterministic argv, spawns the agent in a new process group inside an
// isolated home, and streams normalized events to sink until a terminal event.
func (a *Adapter) run(ctx context.Context, req protocol.AgentRunRequest, sink codingagent.EventSink, isResume bool) (protocol.RunHandle, error) {
	pr := a.ensureProbe(ctx)
	if !pr.installed {
		return protocol.RunHandle{}, errNotDetected
	}

	prompt, err := resolvePrompt(req, readFile)
	if err != nil {
		return protocol.RunHandle{}, fmt.Errorf("kimi: read prompt: %w", err)
	}

	profile := pr.profile
	pf := pr.flags
	if !pr.flagsProbed {
		// Fall back to version-gated flags synthesized from the profile.
		pf = probedFlags{streamJSON: profile.flagStreamJSON, model: profile.flagModel, continued: profile.flagContinue, maxTurns: profile.flagMaxTurns}
	}
	spec := runSpec{
		prompt:     prompt,
		model:      req.Model,
		sessionID:  req.SessionID,
		turnLimit:  req.TurnLimit,
		isResume:   isResume,
		extraArgs:  a.opts.ExtraArgs,
		streamHint: a.opts.ForceStreaming,
	}
	argv := buildArgv(spec, pf, profile)
	streaming := wantStream(spec, pf, profile)

	// Isolated home: relocate Kimi's profile so a run never touches the user's
	// global state (unless explicitly disabled for diagnostics).
	homeEnvName := a.opts.homeEnvName()
	var homeDir string
	if !a.opts.DisableIsolation {
		hd, err := isolatedHomeDir(req.Workspace)
		if err != nil {
			return protocol.RunHandle{}, fmt.Errorf("kimi: isolated home: %w", err)
		}
		homeDir = hd
	}
	env := buildRunEnv(homeEnvName, homeDir, req.AllowlistEnv, a.opts.ExtraEnv)

	runCtx, cancel := context.WithCancel(ctx)
	timedOut := false
	cmd := proctree.NewGroupCommand(pr.path, argv...)
	cmd.Dir = req.Workspace
	cmd.Env = env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return protocol.RunHandle{}, fmt.Errorf("kimi: stdout pipe: %w", err)
	}
	cmd.Stderr = &bytes.Buffer{}
	if err := cmd.Start(); err != nil {
		cancel()
		return protocol.RunHandle{}, fmt.Errorf("kimi: start agent: %w", err)
	}

	runID := req.RunID
	if runID == "" {
		runID = "kimi-run"
	}
	st := &runState{cmd: cmd, cancel: cancel, timedOut: &timedOut}
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

	// Honour req.Timeout as a hard wall-clock limit. On expiry the run is
	// terminated like a cancellation but classified as TIMEOUT (§32). The timer
	// is stopped when supervise returns so it cannot fire after completion.
	var timer *time.Timer
	if req.Timeout > 0 {
		timer = time.AfterFunc(req.Timeout, func() {
			st.mu.Lock()
			timedOut = true
			st.mu.Unlock()
			cancel()
		})
	}

	go func() {
		defer func() {
			if timer != nil {
				timer.Stop()
			}
		}()
		a.supervise(runCtx, runID, st, stdout, sink, streaming, isResume)
	}()
	return handle, nil
}

// supervise reads the agent stream, forwards normalized events to sink, saves
// malformed lines as artifacts, and guarantees a terminal event. It is
// responsive to cancellation: the blocking pipe read happens in a goroutine so
// context cancellation preempts it and terminates the whole process group.
func (a *Adapter) supervise(ctx context.Context, runID string, st *runState, stdout interface {
	Read([]byte) (int, error)
}, sink codingagent.EventSink, streaming, isResume bool) {
	defer func() {
		if st.cancel != nil {
			st.cancel()
		}
		a.mu.Lock()
		delete(a.runs, runID)
		a.mu.Unlock()
	}()

	reader := newBomStripper(stdout)
	scan := newLineScanner(reader)

	type readResult struct {
		line    []byte
		hasMore bool
	}
	ch := make(chan readResult, 1)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			line, hasMore := scan.Next()
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

	sawTerminal := false
	startEmitted := false
	var plainText bytes.Buffer // accumulated stdout when not streaming

	emit := func(ev protocol.NormalizedEvent) bool {
		if ev.RunID == "" {
			ev.RunID = runID
		}
		if ev.Engine == "" {
			ev.Engine = "kimi"
		}
		// Resume remap: a resume run must surface run.resumed (not run.started)
		// as its opening event, even if the engine emitted a plain init.
		if isResume && ev.Type == protocol.EventRunStarted && !startEmitted {
			ev.Type = protocol.EventRunResumed
		}
		if ev.Type == protocol.EventRunStarted || ev.Type == protocol.EventRunResumed {
			startEmitted = true
		}
		if ev.Type.IsTerminal() {
			sawTerminal = true
		}
		if err := sink.OnEvent(ctx, ev); err != nil {
			// Consumer aborted: terminate the group and stop.
			_ = proctree.KillGroup(st.cmd, proctree.SigKill)
			return false
		}
		return true
	}

	readEOF := false
	for !readEOF && !sawTerminal {
		select {
		case <-ctx.Done():
			_ = proctree.KillGroup(st.cmd, proctree.SigKill)
			<-readerDone
			if !sawTerminal {
				_ = sink.OnEvent(context.Background(), a.terminalForCancel(runID, st))
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
			if streaming {
				ev, perr := parseKimiLine(res.line)
				if perr != nil {
					// Malformed/unknown line: persist as artifact and emit the
					// recoverable warning carried by the event. Never fatal.
					a.saveMalformed(runID, res.line)
					if ev.Type != "" {
						if !emit(ev) {
							<-readerDone
							return
						}
					}
					continue
				}
				if !emit(ev) {
					<-readerDone
					return
				}
			} else {
				// Non-streaming: accumulate the raw line as assistant text.
				plainText.Write(res.line)
				plainText.WriteByte('\n')
			}
		}
	}

	// Process exited. Wait to collect exit code / stderr.
	waitErr := st.cmd.Wait()
	stderr := ""
	if buf, ok := st.cmd.Stderr.(*bytes.Buffer); ok {
		stderr = buf.String()
	}
	exitCode := exitCodeFrom(waitErr)

	if !streaming {
		// Synthesize a minimal event sequence for plain-text output.
		if !startEmitted && !sawTerminal {
			_ = emit(protocol.NormalizedEvent{Type: protocol.EventRunStarted, Timestamp: time.Now(), RunID: runID, Engine: "kimi", Model: ""})
		}
		if text := plainText.String(); strings.TrimSpace(text) != "" {
			_ = emit(protocol.NormalizedEvent{
				Type: protocol.EventMessageCompleted, Timestamp: time.Now(), RunID: runID, Engine: "kimi",
				Message: &protocol.MessagePayload{Text: strings.TrimRight(text, "\n"), Role: "assistant"},
			})
		}
	}

	if !sawTerminal {
		// KF-09 / invariant I.9: single terminal decision. A cancel/timeout
		// intent was recorded BEFORE the kill that induced this EOF, so honour
		// it here — never synthesize a non-cancelled terminal from the SIGKILL
		// exit code. Priority: timeout > cancellation > natural exit.
		_ = sink.OnEvent(context.Background(), a.decideTerminal(runID, st, exitCode, stderr))
	}
}

// decideTerminal picks the single terminal event for a run from the recorded
// intents (set before any kill) or, failing those, from the exit code.
func (a *Adapter) decideTerminal(runID string, st *runState, exitCode int, stderr string) protocol.NormalizedEvent {
	switch {
	case st.isTimedOut():
		return protocol.NormalizedEvent{
			Type: protocol.EventRunFailed, Timestamp: time.Now(), RunID: runID, Engine: "kimi",
			Failure: &protocol.FailurePayload{Class: protocol.FailureTimeout, Reason: "kimi: run exceeded its wall-clock timeout", ExitCode: exitCode},
		}
	case st.cancelRequested.Load():
		return protocol.NormalizedEvent{
			Type: protocol.EventRunCancelled, Timestamp: time.Now(), RunID: runID, Engine: "kimi",
			Failure: &protocol.FailurePayload{Class: protocol.FailureCancelled, Reason: "cancelled by caller", ExitCode: exitCode},
		}
	default:
		return a.synthesizeTerminal(runID, exitCode, stderr)
	}
}

// terminalForCancel builds the terminal event for a cancelled/timed-out run.
func (a *Adapter) terminalForCancel(runID string, st *runState) protocol.NormalizedEvent {
	st.mu.Lock()
	timedOut := st.timedOut != nil && *st.timedOut
	st.mu.Unlock()
	if timedOut {
		return protocol.NormalizedEvent{
			Type: protocol.EventRunFailed, Timestamp: time.Now(), RunID: runID, Engine: "kimi",
			Failure: &protocol.FailurePayload{Class: protocol.FailureTimeout, Reason: "kimi: run exceeded its wall-clock timeout"},
		}
	}
	return protocol.NormalizedEvent{
		Type: protocol.EventRunCancelled, Timestamp: time.Now(), RunID: runID, Engine: "kimi",
		Failure: &protocol.FailurePayload{Class: protocol.FailureCancelled, Reason: "cancelled by caller"},
	}
}

// synthesizeTerminal emits a terminal event from the process exit outcome when
// the agent stream did not end with one (e.g. a crash or partial output). The
// class is derived via the shared failure classifier (rule §32: bounded policy).
func (a *Adapter) synthesizeTerminal(runID string, exitCode int, stderr string) protocol.NormalizedEvent {
	fc := a.classifyFailure(exitCode, nil, stderr)
	if exitCode == 0 {
		return protocol.NormalizedEvent{Type: protocol.EventRunCompleted, Timestamp: time.Now(), RunID: runID, Engine: "kimi"}
	}
	return protocol.NormalizedEvent{
		Type: protocol.EventRunFailed, Timestamp: time.Now(), RunID: runID, Engine: "kimi",
		Failure: &protocol.FailurePayload{Class: fc.Class, Reason: fc.Reason, ExitCode: exitCode},
	}
}

// saveMalformed persists a malformed agent output line to the artifacts dir so
// it is recoverable for forensics (spec: malformed event saved to artifacts).
func (a *Adapter) saveMalformed(runID string, line []byte) {
	dir := a.opts.ArtifactsDir
	if dir == "" {
		dir = os.TempDir()
	}
	name := fmt.Sprintf("kimi-malformed-%s-%d.txt", sanitize(runID), time.Now().UnixNano())
	_ = os.WriteFile(filepath.Join(dir, name), line, 0o600)
}

// readFile reads a prompt file from disk.
func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func sanitize(s string) string {
	var b []byte
	for _, r := range s {
		switch r {
		case '/', ' ', ':', '\\':
			b = append(b, '_')
		default:
			b = append(b, []byte(string(r))...)
		}
	}
	return string(b)
}

func exitCodeFrom(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 1
}

// probeErrToMsg renders a non-fatal detection error for VersionResult.Error.
func (p probe) probeErrToMsg() string {
	if p.installed {
		return ""
	}
	return p.detail
}
