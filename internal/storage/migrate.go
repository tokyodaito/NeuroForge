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
	{
		Version:     7,
		Description: "create finalize_intents for crash-consistent run finalization (BF-07)",
		Up: `
-- Finalize intents: durable finalization protocol state (BF-07).
-- Git refs and SQLite cannot share one physical transaction; recovery after a
-- crash between "create result ref" and "commit terminal DB state" is driven
-- by this intent row. Phases:
--   pending   — classification recorded; result ref not yet ensured
--   ref_ready — result ref created/verified at expected_sha (or N/A)
--   (row deleted on successful terminal commit)
CREATE TABLE IF NOT EXISTS finalize_intents (
	workspace_id      TEXT PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
	task_id           TEXT NOT NULL,
	outcome           TEXT NOT NULL,
	run_terminal      TEXT NOT NULL DEFAULT '',
	run_id            TEXT NOT NULL DEFAULT '',
	engine            TEXT NOT NULL DEFAULT '',
	model             TEXT NOT NULL DEFAULT '',
	base_sha          TEXT NOT NULL DEFAULT '',
	actual_head_sha   TEXT NOT NULL DEFAULT '',
	expected_ref_sha  TEXT NOT NULL DEFAULT '',
	result_branch     TEXT NOT NULL DEFAULT '',
	commit_sha        TEXT NOT NULL DEFAULT '',
	git_status_empty  INTEGER NOT NULL DEFAULT 1,
	changed_files     TEXT NOT NULL DEFAULT '[]',
	target_ws_state   TEXT NOT NULL DEFAULT '',
	target_task_state TEXT NOT NULL DEFAULT '',
	phase             TEXT NOT NULL DEFAULT 'pending',
	created_at        TEXT NOT NULL,
	updated_at        TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_finalize_intents_phase ON finalize_intents (phase);
`,
	},
	{
		Version:     8,
		Description: "create task_specifications and task_acceptance_criteria tables (M14-01, compiled task specification)",
		Up: `
-- Compiled task specifications (spec §18.1, §9, milestone M14-01).
-- One row per (task_id, version): the structured specification produced by the
-- task compiler. Versions are immutable once locked (specification_locked is a
-- Merge Governor gate, §28). The compiler itself lands in a later milestone;
-- this table is the durable, versioned substrate.
CREATE TABLE IF NOT EXISTS task_specifications (
	task_id     TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
	version     INTEGER NOT NULL,
	objective   TEXT NOT NULL DEFAULT '',
	risk        TEXT NOT NULL DEFAULT '',     -- R0..R4 (spec §26)
	complexity  TEXT NOT NULL DEFAULT '',     -- C0..C3
	payload     TEXT NOT NULL DEFAULT '{}',   -- JSON: non_goals, assumptions, constraints, proposed_scope, visual_requirements
	locked      INTEGER NOT NULL DEFAULT 0,
	locked_at   TEXT NOT NULL DEFAULT '',
	locked_by   TEXT NOT NULL DEFAULT '',
	created_at  TEXT NOT NULL,
	created_by  TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (task_id, version)
);
CREATE INDEX IF NOT EXISTS idx_task_specifications_task ON task_specifications (task_id);

-- Acceptance criteria with stable ids (spec §27, §18.1). Each criterion has a
-- durable, human-stable identifier (e.g. "AC-1") stored in its own column — not
-- a positional list — so the id survives reordering, partial updates and
-- round-trips through SQLite. Scoped to (task_id, version): a new version is a
-- new snapshot.
CREATE TABLE IF NOT EXISTS task_acceptance_criteria (
	task_id    TEXT NOT NULL,
	version    INTEGER NOT NULL,
	ac_id      TEXT NOT NULL,
	statement  TEXT NOT NULL,
	ordinal    INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (task_id, version, ac_id)
);
CREATE INDEX IF NOT EXISTS idx_task_ac_task_version ON task_acceptance_criteria (task_id, version);

-- Per-task monotonic version counter for compiled specifications. Mirrors
-- task_sequences: reserving a version is an atomic UPSERT-increment so two
-- concurrent compilers/daemons cannot receive the same version number
-- (race-free version allocation under SQLite's single-writer serialisation,
-- spec §11.4). Versions are never reused.
CREATE TABLE IF NOT EXISTS task_specification_sequences (
	task_id       TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
	next_version  INTEGER NOT NULL DEFAULT 1
);
`,
	},
	{
		Version:     9,
		Description: "create work_packages, work_package_dependencies, work_package_attempts tables and add leases.expires_at (M14-05, durable work graph)",
		Up: `
-- Durable Work Graph (spec §18.3, §31, milestone M14-05). One row per work
-- package, keyed by (task_id, package_id). The graph is reconstructed by
-- loading every package for a task plus its dependency rows. AC ownership and
-- allowed scope are JSON columns: they are advisory inputs to the lease layer,
-- not query keys, so normalising them into rows would add complexity without
-- value. Attempts are stored as a JSON column on the package for the same
-- reason (history records, never queried individually).
--
-- Only a *workgraph.ValidatedWorkGraph can be persisted (enforced by the
-- domain store, not the schema), so an invalid DAG cannot become durable
-- runnable state (M14-04 AC2 carried forward).
CREATE TABLE IF NOT EXISTS work_packages (
	task_id          TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
	package_id       TEXT NOT NULL,
	stage            TEXT NOT NULL,
	title            TEXT NOT NULL DEFAULT '',
	objective        TEXT NOT NULL,
	accepted_ac_ids  TEXT NOT NULL DEFAULT '[]',  -- JSON array of stable AC ids
	allowed_scope    TEXT NOT NULL DEFAULT '[]',  -- JSON array of path prefixes
	dependencies     TEXT NOT NULL DEFAULT '[]',  -- JSON array of package ids (denormalised for atomic save)
	state            TEXT NOT NULL DEFAULT 'pending',
	attempts         TEXT NOT NULL DEFAULT '[]',  -- JSON array of Attempt records
	graph_version    INTEGER NOT NULL DEFAULT 1,  -- bumped on each replace
	created_at       TEXT NOT NULL,
	updated_at       TEXT NOT NULL,
	PRIMARY KEY (task_id, package_id)
);
CREATE INDEX IF NOT EXISTS idx_work_packages_task  ON work_packages (task_id);
CREATE INDEX IF NOT EXISTS idx_work_packages_state ON work_packages (state);

-- Dependency edges (spec §18.3 DAG). Stored both as a denormalised JSON column
-- on work_packages (for atomic save/load) and as this normalised table (for
-- future graph-walk queries without a JSON parse). The two are kept in sync by
-- WorkGraphStore within a single transaction.
CREATE TABLE IF NOT EXISTS work_package_dependencies (
	task_id     TEXT NOT NULL,
	package_id  TEXT NOT NULL,
	depends_on  TEXT NOT NULL,
	PRIMARY KEY (task_id, package_id, depends_on)
);
CREATE INDEX IF NOT EXISTS idx_work_package_deps_pkg ON work_package_dependencies (task_id, package_id);
CREATE INDEX IF NOT EXISTS idx_work_package_deps_dep ON work_package_dependencies (task_id, depends_on);

-- Per-package attempt history (spec §31 "attempts" table). Mirrors the
-- WorkPackage.Attempts slice as rows so individual attempts are queryable
-- independently of the package row. package_attempts_seq is the attempt index
-- within the package (0-based, monotonic).
CREATE TABLE IF NOT EXISTS work_package_attempts (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	task_id       TEXT NOT NULL,
	package_id    TEXT NOT NULL,
	attempt_index INTEGER NOT NULL,
	state         TEXT NOT NULL,
	started_at    TEXT NOT NULL,
	finished_at   TEXT NOT NULL DEFAULT '',
	failure_reason TEXT NOT NULL DEFAULT '',
	exit_code     INTEGER NOT NULL DEFAULT 0,
	agent_run_id  TEXT NOT NULL DEFAULT '',
	UNIQUE (task_id, package_id, attempt_index)
);
CREATE INDEX IF NOT EXISTS idx_work_package_attempts_pkg ON work_package_attempts (task_id, package_id, attempt_index);

-- Forward-compatible TTL column for leases (M14-05). Empty string means "no
-- expiry" (perpetual until explicitly released), preserving the M3 contract for
-- pre-existing rows. Non-empty RFC3339Nano timestamp means the lease is
-- logically expired once that time has passed; the row stays state='active'
-- until ExpireLeases sweeps it to 'expired', so the expiry transition is
-- auditable rather than silent.
ALTER TABLE leases ADD COLUMN expires_at TEXT NOT NULL DEFAULT '';

-- Active-resource uniqueness (M14-05 concurrent-claim race fix). At most one
-- ACTIVE lease may exist per (scope, scope_id, kind, resource): the SELECT-
-- then-INSERT pattern in LeaseManager.acquire is otherwise racy under
-- concurrent claims (two callers can both pass the conflict SELECT and both
-- succeed at the INSERT). SQLite's partial UNIQUE index plus its single-writer
-- serialisation make the INSERT the linearisation point — the second writer
-- blocks on busy_timeout, then receives a UNIQUE violation that
-- LeaseManager.acquire maps to a typed ConflictError. The "WHERE state =
-- 'active'" predicate keeps historical rows (released / expired) unconstrained
-- so the audit/history trail stays append-only.
CREATE UNIQUE INDEX IF NOT EXISTS idx_leases_unique_active_resource
ON leases (scope, scope_id, kind, resource) WHERE state = 'active';
`,
	},
	{
		Version:     10,
		Description: "create pipeline_runs, pipeline_stage_records and control_flags tables (M14-06, durable pipeline stage state machine)",
		Up: `
-- Durable pipeline runs (milestone M14-06). One row per task pipeline
-- execution: the task_id primary key enforces "at most one pipeline run per
-- task" at the storage layer. current_stage is the stage cursor the driver
-- resumes from after a restart; run_state distinguishes actively-driven runs
-- from the non-terminal wait states (waiting_quota, blocked) and the terminal
-- outcomes (completed, failed, cancelled, repair_exhausted). stage_attempt is
-- the attempt counter for the current stage (bumped on re-entry, e.g. the
-- repair loop re-entering execute); repair_attempt counts repair cycles and is
-- capped by max_repair_attempts before the run is declared repair_exhausted.
CREATE TABLE IF NOT EXISTS pipeline_runs (
	task_id             TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
	project_id          TEXT NOT NULL,
	current_stage       TEXT NOT NULL,
	run_state           TEXT NOT NULL DEFAULT 'active',
	stage_attempt       INTEGER NOT NULL DEFAULT 1,
	repair_attempt      INTEGER NOT NULL DEFAULT 0,
	max_repair_attempts INTEGER NOT NULL DEFAULT 3,
	failure_category    TEXT,
	failure_reason      TEXT,
	result_ref          TEXT,
	created_at          TEXT NOT NULL,
	updated_at          TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_pipeline_runs_state   ON pipeline_runs (run_state);
CREATE INDEX IF NOT EXISTS idx_pipeline_runs_project ON pipeline_runs (project_id);

-- Append-only stage history. Every stage entry and outcome is a row; the
-- UNIQUE(task_id, stage, attempt, status) constraint makes re-entering a
-- stage after a crash/restart idempotent at the database level (the driver
-- can blindly re-INSERT its "entered" record and rely on the conflict being
-- absorbed). finished_at on the 'entered' row is set when the stage reaches
-- an outcome (completed / failed / skipped) or is swept by the startup
-- reconciler (interrupted).
CREATE TABLE IF NOT EXISTS pipeline_stage_records (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	task_id          TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
	stage            TEXT NOT NULL,
	attempt          INTEGER NOT NULL,
	status           TEXT NOT NULL,  -- entered | completed | failed | skipped
	failure_category TEXT,
	reason           TEXT NOT NULL DEFAULT '',
	evidence_ref     TEXT,
	entered_at       TEXT NOT NULL,
	finished_at      TEXT,
	UNIQUE (task_id, stage, attempt, status)
);
CREATE INDEX IF NOT EXISTS idx_pipeline_stage_records_task ON pipeline_stage_records (task_id, stage, attempt);

-- Keyed control flags (M14-06). Holds the persisted emergency-stop flag so
-- the pipeline driver can cheaply check "am I allowed to start ANY stage"
-- before every stage dispatch, surviving restarts.
CREATE TABLE IF NOT EXISTS control_flags (
	key        TEXT PRIMARY KEY,
	value      TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
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
