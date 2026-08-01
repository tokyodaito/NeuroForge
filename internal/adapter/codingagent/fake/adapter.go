package fake

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/protocol"
)

// defaultModels are the fake engine's catalogue. They are deliberately synthetic
// names so the core never depends on real model identifiers (rule §36.8).
var defaultModels = []protocol.ModelDescriptor{
	{ID: "fake/standard", Engine: "fake", Kind: protocol.ModelKindCoding, ContextWindow: 128000, MaxOutput: 8192, SupportsImages: true, CachedUsage: true},
	{ID: "fake/cheap", Engine: "fake", Kind: protocol.ModelKindCoding, ContextWindow: 32000, MaxOutput: 4096},
}

// Adapter is the in-process fake coding agent (spec §33.1). It is deterministic
// and performs no network or AI calls (rule §36.5). A run selects its behaviour
// via [AdapterOptions.Scenario]; the default is [ScenarioSuccess].
//
// Adapter implements codingagent.Adapter, so it composes with the supervisor and
// registry exactly like a real engine would. Concurrent runs each get their own
// cancel context.
type Adapter struct {
	opts AdapterOptions

	mu   sync.Mutex
	runs map[string]context.CancelFunc // runID -> cancel
}

// AdapterOptions configures a fake [Adapter].
type AdapterOptions struct {
	// Scenario selects the run behaviour (default [ScenarioSuccess]).
	Scenario Scenario
	// Engine overrides the reported engine id (default "fake").
	Engine string
	// Models overrides the reported model catalogue.
	Models []protocol.ModelDescriptor
	// Capabilities overrides the reported capability profile.
	Capabilities *protocol.AgentCapabilities
	// Installed, when false, makes Detect report the engine as missing.
	Installed bool
}

// New returns a fake adapter with the given options applied.
func New(opts AdapterOptions) *Adapter {
	if opts.Engine == "" {
		opts.Engine = "fake"
	}
	if opts.Scenario == "" {
		opts.Scenario = ScenarioSuccess
	}
	if len(opts.Models) == 0 {
		opts.Models = defaultModels
	}
	return &Adapter{opts: opts, runs: map[string]context.CancelFunc{}}
}

// ID implements codingagent.Adapter.
func (a *Adapter) ID() string { return a.opts.Engine }

// Detect implements codingagent.Adapter.
func (a *Adapter) Detect(context.Context) protocol.DetectionResult {
	return protocol.DetectionResult{Installed: a.opts.Installed, Path: "fake", Version: "1.0.0-fake", Detail: "fake coding agent (§33.1)"}
}

// Version implements codingagent.Adapter.
func (a *Adapter) Version(context.Context) protocol.VersionResult {
	return protocol.VersionResult{AdapterVersion: "1.0.0-fake", EngineVersion: "1.0.0-fake", ProtocolVersion: protocol.ProtocolVersion}
}

// Health implements codingagent.Adapter.
func (a *Adapter) Health(context.Context, protocol.Account) protocol.HealthResult {
	return protocol.HealthResult{Status: protocol.HealthOK, Detail: "fake agent always healthy"}
}

// Capabilities implements codingagent.Adapter.
func (a *Adapter) Capabilities(context.Context) protocol.AgentCapabilities {
	if a.opts.Capabilities != nil {
		return *a.opts.Capabilities
	}
	return protocol.AgentCapabilities{
		HeadlessMode:         true,
		StreamingEvents:      true,
		StructuredOutput:     true,
		ImageInput:           true,
		SessionResume:        true,
		LiveUserMessages:     true,
		ModelSelection:       true,
		UsageReporting:       true,
		CachedUsageReporting: true,
	}
}

// ListModels implements codingagent.Adapter.
func (a *Adapter) ListModels(context.Context, protocol.Account) ([]protocol.ModelDescriptor, error) {
	out := make([]protocol.ModelDescriptor, len(a.opts.Models))
	copy(out, a.opts.Models)
	return out, nil
}

// InspectQuota implements codingagent.Adapter.
func (a *Adapter) InspectQuota(context.Context, protocol.Account) protocol.QuotaSnapshot {
	return protocol.QuotaSnapshot{Confidence: protocol.QuotaConfProviderReported, State: protocol.QuotaStateAvailable, Window: "unlimited", Reason: "fake agent has no real quota"}
}

// Start implements codingagent.Adapter.
func (a *Adapter) Start(ctx context.Context, req protocol.AgentRunRequest, sink codingagent.EventSink) (protocol.RunHandle, error) {
	return a.startRun(ctx, req.RunID, req.Engine, req.Model, req.Workspace, false, req.SessionID, req.Prompt, sink)
}

// Resume implements codingagent.Adapter.
func (a *Adapter) Resume(ctx context.Context, req protocol.ResumeRequest, sink codingagent.EventSink) (protocol.RunHandle, error) {
	return a.startRun(ctx, req.RunID, req.Engine, req.Model, req.Workspace, true, req.SessionID, "", sink)
}

