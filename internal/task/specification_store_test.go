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

// ---- SaveIfChanged (M14-03 MAJOR-1 concurrency remediation) ----

// TestSaveIfChanged_IdempotentReuse proves the single-threaded idempotency
// contract: calling SaveIfChanged twice with the same semantic content returns
// the same version with created=false on the second call, and no duplicate
// version is persisted.
func TestSaveIfChanged_IdempotentReuse(t *testing.T) {
	store, _, taskID := newSpecStoreDB(t)
	ctx := context.Background()
	spec := fullSpec(taskID)

	saved1, created1, err := store.SaveIfChanged(ctx, spec)
	if err != nil {
		t.Fatalf("SaveIfChanged #1: %v", err)
	}
	if !created1 {
		t.Fatalf("SaveIfChanged #1 created=false, want true (first save)")
	}
	if saved1.Version != 1 {
		t.Fatalf("SaveIfChanged #1 Version=%d, want 1", saved1.Version)
	}

	// Second call with the same semantic content must be idempotent.
	saved2, created2, err := store.SaveIfChanged(ctx, spec)
	if err != nil {
		t.Fatalf("SaveIfChanged #2: %v", err)
	}
	if created2 {
		t.Fatalf("SaveIfChanged #2 created=true, want false (idempotent reuse)")
	}
	if saved2.Version != saved1.Version {
		t.Fatalf("SaveIfChanged #2 Version=%d, want %d", saved2.Version, saved1.Version)
	}

	versions, err := store.ListVersions(ctx, taskID)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(versions) != 1 || versions[0] != 1 {
		t.Fatalf("versions=%v, want [1]", versions)
	}
}

// TestSaveIfChanged_ConcurrentIdenticalCreatesSingleVersion is the core MAJOR-1
// regression test: 40 goroutines fire the SAME semantic specification through
// the production SpecificationStore.SaveIfChanged concurrently against one
// task. Exactly ONE semantic version must be persisted (created count == 1),
// and every non-error response must reference version 1.
//
// This test MUST fail on the original candidate (78d1ff1) where the
// TOCTOU GetLatest→compare→Save produced up to 7 duplicate versions.
func TestSaveIfChanged_ConcurrentIdenticalCreatesSingleVersion(t *testing.T) {
	store, _, taskID := newSpecStoreDB(t)
	ctx := context.Background()

	const n = 40
	spec := fullSpec(taskID)

	errs := make([]error, n)
	results := make([]Specification, n)
	created := make([]bool, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			saved, didCreate, err := store.SaveIfChanged(ctx, spec)
			errs[i] = err
			results[i] = saved
			created[i] = didCreate
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d error: %v", i, err)
		}
	}

	createdCount := 0
	for i := 0; i < n; i++ {
		if created[i] {
			createdCount++
		}
		if results[i].Version != 1 {
			t.Errorf("goroutine %d: Version=%d, want 1", i, results[i].Version)
		}
		if results[i].Objective != spec.Objective {
			t.Errorf("goroutine %d: Objective differs from input", i)
		}
	}
	if createdCount != 1 {
		t.Errorf("created count = %d, want 1 (exactly one goroutine should win)", createdCount)
	}

	versions, err := store.ListVersions(ctx, taskID)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(versions) != 1 || versions[0] != 1 {
		t.Fatalf("persisted versions=%v, want exactly [1]", versions)
	}
}

// TestSaveIfChanged_ConcurrentDifferentTasksDoNotBlock proves the keyed lock
// does not globally serialize unrelated tasks. Two tasks' SaveIfChanged calls
// run concurrently; both complete without error and each persists exactly one
// version. If the lock were global (a single mutex for all tasks), the test
// would still pass functionally — so this test is paired with the daemon-level
// concurrency test (TestSpecAdapter_ConcurrentDifferentTasksDoNotBlockEachOther)
// that uses controlled barriers to prove overlap.
func TestSaveIfChanged_ConcurrentDifferentTasksDoNotBlock(t *testing.T) {
	store, db, _ := newSpecStoreDB(t)
	ctx := context.Background()

	task1 := seedDomainTask(t, db)
	task2 := seedDomainTask(t, db)

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, 2)

	doSave := func(taskID string, idx int) {
		defer wg.Done()
		<-start
		spec := fullSpec(taskID)
		_, _, errs[idx] = store.SaveIfChanged(ctx, spec)
	}

	wg.Add(2)
	go doSave(task1, 0)
	go doSave(task2, 1)
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}

	v1, err := store.ListVersions(ctx, task1)
	if err != nil {
		t.Fatalf("ListVersions task1: %v", err)
	}
	if len(v1) != 1 || v1[0] != 1 {
		t.Fatalf("task1 versions=%v, want [1]", v1)
	}
	v2, err := store.ListVersions(ctx, task2)
	if err != nil {
		t.Fatalf("ListVersions task2: %v", err)
	}
	if len(v2) != 1 || v2[0] != 1 {
		t.Fatalf("task2 versions=%v, want [1]", v2)
	}
}

