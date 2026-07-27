package storage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// openRaw opens a DB without running migrations, so a test can seed an older
// schema and then exercise the migration forward.
func openRaw(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	db, err := Open(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// migrateUpTo applies migrations 1..target against a fresh DB, simulating a
// database left at an older schema version. It uses the package-private
// migrations slice + applyMigration so the seeded schema is the real one, not a
// hand-rolled approximation.
func migrateUpTo(t *testing.T, db *DB, target int) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL, description TEXT NOT NULL)`); err != nil {
		t.Fatalf("ensure schema_migrations: %v", err)
	}
	for _, m := range migrations {
		if m.Version > target {
			break
		}
		if err := db.applyMigration(ctx, m); err != nil {
			t.Fatalf("apply migration %d: %v", m.Version, err)
		}
	}
}

// TestMigrate_FromV7_AddsSpecificationTables proves migration 8 is additive and
// does not break a database left at the previous (v7) schema: it seeds a v7-era
// database with real data (project, task, audit event), runs the full Migrate,
// and asserts (a) the schema version advanced to 8, (b) the seeded rows survive
// unchanged, and (c) the new tables are present and usable (spec §31, AC:
// migrations do not break existing databases).
func TestMigrate_FromV7_AddsSpecificationTables(t *testing.T) {
	db := openRaw(t)
	ctx := context.Background()
	migrateUpTo(t, db, 7)

	// Seed v7-era data the way the daemon would have: a project, a task, and an
	// audit event.
	seedTime := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.db.ExecContext(ctx, `
INSERT INTO projects (id, name, path, remote, state, profile, created_at, updated_at)
VALUES ('proj-migrate', 'p', '/tmp/p', '', 'DISABLED', 'LOCAL_REVIEW', ?, ?)`,
		seedTime, seedTime); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := db.db.ExecContext(ctx, `
INSERT INTO tasks (id, project_id, title, description, priority, state, created_at, updated_at)
VALUES ('proj-migrate-1', 'proj-migrate', 't', 'desc', 'NORMAL', 'NEW', ?, ?)`,
		seedTime, seedTime); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	if _, err := db.AppendAuditEvent(ctx, AuditEvent{
		Scope: "task", ScopeID: "proj-migrate-1", Type: "task.created", Actor: "daemon", Payload: "{}",
	}); err != nil {
		t.Fatalf("seed audit: %v", err)
	}

	// Run the FULL migration path (the production daemon does this on start).
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("Migrate v7->v8: %v", err)
	}

	v, err := db.CurrentVersion(ctx)
	if err != nil {
		t.Fatalf("CurrentVersion: %v", err)
	}
	if v != migrations[len(migrations)-1].Version {
		t.Fatalf("version = %d, want %d", v, migrations[len(migrations)-1].Version)
	}

	// Seeded data must survive unchanged.
	gotTask, err := db.GetTask(ctx, "proj-migrate-1")
	if err != nil {
		t.Fatalf("GetTask after migrate: %v", err)
	}
	if gotTask.ProjectID != "proj-migrate" || gotTask.Description != "desc" {
		t.Fatalf("seeded task mutated: %+v", gotTask)
	}
	nAudit, err := db.CountAuditEvents(ctx)
	if err != nil {
		t.Fatalf("CountAuditEvents: %v", err)
	}
	if nAudit != 1 {
		t.Fatalf("audit rows = %d, want 1 (seeded data must survive)", nAudit)
	}

	// The new tables must be present AND usable: insert + read a specification.
	if err := db.SaveSpecification(ctx, SpecificationRow{
		TaskID: "proj-migrate-1", Version: 1,
		Objective: "round-trip after migrate",
		Payload:   `{"proposed_scope":["x"]}`,
		CreatedAt: seedTime,
	}, []AcceptanceCriterionRow{
		{AcID: "AC-1", Statement: "works"},
	}); err != nil {
		t.Fatalf("SaveSpecification after migrate: %v", err)
	}
	row, acs, err := db.GetSpecification(ctx, "proj-migrate-1", 1)
	if err != nil {
		t.Fatalf("GetSpecification: %v", err)
	}
	if row.Objective != "round-trip after migrate" || len(acs) != 1 || acs[0].AcID != "AC-1" {
		t.Fatalf("unexpected round-trip: %+v %v", row, acs)
	}
}

// TestMigrate_IdempotentOnV8Schema re-running Migrate against an already-v8
// database is a no-op (restart recovery / backward compatibility).
func TestMigrate_IdempotentOnV8Schema(t *testing.T) {
	db := newTestDB(t) // full migrate
	ctx := context.Background()
	vBefore, _ := db.CurrentVersion(ctx)
	// Seed a spec so we can prove nothing was wiped.
	if err := db.SaveSpecification(ctx, SpecificationRow{
		TaskID: seedTask(t, db), Version: 1, Objective: "persist", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}, []AcceptanceCriterionRow{{AcID: "AC-1", Statement: "s"}}); err != nil {
		t.Fatalf("SaveSpecification: %v", err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("re-Migrate: %v", err)
	}
	vAfter, _ := db.CurrentVersion(ctx)
	if vAfter != vBefore {
		t.Fatalf("version changed on re-migrate: %d -> %d", vBefore, vAfter)
	}
}

// seedTask inserts a minimal task row so a specification's task_id FK is
// satisfied. Returns the task id.
func seedTask(t *testing.T, db *DB) string {
	t.Helper()
	ctx := context.Background()
	pid := fmt.Sprintf("proj-%d", time.Now().UnixNano())
	tn := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.db.ExecContext(ctx, `
INSERT INTO projects (id, name, path, remote, state, profile, created_at, updated_at)
VALUES (?, ?, ?, '', 'DISABLED', 'LOCAL_REVIEW', ?, ?)`,
		pid, pid, "/tmp/"+pid, tn, tn); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	tid := pid + "-1"
	if _, err := db.db.ExecContext(ctx, `
INSERT INTO tasks (id, project_id, title, description, priority, state, created_at, updated_at)
VALUES (?, ?, '', 'd', 'NORMAL', 'NEW', ?, ?)`,
		tid, pid, tn, tn); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	return tid
}

func TestSaveAndGetSpecification_RoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	taskID := seedTask(t, db)

	row := SpecificationRow{
		TaskID: taskID, Version: 1, Objective: "objective text",
		Risk: "R2", Complexity: "C1",
		Payload:   `{"non_goals":["ng"],"assumptions":["a"],"constraints":["c"],"proposed_scope":["ps"],"visual_requirements":{"required":true,"viewport":"390x844","references":["sha256:h"]}}`,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), CreatedBy: "compiler",
	}
	acs := []AcceptanceCriterionRow{
		{TaskID: taskID, Version: 1, AcID: "AC-2", Statement: "second"},
		{TaskID: taskID, Version: 1, AcID: "AC-1", Statement: "first"},
	}
	if err := db.SaveSpecification(ctx, row, acs); err != nil {
		t.Fatalf("SaveSpecification: %v", err)
	}

	gotRow, gotACs, err := db.GetSpecification(ctx, taskID, 1)
	if err != nil {
		t.Fatalf("GetSpecification: %v", err)
	}
	if gotRow.Objective != "objective text" || gotRow.Risk != "R2" || gotRow.Complexity != "C1" {
		t.Fatalf("row not restored: %+v", gotRow)
	}
	if gotRow.CreatedBy != "compiler" {
		t.Fatalf("created_by not restored: %q", gotRow.CreatedBy)
	}
	if gotRow.Payload == "" || !strings.Contains(gotRow.Payload, "non_goals") {
		t.Fatalf("payload not restored: %q", gotRow.Payload)
	}
	if len(gotACs) != 2 {
		t.Fatalf("acs = %d, want 2", len(gotACs))
	}
	// Ordering must be ordinal then ac_id (deterministic, not insertion order).
	if gotACs[0].AcID != "AC-1" || gotACs[1].AcID != "AC-2" {
		t.Fatalf("acs ordering wrong: %+v", gotACs)
	}
	// Stable id preserved exactly.
	if gotACs[0].Statement != "first" {
		t.Fatalf("AC-1 statement wrong: %q", gotACs[0].Statement)
	}
}

func TestSaveSpecification_IdempotentResave(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	taskID := seedTask(t, db)

	row := SpecificationRow{
		TaskID: taskID, Version: 1, Objective: "v1",
		Payload: "{}", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	acs := []AcceptanceCriterionRow{{TaskID: taskID, Version: 1, AcID: "AC-1", Statement: "s"}}
	if err := db.SaveSpecification(ctx, row, acs); err != nil {
		t.Fatalf("first save: %v", err)
	}
	// Re-save with updated content (same version, unlocked): idempotent replace.
	row.Objective = "v1-updated"
	acs = []AcceptanceCriterionRow{
		{TaskID: taskID, Version: 1, AcID: "AC-1", Statement: "s2"},
		{TaskID: taskID, Version: 1, AcID: "AC-2", Statement: "s3"},
	}
	if err := db.SaveSpecification(ctx, row, acs); err != nil {
		t.Fatalf("idempotent re-save: %v", err)
	}
	gotRow, gotACs, err := db.GetSpecification(ctx, taskID, 1)
	if err != nil {
		t.Fatalf("GetSpecification: %v", err)
	}
	if gotRow.Objective != "v1-updated" {
		t.Fatalf("objective not updated: %q", gotRow.Objective)
	}
	if len(gotACs) != 2 {
		t.Fatalf("acs not replaced: %d", len(gotACs))
	}
	// No orphaned AC rows: count must equal exactly the latest set.
	var n int
	if err := db.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM task_acceptance_criteria WHERE task_id = ? AND version = 1`, taskID).Scan(&n); err != nil {
		t.Fatalf("count acs: %v", err)
	}
	if n != 2 {
		t.Fatalf("orphan AC rows: %d", n)
	}
}

