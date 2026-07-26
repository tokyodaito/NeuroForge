package runapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
	"neuroforge/internal/audit"
	"neuroforge/internal/storage"
	"neuroforge/internal/task"
	"neuroforge/internal/workspace"
)

// Finalization protocol (BF-07 / S3) — crash-consistent, retry-safe.
//
// Git refs and SQLite cannot share one physical transaction. Consistency is
// achieved via durable intent + reconciliation/compensation:
//
//  1. inspect Git          (caller-provided Inspection)
//  2. classify             (pure)
//  3. record finalize intent (phase=pending) in SQLite
//  4. create/verify result ref when applicable
//  5. advance intent       (phase=ref_ready)
//  6. atomically persist terminal workspace + task + audit; delete intent
//  7. mark finalization complete (intent row gone)
//
// Recovery after any crash point:
//   - intent pending, no ref  → resume: create ref, commit terminal
//   - intent ref_ready, ref ok → resume: commit terminal
//   - intent + ref wrong SHA  → conflict (never silent overwrite)
//   - terminal already set    → idempotent return
//   - same-process tx failure after ref → leave intent+ref; retry resumes
//     (no orphan: either complete or explicit compensate on abort paths
//     that never wrote intent)
//
// Concurrent finalizers for the same workspace: SQLite serializes intent
// upsert + terminal commit; the loser observes terminal/idempotent result.

// FinalizeRequest is the input to [Service.Finalize]. It carries everything
// the atomic finalize step needs: the workspace id, the terminal adapter
// event, the worktree inspection, and the engine/model ids (for the audit
// payload).
type FinalizeRequest struct {
	WorkspaceID string
	// TerminalEvent is the supervisor's normalized terminal event
	// (run.completed/failed/cancelled). When TimeoutClass is set the event
	// carries Failure.Class == TIMEOUT.
	TerminalEvent protocol.NormalizedEvent
	// Inspection is the actual worktree state from workspace.InspectWorktree
	// (FR-9). Required.
	Inspection workspace.Inspection
	// TaskID is the task this workspace belongs to (so the task state can
	// transition in the same atomic tx).
	TaskID string
	// Engine is the adapter engine id used for this run (for audit).
	Engine string
	// Model is the model id used for this run (for audit).
	Model string
	// RunID is the adapter run id (for audit + idempotency dedup).
	RunID string
}

// FinalizeResult is the value returned by Finalize. It records the classified
// outcome and the durable ids the CLI will surface.
type FinalizeResult struct {
	Outcome       Outcome
	WorkspaceID   string
	TaskID        string
	WorkspacePath string
	BaseSHA       string
	ActualHEAD    string
	CommitSHA     string // the result commit SHA when outcome is *-with-commit, else ""
	ResultBranch  string // refs/heads/forge/result/<task-id> when created, else ""
	ChangedFiles  []string
	Engine        string
	Model         string
	RunID         string
	// Idempotent is true when Finalize detected the workspace was already
	// terminal and returned the recorded outcome without re-applying the
	// transition (OUTCOME_CONTRACT.md §6).
	Idempotent bool
}

// Service is the thin application service that composes the finalize step
// (S3..S5). It owns the single chokepoint where:
//
//   - the run terminal is observed (caller-provided);
//   - Git is inspected (caller-provided);
//   - the outcome is classified (S2);
//   - workspace + task states are persisted atomically (S3);
//   - the result ref is created when applicable (S5);
//   - usage events are recorded for the run (S7);
//   - the CLI result is derived.
//
// The service never imports the daemon, transport, scheduler, failover,
// postmerge, review or merge packages (NFR-7).
type Service struct {
	wm      WorkspaceManager
	tasks   TaskBacklog
	audit   *audit.Recorder
	db      *storage.DB
	now     func() time.Time
	refs    RefCreator
	usage   UsageSink
	taskRev TaskStateReader
	// S7 fields — only populated when the service is constructed via
	// NewServiceWithRunner (the daemon path). Finalize-only constructions
	// (the unit tests) leave these nil and never call Run.
	creator WorkspaceCreator
	sup     SupervisorRunner

	// testHookAfterIntent, if non-nil, is invoked after the intent row is
	// persisted and before the result ref is ensured. Tests use it to model
	// a process crash (loss of in-memory state) without killing the process.
	testHookAfterIntent func() error
	// testHookAfterRef, if non-nil, is invoked after the result ref is
	// ensured and before the terminal DB transaction. Models crash-after-ref.
	testHookAfterRef func() error
}

