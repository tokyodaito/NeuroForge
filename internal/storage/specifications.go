package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// SpecificationRow is the data-only mirror of one task_specifications row (spec
// §18.1, §31, milestone M14-01). The domain-level type lives in package task;
// this struct only carries what is persisted.
//
// Payload holds the list-shaped fields (non_goals, assumptions, constraints,
// proposed_scope, visual_requirements) as a JSON document so the schema stays
// stable while the compiler evolves. Acceptance criteria live in their own
// table (task_acceptance_criteria) because their stable ids are first-class
// (spec §27).
type SpecificationRow struct {
	TaskID     string
	Version    int
	Objective  string
	Risk       string
	Complexity string
	Payload    string
	Locked     bool
	LockedAt   string
	LockedBy   string
	CreatedAt  string
	CreatedBy  string
}

// AcceptanceCriterionRow mirrors one task_acceptance_criteria row. AcID is the
// stable, human-readable identifier (e.g. "AC-1"); it is the primary handle for
// evidence linkage (spec §27).
type AcceptanceCriterionRow struct {
	TaskID    string
	Version   int
	AcID      string
	Statement string
	Ordinal   int
}

// ErrSpecificationNotFound is returned when an expected specification row is
// absent.
var ErrSpecificationNotFound = errors.New("specification not found")

// ErrSpecificationLocked is returned when a caller tries to mutate a locked
// specification version (spec §28: specification_locked is a Merge Governor
// gate; a locked version must not be silently changed).
var ErrSpecificationLocked = errors.New("specification is locked")

// SaveSpecification persists a specification version and its acceptance
// criteria atomically. It is idempotent for an unlocked version: re-saving the
// same (task_id, version) replaces the row and its criteria. A LOCKED version
// is immutable — calling this on a locked (task_id, version) returns
// ErrSpecificationLocked without writing (spec §11.4, §28).
//
// The caller MUST set Version. Use [DB.NextSpecificationVersion] /
// [Tx.NextSpecificationVersion] to reserve the next version inside a
// transaction.
func (d *DB) SaveSpecification(ctx context.Context, row SpecificationRow, acs []AcceptanceCriterionRow) error {
	tx, err := d.BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("storage: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.SaveSpecification(ctx, row, acs); err != nil {
		return err
	}
	return tx.Commit()
}

// SaveSpecification persists a specification version within tx (see
// [DB.SaveSpecification]).
func (t *Tx) SaveSpecification(ctx context.Context, row SpecificationRow, acs []AcceptanceCriterionRow) error {
	if row.TaskID == "" {
		return fmt.Errorf("storage: save specification: task_id is required")
	}
	if row.Version < 1 {
		return fmt.Errorf("storage: save specification: version must be >= 1")
	}

	// Atomically check the locked flag and persist. SQLite serialises writers,
	// so the read-then-write within this transaction is race-free w.r.t. other
	// writers: a concurrent locker that commits first is observed here, and a
	// concurrent saver blocks until this tx ends (busy_timeout).
	var locked int
	err := t.tx.QueryRowContext(ctx,
		`SELECT locked FROM task_specifications WHERE task_id = ? AND version = ?`,
		row.TaskID, row.Version).Scan(&locked)
	switch {
	case err == nil:
		if locked != 0 {
			return ErrSpecificationLocked
		}
		// Replace the version's content (idempotent re-save of a draft).
		if _, err := t.tx.ExecContext(ctx, `
UPDATE task_specifications
	SET objective = ?, risk = ?, complexity = ?, payload = ?, created_by = ?, created_at = ?
	WHERE task_id = ? AND version = ?`,
			row.Objective, row.Risk, row.Complexity, row.Payload, row.CreatedBy, row.CreatedAt,
			row.TaskID, row.Version); err != nil {
			return fmt.Errorf("storage: update specification: %w", err)
		}
	case errors.Is(err, sql.ErrNoRows):
		if _, err := t.tx.ExecContext(ctx, `
INSERT INTO task_specifications
	(task_id, version, objective, risk, complexity, payload, locked, locked_at, locked_by, created_at, created_by)
VALUES (?, ?, ?, ?, ?, ?, 0, '', '', ?, ?)`,
			row.TaskID, row.Version, row.Objective, row.Risk, row.Complexity, row.Payload,
			row.CreatedAt, row.CreatedBy); err != nil {
			return fmt.Errorf("storage: insert specification: %w", err)
		}
	default:
		return fmt.Errorf("storage: read specification lock state: %w", err)
	}

	// Replace the acceptance criteria for this version (stable ids are
	// preserved across re-saves by the caller's choice of AcID).
	if _, err := t.tx.ExecContext(ctx,
		`DELETE FROM task_acceptance_criteria WHERE task_id = ? AND version = ?`,
		row.TaskID, row.Version); err != nil {
		return fmt.Errorf("storage: clear acceptance criteria: %w", err)
	}
	for _, ac := range acs {
		if ac.AcID == "" {
			return fmt.Errorf("storage: acceptance criterion id is required")
		}
		if _, err := t.tx.ExecContext(ctx, `
INSERT INTO task_acceptance_criteria (task_id, version, ac_id, statement, ordinal)
VALUES (?, ?, ?, ?, ?)`,
			row.TaskID, row.Version, ac.AcID, ac.Statement, ac.Ordinal); err != nil {
			return fmt.Errorf("storage: insert acceptance criterion %q: %w", ac.AcID, err)
		}
	}
	return nil
}