func TestSaveSpecification_LockedVersionRejected(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	taskID := seedTask(t, db)

	row := SpecificationRow{
		TaskID: taskID, Version: 1, Objective: "locked",
		Payload: "{}", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	acs := []AcceptanceCriterionRow{{TaskID: taskID, Version: 1, AcID: "AC-1", Statement: "s"}}
	if err := db.SaveSpecification(ctx, row, acs); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := db.LockSpecification(ctx, taskID, 1, "reviewer"); err != nil {
		t.Fatalf("LockSpecification: %v", err)
	}

	// Any subsequent save on the locked version must fail with the locked error
	// (the merge governor's specification_locked gate depends on this, §28).
	row.Objective = "tampered"
	err := db.SaveSpecification(ctx, row, acs)
	if !errors.Is(err, ErrSpecificationLocked) {
		t.Fatalf("expected ErrSpecificationLocked, got %v", err)
	}

	// The stored content must be the ORIGINAL, pre-lock content.
	gotRow, _, err := db.GetSpecification(ctx, taskID, 1)
	if err != nil {
		t.Fatalf("GetSpecification: %v", err)
	}
	if gotRow.Objective != "locked" {
		t.Fatalf("locked version was mutated: %q", gotRow.Objective)
	}
	if !gotRow.Locked {
		t.Fatalf("lock flag lost: %+v", gotRow)
	}
	if gotRow.LockedBy != "reviewer" {
		t.Fatalf("locked_by lost: %q", gotRow.LockedBy)
	}
}

func TestLockSpecification_IdempotentAndNotFound(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	taskID := seedTask(t, db)

	// Locking a non-existent version -> not found.
	if err := db.LockSpecification(ctx, taskID, 7, "x"); !errors.Is(err, ErrSpecificationNotFound) {
		t.Fatalf("expected ErrSpecificationNotFound, got %v", err)
	}

	row := SpecificationRow{
		TaskID: taskID, Version: 1, Objective: "x",
		Payload: "{}", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	acs := []AcceptanceCriterionRow{{TaskID: taskID, Version: 1, AcID: "AC-1", Statement: "s"}}
	if err := db.SaveSpecification(ctx, row, acs); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := db.LockSpecification(ctx, taskID, 1, "first-reviewer"); err != nil {
		t.Fatalf("first lock: %v", err)
	}
	// Lock again by a different actor: idempotent success, original provenance
	// preserved (the lock cannot be silently re-attributed).
	if err := db.LockSpecification(ctx, taskID, 1, "second-reviewer"); err != nil {
		t.Fatalf("idempotent re-lock: %v", err)
	}
	gotRow, _, err := db.GetSpecification(ctx, taskID, 1)
	if err != nil {
		t.Fatalf("GetSpecification: %v", err)
	}
	if gotRow.LockedBy != "first-reviewer" {
		t.Fatalf("locked_by changed on re-lock: %q (provenance not durable)", gotRow.LockedBy)
	}
	if !gotRow.Locked {
		t.Fatalf("not locked after re-lock")
	}
}

func TestNextSpecificationVersion_MonotonicAndRaceFree(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	taskID := seedTask(t, db)

	v1, err := db.NextSpecificationVersion(ctx, taskID)
	if err != nil {
		t.Fatalf("next1: %v", err)
	}
	if v1 != 1 {
		t.Fatalf("first version = %d, want 1", v1)
	}
	// Persist v1 then ask for the next.
	if err := db.SaveSpecification(ctx, SpecificationRow{
		TaskID: taskID, Version: 1, Objective: "x", Payload: "{}",
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}, []AcceptanceCriterionRow{{TaskID: taskID, Version: 1, AcID: "AC-1", Statement: "s"}}); err != nil {
		t.Fatalf("save v1: %v", err)
	}
	v2, err := db.NextSpecificationVersion(ctx, taskID)
	if err != nil {
		t.Fatalf("next2: %v", err)
	}
	if v2 != 2 {
		t.Fatalf("second version = %d, want 2", v2)
	}
}

func TestNextSpecificationVersion_ConcurrentNoCollisions(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	taskID := seedTask(t, db)

	const n = 25
	var wg sync.WaitGroup
	versions := make([]int, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			v, err := db.NextSpecificationVersion(ctx, taskID)
			versions[i] = v
			errs[i] = err
		}()
	}
	close(start)
	wg.Wait()

	seen := make(map[int]bool, n)
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: %v", i, errs[i])
		}
		if versions[i] < 1 {
			t.Fatalf("goroutine %d: bad version %d", i, versions[i])
		}
		if seen[versions[i]] {
			t.Fatalf("duplicate version %d (race condition)", versions[i])
		}
		seen[versions[i]] = true
	}
	// The versions reserved must be a permutation of 1..n (no gaps, no
	// collisions) — the defining property of a race-free version allocator.
	for i := 1; i <= n; i++ {
		if !seen[i] {
			t.Fatalf("version %d never reserved (gap)", i)
		}
	}
}

