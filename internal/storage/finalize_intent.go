package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// FinalizeIntentPhase is the crash-recovery phase of a finalize intent (BF-07).
const (
	FinalizePhasePending  = "pending"   // intent recorded; ref not yet ensured
	FinalizePhaseRefReady = "ref_ready" // result ref at expected_sha (or no ref needed)
)

// FinalizeIntent is the durable finalization protocol row. Git refs and SQLite
// cannot share one physical transaction; this intent + reconciliation /
// compensation keep them consistent across process crashes (BF-07).
type FinalizeIntent struct {
	WorkspaceID     string
	TaskID          string
	Outcome         string
	RunTerminal     string
	RunID           string
	Engine          string
	Model           string
	BaseSHA         string
	ActualHeadSHA   string
	ExpectedRefSHA  string // empty when outcome creates no result ref
	ResultBranch    string
	CommitSHA       string
	GitStatusEmpty  bool
	ChangedFiles    string // JSON array
	TargetWSState   string
	TargetTaskState string
	Phase           string
	CreatedAt       string
	UpdatedAt       string
}

// UpsertFinalizeIntent inserts or replaces the intent for a workspace.
func (d *DB) UpsertFinalizeIntent(ctx context.Context, in FinalizeIntent) error {
	return upsertFinalizeIntent(ctx, d.db, in)
}

// UpsertFinalizeIntent inserts or replaces the intent inside a transaction.
func (t *Tx) UpsertFinalizeIntent(ctx context.Context, in FinalizeIntent) error {
	return upsertFinalizeIntent(ctx, t.tx, in)
}

func upsertFinalizeIntent(ctx context.Context, e executor, in FinalizeIntent) error {
	empty := 0
	if in.GitStatusEmpty {
		empty = 1
	}
	_, err := e.ExecContext(ctx, `
INSERT INTO finalize_intents (
  workspace_id, task_id, outcome, run_terminal, run_id, engine, model,
  base_sha, actual_head_sha, expected_ref_sha, result_branch, commit_sha,
  git_status_empty, changed_files, target_ws_state, target_task_state,
  phase, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(workspace_id) DO UPDATE SET
  task_id = excluded.task_id,
  outcome = excluded.outcome,
  run_terminal = excluded.run_terminal,
  run_id = excluded.run_id,
  engine = excluded.engine,
  model = excluded.model,
  base_sha = excluded.base_sha,
  actual_head_sha = excluded.actual_head_sha,
  expected_ref_sha = excluded.expected_ref_sha,
  result_branch = excluded.result_branch,
  commit_sha = excluded.commit_sha,
  git_status_empty = excluded.git_status_empty,
  changed_files = excluded.changed_files,
  target_ws_state = excluded.target_ws_state,
  target_task_state = excluded.target_task_state,
  phase = excluded.phase,
  updated_at = excluded.updated_at
`,
		in.WorkspaceID, in.TaskID, in.Outcome, in.RunTerminal, in.RunID,
		in.Engine, in.Model, in.BaseSHA, in.ActualHeadSHA, in.ExpectedRefSHA,
		in.ResultBranch, in.CommitSHA, empty, in.ChangedFiles,
		in.TargetWSState, in.TargetTaskState, in.Phase,
		in.CreatedAt, in.UpdatedAt)
	if err != nil {
		return fmt.Errorf("storage: upsert finalize intent: %w", err)
	}
	return nil
}

// GetFinalizeIntent returns the intent for a workspace, or ErrFinalizeIntentNotFound.
func (d *DB) GetFinalizeIntent(ctx context.Context, workspaceID string) (FinalizeIntent, error) {
	return getFinalizeIntent(ctx, d.db, workspaceID)
}

