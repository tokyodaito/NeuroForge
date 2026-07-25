package runapp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
	"neuroforge/internal/audit"
	"neuroforge/internal/storage"
	"neuroforge/internal/task"
	"neuroforge/internal/workspace"
)

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

// RefCreator creates/updates the local result ref, and removes it as the
// compensating action when the finalize transaction fails after the ref was
// created (BF-07). Implemented by *workspace.Manager.
type RefCreator interface {
	EnsureResultRef(ctx context.Context, ws workspace.Workspace, headSHA string) (resultBranch string, err error)
	DeleteResultRef(ctx context.Context, ws workspace.Workspace) error
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

// Finalize is the single atomic chokepoint where the minimal run records its
// terminal state (STATE_MACHINE.md §3.4, §4.2). It opens one SQLite
// transaction that:
//
//   - persists the workspace state (terminal, matching the outcome),
//     head_sha (the actual HEAD from FR-9), result_branch/result_sha when
//     applicable;
//   - persists the task state (COMPLETED / FAILED / CANCELLED per outcome);
//   - appends one run.outcome_decided audit event carrying the full
//     OUTCOME_CONTRACT.md §1.4 payload.
//
// If any of these fail the whole transaction is rolled back — there is no
// "state written, audit forgotten" path. Illegal transitions (e.g.
// terminal→active) return a typed error and roll back.
//
// Finalize is idempotent (OUTCOME_CONTRACT.md §6, S4): a second call on an
// already-terminal workspace returns the recorded outcome without creating a
// duplicate result ref or a second run.outcome_decided event (at most one
// run.finalize_idempotent notice).
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
		// The workspace is already in a terminal state. Return the recorded
		// outcome; emit at most one dedup notice (no second result ref, no
		// second run.outcome_decided).
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
		// Best-effort dedup notice; never fails the call.
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
					"run_id", req.RunID),
			})
		}
		return rec, nil
	}

	// ---- illegal transition guard ----
	targetState := outcome.WorkspaceState()
	if !allowedTransition(ws.State, targetState) {
		return FinalizeResult{}, &ErrIllegalTransition{
			WorkspaceID: ws.ID, From: ws.State, To: targetState,
		}
	}

	// ---- create result ref BEFORE the tx (S5) ----
	// The ref is a git operation (filesystem), not a DB write, so it cannot
	// roll back inside the SQLite tx. We perform it first; if it succeeds the
	// tx records the matching result_branch/result_sha. If the tx fails, the
	// compensating delete below (BF-07) removes the just-created ref so git and
	// DB stay consistent (no orphan ref pointing at an unrecorded result).
	resultBranch := ""
	resultSHA := ""
	commitSHA := ""
	createdRef := false
	if outcome.CreatesResultRef() {
		ref, err := s.refs.EnsureResultRef(ctx, ws, req.Inspection.ActualHEAD)
		if err != nil {
			return FinalizeResult{}, fmt.Errorf("runapp: ensure result ref: %w", err)
		}
		resultBranch = ref
		resultSHA = req.Inspection.ActualHEAD
		createdRef = true
		// OUTCOME_CONTRACT.md §3.1: commit_sha is the actual HEAD when the
		// outcome is *-with-commit. For completed-with-uncommitted-changes
		// there is NO commit — commit_sha stays empty/null so the result is
		// never disguised as a commit (invariant I.6).
		if outcome == OutcomeCompletedWithCommit {
			commitSHA = req.Inspection.ActualHEAD
		}
	}

	// ---- task transition (inside the tx) ----
	taskAction := task.ActionForOutcome(string(outcome))
	// Load the task to capture its current state for the audit payload.
	t, terr := s.tasks.Get(ctx, req.TaskID)
	if terr != nil {
		return FinalizeResult{}, fmt.Errorf("runapp: load task: %w", terr)
	}

	// ---- open the atomic tx ----
	tx, err := s.db.BeginTx(ctx)
	if err != nil {
		// tx failed to open: if we created a ref, compensate now (BF-07).
		if createdRef {
			_ = s.refs.DeleteResultRef(ctx, ws)
		}
		return FinalizeResult{}, fmt.Errorf("runapp: begin tx: %w", err)
	}
	committed := false
	defer func() {
		_ = tx.Rollback()
		// BF-07 atomicity: if the tx did not commit and we created a result ref
		// before it, that ref is now an orphan pointing at a result the DB
		// never recorded. Remove it so git and DB agree. Best-effort: a
		// compensating-delete failure must never mask the original tx error.
		if !committed && createdRef {
			_ = s.refs.DeleteResultRef(ctx, ws)
		}
	}()

	// 1. workspace state (terminal) + head_sha + result_branch + result_sha.
	if err := s.wm.UpdateStateTx(ctx, tx, ws.ID, targetState,
		req.Inspection.ActualHEAD, resultBranch, resultSHA); err != nil {
		return FinalizeResult{}, fmt.Errorf("runapp: update workspace state: %w", err)
	}
	// Also persist engine/model/run_id if not yet set (so the row is
	// self-describing after finalize). This is best-effort inside the tx.
	if _, err := tx.Exec(ctx,
		`UPDATE workspaces
		    SET engine = COALESCE(NULLIF(?, ''), engine),
		        model  = COALESCE(NULLIF(?, ''), model),
		        run_id = COALESCE(NULLIF(?, ''), run_id)
		  WHERE id = ?`,
		req.Engine, req.Model, req.RunID, ws.ID); err != nil {
		return FinalizeResult{}, fmt.Errorf("runapp: stamp engine/model/run_id: %w", err)
	}

	// 2. task state (terminal, matching outcome). The task state machine
	// validates the transition; an illegal transition aborts the whole tx.
	taskTo, taskTransitionErr := taskTransitionFor(ctx, s.tasks, t, taskAction)
	if taskTransitionErr != nil {
		return FinalizeResult{}, fmt.Errorf("runapp: compute task transition: %w", taskTransitionErr)
	}
	// Persist via the same tx so it shares the atomic commit with the
	// workspace state + audit. We write directly via storage to keep this in
	// the tx (the backlog's Transition would open its own tx).
	if err := tx.UpdateTaskState(ctx, req.TaskID, string(taskTo),
		s.now().Format(time.RFC3339Nano)); err != nil {
		return FinalizeResult{}, fmt.Errorf("runapp: update task state: %w", err)
	}

	// 3. one run.outcome_decided audit event carrying OUTCOME_CONTRACT.md
	// §1.4 payload.
	if s.audit != nil {
		payload := audit.Payload(
			"outcome", string(outcome),
			"run_terminal", string(in.Terminal),
			"base_sha", ws.BaseSHA,
			"actual_head_sha", req.Inspection.ActualHEAD,
			"git_status_empty", req.Inspection.StatusPorcelain == "",
			"commit_sha", commitSHA,
			"result_branch", resultBranch,
			"engine", req.Engine,
			"model", req.Model,
			"run_id", req.RunID,
			"workspace_id", ws.ID,
			"task_id", req.TaskID,
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

	if err := tx.Commit(); err != nil {
		return FinalizeResult{}, fmt.Errorf("runapp: commit finalize tx: %w", err)
	}
	committed = true

	return FinalizeResult{
		Outcome:       outcome,
		WorkspaceID:   ws.ID,
		TaskID:        req.TaskID,
		WorkspacePath: ws.Path,
		BaseSHA:       ws.BaseSHA,
		ActualHEAD:    req.Inspection.ActualHEAD,
		CommitSHA:     commitSHA,
		ResultBranch:  resultBranch,
		ChangedFiles:  req.Inspection.ChangedFiles,
		Engine:        req.Engine,
		Model:         req.Model,
		RunID:         req.RunID,
	}, nil
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
		return fallback
	case workspace.StateCancelled:
		return OutcomeCancelled
	case workspace.StateTimedOut:
		return OutcomeTimedOut
	case workspace.StateFailed:
		// Could be failed/no-changes/interrupted — keep what the classifier
		// produced; that is the recorded intent.
		return fallback
	}
	return fallback
}

// _ keeps storage referenced (UpdateStateTx needs *storage.Tx).
var _ = storage.ErrWorkspaceNotFound