// GetSpecification returns one specification version and its acceptance
// criteria. Criteria are returned ordered by ordinal then ac_id so callers get a
// stable, deterministic ordering (spec §27).
func (d *DB) GetSpecification(ctx context.Context, taskID string, version int) (SpecificationRow, []AcceptanceCriterionRow, error) {
	var r SpecificationRow
	var locked int
	err := d.db.QueryRowContext(ctx, `
SELECT task_id, version, objective, risk, complexity, payload, locked, locked_at, locked_by, created_at, created_by
FROM task_specifications WHERE task_id = ? AND version = ?`, taskID, version).Scan(
		&r.TaskID, &r.Version, &r.Objective, &r.Risk, &r.Complexity, &r.Payload,
		&locked, &r.LockedAt, &r.LockedBy, &r.CreatedAt, &r.CreatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return SpecificationRow{}, nil, ErrSpecificationNotFound
	}
	if err != nil {
		return SpecificationRow{}, nil, fmt.Errorf("storage: get specification: %w", err)
	}
	r.Locked = locked != 0

	acs, err := d.listAcceptanceCriteria(ctx, taskID, version)
	if err != nil {
		return SpecificationRow{}, nil, err
	}
	return r, acs, nil
}

func (d *DB) listAcceptanceCriteria(ctx context.Context, taskID string, version int) ([]AcceptanceCriterionRow, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT task_id, version, ac_id, statement, ordinal
FROM task_acceptance_criteria WHERE task_id = ? AND version = ?
ORDER BY ordinal ASC, ac_id ASC`, taskID, version)
	if err != nil {
		return nil, fmt.Errorf("storage: list acceptance criteria: %w", err)
	}
	defer rows.Close()
	var out []AcceptanceCriterionRow
	for rows.Next() {
		var a AcceptanceCriterionRow
		if err := rows.Scan(&a.TaskID, &a.Version, &a.AcID, &a.Statement, &a.Ordinal); err != nil {
			return nil, fmt.Errorf("storage: scan acceptance criterion: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: iterate acceptance criteria: %w", err)
	}
	return out, nil
}

// GetLatestSpecification returns the highest-numbered version of the task's
// specification. If the task has no specification yet, it returns
// ErrSpecificationNotFound.
func (d *DB) GetLatestSpecification(ctx context.Context, taskID string) (SpecificationRow, []AcceptanceCriterionRow, error) {
	var version int
	err := d.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM task_specifications WHERE task_id = ?`, taskID).Scan(&version)
	if err != nil {
		return SpecificationRow{}, nil, fmt.Errorf("storage: latest specification version: %w", err)
	}
	if version == 0 {
		return SpecificationRow{}, nil, ErrSpecificationNotFound
	}
	return d.GetSpecification(ctx, taskID, version)
}