func (a *Adapter) startRun(ctx context.Context, runID, engine, model, workspace string, isResume bool, sessionID, prompt string, sink codingagent.EventSink) (protocol.RunHandle, error) {
	if runID == "" {
		runID = "fake-run"
	}
	if engine == "" {
		engine = a.opts.Engine
	}

	runCtx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	a.runs[runID] = cancel
	a.mu.Unlock()

	handle := protocol.RunHandle{RunID: runID, Engine: engine, Model: model, Account: protocol.Account{}, SessionID: sessionID}
	if handle.SessionID == "" {
		handle.SessionID = "fake-session-1"
	}

	em := &sinkEmitter{sink: sink, runID: runID, workspace: workspace}
	params := runParams{
		workspace:     workspace,
		engine:        engine,
		model:         model,
		runID:         runID,
		sessionID:     handle.SessionID,
		startIsResume: isResume,
		prompt:        prompt,
	}
	// Pick the scenario: the model id can override the adapter's default. This
	// is what the minimal-run black-box tests use to drive each outcome via
	// `forge run --engine fake --model fake/<scenario>` (e.g.
	// `fake/write-commit`, `fake/no-change`, `fake/cancel`, `fake/crash`). When
	// the model is empty or unrecognized, the adapter's configured scenario
	// (default ScenarioSuccess) is used.
	scenario := a.opts.Scenario
	if override, ok := scenarioFromModel(model); ok {
		scenario = override
	}
	sc := resolveScenario(scenario, params)
	// For resume, force the first event to run.resumed.
	if isResume && len(sc.steps) > 0 && sc.steps[0].event != nil {
		sc.steps[0].event.kind = "run.resumed"
	}

	// Replay runs synchronously: Start blocks until the run finishes or is
	// cancelled, mirroring the real adapter contract (Start streams to
	// completion). Hang scenarios block here until Cancel.
	go func() {
		defer func() {
			a.mu.Lock()
			delete(a.runs, runID)
			cancel()
			a.mu.Unlock()
		}()
		_, _ = replay(runCtx, sc, params, em)
	}()

	return handle, nil
}

// scenarioFromModel maps a model id of the form "fake/<scenario-name>" to the
// corresponding Scenario. Returns (zero, false) when the model is empty or does
// not match the fake-scenario form (so normal model ids pass through to the
// adapter's configured scenario). Used by the minimal-run black-box tests to
// drive each outcome via the engine id `fake` plus a model override.
func scenarioFromModel(model string) (Scenario, bool) {
	const prefix = "fake/"
	if !strings.HasPrefix(model, prefix) {
		return "", false
	}
	name := strings.TrimPrefix(model, prefix)
	s := Scenario(name)
	if !IsValidScenario(s) {
		return "", false
	}
	return s, true
}

// SendMessage implements codingagent.Adapter.
func (a *Adapter) SendMessage(context.Context, protocol.RunHandle, protocol.AgentMessage) error {
	return nil
}

// Cancel implements codingagent.Adapter. It cancels the run's context, which
// terminates the replay (hang scenarios emit run.cancelled).
func (a *Adapter) Cancel(_ context.Context, handle protocol.RunHandle) error {
	a.mu.Lock()
	cancel, ok := a.runs[handle.RunID]
	a.mu.Unlock()
	if !ok {
		return fmt.Errorf("fake: unknown run %q", handle.RunID)
	}
	cancel()
	return nil
}

// ClassifyFailure implements codingagent.Adapter.
func (a *Adapter) ClassifyFailure(exitCode int, events []protocol.NormalizedEvent, stderr string) protocol.FailureClassification {
	return codingagent.DefaultClassify(exitCode, events, stderr)
}

// sinkEmitter adapts a codingagent.EventSink to the replay [emitter] interface.
type sinkEmitter struct {
	sink      codingagent.EventSink
	runID     string
	workspace string
}

func (s *sinkEmitter) emit(ctx context.Context, ev protocol.NormalizedEvent) error {
	if s.sink == nil {
		return nil
	}
	return s.sink.OnEvent(ctx, ev)
}

func (s *sinkEmitter) emitRaw(_ context.Context, _ string) error {
	// In-process mode has no raw stdout; the malformed scenario is exercised
	// end-to-end via the declarative/executable path. Emit a warning instead so
	// the in-process consumer still sees the malformed signal.
	if s.sink == nil {
		return nil
	}
	return s.sink.OnEvent(context.Background(), protocol.NormalizedEvent{
		Type:    protocol.EventWarning,
		Warning: &protocol.WarningPayload{Code: "malformed.json", Message: "malformed output (in-process)", Recoverable: true},
		RunID:   s.runID,
	})
}

func (s *sinkEmitter) write(_ context.Context, path, content string) error {
	// Simulated edits go into the run's workspace. When no workspace is provided
	// (e.g. an in-process test that does not care about files) the write is
	// skipped rather than polluting the process working directory.
	if s.workspace == "" {
		return nil
	}
	return fileWrite(s.workspace, path, content)
}

// gitAddAll runs `git add -A` inside the workspace (in-process only). It is
// used by the write-commit scenario to produce a real commit. When no
// workspace is configured the call is a no-op (so tests without a worktree do
// not pollute the process CWD).
func (s *sinkEmitter) gitAddAll(_ context.Context) error {
	if s.workspace == "" {
		return nil
	}
	return gitInWorkspace(s.workspace, "add", "-A")
}

// gitCommit runs `git commit -m <msg>` inside the workspace (in-process only).
// The commit uses a deterministic identity so it never needs the user's git
// config.
func (s *sinkEmitter) gitCommit(_ context.Context, msg string) error {
	if s.workspace == "" {
		return nil
	}
	return gitInWorkspace(s.workspace, "commit", "-m", msg,
		"--author=NeuroForge Fake <fake@neuroforge.local>")
}
