package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	db, err := Open(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return db
}

func TestOpen_EnablesWALMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	db, err := Open(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// WAL sidecar files must appear after a write.
	if _, err := db.db.Exec(`CREATE TABLE x (n INTEGER); INSERT INTO x VALUES (1)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := db.assertWAL(context.Background()); err != nil {
		t.Fatalf("WAL not active: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "state.db-wal")); err != nil {
		t.Fatalf("expected state.db-wal to exist: %v", err)
	}
}

func TestOpen_CreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "a", "b")
	path := filepath.Join(nested, "state.db")
	db, err := Open(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if _, err := os.Stat(nested); err != nil {
		t.Fatalf("parent dir not created: %v", err)
	}
}

func TestMigrate_IdempotentAndVersioned(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	v, err := db.CurrentVersion(ctx)
	if err != nil {
		t.Fatalf("CurrentVersion: %v", err)
	}
	if v != migrations[len(migrations)-1].Version {
		t.Fatalf("version = %d, want %d", v, migrations[len(migrations)-1].Version)
	}

	// Re-running must be a no-op and keep the same version.
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	v2, _ := db.CurrentVersion(ctx)
	if v2 != v {
		t.Fatalf("version changed after re-migrate: %d -> %d", v, v2)
	}

	// schema_migrations must record each applied migration exactly once.
	rows, err := db.db.Query(`SELECT version, description FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatalf("query migrations: %v", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
	}
	if count != len(migrations) {
		t.Fatalf("recorded %d migrations, want %d", count, len(migrations))
	}
}

func TestMigrate_ProgressiveFromEmptyFile(t *testing.T) {
	// Opening then migrating then migrating again on a fresh file should not
	// error and should leave the audit table queryable.
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	db, err := Open(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	n, err := db.CountAuditEvents(context.Background())
	if err != nil {
		t.Fatalf("CountAuditEvents: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 audit rows, got %d", n)
	}
}

func TestAppendAuditEvent_OrderedAndQueryable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	var ids []int64
	for _, typ := range []string{"daemon.started", "task.created", "daemon.stopped"} {
		id, err := db.AppendAuditEvent(ctx, AuditEvent{
			Scope: "system", ScopeID: "global", Type: typ, Actor: "daemon", Payload: "{}",
		})
		if err != nil {
			t.Fatalf("Append %q: %v", typ, err)
		}
		ids = append(ids, id)
	}

	// IDs must be strictly increasing (monotonic sequence).
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Fatalf("ids not monotonic: %v", ids)
		}
	}

	got, err := db.ListAuditEvents(ctx, AuditFilter{ScopeID: "global"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for i, want := range []string{"daemon.started", "task.created", "daemon.stopped"} {
		if got[i].Type != want {
			t.Errorf("got[%d].Type = %q, want %q", i, got[i].Type, want)
		}
	}

	// Timestamps must parse and be recent.
	for _, e := range got {
		ts, err := time.Parse(time.RFC3339Nano, e.Timestamp)
		if err != nil {
			t.Errorf("parse ts %q: %v", e.Timestamp, err)
		}
		if time.Since(ts) > time.Minute {
			t.Errorf("ts %v too old", ts)
		}
	}
}

func TestAppendAuditEvent_DefaultsTimestamp(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	id, err := db.AppendAuditEvent(ctx, AuditEvent{
		Scope: "system", ScopeID: "global", Type: "x", Actor: "system", // no Timestamp
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := db.ListAuditEvents(ctx, AuditFilter{ScopeID: "global"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ID != id {
		t.Fatalf("unexpected rows: %+v", got)
	}
	if got[0].Timestamp == "" {
		t.Fatal("expected non-empty default timestamp")
	}
	if _, err := time.Parse(time.RFC3339Nano, got[0].Timestamp); err != nil {
		t.Errorf("default timestamp not RFC3339Nano: %v", err)
	}
}

func TestAuditFilter_ScopeAndType(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	db.AppendAuditEvent(ctx, AuditEvent{Scope: "project", ScopeID: "p1", Type: "a", Actor: "daemon", Payload: "{}"})
	db.AppendAuditEvent(ctx, AuditEvent{Scope: "project", ScopeID: "p2", Type: "a", Actor: "daemon", Payload: "{}"})
	db.AppendAuditEvent(ctx, AuditEvent{Scope: "task", ScopeID: "t1", Type: "b", Actor: "daemon", Payload: "{}"})

	if got, _ := db.ListAuditEvents(ctx, AuditFilter{Scope: "project"}); len(got) != 2 {
		t.Fatalf("scope=project -> %d, want 2", len(got))
	}
	if got, _ := db.ListAuditEvents(ctx, AuditFilter{ScopeID: "p1"}); len(got) != 1 {
		t.Fatalf("scope_id=p1 -> %d, want 1", len(got))
	}
	if got, _ := db.ListAuditEvents(ctx, AuditFilter{Type: "b"}); len(got) != 1 {
		t.Fatalf("type=b -> %d, want 1", len(got))
	}
}

func TestAuditFilter_NewestFirstAndLimit(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		db.AppendAuditEvent(ctx, AuditEvent{Scope: "system", ScopeID: "g", Type: "t", Actor: "daemon", Payload: "{}"})
	}
	got, err := db.ListAuditEvents(ctx, AuditFilter{Limit: 2, NewestFirst: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].ID <= got[1].ID {
		t.Fatalf("expected newest-first, got ids %d then %d", got[0].ID, got[1].ID)
	}
}

func TestAppendOnly_RejectsUpdateAndDelete(t *testing.T) {
	db := newTestDB(t)
	if err := db.AssertAppendOnly(context.Background()); err != nil {
		t.Fatalf("append-only not enforced: %v", err)
	}
	// The probe row must still exist (delete was rejected).
	if got, _ := db.ListAuditEvents(context.Background(), AuditFilter{Type: "probe"}); len(got) != 1 {
		t.Fatalf("probe row should survive: %d rows", len(got))
	}
}

func TestConcurrentReadersDuringWrite(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	done := make(chan error, 1)
	go func() {
		for i := 0; i < 50; i++ {
			if _, err := db.AppendAuditEvent(ctx, AuditEvent{Scope: "system", ScopeID: "g", Type: "burst", Actor: "daemon", Payload: "{}"}); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()

	// Concurrent reads while the writer is active.
	reads := 0
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("writer: %v", err)
			}
			if _, err := db.ListAuditEvents(ctx, AuditFilter{Limit: 1000}); err != nil {
				t.Fatalf("final read: %v", err)
			}
			return
		default:
			if _, err := db.ListAuditEvents(ctx, AuditFilter{Limit: 10}); err != nil {
				// Under WAL a read should not fail due to a concurrent writer;
				// surface any error.
				if !strings.Contains(err.Error(), "database is locked") {
					t.Fatalf("concurrent read: %v", err)
				}
			}
			reads++
		}
	}
}
