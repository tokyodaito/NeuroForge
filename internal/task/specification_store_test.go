package task

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"neuroforge/internal/audit"
	"neuroforge/internal/storage"
)

// newSpecStoreDB opens a migrated DB and seeds one project+task to satisfy the
// task_specifications FK. It returns the store, the DB (for restart tests) and
// the seeded task id.
func newSpecStoreDB(t *testing.T) (*SpecificationStore, *storage.DB, string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	db, err := storage.Open(ctx, path, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	rec := audit.NewRecorder(db, nil)
	taskID := seedDomainTask(t, db)
	store := NewSpecificationStore(db, rec, nil)
	return store, db, taskID
}

func seedDomainTask(t *testing.T, db *storage.DB) string {
	t.Helper()
	ctx := context.Background()
	pid := "proj-" + time.Now().Format("150405.000000")
	tn := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(ctx, `
INSERT INTO projects (id, name, path, remote, state, profile, created_at, updated_at)
VALUES (?, ?, ?, '', 'DISABLED', 'LOCAL_REVIEW', ?, ?)`,
		pid, pid, "/tmp/"+pid, tn, tn); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	tid := pid + "-1"
	if _, err := db.Exec(ctx, `
INSERT INTO tasks (id, project_id, title, description, priority, state, created_at, updated_at)
VALUES (?, ?, '', 'd', 'NORMAL', 'NEW', ?, ?)`,
		tid, pid, tn, tn); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	return tid
}

func fullSpec(taskID string) Specification {
	return Specification{
		TaskID:    taskID,
		Objective: "Add a retry button to the payment failure screen.",
		AcceptanceCriteria: []AcceptanceCriterion{
			{ID: "AC-1", Statement: "A retry button is shown when payment fails."},
			{ID: "AC-2", Statement: "Clicking retry re-submits within 500ms."},
		},
		NonGoals:      []string{"changing the payment API"},
		Assumptions:   []string{"the failure endpoint returns a retriable error"},
		Constraints:   []string{"no new dependencies"},
		Risk:          RiskR1,
		Complexity:    ComplexityC1,
		ProposedScope: []string{"PaymentView.swift", "PaymentViewModel.swift"},
		VisualRequirements: VisualRequirements{
			Required: true, Viewport: "390x844", Theme: "dark", References: []string{"sha256:ref1"},
		},
	}
}

// TestSpecificationStore_SaveGetRoundTrip proves the full domain specification
// — every field, including ACs with stable ids and the structured visual
// requirements — survives a save→get through the production domain service
// (validation + storage + audit wiring).
func TestSpecificationStore_SaveGetRoundTrip(t *testing.T) {
	store, _, taskID := newSpecStoreDB(t)
	ctx := context.Background()

	spec := fullSpec(taskID)
	saved, err := store.Save(ctx, spec)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved.Version != 1 {
		t.Fatalf("first save version = %d, want 1", saved.Version)
	}

	got, err := store.Get(ctx, taskID, 1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Objective != spec.Objective {
		t.Fatalf("objective lost: %q", got.Objective)
	}
	if got.Risk != RiskR1 || got.Complexity != ComplexityC1 {
		t.Fatalf("risk/complexity lost: %s/%s", got.Risk, got.Complexity)
	}
	if len(got.AcceptanceCriteria) != 2 {
		t.Fatalf("acs = %d", len(got.AcceptanceCriteria))
	}
	// Stable ids preserved exactly.
	if got.AcceptanceCriteria[0].ID != "AC-1" || got.AcceptanceCriteria[1].ID != "AC-2" {
		t.Fatalf("ac ids lost: %+v", got.AcceptanceCriteria)
	}
	if len(got.NonGoals) != 1 || got.NonGoals[0] != "changing the payment API" {
		t.Fatalf("non-goals lost: %v", got.NonGoals)
	}
	if len(got.Constraints) != 1 || got.Constraints[0] != "no new dependencies" {
		t.Fatalf("constraints lost: %v", got.Constraints)
	}
	if len(got.ProposedScope) != 2 {
		t.Fatalf("proposed scope lost: %v", got.ProposedScope)
	}
	if !got.VisualRequirements.Required || got.VisualRequirements.Viewport != "390x844" ||
		got.VisualRequirements.Theme != "dark" || len(got.VisualRequirements.References) != 1 {
		t.Fatalf("visual requirements lost: %+v", got.VisualRequirements)
	}
}

// TestSpecificationStore_RejectsInvalidSpec proves validation is wired into the
// production save path (baseline rule 7: a unit test of ValidateSpecification
// alone does not prove the store enforces it).
func TestSpecificationStore_RejectsInvalidSpec(t *testing.T) {
	store, _, taskID := newSpecStoreDB(t)
	ctx := context.Background()

	spec := fullSpec(taskID)
	spec.Objective = "" // invalid
	if _, err := store.Save(ctx, spec); !errors.Is(err, ErrInvalidSpecification) {
		t.Fatalf("expected ErrInvalidSpecification, got %v", err)
	}
	// Nothing persisted.
	if versions, err := store.ListVersions(ctx, taskID); err != nil {
		t.Fatalf("ListVersions: %v", err)
	} else if len(versions) != 0 {
		t.Fatalf("invalid spec was persisted: %v", versions)
	}
}

// TestSpecificationStore_LockedVersionCannotBeMutated is the headline §28
// invariant through the domain service: after Lock, Save on the same version is
// rejected with ErrSpecificationLocked, and a NEW version is allowed (versioning
// still works).
func TestSpecificationStore_LockedVersionCannotBeMutated(t *testing.T) {
	store, _, taskID := newSpecStoreDB(t)
	ctx := context.Background()

	spec := fullSpec(taskID)
	saved, err := store.Save(ctx, spec)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := store.Lock(ctx, taskID, saved.Version, "reviewer-1"); err != nil {
		t.Fatalf("Lock: %v", err)
	}

	// Mutating the locked version must fail.
	tampered := spec
	tampered.Version = saved.Version
	tampered.Objective = "tampered objective"
	if _, err := store.Save(ctx, tampered); !errors.Is(err, ErrSpecificationLocked) {
		t.Fatalf("expected ErrSpecificationLocked on locked version, got %v", err)
	}

	// The locked content must be intact.
	got, err := store.Get(ctx, taskID, saved.Version)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Objective != spec.Objective {
		t.Fatalf("locked version mutated: %q", got.Objective)
	}
	if !got.Locked || got.LockedBy != "reviewer-1" {
		t.Fatalf("lock provenance lost: locked=%v by=%q", got.Locked, got.LockedBy)
	}

	// A brand-new version (Version=0) is still allowed — locking freezes a
	// version, not the task.
	evolved := spec
	evolved.Version = 0
	evolved.Objective = "evolved objective"
	v2, err := store.Save(ctx, evolved)
	if err != nil {
		t.Fatalf("Save new version after lock: %v", err)
	}
	if v2.Version != 2 {
		t.Fatalf("new version = %d, want 2", v2.Version)
	}
	versions, _ := store.ListVersions(ctx, taskID)
	if len(versions) != 2 {
		t.Fatalf("versions = %v, want 2 entries", versions)
	}
}

// TestSpecificationStore_RestartRecovery is the headline durability AC: the
// full specification is saved, the DB is closed and reopened (simulating a
// daemon restart), the schema is re-migrated (idempotent), and the
// specification is fully restored — including AC ids, lock state, and every
// structured field.
func TestSpecificationStore_RestartRecovery(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")

	openAndMigrate := func() *storage.DB {
		db, err := storage.Open(ctx, path, nil)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if err := db.Migrate(ctx); err != nil {
			t.Fatalf("Migrate: %v", err)
		}
		return db
	}

	// First "daemon lifetime": create, save, lock.
	db1 := openAndMigrate()
	taskID := seedDomainTask(t, db1)
	rec1 := audit.NewRecorder(db1, nil)
	store1 := NewSpecificationStore(db1, rec1, nil)
	spec := fullSpec(taskID)
	saved, err := store1.Save(ctx, spec)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := store1.Lock(ctx, taskID, saved.Version, "pre-restart-reviewer"); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if err := db1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// "Restart": reopen the same DB file and re-migrate.
	db2 := openAndMigrate()
	defer db2.Close()
	rec2 := audit.NewRecorder(db2, nil)
	store2 := NewSpecificationStore(db2, rec2, nil)

	// Everything must be restored.
	got, err := store2.Get(ctx, taskID, saved.Version)
	if err != nil {
		t.Fatalf("Get after restart: %v", err)
	}
	if got.Objective != spec.Objective {
		t.Fatalf("objective not restored: %q", got.Objective)
	}
	if len(got.AcceptanceCriteria) != 2 ||
		got.AcceptanceCriteria[0].ID != "AC-1" || got.AcceptanceCriteria[1].ID != "AC-2" {
		t.Fatalf("ACs/ids not restored: %+v", got.AcceptanceCriteria)
	}
	if len(got.NonGoals) != 1 || len(got.Constraints) != 1 || len(got.ProposedScope) != 2 {
		t.Fatalf("list fields not restored: ng=%d c=%d ps=%d",
			len(got.NonGoals), len(got.Constraints), len(got.ProposedScope))
	}
	if !got.VisualRequirements.Required || len(got.VisualRequirements.References) != 1 {
		t.Fatalf("visual requirements not restored: %+v", got.VisualRequirements)
	}
	if !got.Locked || got.LockedBy != "pre-restart-reviewer" {
		t.Fatalf("lock state not restored: locked=%v by=%q", got.Locked, got.LockedBy)
	}

	// The post-restart store must still refuse to mutate the restored locked
	// version (the lock survives restart, §28).
	tampered := spec
	tampered.Version = saved.Version
	tampered.Objective = "post-restart tamper"
	if _, err := store2.Save(ctx, tampered); !errors.Is(err, ErrSpecificationLocked) {
		t.Fatalf("post-restart locked-version save: expected ErrSpecificationLocked, got %v", err)
	}
}

// TestSpecificationStore_AuditRecorded proves the store writes audit events for
// both save and lock, atomically with the storage mutation (spec §29.4, §11.4).
func TestSpecificationStore_AuditRecorded(t *testing.T) {
	store, db, taskID := newSpecStoreDB(t)
	ctx := context.Background()
	rec := audit.NewRecorder(db, nil)
	store.audit = rec

	spec := fullSpec(taskID)
	if _, err := store.Save(ctx, spec); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := store.Lock(ctx, taskID, 1, "reviewer"); err != nil {
		t.Fatalf("Lock: %v", err)
	}

	events, err := rec.Filter(ctx, storage.AuditFilter{ScopeID: taskID})
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	var sawSave, sawLock bool
	for _, e := range events {
		if e.Type == "task.specification.saved" {
			sawSave = true
		}
		if e.Type == "task.specification.locked" {
			sawLock = true
		}
	}
	if !sawSave {
		t.Errorf("missing task.specification.saved audit event")
	}
	if !sawLock {
		t.Errorf("missing task.specification.locked audit event")
	}
}

// TestSpecificationStore_ConcurrentNewVersionsNoCollisions is the production
// concurrency test: N goroutines each Save a NEW version (Version=0) of the
// same task concurrently through the domain store. The race-free version
// allocator must hand out distinct versions 1..N with no collisions and no
// gaps, and all N specs must be durable.
func TestSpecificationStore_ConcurrentNewVersionsNoCollisions(t *testing.T) {
	store, _, taskID := newSpecStoreDB(t)
	ctx := context.Background()

	const n = 20
	errs := make([]error, n)
	versions := make([]int, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		i := i
		spec := fullSpec(taskID)
		spec.Objective = "concurrent-" + itoa(i)
		spec.Version = 0
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			saved, err := store.Save(ctx, spec)
			versions[i] = saved.Version
			errs[i] = err
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	seen := make(map[int]bool, n)
	for _, v := range versions {
		if v < 1 || v > n {
			t.Fatalf("version %d out of range [1,%d]", v, n)
		}
		if seen[v] {
			t.Fatalf("duplicate version %d (collision)", v)
		}
		seen[v] = true
	}
	for i := 1; i <= n; i++ {
		if !seen[i] {
			t.Fatalf("version %d missing (gap)", i)
		}
	}
	// All N specs must be durable and restorable.
	gotVersions, err := store.ListVersions(ctx, taskID)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(gotVersions) != n {
		t.Fatalf("durable versions = %d, want %d", len(gotVersions), n)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