// GetFinalizeIntent returns the intent inside a transaction.
func (t *Tx) GetFinalizeIntent(ctx context.Context, workspaceID string) (FinalizeIntent, error) {
	return getFinalizeIntent(ctx, t.tx, workspaceID)
}

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func getFinalizeIntent(ctx context.Context, q rowQuerier, workspaceID string) (FinalizeIntent, error) {
	var in FinalizeIntent
	var empty int
	err := q.QueryRowContext(ctx, `
SELECT workspace_id, task_id, outcome, run_terminal, run_id, engine, model,
       base_sha, actual_head_sha, expected_ref_sha, result_branch, commit_sha,
       git_status_empty, changed_files, target_ws_state, target_task_state,
       phase, created_at, updated_at
  FROM finalize_intents WHERE workspace_id = ?`, workspaceID).Scan(
		&in.WorkspaceID, &in.TaskID, &in.Outcome, &in.RunTerminal, &in.RunID,
		&in.Engine, &in.Model, &in.BaseSHA, &in.ActualHeadSHA, &in.ExpectedRefSHA,
		&in.ResultBranch, &in.CommitSHA, &empty, &in.ChangedFiles,
		&in.TargetWSState, &in.TargetTaskState, &in.Phase,
		&in.CreatedAt, &in.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return FinalizeIntent{}, ErrFinalizeIntentNotFound
	}
	if err != nil {
		return FinalizeIntent{}, fmt.Errorf("storage: get finalize intent: %w", err)
	}
	in.GitStatusEmpty = empty != 0
	return in, nil
}

// ListFinalizeIntents returns every pending/ref_ready finalize intent.
func (d *DB) ListFinalizeIntents(ctx context.Context) ([]FinalizeIntent, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT workspace_id, task_id, outcome, run_terminal, run_id, engine, model,
       base_sha, actual_head_sha, expected_ref_sha, result_branch, commit_sha,
       git_status_empty, changed_files, target_ws_state, target_task_state,
       phase, created_at, updated_at
  FROM finalize_intents ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("storage: list finalize intents: %w", err)
	}
	defer rows.Close()
	var out []FinalizeIntent
	for rows.Next() {
		var in FinalizeIntent
		var empty int
		if err := rows.Scan(
			&in.WorkspaceID, &in.TaskID, &in.Outcome, &in.RunTerminal, &in.RunID,
			&in.Engine, &in.Model, &in.BaseSHA, &in.ActualHeadSHA, &in.ExpectedRefSHA,
			&in.ResultBranch, &in.CommitSHA, &empty, &in.ChangedFiles,
			&in.TargetWSState, &in.TargetTaskState, &in.Phase,
			&in.CreatedAt, &in.UpdatedAt); err != nil {
			return nil, fmt.Errorf("storage: scan finalize intent: %w", err)
		}
		in.GitStatusEmpty = empty != 0
		out = append(out, in)
	}
	return out, rows.Err()
}

// DeleteFinalizeIntent removes the intent row (finalization complete).
func (d *DB) DeleteFinalizeIntent(ctx context.Context, workspaceID string) error {
	return deleteFinalizeIntent(ctx, d.db, workspaceID)
}

// DeleteFinalizeIntent removes the intent inside a transaction.
func (t *Tx) DeleteFinalizeIntent(ctx context.Context, workspaceID string) error {
	return deleteFinalizeIntent(ctx, t.tx, workspaceID)
}

func deleteFinalizeIntent(ctx context.Context, e executor, workspaceID string) error {
	_, err := e.ExecContext(ctx, `DELETE FROM finalize_intents WHERE workspace_id = ?`, workspaceID)
	if err != nil {
		return fmt.Errorf("storage: delete finalize intent: %w", err)
	}
	return nil
}

// UpdateFinalizeIntentPhase advances the intent phase.
func (d *DB) UpdateFinalizeIntentPhase(ctx context.Context, workspaceID, phase, updatedAt string) error {
	res, err := d.db.ExecContext(ctx,
		`UPDATE finalize_intents SET phase = ?, updated_at = ? WHERE workspace_id = ?`,
		phase, updatedAt, workspaceID)
	if err != nil {
		return fmt.Errorf("storage: update finalize intent phase: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrFinalizeIntentNotFound
	}
	return nil
}

// ErrFinalizeIntentNotFound is returned when no intent row exists for a workspace.
var ErrFinalizeIntentNotFound = errors.New("finalize intent not found")
