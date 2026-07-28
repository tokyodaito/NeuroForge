package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrLeaseAlreadyExists is returned by CreateLease when a partial unique-index
// violation proves another active lease on the same (scope, scope_id, kind,
// resource) was inserted between the conflict SELECT and the INSERT (the
// classic TOCTOU window). The caller maps this to a typed conflict error after
// re-reading the conflicting lease.
//
// Detection is by SQLite's SQLITE_CONSTRAINT_UNIQUE error class, surfaced by
// modernc.org/sqlite as a string containing "UNIQUE" on the constraint name
// idx_leases_unique_active_resource. The match is intentionally broad ("UNIQUE"
// anywhere in the error string) so a future driver swap does not break it; the
// schema guarantees there is no other UNIQUE constraint on the leases table.
var ErrLeaseAlreadyExists = errors.New("storage: active lease already exists")

// IsLeaseUniqueConstraint reports whether err is the SQLite unique-constraint
// violation raised by idx_leases_unique_active_resource. It is the helper a
// caller uses to decide whether to retry a CreateLease as a conflict lookup
// rather than propagating a generic error.
func IsLeaseUniqueConstraint(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrLeaseAlreadyExists) {
		return true
	}
	return strings.Contains(err.Error(), "UNIQUE")
}

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
	State       string // "active" | "released" | "expired"
	CreatedAt   string
	ReleasedAt  string
	ExpiresAt   string // RFC3339Nano; empty = perpetual
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
INSERT INTO leases (scope, scope_id, kind, resource, workspace_id, state, created_at, released_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.Scope, l.ScopeID, l.Kind, l.Resource, l.WorkspaceID, l.State,
		l.CreatedAt, l.ReleasedAt, l.ExpiresAt)
	if err != nil {
		if IsLeaseUniqueConstraint(err) {
			return 0, ErrLeaseAlreadyExists
		}
		return 0, fmt.Errorf("storage: create lease: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("storage: lease last insert id: %w", err)
	}
	return id, nil
}

// GetActiveLease returns the active lease for a (scope, scope_id, kind,
// resource), or sql.ErrNoRows when none exists. It is the post-race lookup the
// caller uses to convert an ErrLeaseAlreadyExists into a typed conflict
// reason. Expired-but-not-yet-swept rows (state='active', expires_at in the
// past) are returned here so the caller can decide; HasActiveLease already
// excludes them from blocking.
func (d *DB) GetActiveLease(ctx context.Context, scope, scopeID, kind, resource string) (Lease, error) {
	var l Lease
	err := d.db.QueryRowContext(ctx, `
SELECT id, scope, scope_id, kind, resource, workspace_id, state, created_at, released_at, expires_at
FROM leases
WHERE scope = ? AND scope_id = ? AND kind = ? AND resource = ? AND state = 'active'
LIMIT 1`, scope, scopeID, kind, resource).Scan(
		&l.ID, &l.Scope, &l.ScopeID, &l.Kind, &l.Resource,
		&l.WorkspaceID, &l.State, &l.CreatedAt, &l.ReleasedAt, &l.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Lease{}, sql.ErrNoRows
	}
	if err != nil {
		return Lease{}, fmt.Errorf("storage: get active lease: %w", err)
	}
	return l, nil
}

// HasActiveLease reports whether an active lease on the given resource exists
// under the given scope (excluding the workspace identified by excludeWorkspaceID).
//
// "Active" excludes logically-expired leases: a row whose expires_at is in the
// past is treated as expired even before the ExpireLeases sweeper marks it
// state='expired' (defence-in-depth so a slow sweeper cannot falsely block
// execution). A perpetual lease (expires_at = ”) is active until released.
func (d *DB) HasActiveLease(ctx context.Context, scope, scopeID, kind, resource, excludeWorkspaceID string) (bool, error) {
	var n int
	err := d.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM leases
WHERE scope = ? AND scope_id = ? AND kind = ? AND resource = ?
  AND state = 'active' AND workspace_id != ?
  AND (expires_at = '' OR expires_at > ?)`,
		scope, scopeID, kind, resource, excludeWorkspaceID, utcNowRFC3339()).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("storage: has active lease: %w", err)
	}
	return n > 0, nil
}

// ListActiveLeasesByScope returns all active leases for a scope id.
func (d *DB) ListActiveLeasesByScope(ctx context.Context, scope, scopeID string) ([]Lease, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT id, scope, scope_id, kind, resource, workspace_id, state, created_at, released_at, expires_at
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
SELECT id, scope, scope_id, kind, resource, workspace_id, state, created_at, released_at, expires_at
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

// RenewWorkspaceLeases extends the expiry of every ACTIVE lease held by a
// workspace to newExpiresAt (RFC3339Nano). Leases without an expiry
// (perpetual, expires_at = ”) are left untouched — they do not need renewal.
// Returns the number of leases actually extended.
func (d *DB) RenewWorkspaceLeases(ctx context.Context, workspaceID, newExpiresAt string) (int64, error) {
	res, err := d.db.ExecContext(ctx,
		`UPDATE leases SET expires_at = ? WHERE workspace_id = ? AND state = 'active' AND expires_at != ''`,
		newExpiresAt, workspaceID)
	if err != nil {
		return 0, fmt.Errorf("storage: renew leases: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ExpireLeases marks every ACTIVE lease whose expires_at is non-empty and in
// the past (relative to now) as state='expired'. Returns the number swept.
// Perpetual leases (expires_at = ”) are never expired by this call. This is
// the periodic sweeper that converts "logically expired" rows into
// auditable state='expired' rows; HasActiveLease already treats them as
// expired, so a slow sweeper cannot falsely block execution (defence-in-depth).
func (d *DB) ExpireLeases(ctx context.Context, now string) (int64, error) {
	res, err := d.db.ExecContext(ctx,
		`UPDATE leases SET state = 'expired', released_at = ? WHERE state = 'active' AND expires_at != '' AND expires_at <= ?`,
		now, now)
	if err != nil {
		return 0, fmt.Errorf("storage: expire leases: %w", err)
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
			&l.WorkspaceID, &l.State, &l.CreatedAt, &l.ReleasedAt, &l.ExpiresAt); err != nil {
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
