package storage

import (
	"context"
	"fmt"
	"time"
)

// Migration is a single forward-only schema step.
type Migration struct {
	Version     int
	Description string
	// Up is the SQL applied for this migration. Multiple statements are
	// executed sequentially within a single transaction.
	Up string
}

// migrations is the ordered list of all schema migrations. Append only — never
// edit or reorder an already-released migration (spec §31, ADR-0003).
//
// M0 only creates what the foundation actually uses: the bookkeeping table and
// the append-only audit_events table. The remaining §31 tables are added by
// later milestones as their owning packages land (rule §36.25: unimplemented
// requirements must be explicitly marked, not disguised as finished stubs).
var migrations = []Migration{
	{
		Version:     1,
		Description: "create schema_migrations bookkeeping table",
		Up:          `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL, description TEXT NOT NULL)`,
	},
	{
		Version:     2,
		Description: "create audit_events append-only table (AC-30)",
		Up: `
CREATE TABLE IF NOT EXISTS audit_events (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	ts         TEXT    NOT NULL,
	scope      TEXT    NOT NULL,
	scope_id   TEXT    NOT NULL,
	event_type TEXT    NOT NULL,
	actor      TEXT    NOT NULL,
	payload    TEXT    NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_audit_events_scope ON audit_events (scope, scope_id, id);
CREATE INDEX IF NOT EXISTS idx_audit_events_type   ON audit_events (event_type, id);
CREATE INDEX IF NOT EXISTS idx_audit_events_ts     ON audit_events (ts);

-- Append-only enforcement: the audit trail must never be mutated or erased
-- (spec §29.4, AC-30). UPDATE and DELETE are rejected at the storage layer so
-- that even a bug in a caller cannot rewrite history.
CREATE TRIGGER IF NOT EXISTS audit_events_no_update
	BEFORE UPDATE ON audit_events
BEGIN
	SELECT RAISE(ABORT, 'audit_events is append-only');
END;
CREATE TRIGGER IF NOT EXISTS audit_events_no_delete
	BEFORE DELETE ON audit_events
BEGIN
	SELECT RAISE(ABORT, 'audit_events is append-only');
END;
`,
	},
	{
		Version:     3,
		Description: "create projects, tasks and task_attachments tables (M1)",
		Up: `
CREATE TABLE IF NOT EXISTS projects (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL,
	path       TEXT NOT NULL UNIQUE,
	remote     TEXT NOT NULL DEFAULT '',
	state      TEXT NOT NULL DEFAULT 'DISABLED',
	profile    TEXT NOT NULL DEFAULT 'LOCAL_REVIEW',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS tasks (
	id          TEXT PRIMARY KEY,
	project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	title       TEXT NOT NULL DEFAULT '',
	description TEXT NOT NULL,
	priority    TEXT NOT NULL DEFAULT 'NORMAL',
	state       TEXT NOT NULL DEFAULT 'NEW',
	created_at  TEXT NOT NULL,
	updated_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_tasks_project ON tasks (project_id);
CREATE INDEX IF NOT EXISTS idx_tasks_state   ON tasks (state);

CREATE TABLE IF NOT EXISTS task_attachments (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	task_id     TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
	hash        TEXT NOT NULL,
	filename    TEXT NOT NULL,
	mime_type   TEXT NOT NULL,
	size        INTEGER NOT NULL,
	role        TEXT NOT NULL DEFAULT 'GENERAL_CONTEXT',
	created_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_task_attachments_task ON task_attachments (task_id);
CREATE INDEX IF NOT EXISTS idx_task_attachments_hash ON task_attachments (hash);
`,
	},
	{
		Version:     4,
		Description: "create workspaces, checkpoints, leases and continuation_packs tables (M3)",
		Up: `
-- Workspaces: isolated Git worktrees for agent runs (spec §17, ADR-0007).
-- Each workspace is an attempt inside a work package; branch naming follows
-- §17.3. The primary checkout is never recorded here — only the managed
-- worktree path.
CREATE TABLE IF NOT EXISTS workspaces (
	id              TEXT PRIMARY KEY,
	project_id      TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	task_id         TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
	work_package_id TEXT NOT NULL DEFAULT 'main',
	attempt         INTEGER NOT NULL DEFAULT 1,
	kind            TEXT NOT NULL DEFAULT 'attempt',
	path            TEXT NOT NULL,          -- managed worktree filesystem path
	branch          TEXT NOT NULL,          -- forge/<task>/<wp>/attempt-<n>
	result_branch   TEXT NOT NULL DEFAULT '',-- forge/result/<task> (when ready)
	base_sha        TEXT NOT NULL DEFAULT '',
	head_sha        TEXT NOT NULL DEFAULT '',
	result_sha      TEXT NOT NULL DEFAULT '',
	state           TEXT NOT NULL DEFAULT 'pending',
	engine          TEXT NOT NULL DEFAULT '',
	model           TEXT NOT NULL DEFAULT '',
	run_id          TEXT NOT NULL DEFAULT '',
	session_id      TEXT NOT NULL DEFAULT '',
	created_at      TEXT NOT NULL,
	updated_at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_workspaces_project ON workspaces (project_id);
CREATE INDEX IF NOT EXISTS idx_workspaces_task    ON workspaces (task_id);
CREATE INDEX IF NOT EXISTS idx_workspaces_state   ON workspaces (state);

-- Checkpoints: durable records of checkpoint commits (spec §21.3, §5.2).
-- A checkpoint commit lives in the attempt branch and NEVER auto-merges into
-- the user's main branch.
CREATE TABLE IF NOT EXISTS checkpoints (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	workspace_id  TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	commit_sha    TEXT NOT NULL,
	moment        TEXT NOT NULL,  -- plan|first-diff|compile|tests|screenshot|pre-quota-switch|pre-repair|pre-integration|manual
	message       TEXT NOT NULL DEFAULT '',
	created_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_checkpoints_workspace ON checkpoints (workspace_id);

-- Leases: advisory locks on file paths and semantic resources (spec §18.4).
-- Two kinds: 'path' (a repository file/directory) and 'semantic' (one of the
-- §18.4 resource classes). Conflicts block work from starting concurrently.
CREATE TABLE IF NOT EXISTS leases (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	scope         TEXT NOT NULL,  -- 'project' | 'workspace'
	scope_id      TEXT NOT NULL,  -- project id or workspace id
	kind          TEXT NOT NULL,  -- 'path' | 'semantic'
	resource      TEXT NOT NULL,  -- file path or semantic class name
	workspace_id  TEXT NOT NULL DEFAULT '',
	state         TEXT NOT NULL DEFAULT 'active', -- 'active' | 'released'
	created_at    TEXT NOT NULL,
	released_at   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_leases_scope   ON leases (scope, scope_id, state);
CREATE INDEX IF NOT EXISTS idx_leases_resource ON leases (kind, resource, state);

-- Continuation packs: durable references to on-disk continuation-pack files
-- (spec §21.2). Used for provider switching and crash recovery (AC-15, AC-27).
CREATE TABLE IF NOT EXISTS continuation_packs (
	id                INTEGER PRIMARY KEY AUTOINCREMENT,
	workspace_id      TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	file_path         TEXT NOT NULL,
	specification_hash TEXT NOT NULL DEFAULT '',
	base_sha          TEXT NOT NULL DEFAULT '',
	current_sha       TEXT NOT NULL DEFAULT '',
	created_at        TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_packs_workspace ON continuation_packs (workspace_id);
`,
	},
	{
		Version:     5,
		Description: "create post_merge_checks, usage_events and project_memory tables (M12)",
		Up: `
-- Post-merge sentinel results (spec §31, §37, milestone M12). One row per
-- post-merge sentinel run; records the decision, whether a revert happened and
-- the smoke-check outcomes.
CREATE TABLE IF NOT EXISTS post_merge_checks (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	task_id     TEXT NOT NULL,
	commit_sha  TEXT NOT NULL DEFAULT '',
	base_branch TEXT NOT NULL DEFAULT '',
	decision    TEXT NOT NULL,             -- HEALTHY | REVERT | ALERT_ONLY | SKIPPED
	all_passed  INTEGER NOT NULL DEFAULT 0,
	reverted    INTEGER NOT NULL DEFAULT 0,
	revert_sha  TEXT NOT NULL DEFAULT '',
	occurred_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_post_merge_task ON post_merge_checks (task_id);

-- Usage events for token accounting (spec §31, §6.1, §14.4). The quality package
-- aggregates these; they are the durable substrate behind the dashboard totals.
CREATE TABLE IF NOT EXISTS usage_events (
	id                INTEGER PRIMARY KEY AUTOINCREMENT,
	task_id           TEXT NOT NULL DEFAULT '',
	project_id        TEXT NOT NULL DEFAULT '',
	provider          TEXT NOT NULL DEFAULT '',
	model             TEXT NOT NULL DEFAULT '',
	kind              TEXT NOT NULL,           -- coding | image
	input_tokens      INTEGER NOT NULL DEFAULT 0,
	cached_input_tokens INTEGER NOT NULL DEFAULT 0,
	output_tokens     INTEGER NOT NULL DEFAULT 0,
	generations       INTEGER NOT NULL DEFAULT 0,
	cost_usd          REAL NOT NULL DEFAULT 0,
	occurred_at       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_usage_project ON usage_events (project_id, occurred_at);
CREATE INDEX IF NOT EXISTS idx_usage_task    ON usage_events (task_id);

-- Structured project memory (spec §22.9, milestone M12). Keyed by
-- (project_id, category, key) so re-learning a fact updates rather than
-- duplicates.
CREATE TABLE IF NOT EXISTS project_memory (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	project_id  TEXT NOT NULL,
	category    TEXT NOT NULL,
	key         TEXT NOT NULL,
	value       TEXT NOT NULL,
	source      TEXT NOT NULL DEFAULT '',
	confidence  TEXT NOT NULL DEFAULT 'medium',
	scope       TEXT NOT NULL DEFAULT '',
	commit_sha  TEXT NOT NULL DEFAULT '',
	expiration TEXT NOT NULL DEFAULT 'permanent',
	expires_at TEXT NOT NULL DEFAULT '',
	learned_at TEXT NOT NULL,
	version    INTEGER NOT NULL DEFAULT 1,
	UNIQUE (project_id, category, key)
);
CREATE INDEX IF NOT EXISTS idx_memory_project ON project_memory (project_id, category);
`,
	},
	{
		Version:     6,
		Description: "create task_sequences for persistent collision-free task ids (AC: restart/concurrency safety)",
		Up: `
-- Persistent per-project sequence backing task id generation. Without this the
-- task.Backlog held the sequence in an atomic counter that reset to zero on
-- every daemon restart, producing duplicate ids (<project>-1) that failed the
-- tasks.id UNIQUE constraint (blocker: task id collision after restart). The
-- sequence is monotonic and never reused (deleted tasks keep their id; the
-- counter only moves forward) — spec §11.4 durable ids.
CREATE TABLE IF NOT EXISTS task_sequences (
	project_id TEXT PRIMARY KEY,
	next_seq   INTEGER NOT NULL DEFAULT 1
);

-- Backfill each project's next_seq from existing task ids so an existing
-- database migrates without regressing and re-colliding. Only pure-numeric
-- suffixes (the format task.Backlog emits: <project_id>-<seq>) are counted; any
-- non-conforming id contributes 0 and is left untouched. next_seq stores the
-- LAST issued sequence number; NextTaskSeq increments-then-returns, so seeding
-- to the max existing suffix makes the first post-migration task id strictly
-- greater than every existing one (no collision, no gap).
INSERT INTO task_sequences (project_id, next_seq)
SELECT t.project_id,
       COALESCE(MAX(
           CASE WHEN SUBSTR(t.id, LENGTH(t.project_id) + 2) =
                     CAST(CAST(SUBSTR(t.id, LENGTH(t.project_id) + 2) AS INTEGER) AS TEXT)
                THEN CAST(SUBSTR(t.id, LENGTH(t.project_id) + 2) AS INTEGER)
                ELSE 0
           END
       ), 0) AS next_seq
FROM tasks t
GROUP BY t.project_id;
`,
	},
}

