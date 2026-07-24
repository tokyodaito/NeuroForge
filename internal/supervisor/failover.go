package supervisor

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/protocol"
	"neuroforge/internal/audit"
	"neuroforge/internal/storage"
)

// CheckpointMoment labels when a checkpoint is created (mirrors
// workspace.CheckpointMoment; kept here as a string so the supervisor does not
// import the workspace package — the callback translates it). Spec §21.3.
type CheckpointMoment string

const (
	MomentPreQuotaSwitch CheckpointMoment = "pre-quota-switch"
	MomentPreRepair      CheckpointMoment = "pre-repair"
)

// WorkspaceHook abstracts the workspace operations the failover controller
// needs. The real workspace.Manager satisfies it via a thin adapter; tests can
// supply a fake. This keeps the supervisor free of a workspace import and the
// controller unit-testable with the fake agent.
type WorkspaceHook interface {
	// Checkpoint creates a checkpoint commit and returns its SHA.
	Checkpoint(ctx context.Context, workspaceID string, moment CheckpointMoment, message string) (string, error)
	// SetState parks/resumes the workspace (waiting_quota / quarantined / active).
	SetState(ctx context.Context, workspaceID, state string) error
	// HeadSHA returns the current HEAD of the workspace worktree.
	HeadSHA(ctx context.Context, workspaceID string) (string, error)
}

// QuotaHook abstracts the quota/circuit-breaker operations. The real
// quota.Manager satisfies it via an adapter.
type QuotaHook interface {
	// IsAvailable reports whether a route's account is currently routable.
	IsAvailable(engine, account string) bool
	// RecordFailure feeds a failure into the circuit breaker for a route.
	RecordFailure(engine, account string, class protocol.FailureClass)
	// RecordSuccess clears consecutive failures for a route.
	RecordSuccess(engine, account string)
}

// FailoverPlan describes a run with its fallback chain (spec §21.1).
type FailoverPlan struct {
	WorkspaceID   string
	WorkspacePath string
	Primary       Route
	Fallbacks     []Route
	// Prompt is the initial prompt for the primary route.
	Prompt string
	Scope  []string
	// TurnLimit / Timeout per attempt. Zero uses the supervisor default.
	TurnLimit int
	Timeout   time.Duration
	// SpecHash / BaseSHA capture the task context for continuation packs.
	SpecHash string
	BaseSHA  string
	// ArtifactsDir is where continuation packs are written.
	ArtifactsDir string
	// MaxFailoverHops bounds how many engine switches are attempted (defence
	// in depth against a misconfigured chain). Zero means len(Fallbacks).
	MaxFailoverHops int
}

// AttemptRecord is one attempt in the failover history (for audit/explanation).
type AttemptRecord struct {
	Route      Route
	Outcome    string // completed | failed | cancelled
	Failure    *protocol.FailureClassification
	Action     RecoveryAction
	Cooldown   time.Duration
	Resumed    bool
	PromptUsed string // truncated; for audit only
}

// FailoverOutcome is the result of a failover run.
type FailoverOutcome struct {
	Success            bool
	FinalResult        RunResult
	Attempts           []AttemptRecord
	ParkedWaitingQuota bool
	Quarantined        bool
	// TerminalFailure is set when the run ended in a non-retryable failure
	// (build/test/scope/policy). Nil on success or wait/quarantine.
	TerminalFailure *protocol.FailureClassification
	// PacksWritten is the number of continuation packs written during the run.
	PacksWritten int
}

// FailoverController runs an agent across a route chain with continuation packs
// and bounded recovery (spec §21). It is the cross-engine failover orchestrator
// (AC-15): on a provider-side failure it writes a continuation pack, opens the
// circuit, selects a fallback route and continues from the current useful state
// — without transferring the full conversation history to the fallback.
//
// Recovery is bounded: no class triggers an infinite retry (spec §32). When
// every route is quota-exhausted the work is parked in WAITING_QUOTA; an
// unrecoverable failure quarantines it for human review.
type FailoverController struct {
	sup        *Supervisor
	classifier *RecoveryClassifier
	resume     *ResumePolicy
	quota      QuotaHook
	hook       WorkspaceHook
	db         *storage.DB
	audit      *audit.Recorder
	logger     *slog.Logger
	sleep      func(time.Duration)
}

