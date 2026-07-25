package storage

import (
	"context"
	"fmt"
	"time"
)

// MemoryRow is one persisted project_memory row (spec §22.9, §31). It is the
// durable backing store for the in-process memory.Store.
type MemoryRow struct {
	ID         int64
	ProjectID  string
	Category   string
	Key        string
	Value      string
	Source     string
	Confidence string
	Scope      string
	CommitSHA  string
	Expiration string
	ExpiresAt  string
	LearnedAt  time.Time
	Version    int
}

// LearnMemory upserts a memory record keyed by (project_id, category, key). On
// conflict it bumps the version and updates the value (the project_memory table
// has a UNIQUE constraint on those three columns). It mirrors memory.Store.Learn
// but persists durably.
func (d *DB) LearnMemory(ctx context.Context, r MemoryRow) (MemoryRow, error) {
	if r.ProjectID == "" || r.Category == "" || r.Key == "" {
		return MemoryRow{}, fmt.Errorf("storage: learn memory: project_id, category and key are required")
	}
	if r.Confidence == "" {
		r.Confidence = "medium"
	}
	if r.Expiration == "" {
		r.Expiration = "permanent"
	}
	now := time.Now().UTC()
	if r.LearnedAt.IsZero() {
		r.LearnedAt = now
	}

	var version int
	err := d.db.QueryRowContext(ctx, `SELECT version FROM project_memory WHERE project_id=? AND category=? AND key=?`,
		r.ProjectID, r.Category, r.Key).Scan(&version)
	if err == nil {
		version++
		_, err = d.db.ExecContext(ctx, `
UPDATE project_memory
	SET value=?, source=?, confidence=?, scope=?, commit_sha=?, expiration=?, expires_at=?, learned_at=?, version=?
WHERE project_id=? AND category=? AND key=?`,
			r.Value, r.Source, r.Confidence, r.Scope, r.CommitSHA, r.Expiration, r.ExpiresAt,
			r.LearnedAt.Format(time.RFC3339Nano), version,
			r.ProjectID, r.Category, r.Key)
		if err != nil {
			return MemoryRow{}, fmt.Errorf("storage: update memory: %w", err)
		}
		r.Version = version
		return r, nil
	}

	version = 1
	_, err = d.db.ExecContext(ctx, `
INSERT INTO project_memory
	(project_id, category, key, value, source, confidence, scope, commit_sha, expiration, expires_at, learned_at, version)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ProjectID, r.Category, r.Key, r.Value, r.Source, r.Confidence, r.Scope, r.CommitSHA,
		r.Expiration, r.ExpiresAt, r.LearnedAt.Format(time.RFC3339Nano), version)
	if err != nil {
		return MemoryRow{}, fmt.Errorf("storage: insert memory: %w", err)
	}
	r.Version = version
	return r, nil
}

// ListMemory returns the memory rows for a project, ordered by (category, key).
func (d *DB) ListMemory(ctx context.Context, projectID string) ([]MemoryRow, error) {
	q := `SELECT id, project_id, category, key, value, source, confidence, scope,
		commit_sha, expiration, expires_at, learned_at, version
	FROM project_memory WHERE project_id = ? ORDER BY category ASC, key ASC`
	rows, err := d.db.QueryContext(ctx, q, projectID)
	if err != nil {
		return nil, fmt.Errorf("storage: query project_memory: %w", err)
	}
	defer rows.Close()
	var out []MemoryRow
	for rows.Next() {
		var r MemoryRow
		var learnedAt string
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.Category, &r.Key, &r.Value, &r.Source,
			&r.Confidence, &r.Scope, &r.CommitSHA, &r.Expiration, &r.ExpiresAt, &learnedAt, &r.Version); err != nil {
			return nil, fmt.Errorf("storage: scan memory: %w", err)
		}
		r.LearnedAt, _ = time.Parse(time.RFC3339Nano, learnedAt)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: iterate memory: %w", err)
	}
	return out, nil
}
