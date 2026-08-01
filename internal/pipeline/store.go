// This file implements the durable pipeline store (milestone M14-06). It
// wraps [storage.DB]'s pipeline_runs / pipeline_stage_records / control_flags
// substrate (migration v10) and enforces the stage/run-state transition
// rules declared in stage.go.
//
// Concurrency: every mutation runs in a SQLite transaction whose
// linearisation point is a conditional UPDATE on pipeline_runs (guarded by
// the previously-read current_stage / run_state). A concurrent writer that
// loses the race affects 0 rows; the loser re-reads and either takes the
// idempotent path (winner moved to the same destination) or receives
// ErrConcurrentModification. Stage-history inserts rely on
// UNIQUE(task_id, stage, attempt, status) + ON CONFLICT DO NOTHING, so
// re-entry after a crash never duplicates records.

package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"neuroforge/internal/storage"
)

// DefaultMaxRepairAttempts is used by CreateRun when the caller passes a
// non-positive max.
const DefaultMaxRepairAttempts = 3

// ErrRunNotFound is returned (wrapped) when a task has no pipeline run.
var ErrRunNotFound = errors.New("pipeline: run not found")

// ErrConcurrentModification is returned (wrapped) when a mutation loses a
// race against another writer that moved the same run to a different
// destination. The caller should re-read the run and re-decide.
var ErrConcurrentModification = errors.New("pipeline: concurrent modification")

// ErrRunTerminal is returned (wrapped) when a mutation requires a
// non-terminal run but the run has already reached a terminal state.
var ErrRunTerminal = errors.New("pipeline: run is terminal")

// RecordStatus is the lifecycle status of a pipeline_stage_records row.
type RecordStatus string

const (
	// RecordEntered marks a stage entry (persist-before-effect).
	RecordEntered RecordStatus = "entered"
	// RecordCompleted marks a successful stage outcome.
	RecordCompleted RecordStatus = "completed"
	// RecordFailed marks a failed stage outcome (failure_category set).
	RecordFailed RecordStatus = "failed"
	// RecordSkipped is reserved for optional stages (design/visual/delivery)
	// that the minimal local path does not drive. No code path writes it yet.
	RecordSkipped RecordStatus = "skipped"
)

// Run is a durable pipeline run (one row of pipeline_runs).
type Run struct {
	TaskID            string
	ProjectID         string
	CurrentStage      Stage
	State             RunState
	StageAttempt      int
	RepairAttempt     int
	MaxRepairAttempts int
	FailureCategory   FailureCategory // empty when the run has no recorded failure
	FailureReason     string
	ResultRef         string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// StageRecord is one row of the append-only stage history.
type StageRecord struct {
	ID              int64
	TaskID          string
	Stage           Stage
	Attempt         int
	Status          RecordStatus
	FailureCategory FailureCategory // empty unless Status == RecordFailed
	Reason          string
	EvidenceRef     string
	EnteredAt       time.Time
	// FinishedAt is zero while the record is open (an 'entered' record whose
	// stage has not reached an outcome yet).
	FinishedAt time.Time
}

// RunStateChange describes a requested run-state change for SetRunState.
type RunStateChange struct {
	// To is the target run state.
	To RunState
	// Reason is persisted as failure_reason when To is failed, cancelled or
	// repair_exhausted (and Reason is non-empty).
	Reason string
	// ResultRef is persisted as result_ref when To is completed (and
	// ResultRef is non-empty).
	ResultRef string
}

// Store is the durable pipeline stage state machine. It owns persistence and
// transition legality only; driving the actual stage work (compiling,
// dispatching agents, verifying) is the daemon's responsibility.
type Store struct {
	db     *storage.DB
	logger *slog.Logger
	now    func() time.Time
}

// NewStore creates a Store backed by db. The logger may be nil (a quiet
// default is used).
func NewStore(db *storage.DB, logger *slog.Logger) *Store {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	}
	return &Store{
		db:     db,
		logger: logger,
		now:    func() time.Time { return time.Now().UTC() },
	}
}