// TestSaveIfChanged_ChangedInputMintsNewVersion proves that when the semantic
// content differs from the latest, a new version is allocated rather than
// silently overwriting (M14-03 MAJOR-2 B1: the "differs" branch is a defensive
// fallback, not an in-place replace).
func TestSaveIfChanged_ChangedInputMintsNewVersion(t *testing.T) {
	store, _, taskID := newSpecStoreDB(t)
	ctx := context.Background()
	spec := fullSpec(taskID)

	saved1, created1, err := store.SaveIfChanged(ctx, spec)
	if err != nil {
		t.Fatalf("SaveIfChanged #1: %v", err)
	}
	if !created1 {
		t.Fatalf("created1=false, want true")
	}

	// Change the objective (semantic content differs).
	changed := spec
	changed.Objective = "A completely different objective."

	saved2, created2, err := store.SaveIfChanged(ctx, changed)
	if err != nil {
		t.Fatalf("SaveIfChanged #2: %v", err)
	}
	if !created2 {
		t.Fatalf("created2=false, want true (content differs)")
	}
	if saved2.Version != saved1.Version+1 {
		t.Fatalf("changed input Version=%d, want %d (new version)", saved2.Version, saved1.Version+1)
	}

	// Both versions must be durable.
	got1, err := store.Get(ctx, taskID, saved1.Version)
	if err != nil {
		t.Fatalf("Get v%d: %v", saved1.Version, err)
	}
	if got1.Objective != spec.Objective {
		t.Fatalf("v%d objective changed: %q", saved1.Version, got1.Objective)
	}
	got2, err := store.Get(ctx, taskID, saved2.Version)
	if err != nil {
		t.Fatalf("Get v%d: %v", saved2.Version, err)
	}
	if got2.Objective != changed.Objective {
		t.Fatalf("v%d objective not persisted: %q", saved2.Version, got2.Objective)
	}

	versions, err := store.ListVersions(ctx, taskID)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("versions=%v, want 2", versions)
	}
}

// TestSaveIfChanged_IdempotentAfterLock proves that after a version is locked,
// a semantically equal SaveIfChanged still returns that locked version
// unchanged with created=false (no new version, no mutation).
func TestSaveIfChanged_IdempotentAfterLock(t *testing.T) {
	store, _, taskID := newSpecStoreDB(t)
	ctx := context.Background()
	spec := fullSpec(taskID)

	saved1, created1, err := store.SaveIfChanged(ctx, spec)
	if err != nil {
		t.Fatalf("SaveIfChanged #1: %v", err)
	}
	if !created1 {
		t.Fatalf("created1=false, want true")
	}
	if _, err := store.Lock(ctx, taskID, saved1.Version, "reviewer"); err != nil {
		t.Fatalf("Lock: %v", err)
	}

	// SaveIfChanged with the same content after lock must return the locked
	// version unchanged.
	saved2, created2, err := store.SaveIfChanged(ctx, spec)
	if err != nil {
		t.Fatalf("SaveIfChanged after lock: %v", err)
	}
	if created2 {
		t.Fatalf("created2=true after lock, want false (idempotent reuse)")
	}
	if saved2.Version != saved1.Version {
		t.Fatalf("Version after lock =%d, want %d", saved2.Version, saved1.Version)
	}
	if !saved2.Locked {
		t.Fatalf("Locked=false after lock, want true")
	}

	versions, err := store.ListVersions(ctx, taskID)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("versions=%v, want [1]", versions)
	}
}
