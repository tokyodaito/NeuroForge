package storage

import (
	"context"
	"fmt"
)

// Task is the data-only row mirroring the tasks table.
type Task struct {
	ID          string
	ProjectID   string
	Title       string
	Description string
	Priority    string
	State       string
	CreatedAt   string
	UpdatedAt   string
}

// TaskAttachment is the data-only row mirroring the task_attachments table.
type TaskAttachment struct {
	ID        int64
	TaskID    string
	Hash      string
	Filename  string
	MimeType  string
	Size      int64
	Role      string
	CreatedAt string
}

// CreateTask inserts a new task row.
func (d *DB) CreateTask(ctx context.Context, t Task) error {
	return createTask(ctx, d.db, t)
}

// CreateTask inserts a new task row as part of tx.
func (t *Tx) CreateTask(ctx context.Context, task Task) error {
	return createTask(ctx, t.tx, task)
}

func createTask(ctx context.Context, e executor, t Task) error {
	_, err := e.ExecContext(ctx, `
INSERT INTO tasks (id, project_id, title, description, priority, state, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.ProjectID, t.Title, t.Description, t.Priority, t.State,
		t.CreatedAt, t.UpdatedAt)
	if err != nil {
		return fmt.Errorf("storage: create task: %w", err)
	}
	return nil
}

// GetTask returns a single task by id.
func (d *DB) GetTask(ctx context.Context, id string) (Task, error) {
	var t Task
	err := d.db.QueryRowContext(ctx, `
SELECT id, project_id, title, description, priority, state, created_at, updated_at
FROM tasks WHERE id = ?`, id).Scan(
		&t.ID, &t.ProjectID, &t.Title, &t.Description, &t.Priority,
		&t.State, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return Task{}, fmt.Errorf("storage: get task %q: %w", id, err)
	}
	return t, nil
}

// ListTasksByProject returns all tasks for a project, ordered by creation time.
func (d *DB) ListTasksByProject(ctx context.Context, projectID string) ([]Task, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT id, project_id, title, description, priority, state, created_at, updated_at
FROM tasks WHERE project_id = ? ORDER BY created_at`, projectID)
	if err != nil {
		return nil, fmt.Errorf("storage: list tasks: %w", err)
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Description,
			&t.Priority, &t.State, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("storage: scan task: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListAllTasks returns all tasks across all projects, ordered by creation time.
func (d *DB) ListAllTasks(ctx context.Context) ([]Task, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT id, project_id, title, description, priority, state, created_at, updated_at
FROM tasks ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("storage: list all tasks: %w", err)
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Description,
			&t.Priority, &t.State, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("storage: scan task: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpdateTaskState updates the state and updated_at timestamp of a task.
func (d *DB) UpdateTaskState(ctx context.Context, id, state, updatedAt string) error {
	return updateTaskState(ctx, d.db, id, state, updatedAt)
}

// UpdateTaskState updates the state and updated_at timestamp of a task as part
// of tx.
func (t *Tx) UpdateTaskState(ctx context.Context, id, state, updatedAt string) error {
	return updateTaskState(ctx, t.tx, id, state, updatedAt)
}

func updateTaskState(ctx context.Context, e executor, id, state, updatedAt string) error {
	res, err := e.ExecContext(ctx,
		`UPDATE tasks SET state = ?, updated_at = ? WHERE id = ?`,
		state, updatedAt, id)
	if err != nil {
		return fmt.Errorf("storage: update task state: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrTaskNotFound
	}
	return nil
}

// CountTasks returns the total number of tasks.
func (d *DB) CountTasks(ctx context.Context) (int, error) {
	var n int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks`).Scan(&n); err != nil {
		return 0, fmt.Errorf("storage: count tasks: %w", err)
	}
	return n, nil
}

// CountTasksByProject returns the number of tasks for a project.
func (d *DB) CountTasksByProject(ctx context.Context, projectID string) (int, error) {
	var n int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tasks WHERE project_id = ?`, projectID).Scan(&n); err != nil {
		return 0, fmt.Errorf("storage: count tasks by project: %w", err)
	}
	return n, nil
}

// ---- Attachments ----

// CreateAttachment inserts a task attachment metadata row.
func (d *DB) CreateAttachment(ctx context.Context, a TaskAttachment) error {
	return createAttachment(ctx, d.db, a)
}

// CreateAttachment inserts a task attachment metadata row as part of tx.
func (t *Tx) CreateAttachment(ctx context.Context, a TaskAttachment) error {
	return createAttachment(ctx, t.tx, a)
}

func createAttachment(ctx context.Context, e executor, a TaskAttachment) error {
	_, err := e.ExecContext(ctx, `
INSERT INTO task_attachments (task_id, hash, filename, mime_type, size, role, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		a.TaskID, a.Hash, a.Filename, a.MimeType, a.Size, a.Role, a.CreatedAt)
	if err != nil {
		return fmt.Errorf("storage: create attachment: %w", err)
	}
	return nil
}

// ListAttachments returns all attachments for a task.
func (d *DB) ListAttachments(ctx context.Context, taskID string) ([]TaskAttachment, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT id, task_id, hash, filename, mime_type, size, role, created_at
FROM task_attachments WHERE task_id = ? ORDER BY id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("storage: list attachments: %w", err)
	}
	defer rows.Close()
	var out []TaskAttachment
	for rows.Next() {
		var a TaskAttachment
		if err := rows.Scan(&a.ID, &a.TaskID, &a.Hash, &a.Filename,
			&a.MimeType, &a.Size, &a.Role, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("storage: scan attachment: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ErrTaskNotFound is returned when a task row is expected but absent.
var ErrTaskNotFound = fmt.Errorf("task not found")

// NextTaskSeq atomically reserves and returns the next sequence number for
// projectID, within tx. It upserts the task_sequences row (incrementing the
// per-project counter) and reads back the new value. Because both statements
// share tx, and SQLite serialises writers, this is concurrency-safe and
// restart-safe: the counter lives in durable storage and only ever moves
// forward (deleted tasks never reuse an id).
//
// The caller MUST hold tx across the subsequent task insert so the reserved
// sequence and the task row commit atomically (or roll back together).
func (t *Tx) NextTaskSeq(ctx context.Context, projectID string) (int64, error) {
	if projectID == "" {
		return 0, fmt.Errorf("storage: project id is required for task sequence")
	}
	if _, err := t.tx.ExecContext(ctx, `
INSERT INTO task_sequences (project_id, next_seq) VALUES (?, 1)
ON CONFLICT(project_id) DO UPDATE SET next_seq = next_seq + 1`, projectID); err != nil {
		return 0, fmt.Errorf("storage: reserve task sequence: %w", err)
	}
	var seq int64
	if err := t.tx.QueryRowContext(ctx,
		`SELECT next_seq FROM task_sequences WHERE project_id = ?`, projectID).Scan(&seq); err != nil {
		return 0, fmt.Errorf("storage: read task sequence: %w", err)
	}
	if seq <= 0 {
		return 0, fmt.Errorf("storage: task sequence for %q is corrupt (got %d)", projectID, seq)
	}
	return seq, nil
}