// discardWriter is a no-op io.Writer used to silence the default logger when
// the caller does not supply one (mirrors workgraph.WorkGraphStore's pattern).
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// CreateRun creates the pipeline run for a task at stage compile (attempt 1)
// in state active, and records the initial "entered compile" history row.
// Idempotent: if the task already has a run, the existing run is returned
// unchanged (a non-positive maxRepairAttempts is ignored in that case).
func (s *Store) CreateRun(ctx context.Context, taskID, projectID string, maxRepairAttempts int) (*Run, error) {
	if taskID == "" || projectID == "" {
		return nil, fmt.Errorf("pipeline: create run: task_id and project_id are required")
	}
	if maxRepairAttempts <= 0 {
		maxRepairAttempts = DefaultMaxRepairAttempts
	}
	now := s.now().Format(time.RFC3339Nano)

	tx, err := s.db.Underlying().BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("pipeline: create run: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
INSERT INTO pipeline_runs (task_id, project_id, current_stage, run_state, stage_attempt, repair_attempt, max_repair_attempts, created_at, updated_at)
VALUES (?, ?, ?, ?, 1, 0, ?, ?, ?)
ON CONFLICT (task_id) DO NOTHING`,
		taskID, projectID, string(StageCompile), string(RunActive), maxRepairAttempts, now, now); err != nil {
		return nil, fmt.Errorf("pipeline: create run %s: %w", taskID, err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO pipeline_stage_records (task_id, stage, attempt, status, entered_at)
VALUES (?, ?, 1, ?, ?)
ON CONFLICT (task_id, stage, attempt, status) DO NOTHING`,
		taskID, string(StageCompile), string(RecordEntered), now); err != nil {
		return nil, fmt.Errorf("pipeline: create run %s: record compile entry: %w", taskID, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("pipeline: create run %s: commit: %w", taskID, err)
	}
	return s.CurrentRun(ctx, taskID)
}

// CurrentRun returns the persisted run for taskID, or ErrRunNotFound.
func (s *Store) CurrentRun(ctx context.Context, taskID string) (*Run, error) {
	run, err := scanRun(s.db.Underlying().QueryRowContext(ctx, `
SELECT task_id, project_id, current_stage, run_state, stage_attempt, repair_attempt,
       max_repair_attempts, failure_category, failure_reason, result_ref, created_at, updated_at
FROM pipeline_runs WHERE task_id = ?`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("pipeline: current run for task %s: %w", taskID, ErrRunNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("pipeline: current run for task %s: %w", taskID, err)
	}
	return run, nil
}

// ListActiveRuns returns every non-terminal run (active, waiting_quota,
// blocked), ordered by creation time. The startup reconciler uses this to
// find runs that need MarkInterrupted + re-driving.
func (s *Store) ListActiveRuns(ctx context.Context) ([]Run, error) {
	rows, err := s.db.Underlying().QueryContext(ctx, `
SELECT task_id, project_id, current_stage, run_state, stage_attempt, repair_attempt,
       max_repair_attempts, failure_category, failure_reason, result_ref, created_at, updated_at
FROM pipeline_runs
WHERE run_state IN (?, ?, ?)
ORDER BY created_at, task_id`,
		string(RunActive), string(RunWaitingQuota), string(RunBlocked))
	if err != nil {
		return nil, fmt.Errorf("pipeline: list active runs: %w", err)
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("pipeline: list active runs: %w", err)
		}
		out = append(out, *run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pipeline: list active runs: %w", err)
	}
	return out, nil
}

// Transition advances the run's stage cursor to toStage and records the
// "entered" history row at the current stage attempt. Legality is enforced
// via CanTransitionStage.
//
// Idempotency: re-entering the CURRENT stage (toStage == current stage, same
// attempt) is a no-op that returns the existing "entered" record — this is
// the crash-recovery re-drive path. If a concurrent writer already moved the
// run to toStage, the same idempotent record is returned; if it moved the
// run elsewhere, ErrConcurrentModification is returned.
//
// Resuming from a wait state (waiting_quota / blocked) is only legal with
// toStage == ready and re-activates the run (state becomes active).
//
// Before re-entering a PREVIOUSLY-EXECUTED stage for a fresh attempt (e.g.
// execute after repair) the caller must IncrementStageAttempt, otherwise the
// entry is absorbed by the idempotency constraint at the old attempt.
func (s *Store) Transition(ctx context.Context, taskID string, toStage Stage, reason, evidenceRef string) (*StageRecord, error) {
	if !IsValidStage(toStage) {
		return nil, fmt.Errorf("pipeline: transition: unknown stage %q", toStage)
	}
	now := s.now().Format(time.RFC3339Nano)

	tx, err := s.db.Underlying().BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("pipeline: transition: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	run, err := getRunTx(ctx, tx, taskID)
	if err != nil {
		return nil, err
	}

	if run.CurrentStage == toStage {
		// Idempotent re-entry: no state change, ensure the record exists.
		rec, err := ensureEnteredTx(ctx, tx, taskID, toStage, run.StageAttempt, reason, evidenceRef, now)
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("pipeline: transition %s -> %s: commit: %w", run.CurrentStage, toStage, err)
		}
		return rec, nil
	}

	if !CanTransitionStage(run.State, run.CurrentStage, toStage) {
		return nil, &ErrInvalidTransition{TaskID: taskID, RunState: run.State, From: run.CurrentStage, To: toStage}
	}

	// Resuming from a wait state re-activates the run.
	newState := run.State
	if IsWaitState(run.State) {
		newState = RunActive
	}

	res, err := tx.ExecContext(ctx, `
UPDATE pipeline_runs SET current_stage = ?, run_state = ?, updated_at = ?
WHERE task_id = ? AND current_stage = ? AND run_state = ?`,
		string(toStage), string(newState), now,
		taskID, string(run.CurrentStage), string(run.State))
	if err != nil {
		return nil, fmt.Errorf("pipeline: transition %s -> %s: %w", run.CurrentStage, toStage, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_ = tx.Rollback()
		return s.recoverLostRace(ctx, taskID, toStage, run, reason, evidenceRef)
	}

	rec, err := ensureEnteredTx(ctx, tx, taskID, toStage, run.StageAttempt, reason, evidenceRef, now)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("pipeline: transition %s -> %s: commit: %w", run.CurrentStage, toStage, err)
	}
	s.logger.Info("pipeline: stage transition",
		"task", taskID, "from", run.CurrentStage, "to", toStage, "attempt", run.StageAttempt)
	return rec, nil
}

// recoverLostRace handles a Transition that lost the conditional-UPDATE
// race: idempotent success if the winner moved to the same stage, otherwise
// ErrConcurrentModification.
func (s *Store) recoverLostRace(ctx context.Context, taskID string, toStage Stage, run *Run, reason, evidenceRef string) (*StageRecord, error) {
	cur, err := s.CurrentRun(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if cur.CurrentStage != toStage {
		return nil, fmt.Errorf("pipeline: transition %s -> %s for task %s: %w",
			run.CurrentStage, toStage, taskID, ErrConcurrentModification)
	}
	// The winner reached the same destination; return its record.
	tx, err := s.db.Underlying().BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("pipeline: transition: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rec, err := ensureEnteredTx(ctx, tx, taskID, toStage, cur.StageAttempt, reason, evidenceRef,
		s.now().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("pipeline: transition: commit: %w", err)
	}
	return rec, nil
}

// CompleteStage records a successful outcome for the stage the run is
// currently in, closing the open "entered" record. Idempotent: completing an
// already-completed stage attempt is a no-op.
func (s *Store) CompleteStage(ctx context.Context, taskID string, stage Stage, evidenceRef string) error {
	return s.finishStage(ctx, taskID, stage, RecordCompleted, "", "", evidenceRef)
}

// FailStage records a failed outcome for the stage the run is currently in,
// closing the open "entered" record, and copies the failure onto the run row
// (last failure wins). It does NOT change the run state or stage cursor —
// the driver decides the follow-up (Transition to repair, SetRunState to
// failed, ...). Idempotent: failing an already-failed stage attempt is a
// no-op.
func (s *Store) FailStage(ctx context.Context, taskID string, stage Stage, category FailureCategory, reason, evidenceRef string) error {
	if !IsValidFailureCategory(category) {
		return fmt.Errorf("pipeline: fail stage: unknown failure category %q", category)
	}
	return s.finishStage(ctx, taskID, stage, RecordFailed, category, reason, evidenceRef)
}

func (s *Store) finishStage(ctx context.Context, taskID string, stage Stage, status RecordStatus, category FailureCategory, reason, evidenceRef string) error {
	if !IsValidStage(stage) {
		return fmt.Errorf("pipeline: finish stage: unknown stage %q", stage)
	}
	now := s.now().Format(time.RFC3339Nano)

	tx, err := s.db.Underlying().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("pipeline: finish stage: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	run, err := getRunTx(ctx, tx, taskID)
	if err != nil {
		return err
	}

	if run.CurrentStage != stage {
		// Idempotent retry after the run already advanced: succeed only if
		// the outcome record for that stage attempt is already durable.
		exists, err := recordExistsTx(ctx, tx, taskID, stage, run.StageAttempt, status)
		if err != nil {
			return err
		}
		if !exists {
			return &ErrInvalidTransition{TaskID: taskID, RunState: run.State, From: run.CurrentStage, To: stage}
		}
		return tx.Commit()
	}

	exists, err := recordExistsTx(ctx, tx, taskID, stage, run.StageAttempt, status)
	if err != nil {
		return err
	}
	if exists {
		return tx.Commit() // already recorded — idempotent no-op
	}
	if IsTerminalRunState(run.State) {
		return fmt.Errorf("pipeline: finish stage %s for task %s: %w", stage, taskID, ErrRunTerminal)
	}

	if _, err := tx.ExecContext(ctx, `
UPDATE pipeline_stage_records SET finished_at = ?
WHERE task_id = ? AND stage = ? AND attempt = ? AND status = ? AND finished_at IS NULL`,
		now, taskID, string(stage), run.StageAttempt, string(RecordEntered)); err != nil {
		return fmt.Errorf("pipeline: finish stage %s: close entry: %w", stage, err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO pipeline_stage_records (task_id, stage, attempt, status, failure_category, reason, evidence_ref, entered_at, finished_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (task_id, stage, attempt, status) DO NOTHING`,
		taskID, string(stage), run.StageAttempt, string(status),
		nullString(string(category)), reason, nullString(evidenceRef), now, now); err != nil {
		return fmt.Errorf("pipeline: finish stage %s: record outcome: %w", stage, err)
	}

	if status == RecordFailed {
		res, err := tx.ExecContext(ctx, `
UPDATE pipeline_runs SET failure_category = ?, failure_reason = ?, updated_at = ?
WHERE task_id = ? AND current_stage = ?`,
			string(category), reason, now, taskID, string(stage))
		if err != nil {
			return fmt.Errorf("pipeline: fail stage %s: update run: %w", stage, err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			_ = tx.Rollback()
			return fmt.Errorf("pipeline: fail stage %s for task %s: %w", stage, taskID, ErrConcurrentModification)
		}
	}
	return tx.Commit()
}

// IncrementStageAttempt bumps the stage attempt counter and returns the new
// value. The driver calls this before re-entering a previously-executed
// stage (e.g. execute after repair) so the new entry gets its own history
// records instead of being absorbed by the idempotency constraint.
func (s *Store) IncrementStageAttempt(ctx context.Context, taskID string) (int, error) {
	tx, err := s.db.Underlying().BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("pipeline: increment stage attempt: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	run, err := getRunTx(ctx, tx, taskID)
	if err != nil {
		return 0, err
	}
	if IsTerminalRunState(run.State) {
		return 0, fmt.Errorf("pipeline: increment stage attempt for task %s: %w", taskID, ErrRunTerminal)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE pipeline_runs SET stage_attempt = stage_attempt + 1, updated_at = ? WHERE task_id = ?`,
		s.now().Format(time.RFC3339Nano), taskID); err != nil {
		return 0, fmt.Errorf("pipeline: increment stage attempt for task %s: %w", taskID, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("pipeline: increment stage attempt for task %s: commit: %w", taskID, err)
	}
	return run.StageAttempt + 1, nil
}

// IncrementRepairAttempt bumps the repair counter and returns
// (true, newCount, nil). When the counter has already reached
// max_repair_attempts it returns (false, currentCount, nil) without
// incrementing — the caller must then move the run to repair_exhausted via
// SetRunState. The counter is never incremented past the max, so concurrent
// repair loops cannot overshoot.
func (s *Store) IncrementRepairAttempt(ctx context.Context, taskID string) (bool, int, error) {
	tx, err := s.db.Underlying().BeginTx(ctx, nil)
	if err != nil {
		return false, 0, fmt.Errorf("pipeline: increment repair attempt: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	run, err := getRunTx(ctx, tx, taskID)
	if err != nil {
		return false, 0, err
	}
	if IsTerminalRunState(run.State) {
		return false, run.RepairAttempt, fmt.Errorf("pipeline: increment repair attempt for task %s: %w", taskID, ErrRunTerminal)
	}
	if run.RepairAttempt >= run.MaxRepairAttempts {
		if err := tx.Commit(); err != nil {
			return false, run.RepairAttempt, fmt.Errorf("pipeline: increment repair attempt: commit: %w", err)
		}
		return false, run.RepairAttempt, nil
	}
	res, err := tx.ExecContext(ctx, `
UPDATE pipeline_runs SET repair_attempt = repair_attempt + 1, updated_at = ?
WHERE task_id = ? AND repair_attempt = ?`,
		s.now().Format(time.RFC3339Nano), taskID, run.RepairAttempt)
	if err != nil {
		return false, 0, fmt.Errorf("pipeline: increment repair attempt for task %s: %w", taskID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_ = tx.Rollback()
		return false, 0, fmt.Errorf("pipeline: increment repair attempt for task %s: %w", taskID, ErrConcurrentModification)
	}
	if err := tx.Commit(); err != nil {
		return false, 0, fmt.Errorf("pipeline: increment repair attempt: commit: %w", err)
	}
	return true, run.RepairAttempt + 1, nil
}

// MarkInterrupted records an interrupted failure on the run's in-flight
// stage (the open "entered" record for the current stage attempt) WITHOUT
// advancing the stage cursor or run state. The startup reconciler calls it
// before re-driving a recovered run, so the history shows the stage was
// interrupted rather than completed.
//
// To re-drive after MarkInterrupted: IncrementStageAttempt, then Transition
// to the current stage (a re-entry at the new attempt). Idempotent: a second
// call, or a call on a terminal run, is a no-op.
func (s *Store) MarkInterrupted(ctx context.Context, taskID, reason string) error {
	if reason == "" {
		reason = "run interrupted"
	}
	now := s.now().Format(time.RFC3339Nano)

	tx, err := s.db.Underlying().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("pipeline: mark interrupted: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	run, err := getRunTx(ctx, tx, taskID)
	if err != nil {
		return err
	}
	if IsTerminalRunState(run.State) {
		return tx.Commit() // nothing in flight — no-op
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE pipeline_stage_records SET finished_at = ?
WHERE task_id = ? AND stage = ? AND attempt = ? AND status = ? AND finished_at IS NULL`,
		now, taskID, string(run.CurrentStage), run.StageAttempt, string(RecordEntered)); err != nil {
		return fmt.Errorf("pipeline: mark interrupted for task %s: close entry: %w", taskID, err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO pipeline_stage_records (task_id, stage, attempt, status, failure_category, reason, entered_at, finished_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (task_id, stage, attempt, status) DO NOTHING`,
		taskID, string(run.CurrentStage), run.StageAttempt, string(RecordFailed),
		string(FailureInterrupted), reason, now, now); err != nil {
		return fmt.Errorf("pipeline: mark interrupted for task %s: record outcome: %w", taskID, err)
	}
	return tx.Commit()
}

// SetRunState changes the run's lifecycle state, enforcing
// CanTransitionRunState plus the stage-dependent restrictions: waiting_quota
// is only reachable while the run is in execute or verify (quota exhaustion
// happens during agent work), and completed is only reachable from finalize.
// Setting the state the run is already in is a no-op.
//
// Prefer Cancel for cancellation — it also closes the in-flight stage
// record.
func (s *Store) SetRunState(ctx context.Context, taskID string, change RunStateChange) error {
	if !IsValidRunState(change.To) {
		return fmt.Errorf("pipeline: set run state: unknown state %q", change.To)
	}
	now := s.now().Format(time.RFC3339Nano)

	tx, err := s.db.Underlying().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("pipeline: set run state: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	run, err := getRunTx(ctx, tx, taskID)
	if err != nil {
		return err
	}
	if run.State == change.To {
		return tx.Commit() // no-op
	}
	if !CanTransitionRunState(run.State, change.To) {
		return &ErrInvalidRunStateTransition{TaskID: taskID, From: run.State, To: change.To}
	}
	if change.To == RunWaitingQuota &&
		run.CurrentStage != StageExecute && run.CurrentStage != StageVerify {
		return fmt.Errorf("pipeline: set run state: waiting_quota is only reachable from execute/verify (task %s in %s)",
			taskID, run.CurrentStage)
	}
	if change.To == RunCompleted && run.CurrentStage != StageFinalize {
		return fmt.Errorf("pipeline: set run state: completed is only reachable from finalize (task %s in %s)",
			taskID, run.CurrentStage)
	}

	res, err := tx.ExecContext(ctx, `
UPDATE pipeline_runs SET run_state = ?, updated_at = ?
WHERE task_id = ? AND run_state = ?`,
		string(change.To), now, taskID, string(run.State))
	if err != nil {
		return fmt.Errorf("pipeline: set run state %s for task %s: %w", change.To, taskID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_ = tx.Rollback()
		return fmt.Errorf("pipeline: set run state %s for task %s: %w", change.To, taskID, ErrConcurrentModification)
	}

	// Persist the accompanying detail fields in the same transaction.
	switch change.To {
	case RunFailed, RunCancelled, RunRepairExhausted:
		if change.Reason != "" {
			cat := string(run.FailureCategory)
			if change.To == RunCancelled {
				cat = string(FailureCancelled)
			}
			if _, err := tx.ExecContext(ctx, `
UPDATE pipeline_runs SET failure_category = NULLIF(?, ''), failure_reason = ? WHERE task_id = ?`,
				cat, change.Reason, taskID); err != nil {
				return fmt.Errorf("pipeline: set run state %s: record reason: %w", change.To, err)
			}
		}
	case RunCompleted:
		if change.ResultRef != "" {
			if _, err := tx.ExecContext(ctx, `
UPDATE pipeline_runs SET result_ref = ? WHERE task_id = ?`,
				change.ResultRef, taskID); err != nil {
				return fmt.Errorf("pipeline: set run state completed: record result ref: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("pipeline: set run state %s for task %s: commit: %w", change.To, taskID, err)
	}
	s.logger.Info("pipeline: run state change", "task", taskID, "from", run.State, "to", change.To)
	return nil
}

// Cancel transitions the run to cancelled. Legal from any non-terminal state
// (active, waiting_quota, blocked) and idempotent: cancelling a terminal run
// — including an already-cancelled one — is a no-op. The in-flight stage
// record, if any, is closed with a cancelled failure entry.
func (s *Store) Cancel(ctx context.Context, taskID, reason string) error {
	if reason == "" {
		reason = "cancelled"
	}
	now := s.now().Format(time.RFC3339Nano)

	tx, err := s.db.Underlying().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("pipeline: cancel: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	run, err := getRunTx(ctx, tx, taskID)
	if err != nil {
		return err
	}
	if IsTerminalRunState(run.State) {
		return tx.Commit() // no-op after terminal
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE pipeline_stage_records SET finished_at = ?
WHERE task_id = ? AND stage = ? AND attempt = ? AND status = ? AND finished_at IS NULL`,
		now, taskID, string(run.CurrentStage), run.StageAttempt, string(RecordEntered)); err != nil {
		return fmt.Errorf("pipeline: cancel task %s: close entry: %w", taskID, err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO pipeline_stage_records (task_id, stage, attempt, status, failure_category, reason, entered_at, finished_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (task_id, stage, attempt, status) DO NOTHING`,
		taskID, string(run.CurrentStage), run.StageAttempt, string(RecordFailed),
		string(FailureCancelled), reason, now, now); err != nil {
		return fmt.Errorf("pipeline: cancel task %s: record outcome: %w", taskID, err)
	}
	res, err := tx.ExecContext(ctx, `
UPDATE pipeline_runs SET run_state = ?, failure_category = ?, failure_reason = ?, updated_at = ?
WHERE task_id = ? AND run_state = ?`,
		string(RunCancelled), string(FailureCancelled), reason, now, taskID, string(run.State))
	if err != nil {
		return fmt.Errorf("pipeline: cancel task %s: %w", taskID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_ = tx.Rollback()
		return fmt.Errorf("pipeline: cancel task %s: %w", taskID, ErrConcurrentModification)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("pipeline: cancel task %s: commit: %w", taskID, err)
	}
	s.logger.Info("pipeline: run cancelled", "task", taskID, "stage", run.CurrentStage)
	return nil
}

// control_flags keys for the persisted emergency stop.
const (
	emergencyStopFlagKey   = "emergency_stop"
	emergencyStopReasonKey = "emergency_stop_reason"
)

// SetEmergencyStop persists the emergency-stop flag. While on, the pipeline
// driver must refuse to start ANY stage. Turning the flag off clears the
// stored reason. Durable across restarts.
func (s *Store) SetEmergencyStop(ctx context.Context, on bool, reason string) error {
	value := "off"
	if on {
		value = "on"
	} else {
		reason = ""
	}
	now := s.now().Format(time.RFC3339Nano)

	tx, err := s.db.Underlying().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("pipeline: set emergency stop: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, kv := range [][2]string{
		{emergencyStopFlagKey, value},
		{emergencyStopReasonKey, reason},
	} {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO control_flags (key, value, updated_at) VALUES (?, ?, ?)
ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			kv[0], kv[1], now); err != nil {
			return fmt.Errorf("pipeline: set emergency stop: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("pipeline: set emergency stop: commit: %w", err)
	}
	s.logger.Info("pipeline: emergency stop changed", "on", on, "reason", reason)
	return nil
}

// EmergencyStop reports whether the emergency-stop flag is on, and the
// reason recorded with it. It is a single primary-key lookup pair, cheap
// enough for the driver to call before starting ANY stage. A missing flag
// means off.
func (s *Store) EmergencyStop(ctx context.Context) (bool, string, error) {
	value, err := s.controlFlag(ctx, emergencyStopFlagKey)
	if err != nil {
		return false, "", err
	}
	if value != "on" {
		return false, "", nil
	}
	reason, err := s.controlFlag(ctx, emergencyStopReasonKey)
	if err != nil {
		return false, "", err
	}
	return true, reason, nil
}

func (s *Store) controlFlag(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.Underlying().QueryRowContext(ctx,
		`SELECT value FROM control_flags WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("pipeline: read control flag %q: %w", key, err)
	}
	return value, nil
}

// ---- internal helpers ----

// scanner abstracts sql.Row / sql.Rows for scanRun.
type scanner interface {
	Scan(dest ...any) error
}

func scanRun(row scanner) (*Run, error) {
	var run Run
	var stage, state, createdAt, updatedAt string
	var category, reason, resultRef sql.NullString
	if err := row.Scan(&run.TaskID, &run.ProjectID, &stage, &state,
		&run.StageAttempt, &run.RepairAttempt, &run.MaxRepairAttempts,
		&category, &reason, &resultRef, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	run.CurrentStage = Stage(stage)
	run.State = RunState(state)
	if category.Valid {
		run.FailureCategory = FailureCategory(category.String)
	}
	if reason.Valid {
		run.FailureReason = reason.String
	}
	if resultRef.Valid {
		run.ResultRef = resultRef.String
	}
	var err error
	if run.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	if run.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return nil, fmt.Errorf("parse updated_at: %w", err)
	}
	return &run, nil
}

func getRunTx(ctx context.Context, tx *sql.Tx, taskID string) (*Run, error) {
	// Take the SQLite write lock BEFORE any SELECT in this transaction via a
	// no-op write. A deferred read→write lock upgrade fails immediately with
	// SQLITE_BUSY_SNAPSHOT under a concurrent writer (busy_timeout does not
	// apply to snapshot-upgrade conflicts); acquiring the lock up front makes
	// competing writers queue on busy_timeout instead.
	if _, err := tx.ExecContext(ctx,
		`UPDATE pipeline_runs SET updated_at = updated_at WHERE task_id = ?`, taskID); err != nil {
		return nil, fmt.Errorf("pipeline: lock run for task %s: %w", taskID, err)
	}
	run, err := scanRun(tx.QueryRowContext(ctx, `
SELECT task_id, project_id, current_stage, run_state, stage_attempt, repair_attempt,
       max_repair_attempts, failure_category, failure_reason, result_ref, created_at, updated_at
FROM pipeline_runs WHERE task_id = ?`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("pipeline: run for task %s: %w", taskID, ErrRunNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("pipeline: load run for task %s: %w", taskID, err)
	}
	return run, nil
}

// ensureEnteredTx inserts the "entered" record for (stage, attempt) if
// absent and returns the durable record.
func ensureEnteredTx(ctx context.Context, tx *sql.Tx, taskID string, stage Stage, attempt int, reason, evidenceRef, now string) (*StageRecord, error) {
	if _, err := tx.ExecContext(ctx, `
INSERT INTO pipeline_stage_records (task_id, stage, attempt, status, reason, evidence_ref, entered_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (task_id, stage, attempt, status) DO NOTHING`,
		taskID, string(stage), attempt, string(RecordEntered), reason, nullString(evidenceRef), now); err != nil {
		return nil, fmt.Errorf("pipeline: record stage entry %s (attempt %d): %w", stage, attempt, err)
	}
	return getRecordTx(ctx, tx, taskID, stage, attempt, RecordEntered)
}

func getRecordTx(ctx context.Context, tx *sql.Tx, taskID string, stage Stage, attempt int, status RecordStatus) (*StageRecord, error) {
	var rec StageRecord
	var st, statusStr, enteredAt string
	var finishedAt sql.NullString
	var category, evidence sql.NullString
	err := tx.QueryRowContext(ctx, `
SELECT id, task_id, stage, attempt, status, failure_category, reason, evidence_ref, entered_at, finished_at
FROM pipeline_stage_records
WHERE task_id = ? AND stage = ? AND attempt = ? AND status = ?`,
		taskID, string(stage), attempt, string(status)).
		Scan(&rec.ID, &rec.TaskID, &st, &rec.Attempt, &statusStr, &category, &rec.Reason, &evidence, &enteredAt, &finishedAt)
	if err != nil {
		return nil, fmt.Errorf("pipeline: load stage record %s/%s attempt %d: %w", stage, status, attempt, err)
	}
	rec.Stage = Stage(st)
	rec.Status = RecordStatus(statusStr)
	if category.Valid {
		rec.FailureCategory = FailureCategory(category.String)
	}
	if evidence.Valid {
		rec.EvidenceRef = evidence.String
	}
	if rec.EnteredAt, err = time.Parse(time.RFC3339Nano, enteredAt); err != nil {
		return nil, fmt.Errorf("pipeline: parse entered_at: %w", err)
	}
	if finishedAt.Valid && finishedAt.String != "" {
		if rec.FinishedAt, err = time.Parse(time.RFC3339Nano, finishedAt.String); err != nil {
			return nil, fmt.Errorf("pipeline: parse finished_at: %w", err)
		}
	}
	return &rec, nil
}

func recordExistsTx(ctx context.Context, tx *sql.Tx, taskID string, stage Stage, attempt int, status RecordStatus) (bool, error) {
	var n int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(1) FROM pipeline_stage_records
WHERE task_id = ? AND stage = ? AND attempt = ? AND status = ?`,
		taskID, string(stage), attempt, string(status)).Scan(&n); err != nil {
		return false, fmt.Errorf("pipeline: check stage record %s/%s attempt %d: %w", stage, status, attempt, err)
	}
	return n > 0, nil
}

// nullString maps an empty string to SQL NULL so nullable columns stay NULL
// rather than ”.
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
