package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// This file implements the durable Work Graph substrate (spec §18.3, §31,
// milestone M14-05). It is the data-only mirror of the workgraph package's
// domain types; the workgraph.WorkGraphStore wraps it and enforces the
// "only a ValidatedWorkGraph may be persisted" invariant (M14-04 AC2).
//
// All mutations go through [DB.BeginTx] / [*Tx] so they share a single atomic
// transaction with the audit events that record them (spec §11.4, §29.4,
// ADR-0003). Reads return ordered rows so the reconstructed graph is
// deterministic regardless of insertion order.

// ErrWorkPackageNotFound is returned when an expected work_packages row is
// absent. The domain layer wraps this with the workgraph sentinel so callers
// can use errors.Is.
var ErrWorkPackageNotFound = errors.New("work package not found")

// ErrWorkGraphNotFound is returned when a task has no persisted packages.
var ErrWorkGraphNotFound = errors.New("work graph not found")

// WorkPackageRow mirrors one work_packages row. List-shaped fields
// (AcceptedACIDs, AllowedScope, Dependencies, Attempts) are JSON-encoded
// strings so the schema stays stable while the domain model evolves; the
// workgraph store owns the encoding.
type WorkPackageRow struct {
	TaskID        string
	PackageID     string
	Stage         string
	Title         string
	Objective     string
	AcceptedACIDs string // JSON array
	AllowedScope  string // JSON array
	Dependencies  string // JSON array (denormalised mirror of work_package_dependencies)
	State         string
	Attempts      string // JSON array
	GraphVersion  int
	CreatedAt     string
	UpdatedAt     string
}

// WorkPackageAttemptRow mirrors one work_package_attempts row. The package's
// Attempts JSON column is the authoritative read path; this table exists so
// individual attempts are independently queryable (spec §31).
type WorkPackageAttemptRow struct {
	ID            int64
	TaskID        string
	PackageID     string
	AttemptIndex  int
	State         string
	StartedAt     string
	FinishedAt    string
	FailureReason string
	ExitCode      int
	AgentRunID    string
}

// ReplaceWorkGraph atomically replaces the entire work graph for taskID with
// the supplied package rows. Idempotent: re-saving the same graph replaces
// every row (incrementing GraphVersion on each package) and resets the
// dependency rows to the union implied by the package rows' Dependencies
// column. Attempts rows are NOT touched by this operation — they are
// append-only history; the caller updates them through AppendAttempt.
//
// The caller is responsible for guaranteeing the input is a validated graph
// (workgraph.WorkGraphStore enforces this).
func (d *DB) ReplaceWorkGraph(ctx context.Context, taskID string, rows []WorkPackageRow) error {
	tx, err := d.BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("storage: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.ReplaceWorkGraph(ctx, taskID, rows); err != nil {
		return err
	}
	return tx.Commit()
}

// ReplaceWorkGraph runs within tx (see [DB.ReplaceWorkGraph]).
func (t *Tx) ReplaceWorkGraph(ctx context.Context, taskID string, rows []WorkPackageRow) error {
	if taskID == "" {
		return fmt.Errorf("storage: replace work graph: task_id is required")
	}
	if len(rows) == 0 {
		return fmt.Errorf("storage: replace work graph: at least one package is required")
	}

	// Determine the next graph_version: max(existing) +1 across this task's
	// packages, falling back to 1 for a fresh graph. We compute it once so every
	// upserted row in this batch shares the same version (atomic snapshot).
	var maxVersion int
	err := t.tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(graph_version), 0) FROM work_packages WHERE task_id = ?`,
		taskID).Scan(&maxVersion)
	if err != nil {
		return fmt.Errorf("storage: read work graph max version: %w", err)
	}
	nextVersion := maxVersion + 1

	// Clear the dependency table for this task; the package rows carry the
	// authoritative dependency list and we re-insert it below.
	if _, err := t.tx.ExecContext(ctx,
		`DELETE FROM work_package_dependencies WHERE task_id = ?`, taskID); err != nil {
		return fmt.Errorf("storage: clear work_package_dependencies: %w", err)
	}

	for _, r := range rows {
		if r.PackageID == "" {
			return fmt.Errorf("storage: replace work graph: package id is required")
		}
		// Preserve created_at on an existing row (idempotent re-save); stamp
		// it on first insert. We pre-select rather than rely on ON CONFLICT
		// DO UPDATE EXCLUDED.created_at because the EXCLUDED form would
		// overwrite the original timestamp on every re-save, losing the
		// original creation time.
		var createdAt string
		err := t.tx.QueryRowContext(ctx,
			`SELECT created_at FROM work_packages WHERE task_id = ? AND package_id = ?`,
			taskID, r.PackageID).Scan(&createdAt)
		switch {
		case err == nil:
			// keep existing createdAt
		case errors.Is(err, sql.ErrNoRows):
			createdAt = r.CreatedAt
		default:
			return fmt.Errorf("storage: read work_packages.created_at: %w", err)
		}

		if r.UpdatedAt == "" {
			return fmt.Errorf("storage: replace work graph: updated_at is required for package %q", r.PackageID)
		}

		if _, err := t.tx.ExecContext(ctx, `