// FailoverOptions configures a FailoverController.
type FailoverOptions struct {
	Supervisor *Supervisor
	// Classifier defaults to NewRecoveryClassifier().
	Classifier *RecoveryClassifier
	// Resume defaults to NewResumePolicy().
	Resume *ResumePolicy
	// Quota is the circuit-breaker hook (may be nil — then availability is
	// presumed and no failures are fed back to a breaker).
	Quota QuotaHook
	// Hook is the workspace hook (checkpoint + state). May be nil for tests
	// that do not exercise checkpoints/state.
	Hook WorkspaceHook
	// DB records continuation packs durably (may be nil only when Hook is nil
	// and no packs are expected).
	DB     *storage.DB
	Audit  *audit.Recorder
	Logger *slog.Logger
	// Sleep defaults to time.Sleep; overridden in tests for fast cooldown.
	Sleep func(time.Duration)
}

// NewFailoverController constructs a controller. A nil classifier/resume uses
// the defaults.
func NewFailoverController(opts FailoverOptions) *FailoverController {
	if opts.Classifier == nil {
		opts.Classifier = NewRecoveryClassifier()
	}
	if opts.Resume == nil {
		opts.Resume = NewResumePolicy()
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	}
	if opts.Sleep == nil {
		opts.Sleep = time.Sleep
	}
	return &FailoverController{
		sup:        opts.Supervisor,
		classifier: opts.Classifier,
		resume:     opts.Resume,
		quota:      opts.Quota,
		hook:       opts.Hook,
		db:         opts.DB,
		audit:      opts.Audit,
		logger:     opts.Logger,
		sleep:      opts.Sleep,
	}
}

