package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/protocol"
	"neuroforge/internal/audit"
)

// RunRequest describes a supervised agent run.
type RunRequest struct {
	WorkspaceID string
	Engine      string
	Model       string
	Prompt      string
	Scope       []string
	TurnLimit   int
	Timeout     time.Duration
}

// RunResult is the outcome of a supervised run.
type RunResult struct {
	Handle    protocol.RunHandle
	Outcome   protocol.NormalizedEvent // the terminal event (run.completed/failed/cancelled)
	Events    []protocol.NormalizedEvent
	Failed    bool
	Cancelled bool
}

// Supervisor runs coding-agent adapters inside a workspace, enforcing:
//   - an allowlisted environment (no merge creds — AC-28, §29.2);
//   - turn limits (§22.7);
//   - a hard timeout;
//   - checkpoint capture and failure classification.
//
// The supervisor is the single owner of agent process lifecycles (spec §11.4,
// §12). It consumes the versioned adapter protocol (ADR-0005/0012).
type Supervisor struct {
	adapters *codingagent.Registry
	audit    *audit.Recorder
	logger   *slog.Logger
	fullEnv  []string // the daemon's environment (filtered to an allowlist per run)
	now      func() time.Time

	cancelMu sync.Mutex
	cancels  map[string]context.CancelFunc // runID -> cancel
}

// Options configures a Supervisor.
type Options struct {
	// Adapters is the adapter registry. If nil, codingagent.Default() is used.
	Adapters *codingagent.Registry
	Audit    *audit.Recorder
	Logger   *slog.Logger
	// FullEnv is the daemon's environment. The supervisor filters it to an
	// allowlist before passing anything to an agent process (§29.2).
	FullEnv []string
}