// Migrate applies all pending migrations in order. It is idempotent: re-running
// it against an up-to-date database is a no-op. Each migration runs in its own
// transaction; a failure rolls back that migration and aborts.
func (d *DB) Migrate(ctx context.Context) error {
	// Ensure the bookkeeping table exists before we query it. Migration 1 does
	// this too, but we need it present to read applied versions first.
	if _, err := d.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL, description TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("storage: ensure schema_migrations: %w", err)
	}

	applied, err := d.appliedVersions(ctx)
	if err != nil {
		return err
	}

	for _, m := range migrations {
		if _, ok := applied[m.Version]; ok {
			continue
		}
		if err := d.applyMigration(ctx, m); err != nil {
			return fmt.Errorf("storage: migration %d (%s): %w", m.Version, m.Description, err)
		}
		d.logger.Info("storage: migration applied",
			"version", m.Version, "description", m.Description)
	}
	return nil
}

// CurrentVersion returns the highest applied migration version, or 0 if none.
func (d *DB) CurrentVersion(ctx context.Context) (int, error) {
	applied, err := d.appliedVersions(ctx)
	if err != nil {
		return 0, err
	}
	max := 0
	for v := range applied {
		if v > max {
			max = v
		}
	}
	return max, nil
}

func (d *DB) appliedVersions(ctx context.Context) (map[int]bool, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("storage: read schema_migrations: %w", err)
	}
	defer rows.Close()
	out := make(map[int]bool)
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("storage: scan migration version: %w", err)
		}
		out[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: iterate migrations: %w", err)
	}
	return out, nil
}

func (d *DB) applyMigration(ctx context.Context, m Migration) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if m.Up != "" {
		if _, err := tx.ExecContext(ctx, m.Up); err != nil {
			return fmt.Errorf("exec migration: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, applied_at, description) VALUES (?, ?, ?)`,
		m.Version, time.Now().UTC().Format(time.RFC3339Nano), m.Description); err != nil {
		return fmt.Errorf("record migration: %w", err)
	}
	return tx.Commit()
}