// Run executes the plan. It returns when the work succeeds, reaches a terminal
// failure, is parked in WAITING_QUOTA, or is quarantined.
func (f *FailoverController) Run(ctx context.Context, plan FailoverPlan) (FailoverOutcome, error) {
	if f.sup == nil {
		return FailoverOutcome{}, errors.New("failover: supervisor is required")
	}
	out := FailoverOutcome{}

	// Build the ordered chain of routes to try.
	chain := []Route{plan.Primary}
	chain = append(chain, plan.Fallbacks...)
	maxHops := plan.MaxFailoverHops
	if maxHops == 0 {
		maxHops = len(plan.Fallbacks)
	}

	routeIdx := 0
	attemptsOnRoute := 0 // same-route retries for the current class
	lastFailure := protocol.FailureClassification{}
	var accumulatedPack *ContinuationPack

	for routeIdx < len(chain) {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		route := chain[routeIdx]

		// Decide the prompt: original on the primary, continuation-pack prompt
		// on a fallback (never the full conversation, spec §21.2).
		prompt := plan.Prompt
		resumed := false
		if accumulatedPack != nil {
			prompt = RenderFallbackPrompt(*accumulatedPack)
		}

		f.logger.Info("failover attempt", "workspace", plan.WorkspaceID,
			"engine", route.Engine, "model", route.Model,
			"route_idx", routeIdx, "attempts_on_route", attemptsOnRoute,
			"resumed", accumulatedPack != nil)

		result, runErr := f.sup.Run(ctx, RunRequest{
			WorkspaceID: plan.WorkspaceID,
			Engine:      route.Engine,
			Model:       route.Model,
			Prompt:      prompt,
			Scope:       plan.Scope,
			TurnLimit:   plan.TurnLimit,
			Timeout:     plan.Timeout,
		}, plan.WorkspacePath)
		if runErr != nil {
			// The run could not even start (unknown engine etc.) — treat as
			// an internal error and try the next route.
			lastFailure = protocol.FailureClassification{
				Class: protocol.FailureInternalError, Policy: protocol.PolicyRetry,
				Reason: "run failed to start: " + runErr.Error(),
			}
			rec := AttemptRecord{Route: route, Outcome: "failed",
				Failure: &lastFailure, Action: ActionFailover, PromptUsed: truncPrompt(prompt)}
			out.Attempts = append(out.Attempts, rec)
			if f.advanceOrStop(ctx, &out, plan, chain, &routeIdx, &attemptsOnRoute, lastFailure) {
				return out, nil
			}
			continue
		}

		if !result.Failed && !result.Cancelled {
			// Success.
			if f.quota != nil {
				f.quota.RecordSuccess(route.Engine, route.Account)
			}
			out.Success = true
			out.FinalResult = result
			out.Attempts = append(out.Attempts, AttemptRecord{
				Route: route, Outcome: "completed", Resumed: resumed,
				PromptUsed: truncPrompt(prompt),
			})
			f.auditFailover(ctx, plan.WorkspaceID, "failover.completed", route, len(out.Attempts))
			return out, nil
		}

		// Failure: classify it.
		classification := f.classify(result, route)
		lastFailure = classification
		if f.quota != nil && classification.Class.IsValid() {
			f.quota.RecordFailure(route.Engine, route.Account, classification.Class)
		}

		// On a provider-side failure AFTER useful edits, checkpoint before
		// switching (spec §21.3 pre-quota-switch). This is what keeps the
		// progress for the fallback (AC-15).
		if classification.Failover || classification.Policy == protocol.PolicyFailover {
			if f.maybeCheckpoint(ctx, plan) {
				headSHA, _ := f.headSHA(ctx, plan.WorkspaceID)
				pack := BuildPackFromRun(plan.WorkspaceID, plan.WorkspaceID,
					plan.BaseSHA, headSHA, plan.SpecHash, result)
				pack.Remaining = remainingObjective(plan.Prompt, accumulatedPack)
				if accumulatedPack != nil {
					merged := MergePacks(*accumulatedPack, pack)
					accumulatedPack = &merged
				} else {
					accumulatedPack = &pack
				}
				if f.hook != nil {
					_ = f.hook.SetState(ctx, plan.WorkspaceID, "active")
				}
			}
			// Persist the pack durably (AC-15, AC-27).
			if f.db != nil {
				if path, err := WriteContinuationPack(ctx, f.db, f.audit,
					plan.WorkspaceID, plan.ArtifactsDir, *accumulatedPack); err == nil {
					out.PacksWritten++
					f.logger.Info("continuation pack written", "workspace", plan.WorkspaceID, "path", path)
				} else {
					f.logger.Warn("write continuation pack failed", "err", err)
				}
			}
		}

		// Ask the classifier for the bounded recovery action.
		decision := f.classifier.Classify(RecoveryInput{
			Failure:            classification,
			AttemptsUsed:       attemptsOnRoute,
			FallbacksAvailable: routeIdx < len(chain)-1 && routeIdx < maxHops+1,
			AnyRouteAvailable:  f.anyRouteAvailable(chain),
		})

		out.Attempts = append(out.Attempts, AttemptRecord{
			Route: route, Outcome: failOutcome(result), Failure: &classification,
			Action: decision.Action, Cooldown: decision.Cooldown,
			Resumed: resumed, PromptUsed: truncPrompt(prompt),
		})

		f.logger.Info("failover decision", "workspace", plan.WorkspaceID,
			"engine", route.Engine, "action", decision.Action,
			"cooldown", decision.Cooldown, "reason", decision.Reason)

		switch decision.Action {
		case ActionRetry:
			attemptsOnRoute++
			if decision.Cooldown > 0 {
				f.sleep(decision.Cooldown)
			}
			continue // same route

		case ActionFailover:
			attemptsOnRoute = 0
			routeIdx++
			if routeIdx >= len(chain) || routeIdx > maxHops {
				// Exhausted the chain. Fall through to stop logic.
				return f.stop(ctx, out, plan, decision)
			}
			continue

		case ActionWaitQuota:
			return f.stop(ctx, out, plan, decision)

		case ActionQuarantine:
			return f.stop(ctx, out, plan, decision)

		case ActionTerminal:
			cls := classification
			out.TerminalFailure = &cls
			return f.stop(ctx, out, plan, decision)

		case ActionPause:
			return f.stop(ctx, out, plan, decision)

		default:
			return f.stop(ctx, out, plan, decision)
		}
	}

	// Fell out of the loop without a terminal decision.
	return f.stop(ctx, out, plan, RecoveryDecision{
		Action: ActionQuarantine, FailureClass: lastFailure.Class,
		Reason: "route chain exhausted without success",
	})
}

