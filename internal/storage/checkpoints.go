package storage

import (
	"context"
	"fmt"
)

// Checkpoint is the data-only row mirroring the checkpoints table.
type Checkpoint struct {
	ID          int64
	WorkspaceID string
	CommitSHA   string
	Moment      string
	Message     string
	CreatedAt   string
}

// CreateCheckpoint inserts a new checkpoint row.
func (d *DB) CreateCheckpoint(ctx context.Context, c Checkpoint) (int64, error) {
	return createCheckpoint(ctx, d.db, c)
}

// CreateCheckpoint inserts a new checkpoint row as part of tx.
func (t *Tx) CreateCheckpoint(ctx context.Context, c Checkpoint) (int64, error) {
	return createCheckpoint(ctx, t.tx, c)
}

func createCheckpoint(ctx context.Context, e executor, c Checkpoint) (int64, error) {
	res, err := e.ExecContext(ctx, `
INSERT INTO checkpoints (workspace_id, commit_sha, moment, message, created_at)
VALUES (?, ?, ?, ?, ?)`,
		c.WorkspaceID, c.CommitSHA, c.Moment, c.Message, c.CreatedAt)
	if err != nil {
		return 0, fmt.Errorf("storage: create checkpoint: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("storage: checkpoint last insert id: %w", err)
	}
	return id, nil
}

// ListCheckpoints returns all checkpoints for a workspace, ordered oldest-first.
func (d *DB) ListCheckpoints(ctx context.Context, workspaceID string) ([]Checkpoint, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT id, workspace_id, commit_sha, moment, message, created_at
FROM checkpoints WHERE workspace_id = ? ORDER BY id`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("storage: list checkpoints: %w", err)
	}
	defer rows.Close()
	var out []Checkpoint
	for rows.Next() {
		var c Checkpoint
		if err := rows.Scan(&c.ID, &c.WorkspaceID, &c.CommitSHA, &c.Moment,
			&c.Message, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("storage: scan checkpoint: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---- Leases ----

// Lease is the data-only row mirroring the leases table.
type Lease struct {
	ID          int64
	Scope       string // "project" | "workspace"
	ScopeID     string
	Kind        string // "path" | "semantic"
	Resource    string
	WorkspaceID string
	State       string // "active" | "released"
	CreatedAt   string
	ReleasedAt  string
}

// CreateLease inserts a new lease row.
func (d *DB) CreateLease(ctx context.Context, l Lease) (int64, error) {
	return createLease(ctx, d.db, l)
}

// CreateLease inserts a new lease row as part of tx.
func (t *Tx) CreateLease(ctx context.Context, l Lease) (int64, error) {
	return createLease(ctx, t.tx, l)
}

func createLease(ctx context.Context, e executor, l Lease) (int64, error) {
	res, err := e.ExecContext(ctx, `
INSERT INTO leases (scope, scope_id, kind, resource, workspace_id, state, created_at, released_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		l.Scope, l.ScopeID, l.Kind, l.Resource, l.WorkspaceID, l.State,
		l.CreatedAt, l.ReleasedAt)
	if err != nil {
		return 0, fmt.Errorf("storage: create lease: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("storage: lease last insert id: %w", err)
	}
	return id, nil
}

// HasActiveLease reports whether an active lease on the given resource exists
// under the given scope (excluding the workspace identified by excludeWorkspaceID).
func (d *DB) HasActiveLease(ctx context.Context, scope, scopeID, kind, resource, excludeWorkspaceID string) (bool, error) {
	var n int
	err := d.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM leases
WHERE scope = ? AND scope_id = ? AND kind = ? AND resource = ?
  AND state = 'active' AND workspace_id != ?`,
		scope, scopeID, kind, resource, excludeWorkspaceID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("storage: has active lease: %w", err)
	}
	return n > 0, nil
}

// ListActiveLeasesByScope returns all active leases for a scope id.
func (d *DB) ListActiveLeasesByScope(ctx context.Context, scope, scopeID string) ([]Lease, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT id, scope, scope_id, kind, resource, workspace_id, state, created_at, released_at
FROM leases WHERE scope = ? AND scope_id = ? AND state = 'active' ORDER BY id`,
		scope, scopeID)
	if err != nil {
		return nil, fmt.Errorf("storage: list active leases: %w", err)
	}
	defer rows.Close()
	return scanLeases(rows)
}

// ListLeasesByWorkspace returns all leases held by a workspace.
func (d *DB) ListLeasesByWorkspace(ctx context.Context, workspaceID string) ([]Lease, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT id, scope, scope_id, kind, resource, workspace_id, state, created_at, released_at
FROM leases WHERE workspace_id = ? ORDER BY id`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("storage: list leases by workspace: %w", err)
	}
	defer rows.Close()
	return scanLeases(rows)
}

// ReleaseLeasesByWorkspace marks all active leases for a workspace as released.
func (d *DB) ReleaseLeasesByWorkspace(ctx context.Context, workspaceID, releasedAt string) (int64, error) {
	return releaseLeasesByWorkspace(ctx, d.db, workspaceID, releasedAt)
}

// ReleaseLeasesByWorkspace marks all active leases for a workspace as released
// as part of tx.
func (t *Tx) ReleaseLeasesByWorkspace(ctx context.Context, workspaceID, releasedAt string) (int64, error) {
	return releaseLeasesByWorkspace(ctx, t.tx, workspaceID, releasedAt)
}

func releaseLeasesByWorkspace(ctx context.Context, e executor, workspaceID, releasedAt string) (int64, error) {
	res, err := e.ExecContext(ctx,
		`UPDATE leases SET state = 'released', released_at = ? WHERE workspace_id = ? AND state = 'active'`,
		releasedAt, workspaceID)
	if err != nil {
		return 0, fmt.Errorf("storage: release leases: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func scanLeases(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}) ([]Lease, error) {
	var out []Lease
	for rows.Next() {
		var l Lease
		if err := rows.Scan(&l.ID, &l.Scope, &l.ScopeID, &l.Kind, &l.Resource,
			&l.WorkspaceID, &l.State, &l.CreatedAt, &l.ReleasedAt); err != nil {
			return nil, fmt.Errorf("storage: scan lease: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ---- Continuation Packs ----

// ContinuationPack is the data-only row mirroring the continuation_packs table.
type ContinuationPack struct {
	ID                int64
	WorkspaceID       string
	FilePath          string
	SpecificationHash string
	BaseSHA           string
	CurrentSHA        string
	CreatedAt         string
}

// CreateContinuationPack inserts a new continuation pack row.
func (d *DB) CreateContinuationPack(ctx context.Context, p ContinuationPack) (int64, error) {
	return createContinuationPack(ctx, d.db, p)
}

// CreateContinuationPack inserts a new continuation pack row as part of tx.
func (t *Tx) CreateContinuationPack(ctx context.Context, p ContinuationPack) (int64, error) {
	return createContinuationPack(ctx, t.tx, p)
}

func createContinuationPack(ctx context.Context, e executor, p ContinuationPack) (int64, error) {
	res, err := e.ExecContext(ctx, `
INSERT INTO continuation_packs (workspace_id, file_path, specification_hash, base_sha, current_sha, created_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		p.WorkspaceID, p.FilePath, p.SpecificationHash, p.BaseSHA,
		p.CurrentSHA, p.CreatedAt)
	if err != nil {
		return 0, fmt.Errorf("storage: create continuation pack: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("storage: continuation pack last insert id: %w", err)
	}
	return id, nil
}

// ListContinuationPacks returns all continuation packs for a workspace.
func (d *DB) ListContinuationPacks(ctx context.Context, workspaceID string) ([]ContinuationPack, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT id, workspace_id, file_path, specification_hash, base_sha, current_sha, created_at
FROM continuation_packs WHERE workspace_id = ? ORDER BY id`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("storage: list continuation packs: %w", err)
	}
	defer rows.Close()
	var out []ContinuationPack
	for rows.Next() {
		var p ContinuationPack
		if err := rows.Scan(&p.ID, &p.WorkspaceID, &p.FilePath,
			&p.SpecificationHash, &p.BaseSHA, &p.CurrentSHA, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("storage: scan continuation pack: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
