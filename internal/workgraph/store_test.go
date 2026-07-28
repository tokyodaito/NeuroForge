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
	verdicts := workgraph.ComputeReadiness(loaded, nil, time.Now())
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
	verdicts := workgraph.ComputeReadiness(loaded, nil, time.Now())
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
	verdicts := workgraph.ComputeReadiness(loaded, nil, time.Now())
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
	verdicts := workgraph.ComputeReadiness(loaded, active, time.Now())
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
	a := workgraph.ComputeReadiness(v, nil, time.Now())
	b := workgraph.ComputeReadiness(v, nil, time.Now())
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
		TaskID: taskID, PackageID: taskID + "-AC-1", WorkspaceID: "ws-2",
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
		TaskID: taskID, PackageID: taskID + "-AC-2", WorkspaceID: "ws-1",
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
	active, _ := lm.ListActiveByProject(ctx, taskID)
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

	// A different workspace pre-leases src/a.go.
	if _, err := lm.AcquirePath(ctx, taskID, "other-ws", "src/a.go"); err != nil {
		t.Fatalf("AcquirePath: %v", err)
	}

	_, err := sched.Claim(ctx, workgraph.ClaimRequest{
		TaskID: taskID, PackageID: taskID + "-AC-1", WorkspaceID: "ws-1",
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
	if _, err := lm.AcquireSemantic(ctx, taskID, "ws-1", workgraph.SemDatabaseSchema); err != nil {
		t.Fatalf("AcquireSemantic ws-1: %v", err)
	}

	_, err := sched.Claim(ctx, workgraph.ClaimRequest{
		TaskID:         taskID,
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
		TaskID: taskID, PackageID: taskID + "-AC-1", WorkspaceID: "ws-1",
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
				TaskID: taskID, PackageID: taskID + "-AC-1",
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
	active, err := lm.ListActiveByProject(ctx, taskID)
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
		TaskID: "T-1", PackageID: "T-1-AC-1", WorkspaceID: "ws-1",
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
	active, err := lm2.ListActiveByProject(ctx, "T-1")
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
		TaskID: "T-1", PackageID: "T-1-AC-1", WorkspaceID: "ws-2",
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
	verdicts := workgraph.ComputeReadiness(loaded, nil, time.Now())
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