// stop applies the terminal-ish decision's side effects (park/quarantine) and
// returns the outcome.
func (f *FailoverController) stop(ctx context.Context, out FailoverOutcome, plan FailoverPlan, d RecoveryDecision) (FailoverOutcome, error) {
	switch d.Action {
	case ActionWaitQuota:
		out.ParkedWaitingQuota = true
		if f.hook != nil {
			_ = f.hook.SetState(ctx, plan.WorkspaceID, "waiting_quota")
		}
		f.auditFailoverDecision(ctx, plan.WorkspaceID, "failover.waiting_quota", d)
	case ActionQuarantine:
		out.Quarantined = true
		if f.hook != nil {
			_ = f.hook.SetState(ctx, plan.WorkspaceID, "quarantined")
		}
		f.auditFailoverDecision(ctx, plan.WorkspaceID, "failover.quarantined", d)
	case ActionTerminal:
		if f.hook != nil {
			_ = f.hook.SetState(ctx, plan.WorkspaceID, "failed")
		}
		f.auditFailoverDecision(ctx, plan.WorkspaceID, "failover.terminal", d)
	case ActionPause:
		if f.hook != nil {
			_ = f.hook.SetState(ctx, plan.WorkspaceID, "failed")
		}
		f.auditFailoverDecision(ctx, plan.WorkspaceID, "failover.paused", d)
	default:
		f.auditFailoverDecision(ctx, plan.WorkspaceID, "failover.exhausted", d)
	}
	return out, nil
}

// advanceOrStop handles the "run could not start" path: try the next route or
// stop. Returns true if the run is over (stopped).
func (f *FailoverController) advanceOrStop(ctx context.Context, out *FailoverOutcome, plan FailoverPlan, chain []Route, routeIdx, attemptsOnRoute *int, fc protocol.FailureClassification) bool {
	d := f.classifier.Classify(RecoveryInput{
		Failure:            fc,
		AttemptsUsed:       *attemptsOnRoute,
		FallbacksAvailable: *routeIdx < len(chain)-1,
		AnyRouteAvailable:  f.anyRouteAvailable(chain),
	})
	if d.Action == ActionFailover {
		*attemptsOnRoute = 0
		*routeIdx++
		return false
	}
	res, _ := f.stop(ctx, *out, plan, d)
	*out = res
	return true
}

// classify turns a RunResult into a FailureClassification using the adapter's
// ClassifyFailure (spec §12.2) when available, else DefaultPolicy.
func (f *FailoverController) classify(result RunResult, route Route) protocol.FailureClassification {
	if result.Outcome.Failure != nil {
		fc := protocol.DefaultPolicy(result.Outcome.Failure.Class)
		fc.Reason = result.Outcome.Failure.Reason
		fc.ExitCode = result.Outcome.Failure.ExitCode
		return fc
	}
	// No explicit failure payload: synthesize from the terminal event type.
	if result.Cancelled {
		return protocol.DefaultPolicy(protocol.FailureCancelled)
	}
	return protocol.DefaultPolicy(protocol.FailureInternalError)
}