// TestSaveSpecification_ConcurrentLockedGuard proves that under concurrent
// writers the lock invariant holds: a version that is locked cannot be silently
// rewritten by a racing saver. Half the goroutines try to save the unlocked
// version (these must serialise), then after locking, every concurrent save on
// that version must fail.
func TestSaveSpecification_ConcurrentLockedGuard(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	taskID := seedTask(t, db)

	row := SpecificationRow{
		TaskID: taskID, Version: 1, Objective: "original",
		Payload: "{}", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	acs := []AcceptanceCriterionRow{{TaskID: taskID, Version: 1, AcID: "AC-1", Statement: "s"}}
	if err := db.SaveSpecification(ctx, row, acs); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	if err := db.LockSpecification(ctx, taskID, 1, "reviewer"); err != nil {
		t.Fatalf("lock: %v", err)
	}

	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			tampered := row
			tampered.Objective = fmt.Sprintf("tampered-%d", i)
			errs[i] = db.SaveSpecification(ctx, tampered, acs)
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if !errors.Is(err, ErrSpecificationLocked) {
			t.Fatalf("goroutine %d: expected ErrSpecificationLocked, got %v", i, err)
		}
	}
	// Final state: original content, locked.
	gotRow, _, err := db.GetSpecification(ctx, taskID, 1)
	if err != nil {
		t.Fatalf("GetSpecification: %v", err)
	}
	if gotRow.Objective != "original" {
		t.Fatalf("locked content was mutated under concurrency: %q", gotRow.Objective)
	}
}