// ListSpecificationVersions returns every persisted version number for a task,
// ascending. Empty slice if none.
func (d *DB) ListSpecificationVersions(ctx context.Context, taskID string) ([]int, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT version FROM task_specifications WHERE task_id = ? ORDER BY version ASC`, taskID)
	if err != nil {
		return nil, fmt.Errorf("storage: list specification versions: %w", err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("storage: scan version: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: iterate versions: %w", err)
	}
	return out, nil
}

// LockSpecification marks a version immutable. It is idempotent: locking an
// already-locked version is a no-op success. The first successful lock records
// locked_at/locked_by; subsequent calls preserve the original values so the
// lock's provenance is durable (spec §28).
func (d *DB) LockSpecification(ctx context.Context, taskID string, version int, lockedBy string) error {
	tx, err := d.BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("storage: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.LockSpecification(ctx, taskID, version, lockedBy); err != nil {
		return err
	}
	return tx.Commit()
}

// LockSpecification marks a version immutable within tx (see
// [DB.LockSpecification]).
func (t *Tx) LockSpecification(ctx context.Context, taskID string, version int, lockedBy string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// Preserve the original locked_at/locked_by when re-locking so the lock's
	// provenance cannot be silently rewritten (idempotent lock).
	res, err := t.tx.ExecContext(ctx, `
UPDATE task_specifications
	SET locked = 1,
	    locked_at = CASE WHEN COALESCE(locked_at, '') = '' THEN ? ELSE locked_at END,
	    locked_by = CASE WHEN COALESCE(locked_by, '') = '' THEN ? ELSE locked_by END
	WHERE task_id = ? AND version = ?`,
		now, lockedBy, taskID, version)
	if err != nil {
		return fmt.Errorf("storage: lock specification: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrSpecificationNotFound
	}
	return nil
}

// NextSpecificationVersion returns the next free version number for taskID. It
// is implemented as an atomic UPSERT-increment against
// task_specification_sequences, mirroring [Tx.NextTaskSeq]: SQLite's
// single-writer serialisation guarantees two concurrent callers receive
// distinct, monotonically-increasing versions (race-free, never reused, spec
// §11.4).
//
// The caller MUST hold the returned version's transaction open across the
// subsequent [Tx.SaveSpecification] so the reservation and the row commit
// atomically (or roll back together). The DB-level convenience wrapper is
// self-contained (its own transaction) and therefore also race-free.
func (d *DB) NextSpecificationVersion(ctx context.Context, taskID string) (int, error) {
	tx, err := d.BeginTx(ctx)
	if err != nil {
		return 0, fmt.Errorf("storage: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	v, err := tx.NextSpecificationVersion(ctx, taskID)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("storage: commit next version: %w", err)
	}
	return v, nil
}

// NextSpecificationVersion reserves the next version number for taskID within
// tx (see [DB.NextSpecificationVersion]). It MUST be called in the same
// transaction as the subsequent [Tx.SaveSpecification] so the reservation and
// the row commit atomically.
func (t *Tx) NextSpecificationVersion(ctx context.Context, taskID string) (int, error) {
	if taskID == "" {
		return 0, fmt.Errorf("storage: task id is required for specification version")
	}
	if _, err := t.tx.ExecContext(ctx, `
INSERT INTO task_specification_sequences (task_id, next_version) VALUES (?, 1)
ON CONFLICT(task_id) DO UPDATE SET next_version = next_version + 1`, taskID); err != nil {
		return 0, fmt.Errorf("storage: reserve specification version: %w", err)
	}
	var v int
	if err := t.tx.QueryRowContext(ctx,
		`SELECT next_version FROM task_specification_sequences WHERE task_id = ?`, taskID).Scan(&v); err != nil {
		return 0, fmt.Errorf("storage: read reserved specification version: %w", err)
	}
	if v <= 0 {
		return 0, fmt.Errorf("storage: specification version for %q is corrupt (got %d)", taskID, v)
	}
	return v, nil
}