// maybeCheckpoint creates a pre-quota-switch checkpoint when the hook + workspace
// are configured. It returns true if checkpointing was attempted (hook present).
func (f *FailoverController) maybeCheckpoint(ctx context.Context, plan FailoverPlan) bool {
	if f.hook == nil {
		return false
	}
	_, err := f.hook.Checkpoint(ctx, plan.WorkspaceID, MomentPreQuotaSwitch,
		"checkpoint before provider failover (§21.3)")
	if err != nil {
		f.logger.Warn("pre-failover checkpoint failed", "err", err)
	}
	return true
}

func (f *FailoverController) headSHA(ctx context.Context, workspaceID string) (string, error) {
	if f.hook == nil {
		return "", nil
	}
	return f.hook.HeadSHA(ctx, workspaceID)
}

func (f *FailoverController) anyRouteAvailable(chain []Route) bool {
	if f.quota == nil {
		return true
	}
	for _, r := range chain {
		if f.quota.IsAvailable(r.Engine, r.Account) {
			return true
		}
	}
	return false
}

func (f *FailoverController) auditFailover(ctx context.Context, workspaceID, event string, route Route, attempts int) {
	if f.audit == nil {
		return
	}
	_, _ = f.audit.Record(ctx, audit.Event{
		Type: event, Scope: audit.ScopeTask, ScopeID: workspaceID, Actor: audit.ActorDaemon,
		Payload: audit.Payload("engine", route.Engine, "model", route.Model, "attempts", attempts),
	})
}

func (f *FailoverController) auditFailoverDecision(ctx context.Context, workspaceID, event string, d RecoveryDecision) {
	if f.audit == nil {
		return
	}
	_, _ = f.audit.Record(ctx, audit.Event{
		Type: event, Scope: audit.ScopeTask, ScopeID: workspaceID, Actor: audit.ActorDaemon,
		Payload: audit.Payload(
			"action", string(d.Action),
			"failure_class", string(d.FailureClass),
			"reason", d.Reason,
			"attempts_used", d.AttemptsUsed,
			"attempts_max", d.AttemptsMax),
	})
}

func failOutcome(r RunResult) string {
	switch {
	case r.Failed:
		return "failed"
	case r.Cancelled:
		return "cancelled"
	default:
		return "completed"
	}
}

func truncPrompt(p string) string {
	const max = 160
	if len(p) <= max {
		return p
	}
	return p[:max] + "…"
}

// remainingObjective derives the "remaining" list for a continuation pack from
// the original prompt and any prior pack. It keeps the pack focused on what is
// left (spec §21.2).
func remainingObjective(originalPrompt string, prior *ContinuationPack) []string {
	out := []string{"complete-the-objective"}
	if prior != nil {
		out = append(out, prior.Remaining...)
	}
	if originalPrompt != "" {
		out = append(out, "objective: "+firstLine(originalPrompt))
	}
	return dedupeSorted(out)
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}

// Compile-time assertions that the hook interfaces are usable.
var (
	_ WorkspaceHook = (*workspaceHookAdapter)(nil)
)

// workspaceHookAdapter is a placeholder satisfying the interface so the
// controller compiles standalone; the daemon wires a real adapter.
type workspaceHookAdapter struct{}

func (*workspaceHookAdapter) Checkpoint(context.Context, string, CheckpointMoment, string) (string, error) {
	return "", errors.New("workspace hook not wired")
}
func (*workspaceHookAdapter) SetState(context.Context, string, string) error {
	return errors.New("workspace hook not wired")
}
func (*workspaceHookAdapter) HeadSHA(context.Context, string) (string, error) {
	return "", nil
}

// codingagent import retained for the adapter surface used by the supervisor.
var _ = codingagent.Default