// New creates a Supervisor.
func New(opts Options) *Supervisor {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	}
	registry := opts.Adapters
	if registry == nil {
		registry = codingagent.Default()
	}
	return &Supervisor{
		adapters: registry,
		audit:    opts.Audit,
		logger:   opts.Logger,
		fullEnv:  opts.FullEnv,
		now:      func() time.Time { return time.Now().UTC() },
		cancels:  make(map[string]context.CancelFunc),
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// Adapters returns the adapter registry the supervisor uses.
func (s *Supervisor) Adapters() *codingagent.Registry { return s.adapters }

// Run executes a single agent run synchronously, streaming events until the run
// terminates (completed/failed/cancelled) or the timeout fires.
//
// The agent process receives ONLY the allowlisted environment (AC-28). The
// workspace path is passed to the adapter so the agent writes inside the managed
// worktree, never the primary checkout.
func (s *Supervisor) Run(ctx context.Context, req RunRequest, workspacePath string) (RunResult, error) {
	if workspacePath == "" {
		return RunResult{}, errors.New("supervisor: workspace path is required")
	}
	adapter, ok := s.adapters.Lookup(req.Engine)
	if !ok {
		return RunResult{}, fmt.Errorf("supervisor: unknown engine %q", req.Engine)
	}

	runID := fmt.Sprintf("run-%d", s.now().UnixNano())
	if req.Engine == "" {
		req.Engine = adapter.ID()
	}

	// Build the allowlisted environment. This is the ONLY environment the agent
	// process will see (§29.2, AC-28). No merge creds, no daemon token, no
	// unrelated API keys.
	safeEnv := EnvAllowlist(s.fullEnv)
	if err := AssertEnvSafe(safeEnv); err != nil {
		return RunResult{}, fmt.Errorf("%w: %v", ErrEnvLeak, err)
	}

	// Apply the timeout (hard wall-clock limit). Zero means a sensible default.
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	s.registerCancel(runID, cancel)
	defer s.unregisterCancel(runID)

	// Collect events via a SliceSink (thread-safe).
	sink := &codingagent.SliceSink{}

	handle, err := adapter.Start(runCtx, protocol.AgentRunRequest{
		RunID:        runID,
		Engine:       req.Engine,
		Model:        req.Model,
		Workspace:    workspacePath,
		Prompt:       req.Prompt,
		Scope:        req.Scope,
		AllowlistEnv: safeEnv,
		TurnLimit:    req.TurnLimit,
		Timeout:      timeout,
	}, sink)
	if err != nil {
		return RunResult{}, fmt.Errorf("supervisor: start adapter: %w", err)
	}

	if s.audit != nil {
		_, _ = s.audit.Record(ctx, audit.Event{
			Type:    "agent.run_started",
			Scope:   audit.ScopeTask,
			ScopeID: req.WorkspaceID,
			Actor:   audit.ActorDaemon,
			Payload: audit.Payload(
				"engine", req.Engine, "model", req.Model,
				"run_id", runID, "session_id", handle.SessionID,
				"workspace", workspacePath),
		})
	}

	// Wait for the run to reach a terminal state. The adapter's Start streams to
	// completion asynchronously (per the fake adapter contract: Start launches a
	// goroutine that replays events). We poll the sink for a terminal event.
	terminal := s.waitForTerminal(runCtx, sink, timeout)

	events := sink.Events()
	result := RunResult{
		Handle:  handle,
		Events:  events,
		Outcome: terminal,
	}
	if terminal.Type == protocol.EventRunFailed {
		result.Failed = true
	} else if terminal.Type == protocol.EventRunCancelled {
		result.Cancelled = true
	}

	if s.audit != nil {
		auditType := "agent.run_completed"
		if result.Failed {
			auditType = "agent.run_failed"
		} else if result.Cancelled {
			auditType = "agent.run_cancelled"
		}
		payload := audit.Payload(
			"engine", req.Engine, "run_id", runID,
			"terminal", string(terminal.Type))
		if result.Failed && terminal.Failure != nil {
			payload["failure_class"] = string(terminal.Failure.Class)
			payload["failure_reason"] = terminal.Failure.Reason
		}
		_, _ = s.audit.Record(ctx, audit.Event{
			Type:    auditType,
			Scope:   audit.ScopeTask,
			ScopeID: req.WorkspaceID,
			Actor:   audit.ActorDaemon,
			Payload: payload,
		})
	}

	s.logger.Info("agent run finished", "engine", req.Engine, "run_id", runID,
		"terminal", terminal.Type, "events", len(events))
	return result, nil
}

// waitForTerminal polls the sink for a terminal event. The fake adapter runs
// Start asynchronously (it launches a goroutine), so we poll until we see a
// terminal event or the context expires.
func (s *Supervisor) waitForTerminal(ctx context.Context, sink *codingagent.SliceSink, timeout time.Duration) protocol.NormalizedEvent {
	// Give the run a grace period beyond the adapter timeout for event delivery.
	deadline := time.Now().Add(timeout + 5*time.Second)
	for {
		for _, ev := range sink.Events() {
			if ev.Type.IsTerminal() {
				return ev
			}
		}
		if ctx.Err() != nil {
			// Scan once more (the terminal event may have landed just as ctx expired).
			for _, ev := range sink.Events() {
				if ev.Type.IsTerminal() {
					return ev
				}
			}
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return protocol.NormalizedEvent{
					Type:    protocol.EventRunFailed,
					Failure: &protocol.FailurePayload{Class: "TIMEOUT", Reason: "run timed out"},
				}
			}
			return protocol.NormalizedEvent{Type: protocol.EventRunCancelled}
		}
		if time.Now().After(deadline) {
			return protocol.NormalizedEvent{
				Type:    protocol.EventRunFailed,
				Failure: &protocol.FailurePayload{Class: "TIMEOUT", Reason: "supervisor deadline exceeded"},
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Cancel terminates a running agent by runID.
func (s *Supervisor) Cancel(runID string) error {
	s.cancelMu.Lock()
	cancel, ok := s.cancels[runID]
	s.cancelMu.Unlock()
	if !ok {
		return fmt.Errorf("supervisor: unknown run %q", runID)
	}
	cancel()
	return nil
}

func (s *Supervisor) registerCancel(runID string, cancel context.CancelFunc) {
	s.cancelMu.Lock()
	s.cancels[runID] = cancel
	s.cancelMu.Unlock()
}

func (s *Supervisor) unregisterCancel(runID string) {
	s.cancelMu.Lock()
	delete(s.cancels, runID)
	s.cancelMu.Unlock()
}
