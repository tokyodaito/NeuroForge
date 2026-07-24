package storage

import (
	"context"
	"fmt"
)

// Workspace is the data-only row mirroring the workspaces table. Domain logic
// lives in internal/workspace; this struct is the storage-level representation.
type Workspace struct {
	ID            string
	ProjectID     string
	TaskID        string
	WorkPackageID string
	Attempt       int
	Kind          string // "attempt" | "result"
	Path          string
	Branch        string
	ResultBranch  string
	BaseSHA       string
	HeadSHA       string
	ResultSHA     string
	State         string
	Engine        string
	Model         string
	RunID         string
	SessionID     string
	CreatedAt     string
	UpdatedAt     string
}

// CreateWorkspace inserts a new workspace row.
func (d *DB) CreateWorkspace(ctx context.Context, w Workspace) error {
	return createWorkspace(ctx, d.db, w)
}

// CreateWorkspace inserts a new workspace row as part of tx.
func (t *Tx) CreateWorkspace(ctx context.Context, w Workspace) error {
	return createWorkspace(ctx, t.tx, w)
}

func createWorkspace(ctx context.Context, e executor, w Workspace) error {
	_, err := e.ExecContext(ctx, `
INSERT INTO workspaces
  (id, project_id, task_id, work_package_id, attempt, kind, path, branch,
   result_branch, base_sha, head_sha, result_sha, state, engine, model,
   run_id, session_id, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.ID, w.ProjectID, w.TaskID, w.WorkPackageID, w.Attempt, w.Kind,
		w.Path, w.Branch, w.ResultBranch, w.BaseSHA, w.HeadSHA, w.ResultSHA,
		w.State, w.Engine, w.Model, w.RunID, w.SessionID,
		w.CreatedAt, w.UpdatedAt)
	if err != nil {
		return fmt.Errorf("storage: create workspace: %w", err)
	}
	return nil
}

// GetWorkspace returns a single workspace by id.
func (d *DB) GetWorkspace(ctx context.Context, id string) (Workspace, error) {
	var w Workspace
	err := d.db.QueryRowContext(ctx, `
SELECT id, project_id, task_id, work_package_id, attempt, kind, path, branch,
       result_branch, base_sha, head_sha, result_sha, state, engine, model,
       run_id, session_id, created_at, updated_at
FROM workspaces WHERE id = ?`, id).Scan(
		&w.ID, &w.ProjectID, &w.TaskID, &w.WorkPackageID, &w.Attempt, &w.Kind,
		&w.Path, &w.Branch, &w.ResultBranch, &w.BaseSHA, &w.HeadSHA, &w.ResultSHA,
		&w.State, &w.Engine, &w.Model, &w.RunID, &w.SessionID,
		&w.CreatedAt, &w.UpdatedAt)
	if err != nil {
		return Workspace{}, fmt.Errorf("storage: get workspace %q: %w", id, err)
	}
	return w, nil
}

// ListWorkspacesByTask returns all workspaces for a task, ordered by creation.
func (d *DB) ListWorkspacesByTask(ctx context.Context, taskID string) ([]Workspace, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT id, project_id, task_id, work_package_id, attempt, kind, path, branch,
       result_branch, base_sha, head_sha, result_sha, state, engine, model,
       run_id, session_id, created_at, updated_at
FROM workspaces WHERE task_id = ? ORDER BY created_at`, taskID)
	if err != nil {
		return nil, fmt.Errorf("storage: list workspaces by task: %w", err)
	}
	defer rows.Close()
	return scanWorkspaces(rows)
}

// ListWorkspacesByProject returns all workspaces for a project, ordered by creation.
func (d *DB) ListWorkspacesByProject(ctx context.Context, projectID string) ([]Workspace, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT id, project_id, task_id, work_package_id, attempt, kind, path, branch,
       result_branch, base_sha, head_sha, result_sha, state, engine, model,
       run_id, session_id, created_at, updated_at
FROM workspaces WHERE project_id = ? ORDER BY created_at`, projectID)
	if err != nil {
		return nil, fmt.Errorf("storage: list workspaces by project: %w", err)
	}
	defer rows.Close()
	return scanWorkspaces(rows)
}

// ListAllWorkspaces returns every workspace, ordered by creation.
func (d *DB) ListAllWorkspaces(ctx context.Context) ([]Workspace, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT id, project_id, task_id, work_package_id, attempt, kind, path, branch,
       result_branch, base_sha, head_sha, result_sha, state, engine, model,
       run_id, session_id, created_at, updated_at
FROM workspaces ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("storage: list all workspaces: %w", err)
	}
	defer rows.Close()
	return scanWorkspaces(rows)
}

func scanWorkspaces(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}) ([]Workspace, error) {
	var out []Workspace
	for rows.Next() {
		var w Workspace
		if err := rows.Scan(&w.ID, &w.ProjectID, &w.TaskID, &w.WorkPackageID,
			&w.Attempt, &w.Kind, &w.Path, &w.Branch, &w.ResultBranch,
			&w.BaseSHA, &w.HeadSHA, &w.ResultSHA, &w.State, &w.Engine,
			&w.Model, &w.RunID, &w.SessionID, &w.CreatedAt, &w.UpdatedAt); err != nil {
			return nil, fmt.Errorf("storage: scan workspace: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// UpdateWorkspaceState updates the state + head/run/session fields of a workspace.
func (d *DB) UpdateWorkspaceState(ctx context.Context, id, state, headSHA, runID, sessionID, updatedAt string) error {
	return updateWorkspaceState(ctx, d.db, id, state, headSHA, runID, sessionID, updatedAt)
}

// UpdateWorkspaceState updates the state + head/run/session fields of a workspace
// as part of tx.
func (t *Tx) UpdateWorkspaceState(ctx context.Context, id, state, headSHA, runID, sessionID, updatedAt string) error {
	return updateWorkspaceState(ctx, t.tx, id, state, headSHA, runID, sessionID, updatedAt)
}

func updateWorkspaceState(ctx context.Context, e executor, id, state, headSHA, runID, sessionID, updatedAt string) error {
	res, err := e.ExecContext(ctx, `
UPDATE workspaces SET state = ?, head_sha = ?, run_id = ?, session_id = ?, updated_at = ?
WHERE id = ?`,
		state, headSHA, runID, sessionID, updatedAt, id)
	if err != nil {
		return fmt.Errorf("storage: update workspace state: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrWorkspaceNotFound
	}
	return nil
}

// SetWorkspaceResult records the result branch + result SHA for a workspace.
func (d *DB) SetWorkspaceResult(ctx context.Context, id, resultBranch, resultSHA, updatedAt string) error {
	res, err := d.db.ExecContext(ctx, `
UPDATE workspaces SET result_branch = ?, result_sha = ?, updated_at = ? WHERE id = ?`,
		resultBranch, resultSHA, updatedAt, id)
	if err != nil {
		return fmt.Errorf("storage: set workspace result: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrWorkspaceNotFound
	}
	return nil
}

// DeleteWorkspace removes a workspace row by id.
func (d *DB) DeleteWorkspace(ctx context.Context, id string) error {
	res, err := d.db.ExecContext(ctx, `DELETE FROM workspaces WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("storage: delete workspace: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrWorkspaceNotFound
	}
	return nil
}

// ErrWorkspaceNotFound is returned when a workspace row is expected but absent.
var ErrWorkspaceNotFound = fmt.Errorf("workspace not found")