func TestGetLatestSpecification_AndListVersions(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	taskID := seedTask(t, db)

	// No spec yet -> not found.
	if _, _, err := db.GetLatestSpecification(ctx, taskID); !errors.Is(err, ErrSpecificationNotFound) {
		t.Fatalf("expected ErrSpecificationNotFound, got %v", err)
	}

	// Persist 3 versions.
	for i := 1; i <= 3; i++ {
		if err := db.SaveSpecification(ctx, SpecificationRow{
			TaskID: taskID, Version: i,
			Objective: fmt.Sprintf("v%d", i),
			Payload:   "{}", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		}, []AcceptanceCriterionRow{{TaskID: taskID, Version: i, AcID: "AC-1", Statement: "s"}}); err != nil {
			t.Fatalf("save v%d: %v", i, err)
		}
	}

	row, _, err := db.GetLatestSpecification(ctx, taskID)
	if err != nil {
		t.Fatalf("GetLatestSpecification: %v", err)
	}
	if row.Version != 3 {
		t.Fatalf("latest = v%d, want 3", row.Version)
	}

	versions, err := db.ListSpecificationVersions(ctx, taskID)
	if err != nil {
		t.Fatalf("ListSpecificationVersions: %v", err)
	}
	if len(versions) != 3 || versions[0] != 1 || versions[2] != 3 {
		t.Fatalf("versions = %v, want [1 2 3]", versions)
	}
}

