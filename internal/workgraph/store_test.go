package workgraph_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"neuroforge/internal/storage"
	"neuroforge/internal/task"
	"neuroforge/internal/workgraph"
)

// setupWGDB mirrors the lease-test DB helper but also inserts a project and a
// task so the work_packages FK is satisfied. Returns the DB and the task ID.
func setupWGDB(t *testing.T) (*storage.DB, string) {
	t.Helper()
	home := t.TempDir()
	db, err := storage.Open(context.Background(), home+"/state.db", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := db.CreateProject(context.Background(), storage.Project{
		ID: "proj", Name: "T", Path: t.TempDir(), State: "IDLE",
		Profile: "LOCAL_REVIEW", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(context.Background(), storage.Task{
		ID: "T-1", ProjectID: "proj", Title: "demo", Description: "d",
		Priority: "NORMAL", State: "NEW", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	return db, "T-1"
}

// helperValidatedGraph builds a small valid graph for task T-1:
//
//	T-1-AC-1 (AllowedScope: [src/a.go])
//	  → T-1-AC-2 (AllowedScope: [src/b.go, src/shared.go])
//
// AC-1's package has no dependencies (ready by default); AC-2's depends on
// AC-1's.
func helperValidatedGraph(t *testing.T, taskID string) *workgraph.ValidatedWorkGraph {
	t.Helper()
	p1 := workgraph.WorkPackage{
		ID: taskID + "-AC-1", TaskID: taskID, Stage: workgraph.StageImplementation,
		Title: "Implement AC-1", Objective: "obj",
		AcceptedACIDs: []string{"AC-1"}, AllowedScope: []string{"src/a.go"},
		State: workgraph.PackagePending,
	}
	p2 := workgraph.WorkPackage{
		ID: taskID + "-AC-2", TaskID: taskID, Stage: workgraph.StageImplementation,
		Title: "Implement AC-2", Objective: "obj",
		AcceptedACIDs: []string{"AC-2"}, AllowedScope: []string{"src/b.go", "src/shared.go"},
		Dependencies: []string{taskID + "-AC-1"},
		State:        workgraph.PackagePending,
	}
	v, err := workgraph.ValidateWorkGraph(workgraph.WorkGraph{
		TaskID: taskID, Packages: []workgraph.WorkPackage{p1, p2},
	})
	if err != nil {
		t.Fatalf("ValidateWorkGraph: %v", err)
	}
	return v
}

// ---- WorkGraphStore tests ----

func TestWorkGraphStore_SaveAndLoad(t *testing.T) {
	db, taskID := setupWGDB(t)
	store := workgraph.NewWorkGraphStore(db, nil, nil)
	ctx := context.Background()

	v := helperValidatedGraph(t, taskID)
	if _, err := store.Save(ctx, v); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := store.LoadValidated(ctx, taskID)
	if err != nil {
		t.Fatalf("LoadValidated: %v", err)
	}
	if loaded.TaskID() != taskID {
		t.Errorf("TaskID = %q want %q", loaded.TaskID(), taskID)
	}
	pkgs := loaded.Packages()
	if len(pkgs) != 2 {
		t.Fatalf("packages = %d want 2", len(pkgs))
	}
	// Canonical (sorted-by-ID) order: AC-1 before AC-2.
	if pkgs[0].ID != taskID+"-AC-1" || pkgs[1].ID != taskID+"-AC-2" {
		t.Fatalf("package order = %q, %q", pkgs[0].ID, pkgs[1].ID)
	}
	// Dependencies survive the round-trip.
	if len(pkgs[1].Dependencies) != 1 || pkgs[1].Dependencies[0] != taskID+"-AC-1" {
		t.Fatalf("AC-2 dependencies lost: %v", pkgs[1].Dependencies)
	}
	// AllowedScope survives the round-trip (order preserved).
	if fmt.Sprint(pkgs[1].AllowedScope) != "[src/b.go src/shared.go]" {
		t.Fatalf("AC-2 allowed scope lost: %v", pkgs[1].AllowedScope)
	}
}

func TestWorkGraphStore_SaveRejectsNil(t *testing.T) {
	db, _ := setupWGDB(t)
	store := workgraph.NewWorkGraphStore(db, nil, nil)
	if _, err := store.Save(context.Background(), nil); err == nil {
		t.Fatal("Save(nil) must error")
	}
}

func TestWorkGraphStore_SaveIsIdempotentAndPreservesAttempts(t *testing.T) {
	db, taskID := setupWGDB(t)
	store := workgraph.NewWorkGraphStore(db, nil, nil)
	ctx := context.Background()
	v := helperValidatedGraph(t, taskID)

	if _, err := store.Save(ctx, v); err != nil {
		t.Fatalf("Save #1: %v", err)
	}
	pkgID := taskID + "-AC-1"

	// Append an attempt and transition state, the durable "execution history".
	if err := store.AppendAttempt(ctx, taskID, pkgID, workgraph.Attempt{
		State: workgraph.PackageSucceeded, FailureReason: "",
	}); err != nil {
		t.Fatalf("AppendAttempt: %v", err)
	}
	if err := store.TransitionPackage(ctx, taskID, pkgID, workgraph.PackageSucceeded); err != nil {
		t.Fatalf("TransitionPackage: %v", err)
	}

	// Re-save the original validated graph (the in-memory v has no attempts
	// because helperValidatedGraph built it that way). The store MUST preserve
	// the persisted attempts and state.
	if _, err := store.Save(ctx, v); err != nil {
		t.Fatalf("Save #2 (re-save): %v", err)
	}
	loaded, err := store.LoadValidated(ctx, taskID)
	if err != nil {
		t.Fatalf("LoadValidated: %v", err)
	}
	p1, ok := findPackage(loaded.Packages(), pkgID)
	if !ok {
		t.Fatalf("package %s missing after re-save", pkgID)
	}
	if p1.State != workgraph.PackageSucceeded {
		t.Errorf("state lost on re-save: got %q want succeeded", p1.State)
	}
	if len(p1.Attempts) != 1 {
		t.Errorf("attempts lost on re-save: got %d want 1", len(p1.Attempts))
	}
}

func TestWorkGraphStore_LoadMissingTask(t *testing.T) {
	db, _ := setupWGDB(t)
	store := workgraph.NewWorkGraphStore(db, nil, nil)
	_, err := store.LoadValidated(context.Background(), "no-such-task")
	if !errors.Is(err, workgraph.ErrWorkGraphNotFound) {
		t.Fatalf("expected ErrWorkGraphNotFound, got %v", err)
	}
}

func TestWorkGraphStore_GetPackage(t *testing.T) {
	db, taskID := setupWGDB(t)
	store := workgraph.NewWorkGraphStore(db, nil, nil)
	ctx := context.Background()
	v := helperValidatedGraph(t, taskID)
	if _, err := store.Save(ctx, v); err != nil {
		t.Fatalf("Save: %v", err)
	}
	p, err := store.GetPackage(ctx, taskID, taskID+"-AC-2")
	if err != nil {
		t.Fatalf("GetPackage: %v", err)
	}
	if p.ID != taskID+"-AC-2" {
		t.Errorf("got package %q", p.ID)
	}
	if _, err := store.GetPackage(ctx, taskID, "nope"); !errors.Is(err, workgraph.ErrWorkPackageNotFound) {
		t.Fatalf("expected ErrWorkPackageNotFound, got %v", err)
	}
}

func TestWorkGraphStore_TransitionPackage(t *testing.T) {
	db, taskID := setupWGDB(t)
	store := workgraph.NewWorkGraphStore(db, nil, nil)
	ctx := context.Background()
	v := helperValidatedGraph(t, taskID)
	if _, err := store.Save(ctx, v); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.TransitionPackage(ctx, taskID, taskID+"-AC-1", workgraph.PackageRunning); err != nil {
		t.Fatalf("TransitionPackage: %v", err)
	}
	p, _ := store.GetPackage(ctx, taskID, taskID+"-AC-1")
	if p.State != workgraph.PackageRunning {
		t.Fatalf("state = %q want running", p.State)
	}
	// Missing package surfaces ErrWorkPackageNotFound.
	if err := store.TransitionPackage(ctx, taskID, "nope", workgraph.PackageRunning); !errors.Is(err, workgraph.ErrWorkPackageNotFound) {
		t.Fatalf("expected ErrWorkPackageNotFound, got %v", err)
	}
	// Invalid state surfaces a plain error (not the not-found sentinel).
	if err := store.TransitionPackage(ctx, taskID, taskID+"-AC-1", workgraph.PackageState("bogus")); err == nil {
		t.Fatal("expected error for invalid state")
	}
}

func TestWorkGraphStore_ReplaceGraphPrunesRemovedPackages(t *testing.T) {
	db, taskID := setupWGDB(t)
	store := workgraph.NewWorkGraphStore(db, nil, nil)
	ctx := context.Background()
	v := helperValidatedGraph(t, taskID)
	if _, err := store.Save(ctx, v); err != nil {
		t.Fatalf("Save #1: %v", err)
	}

	// Now save a smaller graph (drop AC-2). The store must prune it.
	p1 := workgraph.WorkPackage{
		ID: taskID + "-AC-1", TaskID: taskID, Stage: workgraph.StageImplementation,
		Title: "Implement AC-1", Objective: "obj",
		AcceptedACIDs: []string{"AC-1"}, AllowedScope: []string{"src/a.go"},
		State: workgraph.PackagePending,
	}
	smaller, err := workgraph.ValidateWorkGraph(workgraph.WorkGraph{
		TaskID: taskID, Packages: []workgraph.WorkPackage{p1},
	})
	if err != nil {
		t.Fatalf("ValidateWorkGraph: %v", err)
	}
	if _, err := store.Save(ctx, smaller); err != nil {
		t.Fatalf("Save #2: %v", err)
	}
	loaded, err := store.LoadValidated(ctx, taskID)
	if err != nil {
		t.Fatalf("LoadValidated: %v", err)
	}
	if len(loaded.Packages()) != 1 {
		t.Fatalf("packages = %d want 1 (AC-2 should have been pruned)", len(loaded.Packages()))
	}
}

func findPackage(pkgs []workgraph.WorkPackage, id string) (workgraph.WorkPackage, bool) {
	for _, p := range pkgs {
		if p.ID == id {
			return p, true
		}
	}
	return workgraph.WorkPackage{}, false
}

// ---- Readiness tests (mandatory AC: "Package not runnable until completion dependencies") ----

func TestReadiness_BlockedByDependency(t *testing.T) {
	db, taskID := setupWGDB(t)
	store := workgraph.NewWorkGraphStore(db, nil, nil)
	ctx := context.Background()
	v := helperValidatedGraph(t, taskID)
	if _, err := store.Save(ctx, v); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Nothing has run yet: AC-1 ready, AC-2 blocked by AC-1.
	loaded, err := store.LoadValidated(ctx, taskID)
	if err != nil {
		t.Fatalf("LoadValidated: %v", err)
	}
	verdicts := workgraph.ComputeReadiness(loaded, nil, time.Now(), "")
	if len(verdicts) != 2 {
		t.Fatalf("verdicts = %d want 2", len(verdicts))
	}
	byID := map[string]workgraph.Readiness{}
	for _, r := range verdicts {
		byID[r.PackageID] = r
	}
	if !byID[taskID+"-AC-1"].Ready {
		t.Errorf("AC-1 should be ready (no deps): %+v", byID[taskID+"-AC-1"])
	}
	if byID[taskID+"-AC-2"].Ready {
		t.Errorf("AC-2 should be blocked (dep AC-1 not succeeded)")
	}
	if !byID[taskID+"-AC-2"].HasReason("not succeeded") {
		t.Errorf("AC-2 should report dependency-not-succeeded reason: %+v", byID[taskID+"-AC-2"])
	}
}

func TestReadiness_UnblocksAfterDependencySucceeds(t *testing.T) {
	db, taskID := setupWGDB(t)
	store := workgraph.NewWorkGraphStore(db, nil, nil)
	ctx := context.Background()
	v := helperValidatedGraph(t, taskID)
	if _, err := store.Save(ctx, v); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Mark AC-1 succeeded in storage. AC-2 should now be ready.
	if err := store.TransitionPackage(ctx, taskID, taskID+"-AC-1", workgraph.PackageSucceeded); err != nil {
		t.Fatalf("TransitionPackage: %v", err)
	}
	loaded, _ := store.LoadValidated(ctx, taskID)
	verdicts := workgraph.ComputeReadiness(loaded, nil, time.Now(), "")
	byID := map[string]workgraph.Readiness{}
	for _, r := range verdicts {
		byID[r.PackageID] = r
	}
	if !byID[taskID+"-AC-2"].Ready {
		t.Errorf("AC-2 should be ready after AC-1 succeeded: %+v", byID[taskID+"-AC-2"])
	}
}

func TestReadiness_TerminalStateNotReady(t *testing.T) {
	db, taskID := setupWGDB(t)
	store := workgraph.NewWorkGraphStore(db, nil, nil)
	ctx := context.Background()
	v := helperValidatedGraph(t, taskID)
	if _, err := store.Save(ctx, v); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.TransitionPackage(ctx, taskID, taskID+"-AC-1", workgraph.PackageFailed); err != nil {
		t.Fatalf("TransitionPackage: %v", err)
	}
	loaded, _ := store.LoadValidated(ctx, taskID)
	verdicts := workgraph.ComputeReadiness(loaded, nil, time.Now(), "")
	for _, r := range verdicts {
		if r.PackageID == taskID+"-AC-1" && r.Ready {
			t.Errorf("AC-1 in failed state must not be ready: %+v", r)
		}
	}
}

func TestReadiness_BlockedByPathLease(t *testing.T) {
	db, taskID := setupWGDB(t)
	store := workgraph.NewWorkGraphStore(db, nil, nil)
	lm := workgraph.NewLeaseManager(db)
	ctx := context.Background()
	v := helperValidatedGraph(t, taskID)
	if _, err := store.Save(ctx, v); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// A different workspace leases src/a.go (AC-1's path).
	if _, err := lm.AcquirePath(ctx, taskID, "other-ws", "src/a.go"); err != nil {
		t.Fatalf("AcquirePath: %v", err)
	}
	active, err := lm.ListActiveByProject(ctx, taskID)
	if err != nil {
		t.Fatalf("ListActiveByProject: %v", err)
	}
	loaded, _ := store.LoadValidated(ctx, taskID)
	verdicts := workgraph.ComputeReadiness(loaded, active, time.Now(), "")
	for _, r := range verdicts {
		if r.PackageID == taskID+"-AC-1" {
			if r.Ready {
				t.Errorf("AC-1 should be blocked by path lease: %+v", r)
			}
			if !r.HasReason("src/a.go") || !r.HasReason("other-ws") {
				t.Errorf("AC-1 reason should name path + workspace: %+v", r)
			}
		}
	}
}

func TestReadiness_Deterministic(t *testing.T) {
	v := helperValidatedGraph(t, "T-det")
	a := workgraph.ComputeReadiness(v, nil, time.Now(), "")
	b := workgraph.ComputeReadiness(v, nil, time.Now(), "")
	if len(a) != len(b) {
		t.Fatalf("len mismatch %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].PackageID != b[i].PackageID {
			t.Fatalf("order mismatch at %d: %q vs %q", i, a[i].PackageID, b[i].PackageID)
		}
		if fmt.Sprint(a[i].BlockedReasons) != fmt.Sprint(b[i].BlockedReasons) {
			t.Fatalf("reasons mismatch at %d: %v vs %v", i, a[i].BlockedReasons, b[i].BlockedReasons)
		}
	}
}

// ---- Lease TTL / expiry / reclaim tests (mandatory AC) ----

func TestLease_TTL_ExpiryReclaim(t *testing.T) {
	db, taskID := setupWGDB(t)
	lm := workgraph.NewLeaseManager(db)
	ctx := context.Background()

	// ws-1 acquires src/a.go with a 50ms TTL.
	_, err := lm.AcquirePathTTL(ctx, taskID, "ws-1", "src/a.go", 50*time.Millisecond)
	if err != nil {
		t.Fatalf("AcquirePathTTL: %v", err)
	}

	// ws-2 cannot acquire immediately (lease still valid).
	if _, err := lm.AcquirePath(ctx, taskID, "ws-2", "src/a.go"); err == nil {
		t.Fatal("expected conflict before expiry")
	}

	// Wait past expiry.
	time.Sleep(80 * time.Millisecond)

	// Even before the sweeper runs, ListActiveByProject must exclude the
	// logically-expired lease (defence-in-depth so a slow sweeper cannot
	// falsely block execution).
	active, _ := lm.ListActiveByProject(ctx, taskID)
	if len(active) != 0 {
		t.Fatalf("expected 0 active leases after logical expiry, got %d", len(active))
	}

	// ws-2 can now acquire (HasActiveLease excludes logically-expired rows).
	lease, err := lm.AcquirePath(ctx, taskID, "ws-2", "src/a.go")
	if err != nil {
		t.Fatalf("AcquirePath ws-2 after expiry: %v", err)
	}
	if lease.WorkspaceID != "ws-2" {
		t.Errorf("workspace = %q want ws-2", lease.WorkspaceID)
	}

	// Run the sweeper now. It must mark ws-1's expired row as state='expired'
	// — though the inline sweep during ws-2's acquire may already have done
	// the work, so the count returned here can be 0. What matters is that the
	// stale row ends up 'expired' (verifiable via ListByWorkspace).
	if _, err := lm.ExpireLeases(ctx); err != nil {
		t.Fatalf("ExpireLeases: %v", err)
	}
	all, err := lm.ListByWorkspace(ctx, "ws-1")
	if err != nil {
		t.Fatalf("ListByWorkspace: %v", err)
	}
	sawExpired := false
	for _, l := range all {
		if l.WorkspaceID == "ws-1" && l.State == "expired" {
			sawExpired = true
		}
	}
	if !sawExpired {
		t.Fatalf("ws-1's logically-expired lease was not swept to state=expired: %+v", all)
	}
}

func TestLease_Renew(t *testing.T) {
	db, taskID := setupWGDB(t)
	lm := workgraph.NewLeaseManager(db)
	ctx := context.Background()

	_, err := lm.AcquirePathTTL(ctx, taskID, "ws-1", "src/a.go", 50*time.Millisecond)
	if err != nil {
		t.Fatalf("AcquirePathTTL: %v", err)
	}
	// Renew before expiry.
	time.Sleep(20 * time.Millisecond)
	if n, err := lm.RenewAll(ctx, "ws-1", 200*time.Millisecond); err != nil {
		t.Fatalf("RenewAll: %v", err)
	} else if n != 1 {
		t.Fatalf("renewed %d, want 1", n)
	}
	// 80ms later (past the original TTL but before the renewed one), the
	// lease is still active.
	time.Sleep(80 * time.Millisecond)
	active, _ := lm.ListActiveByProject(ctx, taskID)
	if len(active) != 1 {
		t.Fatalf("expected lease to remain active after renew, got %d", len(active))
	}
}

func TestLease_RenewDoesNotAffectPerpetual(t *testing.T) {
	db, taskID := setupWGDB(t)
	lm := workgraph.NewLeaseManager(db)
	ctx := context.Background()

	// Acquire perpetual (no TTL).
	if _, err := lm.AcquirePath(ctx, taskID, "ws-1", "src/a.go"); err != nil {
		t.Fatalf("AcquirePath: %v", err)
	}
	// RenewAll should be a no-op for perpetual leases.
	n, err := lm.RenewAll(ctx, "ws-1", 100*time.Millisecond)
	if err != nil {
		t.Fatalf("RenewAll: %v", err)
	}
	if n != 0 {
		t.Errorf("renewed %d perpetual leases, want 0", n)
	}
}

func TestLease_SweeperIdempotent(t *testing.T) {
	db, taskID := setupWGDB(t)
	lm := workgraph.NewLeaseManager(db)
	ctx := context.Background()
	if _, err := lm.AcquirePathTTL(ctx, taskID, "ws-1", "src/a.go", 10*time.Millisecond); err != nil {
		t.Fatalf("AcquirePathTTL: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	n1, _ := lm.ExpireLeases(ctx)
	n2, _ := lm.ExpireLeases(ctx)
	if n1 < 1 {
		t.Fatalf("first sweep should expire >=1, got %d", n1)
	}
	if n2 != 0 {
		t.Fatalf("second sweep should be a no-op, got %d", n2)
	}
}

func TestLease_ConflictExplainsCause(t *testing.T) {
	db, taskID := setupWGDB(t)
	lm := workgraph.NewLeaseManager(db)
	ctx := context.Background()

	if _, err := lm.AcquirePathTTL(ctx, taskID, "ws-1", "src/a.go", time.Hour); err != nil {
		t.Fatalf("AcquirePathTTL: %v", err)
	}
	_, err := lm.AcquirePath(ctx, taskID, "ws-2", "src/a.go")
	if err == nil {
		t.Fatal("expected conflict")
	}
	if !errors.Is(err, workgraph.ErrLeaseConflict) {
		t.Fatalf("error not wrapping ErrLeaseConflict: %v", err)
	}
	ce, ok := workgraph.AsConflictError(err)
	if !ok {
		t.Fatalf("expected *ConflictError, got %T: %v", err, err)
	}
	if len(ce.Reasons) != 1 {
		t.Fatalf("reasons = %d want 1", len(ce.Reasons))
	}
	r := ce.Reasons[0]
	if r.Resource != "src/a.go" {
		t.Errorf("resource = %q want src/a.go", r.Resource)
	}
	if r.WorkspaceID != "ws-1" {
		t.Errorf("workspace = %q want ws-1", r.WorkspaceID)
	}
	if r.HeldBy == "" {
		t.Errorf("HeldBy should be non-empty (expiry was set): %+v", r)
	}
}

// ---- Scheduler Claim / Renew / Release / Expire tests (mandatory AC) ----

func TestScheduler_ClaimSuccess(t *testing.T) {
	db, taskID := setupWGDB(t)
	store := workgraph.NewWorkGraphStore(db, nil, nil)
	lm := workgraph.NewLeaseManager(db)
	sched := workgraph.NewScheduler(store, lm)
	ctx := context.Background()

	v := helperValidatedGraph(t, taskID)
	if _, err := store.Save(ctx, v); err != nil {
		t.Fatalf("Save: %v", err)
	}

	res, err := sched.Claim(ctx, workgraph.ClaimRequest{
		TaskID:      taskID,
		ProjectID:   "proj",
		PackageID:   taskID + "-AC-1",
		WorkspaceID: "ws-1",
	})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if res.State != workgraph.PackageRunning {
		t.Errorf("State = %q want running", res.State)
	}
	if len(res.Leases) != 1 {
		t.Fatalf("leases = %d want 1 (AllowedScope src/a.go)", len(res.Leases))
	}
	// Package is now "running" — re-claiming must fail.
	if _, err := sched.Claim(ctx, workgraph.ClaimRequest{
		TaskID: taskID, ProjectID: "proj", PackageID: taskID + "-AC-1", WorkspaceID: "ws-2",
	}); err == nil {
		t.Fatal("re-claim of running package must fail")
	}
}

func TestScheduler_ClaimBlockedByDependency(t *testing.T) {
	db, taskID := setupWGDB(t)
	store := workgraph.NewWorkGraphStore(db, nil, nil)
	lm := workgraph.NewLeaseManager(db)
	sched := workgraph.NewScheduler(store, lm)
	ctx := context.Background()

	v := helperValidatedGraph(t, taskID)
	if _, err := store.Save(ctx, v); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// AC-2 depends on AC-1; AC-1 is still pending. Claim must refuse.
	_, err := sched.Claim(ctx, workgraph.ClaimRequest{
		TaskID: taskID, ProjectID: "proj", PackageID: taskID + "-AC-2", WorkspaceID: "ws-1",
	})
	if err == nil {
		t.Fatal("Claim should fail for unmet dependency")
	}
	if !errors.Is(err, workgraph.ErrPackageNotReady) {
		t.Fatalf("expected ErrPackageNotReady, got %v", err)
	}
	nre, ok := workgraph.AsNotReadyError(err)
	if !ok {
		t.Fatalf("expected *NotReadyError, got %T", err)
	}
	if !containsAny(nre.Reasons, "not succeeded") {
		t.Errorf("reasons should mention dependency not succeeded: %v", nre.Reasons)
	}

	// No leases should have been acquired.
	active, _ := lm.ListActiveByProject(ctx, "proj")
	if len(active) != 0 {
		t.Fatalf("expected 0 active leases after blocked claim, got %d", len(active))
	}
}

func TestScheduler_ClaimBlockedByLeaseConflict(t *testing.T) {
	db, taskID := setupWGDB(t)
	store := workgraph.NewWorkGraphStore(db, nil, nil)
	lm := workgraph.NewLeaseManager(db)
	sched := workgraph.NewScheduler(store, lm)
	ctx := context.Background()

	v := helperValidatedGraph(t, taskID)
	if _, err := store.Save(ctx, v); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// A different workspace pre-leases src/a.go at the PROJECT scope (the
	// scope Claim uses since the MAJOR-1 fix).
	if _, err := lm.AcquirePath(ctx, "proj", "other-ws", "src/a.go"); err != nil {
		t.Fatalf("AcquirePath: %v", err)
	}

	_, err := sched.Claim(ctx, workgraph.ClaimRequest{
		TaskID: taskID, ProjectID: "proj", PackageID: taskID + "-AC-1", WorkspaceID: "ws-1",
	})
	if err == nil {
		t.Fatal("Claim should fail due to path-lease conflict")
	}
	// The conflict may surface as either ErrLeaseConflict (if it reaches the
	// acquire path) or ErrPackageNotReady (if the readiness pre-check sees
	// the conflict first). Both satisfy the mandatory AC: "Conflicting lease
	// blocks execution with explainable cause" — we only require the cause
	// string to name the path and the holding workspace.
	isExpectedErr := errors.Is(err, workgraph.ErrLeaseConflict) || errors.Is(err, workgraph.ErrPackageNotReady)
	if !isExpectedErr {
		t.Fatalf("expected ErrLeaseConflict or ErrPackageNotReady, got %v", err)
	}
	if !containsSubstr(err.Error(), "src/a.go") || !containsSubstr(err.Error(), "other-ws") {
		t.Errorf("error should name the conflicting path + workspace: %v", err)
	}
	// Package must remain pending (not transitioned).
	p, _ := store.GetPackage(ctx, taskID, taskID+"-AC-1")
	if p.State != workgraph.PackagePending {
		t.Errorf("state = %q want pending (claim must not transition on conflict)", p.State)
	}
}

// helperSharedScopeGraph builds a chain of two packages with an identical
// AllowedScope — exactly the shape workgraph.Decompose produces for a spec
// with ≥2 ACs and a non-empty ProposedScope (chain dependency + shared scope).
func helperSharedScopeGraph(t *testing.T, taskID string) *workgraph.ValidatedWorkGraph {
	t.Helper()
	mk := func(id string, deps ...string) workgraph.WorkPackage {
		return workgraph.WorkPackage{
			ID: taskID + "-" + id, TaskID: taskID, Stage: workgraph.StageImplementation,
			Title: "Implement " + id, Objective: "obj",
			AcceptedACIDs: []string{id}, AllowedScope: []string{"src/shared.go"},
			Dependencies: deps,
			State:        workgraph.PackagePending,
		}
	}
	v, err := workgraph.ValidateWorkGraph(workgraph.WorkGraph{
		TaskID: taskID, Packages: []workgraph.WorkPackage{mk("AC-1"), mk("AC-2", taskID+"-AC-1")},
	})
	if err != nil {
		t.Fatalf("ValidateWorkGraph: %v", err)
	}
	return v
}

// TestScheduler_ClaimSameWorkspaceSharedScope_NotSelfBlocked is the N1
// regression test: two packages of the SAME run (one workspace) share an
// AllowedScope. Claiming the second after the first must NOT report the
// workspace's own lease as a conflict — before the fix, ComputeReadiness
// counted it and the second claim failed with NotReady ("held by workspace
// <its own ws>"), which the daemon classified as lease_lost and parked the
// run in blocked until the 4h TTL.
func TestScheduler_ClaimSameWorkspaceSharedScope_NotSelfBlocked(t *testing.T) {
	db, taskID := setupWGDB(t)
	store := workgraph.NewWorkGraphStore(db, nil, nil)
	lm := workgraph.NewLeaseManager(db)
	sched := workgraph.NewScheduler(store, lm)
	ctx := context.Background()

	v := helperSharedScopeGraph(t, taskID)
	if _, err := store.Save(ctx, v); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := sched.Claim(ctx, workgraph.ClaimRequest{
		TaskID: taskID, ProjectID: "proj", PackageID: taskID + "-AC-1", WorkspaceID: "ws-1",
	}); err != nil {
		t.Fatalf("claim AC-1: %v", err)
	}

	// While AC-1 is still running, AC-2's refusal must name ONLY the unmet
	// dependency — never the workspace's own lease. Before the fix the
	// reasons also carried "held by workspace ws-1", which the daemon
	// classified as lease_lost and parked the run in blocked.
	_, err := sched.Claim(ctx, workgraph.ClaimRequest{
		TaskID: taskID, ProjectID: "proj", PackageID: taskID + "-AC-2", WorkspaceID: "ws-1",
	})
	if err == nil {
		t.Fatal("claim AC-2 with unmet dependency must fail")
	}
	nre, ok := workgraph.AsNotReadyError(err)
	if !ok {
		t.Fatalf("expected *NotReadyError, got %v", err)
	}
	if containsAny(nre.Reasons, "held by workspace") {
		t.Errorf("own-workspace lease reported as conflict: %v", nre.Reasons)
	}
	if !containsAny(nre.Reasons, "not succeeded") {
		t.Errorf("reasons should name the unmet dependency: %v", nre.Reasons)
	}

	// Once the dependency succeeds, the same workspace claims AC-2 cleanly:
	// its own lease on the shared scope is re-acquired idempotently.
	if err := store.TransitionPackage(ctx, taskID, taskID+"-AC-1", workgraph.PackageSucceeded); err != nil {
		t.Fatalf("succeed AC-1: %v", err)
	}
	res, err := sched.Claim(ctx, workgraph.ClaimRequest{
		TaskID: taskID, ProjectID: "proj", PackageID: taskID + "-AC-2", WorkspaceID: "ws-1",
	})
	if err != nil {
		t.Fatalf("claim AC-2 by the same workspace must not self-block: %v", err)
	}
	if res.State != workgraph.PackageRunning {
		t.Errorf("AC-2 state = %q want running", res.State)
	}
}

// TestScheduler_ClaimForeignWorkspaceSharedScope_Blocked is the N1 isolation
// guard: the own-workspace exclusion must not weaken M14-05 project-scoped
// isolation — a FOREIGN workspace in the same project still conflicts.
func TestScheduler_ClaimForeignWorkspaceSharedScope_Blocked(t *testing.T) {
	db, taskID := setupWGDB(t)
	store := workgraph.NewWorkGraphStore(db, nil, nil)
	lm := workgraph.NewLeaseManager(db)
	sched := workgraph.NewScheduler(store, lm)
	ctx := context.Background()

	v := helperSharedScopeGraph(t, taskID)
	if _, err := store.Save(ctx, v); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := sched.Claim(ctx, workgraph.ClaimRequest{
		TaskID: taskID, ProjectID: "proj", PackageID: taskID + "-AC-1", WorkspaceID: "ws-1",
	}); err != nil {
		t.Fatalf("claim AC-1: %v", err)
	}
	if err := store.TransitionPackage(ctx, taskID, taskID+"-AC-1", workgraph.PackageSucceeded); err != nil {
		t.Fatalf("succeed AC-1: %v", err)
	}

	_, err := sched.Claim(ctx, workgraph.ClaimRequest{
		TaskID: taskID, ProjectID: "proj", PackageID: taskID + "-AC-2", WorkspaceID: "ws-2",
	})
	if err == nil {
		t.Fatal("claim by a foreign workspace must fail on the shared scope")
	}
	if !errors.Is(err, workgraph.ErrLeaseConflict) && !errors.Is(err, workgraph.ErrPackageNotReady) {
		t.Fatalf("expected ErrLeaseConflict or ErrPackageNotReady, got %v", err)
	}
	if !containsSubstr(err.Error(), "src/shared.go") || !containsSubstr(err.Error(), "ws-1") {
		t.Errorf("error should name the conflicting path + holding workspace: %v", err)
	}
	p, _ := store.GetPackage(ctx, taskID, taskID+"-AC-2")
	if p.State != workgraph.PackagePending {
		t.Errorf("AC-2 state = %q want pending (foreign claim must not transition)", p.State)
	}
}

// TestReadiness_OwnWorkspaceLeaseExcluded pins the readiness-level semantics:
// a lease held by the requesting workspace is not a conflict; the
// unprivileged view ("") and any foreign workspace still see the block.
func TestReadiness_OwnWorkspaceLeaseExcluded(t *testing.T) {
	db, taskID := setupWGDB(t)
	store := workgraph.NewWorkGraphStore(db, nil, nil)
	ctx := context.Background()

	v := helperValidatedGraph(t, taskID)
	if _, err := store.Save(ctx, v); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := store.LoadValidated(ctx, taskID)
	if err != nil {
		t.Fatalf("LoadValidated: %v", err)
	}
	now := time.Now()
	leases := []workgraph.Lease{{
		Scope: "project", ScopeID: "proj", Kind: workgraph.LeasePath,
		Resource: "src/a.go", WorkspaceID: "ws-1", State: "active",
	}}

	readyFor := func(requesting string) workgraph.Readiness {
		t.Helper()
		for _, r := range workgraph.ComputeReadiness(loaded, leases, now, requesting) {
			if r.PackageID == taskID+"-AC-1" {
				return r
			}
		}
		t.Fatalf("no verdict for AC-1")
		return workgraph.Readiness{}
	}

	if r := readyFor("ws-1"); !r.Ready {
		t.Errorf("requesting workspace's own lease must not block: %v", r.BlockedReasons)
	}
	if r := readyFor(""); r.Ready || !r.HasReason("ws-1") {
		t.Errorf("unprivileged view must report the conflict: ready=%v reasons=%v", r.Ready, r.BlockedReasons)
	}
	if r := readyFor("ws-2"); r.Ready || !r.HasReason("ws-1") {
		t.Errorf("foreign workspace must still conflict: ready=%v reasons=%v", r.Ready, r.BlockedReasons)
	}
}

func TestScheduler_ClaimSemanticTTL(t *testing.T) {
	db, taskID := setupWGDB(t)
	store := workgraph.NewWorkGraphStore(db, nil, nil)
	lm := workgraph.NewLeaseManager(db)
	sched := workgraph.NewScheduler(store, lm)
	ctx := context.Background()

	v := helperValidatedGraph(t, taskID)
	if _, err := store.Save(ctx, v); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// ws-1 claims AC-1 (no deps) with a semantic lease on database_schema.
	if _, err := sched.Claim(ctx, workgraph.ClaimRequest{
		TaskID:         taskID,
		ProjectID:      "proj",
		PackageID:      taskID + "-AC-1",
		WorkspaceID:    "ws-1",
		PathLeases:     []string{"src/a.go"},
		SemanticLeases: []workgraph.SemanticResource{workgraph.SemDatabaseSchema},
		TTL:            time.Hour,
	}); err != nil {
		t.Fatalf("Claim ws-1: %v", err)
	}

	// Mark AC-1 succeeded so AC-2 is ready to claim (otherwise the readiness
	// check rejects on the unmet dependency).
	if err := store.TransitionPackage(ctx, taskID, taskID+"-AC-1", workgraph.PackageSucceeded); err != nil {
		t.Fatalf("TransitionPackage AC-1: %v", err)
	}
	// Release ws-1's path lease on src/a.go so AC-2 can claim its own scope.
	if _, err := lm.ReleaseAll(ctx, "ws-1"); err != nil {
		t.Fatalf("ReleaseAll ws-1: %v", err)
	}

	// ws-2 tries to claim AC-2 requesting the SAME semantic resource that
	// ws-1 still holds. Note: ws-1's path leases were released above, but the
	// semantic was acquired through a separate Claim that did NOT release —
	// the test re-establishes the semantic for ws-1 via a fresh acquire.
	if _, err := lm.AcquireSemantic(ctx, "proj", "ws-1", workgraph.SemDatabaseSchema); err != nil {
		t.Fatalf("AcquireSemantic ws-1: %v", err)
	}

	_, err := sched.Claim(ctx, workgraph.ClaimRequest{
		TaskID:         taskID,
		ProjectID:      "proj",
		PackageID:      taskID + "-AC-2",
		WorkspaceID:    "ws-2",
		SemanticLeases: []workgraph.SemanticResource{workgraph.SemDatabaseSchema},
	})
	if err == nil {
		t.Fatal("expected semantic conflict")
	}
	if !errors.Is(err, workgraph.ErrLeaseConflict) {
		t.Fatalf("expected ErrLeaseConflict (semantic conflicts surface at acquire time), got %v", err)
	}
	// AC-2 stays pending — the claim failed.
	p, _ := store.GetPackage(ctx, taskID, taskID+"-AC-2")
	if p.State != workgraph.PackagePending {
		t.Errorf("AC-2 state = %q want pending (failed claim must not transition)", p.State)
	}
}

// TestScheduler_CrossTaskLeaseConflict_ProjectScoped is the regression test
// for the M14-05 MAJOR-1 defect (ClaimRequest.ProjectID() returned TaskID,
// weakening lease isolation to per-task instead of per-project). It proves
// that two work packages in DIFFERENT tasks of the SAME project cannot
// concurrently lease the same file path: ws-A claims src/shared.go on behalf
// of task T-A; ws-B's claim for task T-B (same project, same path) must fail
// with ErrLeaseConflict and an explainable cause naming the path + ws-A.
//
// Before the fix this test failed: T-B's claim succeeded because leases were
// scoped to (project, TaskID) and T-A/T-B have distinct task IDs.
func TestScheduler_CrossTaskLeaseConflict_ProjectScoped(t *testing.T) {
	// Build a DB with one project and TWO tasks in it.
	home := t.TempDir()
	db, err := storage.Open(context.Background(), home+"/state.db", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := db.CreateProject(context.Background(), storage.Project{
		ID: "proj", Name: "T", Path: home, State: "IDLE",
		Profile: "LOCAL_REVIEW", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	for _, tid := range []string{"T-A", "T-B"} {
		if err := db.CreateTask(context.Background(), storage.Task{
			ID: tid, ProjectID: "proj", Title: "demo", Description: "d",
			Priority: "NORMAL", State: "NEW", CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	store := workgraph.NewWorkGraphStore(db, nil, nil)
	lm := workgraph.NewLeaseManager(db)
	sched := workgraph.NewScheduler(store, lm)
	ctx := context.Background()

	// Both tasks own a package whose AllowedScope collides on src/shared.go.
	// This mirrors the real hazard: two tasks touching the same shared file.
	mkGraph := func(taskID string) *workgraph.ValidatedWorkGraph {
		p := workgraph.WorkPackage{
			ID: taskID + "-pkg", TaskID: taskID, Stage: workgraph.StageImplementation,
			Title: "p", Objective: "o", AcceptedACIDs: []string{"AC-1"},
			AllowedScope: []string{"src/shared.go"},
			State:        workgraph.PackagePending,
		}
		v, err := workgraph.ValidateWorkGraph(workgraph.WorkGraph{TaskID: taskID, Packages: []workgraph.WorkPackage{p}})
		if err != nil {
			t.Fatalf("ValidateWorkGraph %s: %v", taskID, err)
		}
		return v
	}
	if _, err := store.Save(ctx, mkGraph("T-A")); err != nil {
		t.Fatalf("Save T-A: %v", err)
	}
	if _, err := store.Save(ctx, mkGraph("T-B")); err != nil {
		t.Fatalf("Save T-B: %v", err)
	}

	// ws-A claims T-A's package — succeeds and acquires a project-scoped
	// lease on src/shared.go.
	if _, err := sched.Claim(ctx, workgraph.ClaimRequest{
		TaskID: "T-A", ProjectID: "proj", PackageID: "T-A-pkg", WorkspaceID: "ws-A",
	}); err != nil {
		t.Fatalf("Claim T-A (should succeed): %v", err)
	}

	// ws-B claims T-B's package — must FAIL because src/shared.go is already
	// leased at the project scope by ws-A. This is the cross-task isolation
	// the spec §18.4 contract guarantees.
	_, err = sched.Claim(ctx, workgraph.ClaimRequest{
		TaskID: "T-B", ProjectID: "proj", PackageID: "T-B-pkg", WorkspaceID: "ws-B",
	})
	if err == nil {
		t.Fatal("Cross-task defect: T-B claim succeeded even though ws-A holds src/shared.go in the same project (MAJOR-1 regression)")
	}
	// The block may surface as ErrLeaseConflict (acquire path) or
	// ErrPackageNotReady (readiness pre-check sees the path-lease conflict).
	// Both satisfy the spec: the conflicting lease blocks execution.
	blocked := errors.Is(err, workgraph.ErrLeaseConflict) || errors.Is(err, workgraph.ErrPackageNotReady)
	if !blocked {
		t.Fatalf("expected ErrLeaseConflict or ErrPackageNotReady, got %v", err)
	}
	// The cause must name the conflicting path AND the holding workspace so
	// the dispatcher / user can act on it.
	if !containsSubstr(err.Error(), "src/shared.go") {
		t.Errorf("error should name the conflicting path src/shared.go: %v", err)
	}
	if !containsSubstr(err.Error(), "ws-A") {
		t.Errorf("error should name the holding workspace ws-A: %v", err)
	}

	// T-B's package must remain pending (the failed claim must not transition).
	pb, _ := store.GetPackage(ctx, "T-B", "T-B-pkg")
	if pb.State != workgraph.PackagePending {
		t.Errorf("T-B-pkg state = %q want pending (failed claim must not transition)", pb.State)
	}

	// A different path in the same project does NOT conflict: ws-B can claim
	// src/other.go (no collision with ws-A's src/shared.go). This proves the
	// isolation is per-resource, not a blanket per-project lock.
	pOther := workgraph.WorkPackage{
		ID: "T-B-pkg2", TaskID: "T-B", Stage: workgraph.StageImplementation,
		Title: "p2", Objective: "o", AcceptedACIDs: []string{"AC-2"},
		AllowedScope: []string{"src/other.go"},
		State:        workgraph.PackagePending,
	}
	v2, err := workgraph.ValidateWorkGraph(workgraph.WorkGraph{TaskID: "T-B", Packages: []workgraph.WorkPackage{pOther}})
	if err != nil {
		t.Fatalf("ValidateWorkGraph T-B-pkg2: %v", err)
	}
	if _, err := store.Save(ctx, v2); err != nil {
		t.Fatalf("Save T-B-pkg2: %v", err)
	}
	if _, err := sched.Claim(ctx, workgraph.ClaimRequest{
		TaskID: "T-B", ProjectID: "proj", PackageID: "T-B-pkg2", WorkspaceID: "ws-B",
	}); err != nil {
		t.Fatalf("Claim T-B-pkg2 on a non-colliding path should succeed: %v", err)
	}
}

// TestScheduler_ClaimMissingProjectID proves the scheduler rejects a Claim
// that does not identify the project. This guards the MAJOR-1 fix: a missing
// ProjectID must fail loudly rather than silently fall back to the TaskID
// (the old defect).
func TestScheduler_ClaimMissingProjectID(t *testing.T) {
	db, taskID := setupWGDB(t)
	store := workgraph.NewWorkGraphStore(db, nil, nil)
	lm := workgraph.NewLeaseManager(db)
	sched := workgraph.NewScheduler(store, lm)
	ctx := context.Background()
	v := helperValidatedGraph(t, taskID)
	if _, err := store.Save(ctx, v); err != nil {
		t.Fatalf("Save: %v", err)
	}

	_, err := sched.Claim(ctx, workgraph.ClaimRequest{
		TaskID: taskID, PackageID: taskID + "-AC-1", WorkspaceID: "ws-1",
		// ProjectID intentionally omitted.
	})
	if err == nil {
		t.Fatal("Claim without ProjectID must fail (MAJOR-1 guard)")
	}
	if !containsSubstr(err.Error(), "project_id") {
		t.Errorf("error should mention project_id: %v", err)
	}
}

func TestScheduler_RenewAndRelease(t *testing.T) {
	db, taskID := setupWGDB(t)
	store := workgraph.NewWorkGraphStore(db, nil, nil)
	lm := workgraph.NewLeaseManager(db)
	sched := workgraph.NewScheduler(store, lm)
	ctx := context.Background()

	v := helperValidatedGraph(t, taskID)
	if _, err := store.Save(ctx, v); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := sched.Claim(ctx, workgraph.ClaimRequest{
		TaskID: taskID, ProjectID: "proj", PackageID: taskID + "-AC-1", WorkspaceID: "ws-1",
		TTL: 100 * time.Millisecond,
	}); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if n, err := sched.Renew(ctx, "ws-1", time.Hour); err != nil {
		t.Fatalf("Renew: %v", err)
	} else if n != 1 {
		t.Fatalf("renewed %d want 1", n)
	}
	n, err := sched.Release(ctx, "ws-1")
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if n != 1 {
		t.Errorf("released %d want 1", n)
	}
}

// ---- Concurrent claim race test (mandatory AC) ----
//
// Two schedulers racing to Claim the same package must result in exactly one
// winner (one ClaimResult, one state transition to running, one set of
// acquired leases). SQLite's partial UNIQUE index + single-writer
// serialisation make the INSERT the linearisation point; the loser surfaces
// either ErrLeaseConflict or ErrPackageNotReady.
func TestScheduler_ConcurrentClaimRace(t *testing.T) {
	db, taskID := setupWGDB(t)
	store := workgraph.NewWorkGraphStore(db, nil, nil)
	lm := workgraph.NewLeaseManager(db)
	ctx := context.Background()

	v := helperValidatedGraph(t, taskID)
	if _, err := store.Save(ctx, v); err != nil {
		t.Fatalf("Save: %v", err)
	}

	const N = 16
	var wg sync.WaitGroup
	wg.Add(N)
	var wins int64
	var fails int64
	var firstErr error
	var errMu sync.Mutex
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			sched := workgraph.NewScheduler(store, lm)
			_, err := sched.Claim(ctx, workgraph.ClaimRequest{
				TaskID: taskID, ProjectID: "proj", PackageID: taskID + "-AC-1",
				WorkspaceID: fmt.Sprintf("ws-%d", i),
			})
			if err == nil {
				atomic.AddInt64(&wins, 1)
				return
			}
			atomic.AddInt64(&fails, 1)
			if !errors.Is(err, workgraph.ErrLeaseConflict) && !errors.Is(err, workgraph.ErrPackageNotReady) {
				errMu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("unexpected loser error: %w", err)
				}
				errMu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	if wins != 1 {
		t.Fatalf("wins = %d want exactly 1 (race not linearised)", wins)
	}
	if fails != N-1 {
		t.Fatalf("fails = %d want %d", fails, N-1)
	}
	if firstErr != nil {
		t.Fatalf("loser returned an unexpected error type: %v", firstErr)
	}
	// Exactly one active lease for src/a.go (the winner's).
	active, err := lm.ListActiveByProject(ctx, "proj")
	if err != nil {
		t.Fatalf("ListActiveByProject: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("active leases = %d want 1 (winner only)", len(active))
	}
}

// ---- Restart-recovery test (mandatory AC) ----
//
// Simulate a daemon restart by closing the storage handle and re-opening it
// against the same DB file. The graph, package states, attempts and leases
// must all survive.
func TestStore_RestartRecoversGraphAndLeases(t *testing.T) {
	home := t.TempDir()
	dbPath := home + "/state.db"

	// First " incarnation ": open, migrate, save a graph, claim, append
	// attempt.
	db1, err := storage.Open(context.Background(), dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := db1.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := db1.CreateProject(context.Background(), storage.Project{
		ID: "proj", Name: "T", Path: home, State: "IDLE",
		Profile: "LOCAL_REVIEW", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db1.CreateTask(context.Background(), storage.Task{
		ID: "T-1", ProjectID: "proj", Title: "demo", Description: "d",
		Priority: "NORMAL", State: "NEW", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	store1 := workgraph.NewWorkGraphStore(db1, nil, nil)
	lm1 := workgraph.NewLeaseManager(db1)
	sched1 := workgraph.NewScheduler(store1, lm1)
	ctx := context.Background()
	v := helperValidatedGraph(t, "T-1")
	if _, err := store1.Save(ctx, v); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := sched1.Claim(ctx, workgraph.ClaimRequest{
		TaskID: "T-1", ProjectID: "proj", PackageID: "T-1-AC-1", WorkspaceID: "ws-1",
		TTL: time.Hour,
	}); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := store1.AppendAttempt(ctx, "T-1", "T-1-AC-1", workgraph.Attempt{
		State: workgraph.PackageRunning,
	}); err != nil {
		t.Fatalf("AppendAttempt: %v", err)
	}

	// "Crash": close the handle without an orderly release.
	if err := db1.Close(); err != nil {
		t.Fatalf("close db1: %v", err)
	}

	// "Restart": open a fresh handle against the same DB file.
	db2, err := storage.Open(context.Background(), dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	if err := db2.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	store2 := workgraph.NewWorkGraphStore(db2, nil, nil)
	lm2 := workgraph.NewLeaseManager(db2)

	// Graph survives.
	loaded, err := store2.LoadValidated(ctx, "T-1")
	if err != nil {
		t.Fatalf("LoadValidated after restart: %v", err)
	}
	if len(loaded.Packages()) != 2 {
		t.Fatalf("packages after restart = %d want 2", len(loaded.Packages()))
	}
	// Package state survives (AC-1 is running, AC-2 is pending).
	p1, _ := store2.GetPackage(ctx, "T-1", "T-1-AC-1")
	if p1.State != workgraph.PackageRunning {
		t.Errorf("AC-1 state after restart = %q want running", p1.State)
	}
	// Attempt history survives.
	if len(p1.Attempts) != 1 {
		t.Errorf("AC-1 attempts after restart = %d want 1", len(p1.Attempts))
	}
	// Leases survive.
	active, err := lm2.ListActiveByProject(ctx, "proj")
	if err != nil {
		t.Fatalf("ListActiveByProject after restart: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("active leases after restart = %d want 1 (src/a.go held by ws-1)", len(active))
	}
	if active[0].WorkspaceID != "ws-1" {
		t.Errorf("lease holder after restart = %q want ws-1", active[0].WorkspaceID)
	}

	// Re-claiming AC-1 (now running) must fail for any new workspace.
	sched2 := workgraph.NewScheduler(store2, lm2)
	if _, err := sched2.Claim(ctx, workgraph.ClaimRequest{
		TaskID: "T-1", ProjectID: "proj", PackageID: "T-1-AC-1", WorkspaceID: "ws-2",
	}); err == nil {
		t.Fatal("Claim after restart on already-running package must fail")
	}
}

// ---- Integration: real task.Compile → workgraph.Decompose → store → readiness ----

func TestIntegration_CompileDecomposeSaveReadiness(t *testing.T) {
	db, _ := setupWGDB(t)
	// Use a separate task ID for this test so FK is satisfied.
	if err := db.CreateTask(context.Background(), storage.Task{
		ID: "T-INT", ProjectID: "proj", Title: "int", Description: "d",
		Priority: "NORMAL", State: "NEW",
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatal(err)
	}
	store := workgraph.NewWorkGraphStore(db, nil, nil)
	ctx := context.Background()

	// Use the real M14-02 task compiler.
	res, err := task.Compile(task.CompileInput{
		TaskID: "T-INT",
		Title:  "Add retry button",
		Description: "Objective: Persist compiled specification across daemon restart.\n" +
			"Acceptance Criteria:\n" +
			"- Spec is durable in SQLite.\n" +
			"- Lock state survives restart.",
	})
	if err != nil {
		t.Fatalf("task.Compile: %v", err)
	}
	v, err := workgraph.Decompose(res.Specification)
	if err != nil {
		t.Fatalf("workgraph.Decompose: %v", err)
	}
	if _, err := store.Save(ctx, v); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := store.LoadValidated(ctx, "T-INT")
	if err != nil {
		t.Fatalf("LoadValidated: %v", err)
	}
	verdicts := workgraph.ComputeReadiness(loaded, nil, time.Now(), "")
	// Every package is decomposed as a chain (AC-1 → AC-2 → ...), so only
	// the first is ready; the rest are blocked by their predecessor.
	readyCount := 0
	for _, r := range verdicts {
		if r.Ready {
			readyCount++
		}
	}
	if readyCount != 1 {
		t.Errorf("ready = %d want 1 (only the first package in the chain is ready)", readyCount)
	}
}

// ---- helpers ----

func containsAny(items []string, substr string) bool {
	for _, s := range items {
		if containsSubstr(s, substr) {
			return true
		}
	}
	return false
}

// containsSubstr is a case-sensitive substring test on a single string. Kept
// local to avoid pulling strings.Contains (this file's only need is a few
// substring assertions in tests).
func containsSubstr(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