// WorkspaceManager is the subset of *workspace.Manager the runapp service
// consumes. Declared as an interface so the runapp package does not pull the
// whole workspace API (and so tests can stub it).
type WorkspaceManager interface {
	Get(ctx context.Context, id string) (workspace.Workspace, error)
	UpdateStateTx(ctx context.Context, tx *storage.Tx, id string, state workspace.State, headSHA, resultBranch, resultSHA string) error
	InspectWorktree(ctx context.Context, ws workspace.Workspace) (workspace.Inspection, error)
	EnsureResultRef(ctx context.Context, ws workspace.Workspace, headSHA string) (resultBranch string, err error)
	DeleteResultRef(ctx context.Context, ws workspace.Workspace) error
	ResolveResultRef(ctx context.Context, taskID, dir string) (string, error)
}

// TaskBacklog is the subset of *task.Backlog the runapp service consumes.
type TaskBacklog interface {
	Get(ctx context.Context, id string) (task.Task, error)
	Transition(ctx context.Context, id string, action task.Action) (task.Task, error)
}

// TaskStateReader reads the current task state without transitioning it.
// Implemented by *task.Backlog via Get.
type TaskStateReader interface {
	Get(ctx context.Context, id string) (task.Task, error)
}

// RefCreator creates/updates the local result ref, resolves it, and removes
// it as a compensating action when aborting before intent was durable (BF-07).
// Implemented by *workspace.Manager.
type RefCreator interface {
	EnsureResultRef(ctx context.Context, ws workspace.Workspace, headSHA string) (resultBranch string, err error)
	DeleteResultRef(ctx context.Context, ws workspace.Workspace) error
	ResolveResultRef(ctx context.Context, taskID, dir string) (string, error)
}

// UsageSink durably persists one usage event. Reused from the scheduler.
type UsageSink interface {
	RecordUsage(ctx context.Context, e UsageEvent) error
}

// UsageEvent is the runapp-local usage event shape (so runapp does not import
// the quality package). The daemon-side adapter converts.
type UsageEvent struct {
	TaskID            string
	ProjectID         string
	Provider          string
	Model             string
	Kind              string
	InputTokens       int64
	CachedInputTokens int64
	OutputTokens      int64
	Generations       int64
	CostUSD           float64
	OccurredAt        time.Time
}

// Options configures a Service construction.
type Options struct {
	Workspaces WorkspaceManager
	Tasks      TaskBacklog
	Audit      *audit.Recorder
	DB         *storage.DB
	Usage      UsageSink
	Now        func() time.Time
}

// NewService constructs a Service. db is the same *storage.DB the workspace
// manager and task backlog use — Finalize opens a transaction against it so the
// state + audit + task transitions are atomic (STATE_MACHINE.md §3.4).
func NewService(opts Options) *Service {
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{
		wm:      opts.Workspaces,
		tasks:   opts.Tasks,
		audit:   opts.Audit,
		db:      opts.DB,
		usage:   opts.Usage,
		now:     now,
		refs:    opts.Workspaces,
		taskRev: opts.Tasks,
	}
}

// SetTestHooks installs crash-injection hooks used only by fault-injection
// tests (BF-07). Production code never calls this.
func (s *Service) SetTestHooks(afterIntent, afterRef func() error) {
	s.testHookAfterIntent = afterIntent
	s.testHookAfterRef = afterRef
}

// ErrIllegalTransition is returned by Finalize when the workspace state
// machine refuses the requested transition (e.g. a terminal workspace being
// re-finalized into a non-terminal state, which is forbidden by
// STATE_MACHINE.md §3.3).
type ErrIllegalTransition struct {
	WorkspaceID string
	From        workspace.State
	To          workspace.State
}

func (e *ErrIllegalTransition) Error() string {
	return fmt.Sprintf("runapp: illegal workspace transition %s → %s for %q", e.From, e.To, e.WorkspaceID)
}

// ErrAlreadyTerminal is returned by Finalize when the workspace was already
// terminal and the call was a no-op (idempotent path, OUTCOME_CONTRACT.md §6).
// The caller receives the recorded outcome via FinalizeResult instead of this
// error; this typed error is for callers that need to distinguish.
var ErrAlreadyTerminal = errors.New("runapp: workspace already terminal")