func TestGetSpecification_NotFound(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	taskID := seedTask(t, db)
	if _, _, err := db.GetSpecification(ctx, taskID, 99); !errors.Is(err, ErrSpecificationNotFound) {
		t.Fatalf("expected ErrSpecificationNotFound, got %v", err)
	}
}

func TestSaveSpecification_ValidationErrors(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := db.SaveSpecification(ctx, SpecificationRow{TaskID: "", Version: 1}, nil); err == nil {
		t.Fatal("expected error for empty task_id")
	}
	if err := db.SaveSpecification(ctx, SpecificationRow{TaskID: "x", Version: 0}, nil); err == nil {
		t.Fatal("expected error for version 0")
	}
}

// TestSpecification_CascadesOnTaskDelete proves the spec rows follow the task:
// deleting the task removes its specifications (ON DELETE CASCADE), so there is
// no dangling durable state after a task is removed.
func TestSpecification_CascadesOnTaskDelete(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	taskID := seedTask(t, db)
	if err := db.SaveSpecification(ctx, SpecificationRow{
		TaskID: taskID, Version: 1, Objective: "x", Payload: "{}",
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}, []AcceptanceCriterionRow{{TaskID: taskID, Version: 1, AcID: "AC-1", Statement: "s"}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := db.db.ExecContext(ctx, `DELETE FROM tasks WHERE id = ?`, taskID); err != nil {
		t.Fatalf("delete task: %v", err)
	}
	if _, _, err := db.GetSpecification(ctx, taskID, 1); !errors.Is(err, ErrSpecificationNotFound) {
		t.Fatalf("expected cascade to remove spec, got %v", err)
	}
}
