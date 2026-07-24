package workgraph_test

import (
	"context"
	"testing"
	"time"

	"neuroforge/internal/storage"
	"neuroforge/internal/workgraph"
)

func setupLeaseDB(t *testing.T) *storage.DB {
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
	// Insert a project for FK satisfaction.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := db.CreateProject(context.Background(), storage.Project{
		ID: "proj", Name: "T", Path: "/tmp", State: "IDLE",
		Profile: "LOCAL_REVIEW", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestAcquirePath_ThenConflict(t *testing.T) {
	db := setupLeaseDB(t)
	lm := workgraph.NewLeaseManager(db)
	ctx := context.Background()

	// First workspace acquires the path.
	_, err := lm.AcquirePath(ctx, "proj", "ws-1", "src/main.go")
	if err != nil {
		t.Fatalf("AcquirePath ws-1: %v", err)
	}

	// Same workspace re-acquiring is fine (idempotent for the holder).
	_, err = lm.AcquirePath(ctx, "proj", "ws-1", "src/main.go")
	if err != nil {
		t.Fatalf("AcquirePath ws-1 again: %v", err)
	}

	// Different workspace on the same path -> conflict.
	_, err = lm.AcquirePath(ctx, "proj", "ws-2", "src/main.go")
	if err == nil {
		t.Fatal("expected lease conflict for ws-2 on same path")
	}
}

func TestAcquireSemantic_Conflict(t *testing.T) {
	db := setupLeaseDB(t)
	lm := workgraph.NewLeaseManager(db)
	ctx := context.Background()

	_, err := lm.AcquireSemantic(ctx, "proj", "ws-1", workgraph.SemDatabaseSchema)
	if err != nil {
		t.Fatalf("AcquireSemantic ws-1: %v", err)
	}

	// Different resource is fine.
	_, err = lm.AcquireSemantic(ctx, "proj", "ws-2", workgraph.SemDesignSystem)
	if err != nil {
		t.Fatalf("AcquireSemantic ws-2 design_system: %v", err)
	}

	// Same semantic resource by different workspace -> conflict.
	_, err = lm.AcquireSemantic(ctx, "proj", "ws-2", workgraph.SemDatabaseSchema)
	if err == nil {
		t.Fatal("expected lease conflict for database_schema")
	}
}

func TestReleaseAll(t *testing.T) {
	db := setupLeaseDB(t)
	lm := workgraph.NewLeaseManager(db)
	ctx := context.Background()

	lm.AcquirePath(ctx, "proj", "ws-1", "a.go")
	lm.AcquireSemantic(ctx, "proj", "ws-1", workgraph.SemBuildConfiguration)

	n, err := lm.ReleaseAll(ctx, "ws-1")
	if err != nil {
		t.Fatalf("ReleaseAll: %v", err)
	}
	if n != 2 {
		t.Errorf("released %d, want 2", n)
	}

	// Now ws-2 can acquire the same resources.
	_, err = lm.AcquirePath(ctx, "proj", "ws-2", "a.go")
	if err != nil {
		t.Fatalf("AcquirePath ws-2 after release: %v", err)
	}
}

func TestInvalidSemantic(t *testing.T) {
	db := setupLeaseDB(t)
	lm := workgraph.NewLeaseManager(db)
	_, err := lm.AcquireSemantic(context.Background(), "proj", "ws-1", workgraph.SemanticResource("not_a_resource"))
	if err == nil {
		t.Fatal("expected error for invalid semantic resource")
	}
}

func TestListActive(t *testing.T) {
	db := setupLeaseDB(t)
	lm := workgraph.NewLeaseManager(db)
	ctx := context.Background()

	lm.AcquirePath(ctx, "proj", "ws-1", "a.go")
	lm.AcquireSemantic(ctx, "proj", "ws-1", workgraph.SemNavigationGraph)
	lm.AcquirePath(ctx, "proj", "ws-2", "b.go")

	active, err := lm.ListActiveByProject(ctx, "proj")
	if err != nil {
		t.Fatalf("ListActiveByProject: %v", err)
	}
	if len(active) != 3 {
		t.Errorf("active leases = %d, want 3", len(active))
	}

	lm.ReleaseAll(ctx, "ws-1")
	active, _ = lm.ListActiveByProject(ctx, "proj")
	if len(active) != 1 {
		t.Errorf("active leases after release = %d, want 1", len(active))
	}
}
