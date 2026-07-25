package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"neuroforge/internal/audit"
	"neuroforge/internal/quality"
)

// DispatchOptions tunes a single dispatch invocation.
type DispatchOptions struct {
	Engine        string        // override; empty → "fake" (rule §36.6)
	Model         string        // override; empty → default
	WorkPackageID string        // default "main"
	BaseBranch    string        // default project's current branch
	Timeout       time.Duration // default 5m
	// BuildContextPack, when true and the project path resolves, builds a
	// token-budgeted Context Pack (§22.3) and prepends it to the agent prompt
	// (rule §36.11: never dump the whole repo).
	BuildContextPack bool
}

// DispatchResult is the outcome of a single task dispatch through the production
// execution path.
type DispatchResult struct {
	TaskID      string
	ProjectID   string
	WorkspaceID string
	Outcome     string // completed | failed | cancelled
	UsageEvents int
	// EstimatedTokens is the sum of input+output+cached from the run.
	EstimatedTokens int
	// ContextPackBuilt reports whether a Context Pack was prepended to the prompt.
	ContextPackBuilt bool
	// MemoryLearned reports whether a project memory fact was recorded.
	MemoryLearned bool
}

// Scheduler is the production composition root. The daemon constructs it once
// during startup and shares it across request handlers. It owns the task
// execution path and the post-merge sentinel wiring.
//
// The scheduler is safe for concurrent use: every Dispatch runs independently
// against the injected Runtime, and the quality/accounting/memory sinks are
// themselves concurrency-safe.
type Scheduler struct {
	runtime     Runtime
	resolver    ProjectResolver
	usage       UsageSink
	memory      MemorySink
	postmerge   PostMergeSink
	packBuilder PackBuilder
	accounting  *quality.Accounting
	statistics  *quality.Statistics
	audit       *audit.Recorder
	logger      *slog.Logger
	now         func() time.Time
	reopener    TaskReopener
}

// Deps bundles the injected dependencies for New.
type Deps struct {
	Runtime     Runtime
	Resolver    ProjectResolver
	Usage       UsageSink
	Memory      MemorySink
	PostMerge   PostMergeSink
	PackBuilder PackBuilder
	Accounting  *quality.Accounting
	Statistics  *quality.Statistics
	Audit       *audit.Recorder
	Logger      *slog.Logger
}

// New constructs a Scheduler. Accounting/Statistics default to fresh in-process
// stores when nil so the scheduler is always usable.
func New(d Deps) (*Scheduler, error) {
	if d.Runtime == nil {
		return nil, fmt.Errorf("scheduler: Runtime is required")
	}
	if d.Resolver == nil {
		return nil, fmt.Errorf("scheduler: Resolver is required")
	}
	if d.Usage == nil {
		return nil, fmt.Errorf("scheduler: Usage sink is required")
	}
	if d.Memory == nil {
		return nil, fmt.Errorf("scheduler: Memory sink is required")
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	}
	acc := d.Accounting
	if acc == nil {
		acc = quality.NewAccounting()
	}
	stats := d.Statistics
	if stats == nil {
		stats = quality.NewStatistics()
	}
	return &Scheduler{
		runtime:     d.Runtime,
		resolver:    d.Resolver,
		usage:       d.Usage,
		memory:      d.Memory,
		postmerge:   d.PostMerge,
		packBuilder: d.PackBuilder,
		accounting:  acc,
		statistics:  stats,
		audit:       d.Audit,
		logger:      logger,
		now:         func() time.Time { return time.Now().UTC() },
	}, nil
}

// Accounting exposes the in-process token accounting (for the dashboard).
func (s *Scheduler) Accounting() *quality.Accounting { return s.accounting }

// Statistics exposes the in-process quality statistics (for routing feedback).
func (s *Scheduler) Statistics() *quality.Statistics { return s.statistics }

