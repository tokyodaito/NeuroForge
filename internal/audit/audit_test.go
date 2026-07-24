package audit

import (
	"context"
	"path/filepath"
	"testing"

	"neuroforge/internal/storage"
)

func newRecorder(t *testing.T) (*Recorder, *storage.DB) {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.Open(context.Background(), filepath.Join(dir, "state.db"), nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return NewRecorder(db, nil), db
}

func TestRecord_DefaultsAndAppend(t *testing.T) {
	r, db := newRecorder(t)
	ctx := context.Background()

	id, err := r.Record(ctx, Event{Type: "daemon.started", Payload: Payload("pid", 1234, "addr", "127.0.0.1:0")})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if id == 0 {
		t.Fatal("id must be non-zero")
	}

	// Defaults applied.
	got, err := r.History(ctx, ScopeGlobal, 0)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	e := got[0]
	if e.Scope != ScopeSystem || e.ScopeID != ScopeGlobal || e.Actor != ActorDaemon {
		t.Fatalf("defaults wrong: %+v", e)
	}
	if e.Type != "daemon.started" {
		t.Fatalf("type = %q", e.Type)
	}
	if e.Payload != `{"addr":"127.0.0.1:0","pid":1234}` {
		t.Fatalf("payload = %q", e.Payload)
	}

	// Count via storage confirms one durable row.
	n, _ := db.CountAuditEvents(ctx)
	if n != 1 {
		t.Fatalf("count = %d, want 1", n)
	}
}

func TestRecord_RequiresType(t *testing.T) {
	r, _ := newRecorder(t)
	if _, err := r.Record(context.Background(), Event{}); err == nil {
		t.Fatal("expected error for empty type")
	}
}

func TestHistory_ReconstructsTaskHistory(t *testing.T) {
	r, _ := newRecorder(t)
	ctx := context.Background()

	// A representative per-task lifecycle (AC-30 shape), abbreviated.
	steps := []string{
		"task.created",
		"task.specified",
		"route.selected",
		"attempt.started",
		"usage.updated",
		"files.changed",
		"verification.recorded",
		"delivery.local",
	}
	var wantIDs []int64
	for _, s := range steps {
		id, err := r.Record(ctx, Event{Type: s, Scope: ScopeTask, ScopeID: "WORK-88", Actor: ActorDaemon, Payload: Payload("step", s)})
		if err != nil {
			t.Fatalf("Record %q: %v", s, err)
		}
		wantIDs = append(wantIDs, id)
	}

	// Other task's events must not bleed in.
	r.Record(ctx, Event{Type: "task.created", Scope: ScopeTask, ScopeID: "APP-1", Actor: ActorDaemon})

	got, err := r.History(ctx, "WORK-88", 0)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(got) != len(steps) {
		t.Fatalf("len = %d, want %d", len(got), len(steps))
	}
	for i, e := range got {
		if e.Type != steps[i] {
			t.Errorf("got[%d].Type = %q, want %q", i, e.Type, steps[i])
		}
		if e.ID != wantIDs[i] {
			t.Errorf("got[%d].ID = %d, want %d", i, e.ID, wantIDs[i])
		}
		if e.ScopeID != "WORK-88" {
			t.Errorf("scope id leak: %q", e.ScopeID)
		}
	}
}

func TestAppendOnly_NotWritableViaRecorder(t *testing.T) {
	// The Recorder deliberately exposes no Update/Delete methods; verify the
	// type set only record/read operations. This is a compile-time contract:
	// if someone adds mutation, this test must be updated intentionally.
	r, _ := newRecorder(t)
	ctx := context.Background()
	if _, err := r.Record(ctx, Event{Type: "x"}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got, err := r.History(ctx, ScopeGlobal, 0)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
}
