package task

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"neuroforge/internal/audit"
	"neuroforge/internal/storage"
)

// TestBacklog_NoCollisionAfterRestart proves the per-project task sequence is
// persisted in SQLite, so a daemon restart (a brand-new Backlog on the same DB)
// never re-issues an existing id (blocker 4 fix). Previously the sequence lived
// in an in-memory atomic counter that reset to zero on every restart.
func TestBacklog_NoCollisionAfterRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	ctx := context.Background()
	mk := func() *Backlog {
		db, err := storage.Open(ctx, dbPath, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		if err := db.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		if err := db.CreateProject(ctx, storage.Project{
			ID: "p", Name: "p", Path: "/tmp/p", State: "ACTIVE", Profile: "LOCAL_REVIEW",
			CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z",
		}); err != nil && !isUniqueErr(err) {
			t.Fatal(err)
		}
		return NewBacklog(db, audit.NewRecorder(db, nil), "", nil)
	}

	b1 := mk()
	t1, err := b1.Add(ctx, AddRequest{ProjectID: "p", Description: "first"})
	if err != nil {
		t.Fatalf("first add: %v", err)
	}
	if t1.ID != "p-1" {
		t.Fatalf("first id = %q, want p-1", t1.ID)
	}
	t2, err := b1.Add(ctx, AddRequest{ProjectID: "p", Description: "second"})
	if err != nil {
		t.Fatalf("second add: %v", err)
	}
	if t2.ID != "p-2" {
		t.Fatalf("second id = %q, want p-2", t2.ID)
	}

	// Simulate a daemon restart: a brand-new Backlog with a fresh DB handle on
	// the SAME database file. The in-memory counter would reset here.
	b2 := mk()
	t3, err := b2.Add(ctx, AddRequest{ProjectID: "p", Description: "third"})
	if err != nil {
		t.Fatalf("post-restart add collided or failed: %v", err)
	}
	if t3.ID != "p-3" {
		t.Fatalf("post-restart id = %q, want p-3 (sequence must persist)", t3.ID)
	}
}

// TestBacklog_ConcurrentTaskAddNoCollision proves concurrent task creation does
// not collide: each goroutine reserves a distinct sequence number inside its own
// transaction (SQLite serialises the writers; the UPSERT is monotonic).
func TestBacklog_ConcurrentTaskAddNoCollision(t *testing.T) {
	db, rec, _ := testDBProject(t, "conc")
	ctx := context.Background()
	b := NewBacklog(db, rec, "", nil)

	const n = 25
	var wg sync.WaitGroup
	ids := make(chan string, n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tk, err := b.Add(ctx, AddRequest{ProjectID: "conc", Description: fmt.Sprintf("task %d", i)})
			if err != nil {
				errs <- err
				return
			}
			ids <- tk.ID
		}(i)
	}
	wg.Wait()
	close(ids)
	close(errs)

	if err := <-errs; err != nil {
		t.Fatalf("concurrent add failed: %v", err)
	}

	seen := make(map[string]bool, n)
	count := 0
	for id := range ids {
		count++
		if seen[id] {
			t.Fatalf("duplicate task id from concurrent add: %s", id)
		}
		seen[id] = true
	}
	if count != n {
		t.Fatalf("created %d tasks, want %d", count, n)
	}

	// The persisted sequence equals the number created (no gaps: each tx
	// commits, so next_seq == n after n increments from 0).
	var got int
	if err := db.Underlying().QueryRowContext(ctx,
		`SELECT next_seq FROM task_sequences WHERE project_id = ?`, "conc").Scan(&got); err != nil {
		t.Fatalf("read seq: %v", err)
	}
	if got != n {
		t.Errorf("persisted next_seq = %d, want %d", got, n)
	}
}