// Dispatch is the production execution path for one task (spec §10, §11.4, §22).
// It creates an attempt (workspace), optionally builds a Context Pack, runs the
// agent, records usage events durably (§6.1/§14.4), records the task outcome in
// the quality statistics (§19.1), and learns a project memory fact (§22.9).
//
// The scheduler performs NO Git network operations and NO delivery (AC-7, §28).
func (s *Scheduler) Dispatch(ctx context.Context, taskID string, opts DispatchOptions) (DispatchResult, error) {
	res := DispatchResult{TaskID: taskID}
	pctx, err := s.resolver.Resolve(ctx, taskID)
	if err != nil {
		return res, fmt.Errorf("scheduler: resolve task: %w", err)
	}
	res.ProjectID = pctx.ProjectID
	if opts.Engine == "" {
		opts.Engine = "fake"
	}
	if opts.WorkPackageID == "" {
		opts.WorkPackageID = "main"
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Minute
	}

	// 1. Create the attempt (workspace) via the dispatcher.
	wsID, wsPath, err := s.runtime.CreateAttempt(ctx, taskID, opts.WorkPackageID, opts.BaseBranch)
	if err != nil {
		return res, fmt.Errorf("scheduler: create attempt: %w", err)
	}
	res.WorkspaceID = wsID

	s.auditf(ctx, "scheduler.attempt_created", pctx.ProjectID, taskID, audit.Payload(
		"workspace", wsID, "engine", opts.Engine, "model", opts.Model))

	// 2. Build the prompt. Optionally prepend a token-budgeted Context Pack.
	prompt := pctx.TaskDesc
	if opts.BuildContextPack && s.packBuilder != nil {
		pack := s.buildPack(ctx, pctx)
		if pack != "" {
			prompt = pack + "\n\n---\n\nTask: " + pctx.TaskDesc
			res.ContextPackBuilt = true
		}
	}

	// 3. Run the agent inside the workspace.
	outcome, events, runErr := s.runtime.RunAgent(ctx, wsID, wsPath, opts.Engine, opts.Model, prompt, opts.Timeout)
	res.Outcome = outcome
	if runErr != nil {
		s.logger.Warn("scheduler: agent run failed", "task", taskID, "err", runErr)
	}

	// 4. Record usage events (§6.1/§14.4) — durable + in-process.
	for _, ev := range events {
		if ev.Type != "usage.updated" {
			continue
		}
		ue := quality.UsageEvent{
			TaskID:            taskID,
			ProjectID:         pctx.ProjectID,
			Provider:          opts.Engine,
			Model:             opts.Model,
			Kind:              quality.UsageCoding,
			InputTokens:       int(ev.InputTokens),
			CachedInputTokens: int(ev.CacheRead),
			OutputTokens:      int(ev.OutputTokens),
			CostUSD:           ev.CostUSD,
			OccurredAt:        s.now(),
		}
		if err := s.usage.RecordUsage(ctx, ue); err != nil {
			s.logger.Warn("scheduler: record usage failed", "task", taskID, "err", err)
		}
		s.accounting.Record(ue)
		res.UsageEvents++
		res.EstimatedTokens += int(ev.InputTokens + ev.OutputTokens + ev.CacheRead)
	}

	// 5. Record the quality outcome (§19.1 routing feedback).
	outcomeKind := quality.OutcomeSuccess
	if outcome == "failed" || outcome == "cancelled" {
		outcomeKind = quality.OutcomeFailure
	}
	s.statistics.Record(quality.TaskOutcome{
		TaskID:    taskID,
		ProjectID: pctx.ProjectID,
		Engine:    opts.Engine,
		Model:     opts.Model,
		RouteTier: "STANDARD",
		Outcome:   outcomeKind,
	})

	// 6. Learn a project memory fact (§22.9): that the task was dispatched.
	memVal := fmt.Sprintf("Task %s dispatched via %s/%s — outcome %s.", taskID, opts.Engine, opts.Model, outcome)
	if err := s.memory.Learn(ctx, pctx.ProjectID, "accepted_decision", "dispatch-"+taskID, memVal, "medium"); err != nil {
		s.logger.Warn("scheduler: learn memory failed", "task", taskID, "err", err)
	} else {
		res.MemoryLearned = true
	}

	s.auditf(ctx, "scheduler.dispatch_completed", pctx.ProjectID, taskID, audit.Payload(
		"outcome", outcome, "usage_events", res.UsageEvents,
		"est_tokens", res.EstimatedTokens, "memory_learned", res.MemoryLearned))
	return res, nil
}

// buildPack builds a token-budgeted Context Pack (§22.3) from the project repo
// index, feeding high-confidence project memory rules + known failures. It
// returns "" when no pack can be built (e.g. empty repo). The scheduler never
// dumps the whole repo (rule §36.11) — the pack is trimmed to a budget by the
// repoinfo package.
func (s *Scheduler) buildPack(ctx context.Context, pctx ProjectContext) string {
	rules := s.memory.Rules(ctx, pctx.ProjectID)
	pack, err := s.packBuilder.BuildPack(ctx, pctx.ProjectPath, pctx.TaskDesc, rules)
	if err != nil {
		s.logger.Warn("scheduler: build context pack failed", "err", err)
		return ""
	}
	return pack
}

// auditf records a scheduler audit event (§29.4). Errors are logged but never
// block the dispatch.
func (s *Scheduler) auditf(ctx context.Context, eventType, projectID, taskID string, payload map[string]any) {
	if s.audit == nil {
		return
	}
	if _, err := s.audit.Record(ctx, audit.Event{
		Type:    eventType,
		Scope:   audit.ScopeTask,
		ScopeID: taskID,
		Actor:   audit.ActorDaemon,
		Payload: payload,
	}); err != nil {
		s.logger.Warn("scheduler: audit record failed", "type", eventType, "err", err)
	}
}

// discardWriter is a no-op writer for the default logger.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