INSERT INTO work_packages
	(task_id, package_id, stage, title, objective,
	 accepted_ac_ids, allowed_scope, dependencies,
	 state, attempts, graph_version, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(task_id, package_id) DO UPDATE SET
	stage           = excluded.stage,
	title           = excluded.title,
	objective       = excluded.objective,
	accepted_ac_ids = excluded.accepted_ac_ids,
	allowed_scope   = excluded.allowed_scope,
	dependencies    = excluded.dependencies,
	state           = excluded.state,
	attempts        = excluded.attempts,
	graph_version   = excluded.graph_version,
	created_at      = excluded.created_at,
	updated_at      = excluded.updated_at`,
			taskID, r.PackageID, r.Stage, r.Title, r.Objective,
			r.AcceptedACIDs, r.AllowedScope, r.Dependencies,
			r.State, r.Attempts, nextVersion, createdAt, r.UpdatedAt); err != nil {
			return fmt.Errorf("storage: upsert work package %q: %w", r.PackageID, err)
		}

		// Mirror the dependency list into the normalised table.
		deps, err := decodeJSONStrings(r.Dependencies)
		if err != nil {
			return fmt.Errorf("storage: decode dependencies for package %q: %w", r.PackageID, err)
		}
		for _, dep := range deps {
			if _, err := t.tx.ExecContext(ctx, `
INSERT OR IGNORE INTO work_package_dependencies (task_id, package_id, depends_on)
VALUES (?, ?, ?)`, taskID, r.PackageID, dep); err != nil {
				return fmt.Errorf("storage: insert dependency %q->%q: %w", r.PackageID, dep, err)
			}
		}
	}

	// Drop packages that are no longer in the graph (the graph shrank). Use a
	// parameterised IN-list built from the supplied package ids.
	ids := make([]any, 0, len(rows)+1)
	ids = append(ids, taskID)
	for _, r := range rows {
		ids = append(ids, r.PackageID)
	}
	placeholders := "("
	for i := range rows {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
	}
	placeholders += ")"
	if _, err := t.tx.ExecContext(ctx,
		`DELETE FROM work_packages WHERE task_id = ? AND package_id NOT IN `+placeholders,
		ids...); err != nil {
		return fmt.Errorf("storage: prune work_packages: %w", err)
	}
	return nil
}

// ListWorkPackages returns every work_packages row for taskID, ordered by
// package_id so the reconstructed graph is deterministic regardless of insert
// order. Returns ErrWorkGraphNotFound when the task has no persisted packages.
func (d *DB) ListWorkPackages(ctx context.Context, taskID string) ([]WorkPackageRow, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT task_id, package_id, stage, title, objective,
       accepted_ac_ids, allowed_scope, dependencies, state, attempts,
       graph_version, created_at, updated_at
FROM work_packages WHERE task_id = ?
ORDER BY package_id ASC`, taskID)
	if err != nil {
		return nil, fmt.Errorf("storage: list work packages: %w", err)
	}
	defer rows.Close()
	out := make([]WorkPackageRow, 0)
	for rows.Next() {
		var r WorkPackageRow
		if err := rows.Scan(&r.TaskID, &r.PackageID, &r.Stage, &r.Title, &r.Objective,
			&r.AcceptedACIDs, &r.AllowedScope, &r.Dependencies, &r.State, &r.Attempts,
			&r.GraphVersion, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("storage: scan work package: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: iterate work packages: %w", err)
	}
	if len(out) == 0 {
		return nil, ErrWorkGraphNotFound
	}
	return out, nil
}

// GetWorkPackage returns one work_packages row by (taskID, packageID).
func (d *DB) GetWorkPackage(ctx context.Context, taskID, packageID string) (WorkPackageRow, error) {
	var r WorkPackageRow
	err := d.db.QueryRowContext(ctx, `
SELECT task_id, package_id, stage, title, objective,
       accepted_ac_ids, allowed_scope, dependencies, state, attempts,
       graph_version, created_at, updated_at
FROM work_packages WHERE task_id = ? AND package_id = ?`, taskID, packageID).Scan(
		&r.TaskID, &r.PackageID, &r.Stage, &r.Title, &r.Objective,
		&r.AcceptedACIDs, &r.AllowedScope, &r.Dependencies, &r.State, &r.Attempts,
		&r.GraphVersion, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkPackageRow{}, ErrWorkPackageNotFound
	}
	if err != nil {
		return WorkPackageRow{}, fmt.Errorf("storage: get work package: %w", err)
	}
	return r, nil
}