// TestBacklog_DeletedTaskIDNotReused proves deleting a task does not let its id
// be reissued (monotonic forward-only sequence — spec §11.4 durable ids).
func TestBacklog_DeletedTaskIDNotReused(t *testing.T) {
	db, rec, _ := testDBProject(t, "del")
	ctx := context.Background()
	b := NewBacklog(db, rec, "", nil)

	t1, err := b.Add(ctx, AddRequest{ProjectID: "del", Description: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Add(ctx, AddRequest{ProjectID: "del", Description: "two"}); err != nil {
		t.Fatal(err)
	}
	// Delete the first task directly from storage.
	if _, err := db.Underlying().ExecContext(ctx, `DELETE FROM tasks WHERE id = ?`, t1.ID); err != nil {
		t.Fatal(err)
	}
	t3, err := b.Add(ctx, AddRequest{ProjectID: "del", Description: "three"})
	if err != nil {
		t.Fatal(err)
	}
	if t3.ID == t1.ID {
		t.Fatalf("deleted task id %q was reused; sequence must be forward-only", t1.ID)
	}
	if t3.ID != "del-3" {
		t.Errorf("new id = %q, want del-3", t3.ID)
	}
}

// TestBacklog_ExistingDBMigrationSeedsSequence proves an existing database (with
// tasks created under the old in-memory scheme) migrates safely: the backfill
// seeds next_seq past the highest existing id so the first post-migration task
// does not collide.
func TestBacklog_ExistingDBMigrationSeedsSequence(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	ctx := context.Background()

	// 1. Create an "old" database at schema v5 and insert tasks the legacy way
	//    (raw rows, simulating pre-migration data). Use v1..v5 only.
	db1, err := storage.Open(ctx, dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Migrate to v5 only by running the full migrate then deleting the v6 row
	// and the task_sequences table, to simulate a pre-v6 database.
	if err := db1.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db1.Underlying().ExecContext(ctx,
		`INSERT INTO projects (id,name,path,state,profile,created_at,updated_at) VALUES ('old','old','/tmp/old','ACTIVE','LOCAL_REVIEW','2026-01-01','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"old-1", "old-2", "old-7"} {
		if _, err := db1.Underlying().ExecContext(ctx,
			`INSERT INTO tasks (id,project_id,description,priority,state,created_at,updated_at) VALUES (?,?,?,?,?,?,?)`,
			id, "old", "legacy", "NORMAL", "NEW",
			time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	// Roll back to pre-v6: drop the seeded table and the migration record, then
	// re-run Migrate on a fresh handle to exercise the backfill.
	if _, err := db1.Underlying().ExecContext(ctx, `DROP TABLE IF EXISTS task_sequences`); err != nil {
		t.Fatal(err)
	}
	if _, err := db1.Underlying().ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = 6`); err != nil {
		t.Fatal(err)
	}
	db1.Close()

	// 2. Re-open and migrate (this applies v6 with the backfill).
	db2, err := storage.Open(ctx, dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	if err := db2.Migrate(ctx); err != nil {
		t.Fatalf("migrate existing db: %v", err)
	}

	// 3. The next task id must be > the highest existing suffix (7).
	b := NewBacklog(db2, audit.NewRecorder(db2, nil), "", nil)
	tk, err := b.Add(ctx, AddRequest{ProjectID: "old", Description: "post-migration"})
	if err != nil {
		t.Fatalf("post-migration add: %v", err)
	}
	if tk.ID != "old-8" {
		t.Errorf("post-migration id = %q, want old-8 (seeded past max suffix 7)", tk.ID)
	}
}

// TestBacklog_SequenceCorruptReturnsError proves a corrupted sequence (<=0)
// surfaces a clear error rather than a silent collision or panic.
func TestBacklog_SequenceCorruptReturnsError(t *testing.T) {
	db, rec, _ := testDBProject(t, "corrupt")
	ctx := context.Background()
	b := NewBacklog(db, rec, "", nil)

	// First task to establish the sequence row, then corrupt it to a negative
	// value directly. NextTaskSeq will UPSERT (increment) then SELECT; the
	// guard rejects any non-positive result.
	if _, err := b.Add(ctx, AddRequest{ProjectID: "corrupt", Description: "x"}); err != nil {
		t.Fatal(err)
	}
	// Force the persisted value far enough negative that one increment still
	// leaves it non-positive.
	if _, err := db.Underlying().ExecContext(ctx,
		`UPDATE task_sequences SET next_seq = -5 WHERE project_id = 'corrupt'`); err != nil {
		t.Fatal(err)
	}
	_, err := b.Add(ctx, AddRequest{ProjectID: "corrupt", Description: "y"})
	if err == nil {
		t.Fatal("expected corrupt-sequence error, got nil (guard did not fire)")
	}
}

func testDBProject(t *testing.T, projectID string) (*storage.DB, *audit.Recorder, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	ctx := context.Background()
	db, err := storage.Open(ctx, dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateProject(ctx, storage.Project{
		ID: projectID, Name: projectID, Path: "/tmp/" + projectID,
		State: "ACTIVE", Profile: "LOCAL_REVIEW",
		CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	return db, audit.NewRecorder(db, nil), dir
}

func isUniqueErr(err error) bool {
	if err == nil {
		return false
	}
	return contains(err.Error(), "UNIQUE") || contains(err.Error(), "unique")
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