// ErrResultRefConflict is returned when the result ref exists at an unexpected
// SHA and must not be overwritten (BF-07 B6).
type ErrResultRefConflict struct {
	WorkspaceID string
	TaskID      string
	Ref         string
	Existing    string
	Want        string
}

func (e *ErrResultRefConflict) Error() string {
	return fmt.Sprintf("runapp: result ref conflict for workspace %s task %s: %s at %s, want %s",
		e.WorkspaceID, e.TaskID, e.Ref, e.Existing, e.Want)
}

// Finalize is the single atomic chokepoint where the minimal run records its
// terminal state (STATE_MACHINE.md §3.4, §4.2). See the package-level
// finalization protocol comment above for the crash-consistent order.
//
// Finalize is idempotent (OUTCOME_CONTRACT.md §6, S4): a second call on an
// already-terminal workspace returns the recorded outcome without creating a
// duplicate result ref or a second run.outcome_decided event (at most one
// run.finalize_idempotent notice). A call that finds a durable finalize
// intent resumes the protocol from the recorded phase (BF-07 recovery).
func (s *Service) Finalize(ctx context.Context, req FinalizeRequest) (FinalizeResult, error) {
	if s.db == nil {
		return FinalizeResult{}, errors.New("runapp: nil storage")
	}
	ws, err := s.wm.Get(ctx, req.WorkspaceID)
	if err != nil {
		return FinalizeResult{}, fmt.Errorf("runapp: load workspace: %w", err)
	}

	// ---- classify (S2) ----
	in := ClassifyInput{
		BaseSHA:         ws.BaseSHA,
		ActualHEAD:      req.Inspection.ActualHEAD,
		StatusPorcelain: req.Inspection.StatusPorcelain,
	}
	switch req.TerminalEvent.Type {
	case protocol.EventRunCompleted:
		in.Terminal = TerminalCompleted
	case protocol.EventRunCancelled:
		in.Terminal = TerminalCancelled
	case protocol.EventRunFailed:
		in.Terminal = TerminalFailed
		if req.TerminalEvent.Failure != nil && req.TerminalEvent.Failure.Class == protocol.FailureTimeout {
			in.TimeoutClass = true
		}
	}
	outcome := Classify(in)

	// ---- idempotent short-circuit (S4) ----
	if isWorkspaceTerminal(ws.State) {
		return s.idempotentResult(ctx, ws, req, outcome)
	}

	// ---- resume from durable intent if present (BF-07 recovery) ----
	if existing, gerr := s.db.GetFinalizeIntent(ctx, ws.ID); gerr == nil {
		return s.resumeFromIntent(ctx, ws, existing, req)
	} else if !errors.Is(gerr, storage.ErrFinalizeIntentNotFound) {
		return FinalizeResult{}, fmt.Errorf("runapp: load finalize intent: %w", gerr)
	}

	// ---- illegal transition guard ----
	targetState := outcome.WorkspaceState()
	if !allowedTransition(ws.State, targetState) {
		return FinalizeResult{}, &ErrIllegalTransition{
			WorkspaceID: ws.ID, From: ws.State, To: targetState,
		}
	}

	// ---- task transition target ----
	taskAction := task.ActionForOutcome(string(outcome))
	t, terr := s.tasks.Get(ctx, req.TaskID)
	if terr != nil {
		return FinalizeResult{}, fmt.Errorf("runapp: load task: %w", terr)
	}
	taskTo, taskTransitionErr := taskTransitionFor(ctx, s.tasks, t, taskAction)
	if taskTransitionErr != nil {
		return FinalizeResult{}, fmt.Errorf("runapp: compute task transition: %w", taskTransitionErr)
	}

	resultBranch := ""
	resultSHA := ""
	commitSHA := ""
	if outcome.CreatesResultRef() {
		resultBranch = workspace.FullyQualifiedResultBranch(ws.TaskID)
		resultSHA = req.Inspection.ActualHEAD
		if outcome == OutcomeCompletedWithCommit {
			commitSHA = req.Inspection.ActualHEAD
		}
	}

	changedJSON, _ := json.Marshal(req.Inspection.ChangedFiles)
	if changedJSON == nil {
		changedJSON = []byte("[]")
	}
	now := s.now().Format(time.RFC3339Nano)
	intent := storage.FinalizeIntent{
		WorkspaceID:     ws.ID,
		TaskID:          req.TaskID,
		Outcome:         string(outcome),
		RunTerminal:     string(in.Terminal),
		RunID:           req.RunID,
		Engine:          req.Engine,
		Model:           req.Model,
		BaseSHA:         ws.BaseSHA,
		ActualHeadSHA:   req.Inspection.ActualHEAD,
		ExpectedRefSHA:  resultSHA,
		ResultBranch:    resultBranch,
		CommitSHA:       commitSHA,
		GitStatusEmpty:  req.Inspection.StatusPorcelain == "",
		ChangedFiles:    string(changedJSON),
		TargetWSState:   string(targetState),
		TargetTaskState: string(taskTo),
		Phase:           storage.FinalizePhasePending,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	// Step 3: durable finalization intent (survives process crash).
	if err := s.db.UpsertFinalizeIntent(ctx, intent); err != nil {
		return FinalizeResult{}, fmt.Errorf("runapp: record finalize intent: %w", err)
	}

	if s.testHookAfterIntent != nil {
		if herr := s.testHookAfterIntent(); herr != nil {
			return FinalizeResult{}, herr
		}
	}

	// Step 4–5: result ref + phase=ref_ready (or skip when no ref).
	if err := s.ensureRefForIntent(ctx, ws, &intent); err != nil {
		return FinalizeResult{}, err
	}

	if s.testHookAfterRef != nil {
		if herr := s.testHookAfterRef(); herr != nil {
			return FinalizeResult{}, herr
		}
	}

	// Step 6–7: terminal DB commit + clear intent.
	return s.commitTerminal(ctx, ws, intent, req.Inspection.ChangedFiles)
}

// resumeFromIntent continues a partial finalization after crash/retry (BF-07).
func (s *Service) resumeFromIntent(ctx context.Context, ws workspace.Workspace, intent storage.FinalizeIntent, req FinalizeRequest) (FinalizeResult, error) {
	// If the workspace became terminal out-of-band, clear stale intent and
	// return the recorded outcome.
	if isWorkspaceTerminal(ws.State) {
		_ = s.db.DeleteFinalizeIntent(ctx, ws.ID)
		return s.idempotentResult(ctx, ws, req, Outcome(intent.Outcome))
	}

	// Conflict: intent expects a specific ref SHA but git has another.
	if intent.ExpectedRefSHA != "" {
		existing, err := s.refs.ResolveResultRef(ctx, ws.TaskID, ws.Path)
		if err != nil {
			return FinalizeResult{}, fmt.Errorf("runapp: resolve result ref: %w", err)
		}
		if existing != "" && existing != intent.ExpectedRefSHA {
			return FinalizeResult{}, &ErrResultRefConflict{
				WorkspaceID: ws.ID,
				TaskID:      ws.TaskID,
				Ref:         intent.ResultBranch,
				Existing:    existing,
				Want:        intent.ExpectedRefSHA,
			}
		}
	}

	if intent.Phase == storage.FinalizePhasePending {
		if err := s.ensureRefForIntent(ctx, ws, &intent); err != nil {
			return FinalizeResult{}, err
		}
	}

	var changed []string
	if intent.ChangedFiles != "" {
		_ = json.Unmarshal([]byte(intent.ChangedFiles), &changed)
	}
	return s.commitTerminal(ctx, ws, intent, changed)
}

// ensureRefForIntent creates/verifies the result ref and advances phase to
// ref_ready. No-op (phase advance only) when the outcome creates no ref.
func (s *Service) ensureRefForIntent(ctx context.Context, ws workspace.Workspace, intent *storage.FinalizeIntent) error {
	if intent.ExpectedRefSHA != "" {
		ref, err := s.refs.EnsureResultRef(ctx, ws, intent.ExpectedRefSHA)
		if err != nil {
			var conf *workspace.ErrResultRefConflict
			if errors.As(err, &conf) {
				return &ErrResultRefConflict{
					WorkspaceID: ws.ID,
					TaskID:      ws.TaskID,
					Ref:         conf.Ref,
					Existing:    conf.Existing,
					Want:        conf.Want,
				}
			}
			return fmt.Errorf("runapp: ensure result ref: %w", err)
		}
		intent.ResultBranch = ref
	}
	now := s.now().Format(time.RFC3339Nano)
	intent.Phase = storage.FinalizePhaseRefReady
	intent.UpdatedAt = now
	if err := s.db.UpsertFinalizeIntent(ctx, *intent); err != nil {
		return fmt.Errorf("runapp: advance finalize intent to ref_ready: %w", err)
	}
	return nil
}

// commitTerminal persists workspace + task + audit in one SQLite transaction
// and deletes the finalize intent (BF-07 step 6–7).
//
// Concurrency (BF-07 B4): the terminal claim is a conditional UPDATE
// `WHERE state = 'active'`. Exactly one concurrent finalizer wins the claim;
// losers roll back and return the idempotent recorded outcome. This guarantees
// a single run.outcome_decided audit and a single terminal decision.
func (s *Service) commitTerminal(ctx context.Context, ws workspace.Workspace, intent storage.FinalizeIntent, changed []string) (FinalizeResult, error) {
	idempotentReq := FinalizeRequest{
		WorkspaceID: ws.ID, TaskID: intent.TaskID,
		Engine: intent.Engine, Model: intent.Model, RunID: intent.RunID,
		Inspection: workspace.Inspection{
			ActualHEAD: intent.ActualHeadSHA, ChangedFiles: changed,
		},
	}

	// Fast path outside the tx (common after a concurrent winner finished).
	fresh, err := s.wm.Get(ctx, ws.ID)
	if err != nil {
		return FinalizeResult{}, fmt.Errorf("runapp: reload workspace: %w", err)
	}
	if isWorkspaceTerminal(fresh.State) {
		_ = s.db.DeleteFinalizeIntent(ctx, ws.ID)
		return s.idempotentResult(ctx, fresh, idempotentReq, Outcome(intent.Outcome))
	}

	tx, err := s.db.BeginTx(ctx)
	if err != nil {
		return FinalizeResult{}, fmt.Errorf("runapp: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	now := s.now().Format(time.RFC3339Nano)
	targetState := intent.TargetWSState

	// Single-owner terminal claim: only the first UPDATE from active wins.
	res, err := tx.Exec(ctx, `
UPDATE workspaces
   SET state = ?,
       head_sha = ?,
       result_branch = CASE WHEN ? != '' THEN ? ELSE result_branch END,
       result_sha = CASE WHEN ? != '' THEN ? ELSE result_sha END,
       engine = COALESCE(NULLIF(?, ''), engine),
       model  = COALESCE(NULLIF(?, ''), model),
       run_id = COALESCE(NULLIF(?, ''), run_id),
       updated_at = ?
 WHERE id = ? AND state = 'active'`,
		targetState,
		intent.ActualHeadSHA,
		intent.ResultBranch, intent.ResultBranch,
		intent.ExpectedRefSHA, intent.ExpectedRefSHA,
		intent.Engine, intent.Model, intent.RunID,
		now, ws.ID)
	if err != nil {
		return FinalizeResult{}, fmt.Errorf("runapp: claim terminal workspace: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Lost the race — another finalizer already committed terminal state.
		_ = tx.Rollback()
		committed = true // prevent double-rollback
		fresh2, gerr := s.wm.Get(ctx, ws.ID)
		if gerr != nil {
			return FinalizeResult{}, fmt.Errorf("runapp: reload after lost claim: %w", gerr)
		}
		_ = s.db.DeleteFinalizeIntent(ctx, ws.ID)
		return s.idempotentResult(ctx, fresh2, idempotentReq, Outcome(intent.Outcome))
	}

	// Task transition only for the winning finalizer. Conditional on non-terminal
	// task state so a concurrent loser cannot revive a terminal task either.
	if _, err := tx.Exec(ctx, `
UPDATE tasks SET state = ?, updated_at = ?
 WHERE id = ? AND state NOT IN ('COMPLETED','FAILED','CANCELLED','REJECTED')`,
		intent.TargetTaskState, now, intent.TaskID); err != nil {
		return FinalizeResult{}, fmt.Errorf("runapp: update task state: %w", err)
	}

	if s.audit != nil {
		payload := audit.Payload(
			"outcome", intent.Outcome,
			"run_terminal", intent.RunTerminal,
			"base_sha", intent.BaseSHA,
			"actual_head_sha", intent.ActualHeadSHA,
			"git_status_empty", intent.GitStatusEmpty,
			"commit_sha", intent.CommitSHA,
			"result_branch", intent.ResultBranch,
			"engine", intent.Engine,
			"model", intent.Model,
			"run_id", intent.RunID,
			"workspace_id", ws.ID,
			"task_id", intent.TaskID,
		)
		if _, err := s.audit.RecordTx(ctx, tx, audit.Event{
			Type:    "run.outcome_decided",
			Scope:   audit.ScopeTask,
			ScopeID: ws.ID,
			Actor:   audit.ActorDaemon,
			Payload: payload,
		}); err != nil {
			return FinalizeResult{}, fmt.Errorf("runapp: audit run.outcome_decided: %w", err)
		}
	}

	// Clear intent inside the same tx so a crash after commit never re-applies.
	if err := tx.DeleteFinalizeIntent(ctx, ws.ID); err != nil {
		return FinalizeResult{}, fmt.Errorf("runapp: clear finalize intent: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return FinalizeResult{}, fmt.Errorf("runapp: commit finalize tx: %w", err)
	}
	committed = true

	return FinalizeResult{
		Outcome:       Outcome(intent.Outcome),
		WorkspaceID:   ws.ID,
		TaskID:        intent.TaskID,
		WorkspacePath: ws.Path,
		BaseSHA:       intent.BaseSHA,
		ActualHEAD:    intent.ActualHeadSHA,
		CommitSHA:     intent.CommitSHA,
		ResultBranch:  intent.ResultBranch,
		ChangedFiles:  changed,
		Engine:        intent.Engine,
		Model:         intent.Model,
		RunID:         intent.RunID,
	}, nil
}

func (s *Service) idempotentResult(ctx context.Context, ws workspace.Workspace, req FinalizeRequest, outcome Outcome) (FinalizeResult, error) {
	rec := FinalizeResult{
		Outcome:       outcomeForTerminalState(ws.State, outcome),
		WorkspaceID:   ws.ID,
		TaskID:        ws.TaskID,
		WorkspacePath: ws.Path,
		BaseSHA:       ws.BaseSHA,
		ActualHEAD:    req.Inspection.ActualHEAD,
		CommitSHA:     ws.ResultSHA,
		ResultBranch:  ws.ResultBranch,
		ChangedFiles:  req.Inspection.ChangedFiles,
		Engine:        req.Engine,
		Model:         req.Model,
		RunID:         req.RunID,
		Idempotent:    true,
	}
	// Prefer durable result fields when the inspection is empty (late event).
	if rec.ActualHEAD == "" {
		rec.ActualHEAD = ws.HeadSHA
	}
	if rec.CommitSHA == "" && rec.Outcome == OutcomeCompletedWithCommit {
		rec.CommitSHA = ws.ResultSHA
	}
	if s.audit != nil {
		_, _ = s.audit.Record(ctx, audit.Event{
			Type:    "run.finalize_idempotent",
			Scope:   audit.ScopeTask,
			ScopeID: ws.ID,
			Actor:   audit.ActorDaemon,
			Payload: audit.Payload(
				"workspace", ws.ID,
				"state", string(ws.State),
				"outcome", string(rec.Outcome),
				"run_id", req.RunID,
				"late_event", string(req.TerminalEvent.Type),
			),
		})
	}
	return rec, nil
}

// RecoverPendingFinalizations resumes every durable finalize intent left by a
// crashed process (BF-07). Called from the daemon startup reconciler before
// the listener binds. Each intent is resumed independently; failures are
// collected but do not abort other recoveries.
func (s *Service) RecoverPendingFinalizations(ctx context.Context) ([]FinalizeResult, []error) {
	if s.db == nil {
		return nil, []error{errors.New("runapp: nil storage")}
	}
	intents, err := s.db.ListFinalizeIntents(ctx)
	if err != nil {
		return nil, []error{err}
	}
	var results []FinalizeResult
	var errs []error
	for _, intent := range intents {
		ws, gerr := s.wm.Get(ctx, intent.WorkspaceID)
		if gerr != nil {
			errs = append(errs, fmt.Errorf("workspace %s: %w", intent.WorkspaceID, gerr))
			continue
		}
		if isWorkspaceTerminal(ws.State) {
			_ = s.db.DeleteFinalizeIntent(ctx, ws.ID)
			continue
		}
		var changed []string
		_ = json.Unmarshal([]byte(intent.ChangedFiles), &changed)
		req := FinalizeRequest{
			WorkspaceID: ws.ID,
			TaskID:      intent.TaskID,
			Engine:      intent.Engine,
			Model:       intent.Model,
			RunID:       intent.RunID,
			Inspection: workspace.Inspection{
				ActualHEAD: intent.ActualHeadSHA, ChangedFiles: changed,
			},
			// TerminalEvent is reconstructed only for the idempotent path;
			// resume uses the intent payload.
			TerminalEvent: protocol.NormalizedEvent{Type: protocol.EventRunCompleted},
		}
		res, rerr := s.resumeFromIntent(ctx, ws, intent, req)
		if rerr != nil {
			errs = append(errs, fmt.Errorf("resume %s: %w", intent.WorkspaceID, rerr))
			continue
		}
		results = append(results, res)
	}
	return results, errs
}

// taskTransitionFor validates the requested task transition against the state
// machine and returns the target state without persisting it. An illegal
// transition (e.g. terminal→RUNNING) is rejected here.
func taskTransitionFor(ctx context.Context, backlog TaskBacklog, t task.Task, action task.Action) (task.State, error) {
	to, err := task.CanTransition(t.State, action)
	if err != nil {
		return "", err
	}
	return to, nil
}

// isWorkspaceTerminal reports whether state is one of the workspace terminal
// states per STATE_MACHINE.md §3.1.
func isWorkspaceTerminal(s workspace.State) bool {
	switch s {
	case workspace.StateCompleted,
		workspace.StateFailed,
		workspace.StateCancelled,
		workspace.StateTimedOut,
		workspace.StateKept,
		workspace.StateRejected,
		workspace.StateDeleted,
		workspace.StateQuarantined:
		return true
	}
	return false
}

// allowedTransition reports whether moving from → to is legal for the
// minimal run (STATE_MACHINE.md §3.2). It is the runtime-grade mirror of
// workspace.allowedRecoveryTransition, but stricter: it forbids
// terminal→active even from `failed`, and forbids pending→completed directly.
func allowedTransition(from, to workspace.State) bool {
	// Hard-absorbing terminals: nothing leaves them via the run path.
	switch from {
	case workspace.StateCompleted,
		workspace.StateCancelled,
		workspace.StateTimedOut,
		workspace.StateKept,
		workspace.StateRejected,
		workspace.StateDeleted,
		workspace.StateQuarantined:
		return false
	}
	switch from {
	case workspace.StatePending:
		// pending may only go to active (worktree created) or failed (worktree
		// creation failed). It may NOT jump to a terminal outcome state.
		switch to {
		case workspace.StateActive, workspace.StateFailed:
			return true
		}
		return false
	case workspace.StateActive:
		switch to {
		case workspace.StateCompleted, workspace.StateFailed,
			workspace.StateCancelled, workspace.StateTimedOut,
			workspace.StateWaitingQuota, workspace.StateQuarantined:
			return true
		}
		return false
	case workspace.StateWaitingQuota, workspace.StateFailed:
		// A failed workspace may be moved to failed again (idempotent path is
		// handled earlier; this is for safety). It may not jump to completed
		// without going through a new attempt.
		switch to {
		case workspace.StateFailed, workspace.StateWaitingQuota, workspace.StateQuarantined:
			return true
		}
		return false
	}
	return false
}

// outcomeForTerminalState maps an already-terminal workspace state to the
// matching outcome when the idempotent short-circuit fires. The `fallback`
// outcome is the one the classifier just produced — we use it when the
// terminal state is unambiguous (e.g. completed always pairs with
// completed-*).
func outcomeForTerminalState(s workspace.State, fallback Outcome) Outcome {
	switch s {
	case workspace.StateCompleted:
		// Prefer the recorded completed-* variant from the classifier when it
		// still classifies as completed-*; otherwise keep completed-with-commit
		// as a safe default for a completed workspace.
		switch fallback {
		case OutcomeCompletedWithCommit, OutcomeCompletedWithUncommittedChanges:
			return fallback
		}
		return OutcomeCompletedWithCommit
	case workspace.StateCancelled:
		return OutcomeCancelled
	case workspace.StateTimedOut:
		return OutcomeTimedOut
	case workspace.StateFailed:
		// Could be failed/no-changes/interrupted — keep what the classifier
		// produced only when it maps to a failed-family outcome; otherwise
		// preserve failed (late completed event must not revive).
		switch fallback {
		case OutcomeFailed, OutcomeCompletedNoChanges, OutcomeInterrupted:
			return fallback
		}
		return OutcomeFailed
	}
	return fallback
}

// _ keeps storage referenced (UpdateStateTx needs *storage.Tx).
var _ = storage.ErrWorkspaceNotFound