// UpdateWorkPackageState atomically transitions a package's state. Returns
// ErrWorkPackageNotFound when the (taskID, packageID) row does not exist.
// updatedAt is the caller-supplied timestamp (RFC3339Nano) recorded on the row
// so the audit trail and the row agree on when the transition happened.
func (d *DB) UpdateWorkPackageState(ctx context.Context, taskID, packageID, newState, updatedAt string) error {
	tx, err := d.BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("storage: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.UpdateWorkPackageState(ctx, taskID, packageID, newState, updatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateWorkPackageState runs within tx (see [DB.UpdateWorkPackageState]).
func (t *Tx) UpdateWorkPackageState(ctx context.Context, taskID, packageID, newState, updatedAt string) error {
	res, err := t.tx.ExecContext(ctx,
		`UPDATE work_packages SET state = ?, updated_at = ? WHERE task_id = ? AND package_id = ?`,
		newState, updatedAt, taskID, packageID)
	if err != nil {
		return fmt.Errorf("storage: update work package state: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrWorkPackageNotFound
	}
	return nil
}

// SetWorkPackageAttempts atomically replaces the JSON-encoded Attempts column
// for a package. It is the durable write behind workgraph.WorkGraphStore's
// attempt-append API (the workgraph store owns the encoding so the schema
// stays stable while the Attempt type evolves).
func (t *Tx) SetWorkPackageAttempts(ctx context.Context, taskID, packageID, attemptsJSON, updatedAt string) error {
	res, err := t.tx.ExecContext(ctx,
		`UPDATE work_packages SET attempts = ?, updated_at = ? WHERE task_id = ? AND package_id = ?`,
		attemptsJSON, updatedAt, taskID, packageID)
	if err != nil {
		return fmt.Errorf("storage: set work package attempts: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrWorkPackageNotFound
	}
	return nil
}

// AppendAttempt inserts a new work_package_attempts row. The unique constraint
// on (task_id, package_id, attempt_index) protects against duplicate inserts
// under retry; a duplicate insert surfaces as a SQLite constraint error which
// the caller can map to a typed "already exists" error.
func (t *Tx) AppendAttempt(ctx context.Context, r WorkPackageAttemptRow) error {
	_, err := t.tx.ExecContext(ctx, `
INSERT INTO work_package_attempts
	(task_id, package_id, attempt_index, state, started_at, finished_at,
	 failure_reason, exit_code, agent_run_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.TaskID, r.PackageID, r.AttemptIndex, r.State, r.StartedAt, r.FinishedAt,
		r.FailureReason, r.ExitCode, r.AgentRunID)
	if err != nil {
		return fmt.Errorf("storage: append attempt: %w", err)
	}
	return nil
}

// ListAttempts returns every attempt row for a package, ordered by index
// ascending. Empty slice if the package has no recorded attempts yet.
func (d *DB) ListAttempts(ctx context.Context, taskID, packageID string) ([]WorkPackageAttemptRow, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT id, task_id, package_id, attempt_index, state, started_at, finished_at,
       failure_reason, exit_code, agent_run_id
FROM work_package_attempts
WHERE task_id = ? AND package_id = ?
ORDER BY attempt_index ASC`, taskID, packageID)
	if err != nil {
		return nil, fmt.Errorf("storage: list attempts: %w", err)
	}
	defer rows.Close()
	out := make([]WorkPackageAttemptRow, 0)
	for rows.Next() {
		var r WorkPackageAttemptRow
		if err := rows.Scan(&r.ID, &r.TaskID, &r.PackageID, &r.AttemptIndex, &r.State,
			&r.StartedAt, &r.FinishedAt, &r.FailureReason, &r.ExitCode, &r.AgentRunID); err != nil {
			return nil, fmt.Errorf("storage: scan attempt: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---- helpers ----

// decodeJSONStrings decodes a JSON array of strings. Empty input returns nil
// without error (the column default is '[]').
func decodeJSONStrings(s string) ([]string, error) {
	if s == "" || s == "null" {
		return nil, nil
	}
	var out []string
	if err := jsonUnmarshalStrings([]byte(s), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// jsonUnmarshalStrings is a thin wrapper kept in this file to avoid pulling a
// second copy of encoding/json into the package's import surface. It exists
// as a named function so tests can target the decode path directly.
func jsonUnmarshalStrings(data []byte, out *[]string) error {
	return jsonDecode(data, out)
}
