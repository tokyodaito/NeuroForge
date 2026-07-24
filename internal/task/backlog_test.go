package task

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"neuroforge/internal/audit"
	"neuroforge/internal/storage"
)

func testDB(t *testing.T) (*storage.DB, *audit.Recorder, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	artifactsDir := filepath.Join(dir, "artifacts")
	ctx := context.Background()
	db, err := storage.Open(ctx, dbPath, nil)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := os.MkdirAll(artifactsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Create a project row so FK constraints are satisfied.
	if err := db.CreateProject(ctx, storage.Project{
		ID: "test-proj", Name: "Test", Path: "/tmp/test",
		State: "DISABLED", Profile: "LOCAL_REVIEW",
		CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	rec := audit.NewRecorder(db, nil)
	return db, rec, artifactsDir
}

func TestBacklog_AddFreeFormTask(t *testing.T) {
	db, rec, artDir := testDB(t)
	bl := NewBacklog(db, rec, artDir, nil)
	ctx := context.Background()

	task, err := bl.Add(ctx, AddRequest{
		ProjectID:   "test-proj",
		Description: "Fix the login screen — sometimes two progress indicators appear",
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if task.ID == "" {
		t.Error("expected non-empty ID")
	}
	if task.State != StateNew {
		t.Errorf("state=%s, want NEW", task.State)
	}
	if task.Description == "" {
		t.Error("description should be stored")
	}
}

func TestBacklog_DescriptionIsRequiredWithoutAttachment(t *testing.T) {
	db, rec, artDir := testDB(t)
	bl := NewBacklog(db, rec, artDir, nil)

	_, err := bl.Add(context.Background(), AddRequest{
		ProjectID: "test-proj",
	})
	if err == nil {
		t.Fatal("expected error for empty description without attachment")
	}
}

func TestBacklog_AddWithAttachment(t *testing.T) {
	db, rec, artDir := testDB(t)
	bl := NewBacklog(db, rec, artDir, nil)

	// Create a test attachment file.
	attDir := t.TempDir()
	attPath := filepath.Join(attDir, "screenshot.png")
	content := []byte("fake-png-content-for-testing")
	if err := os.WriteFile(attPath, content, 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	task, err := bl.Add(ctx, AddRequest{
		ProjectID:   "test-proj",
		Description: "Fix this screen",
		Attachments: []AttachmentInput{{Path: attPath}},
	})
	if err != nil {
		t.Fatalf("Add with attachment: %v", err)
	}

	// Verify the attachment was stored.
	full, err := bl.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(full.Attachments) != 1 {
		t.Fatalf("attachments=%d, want 1", len(full.Attachments))
	}
	att := full.Attachments[0]
	if att.Filename != "screenshot.png" {
		t.Errorf("filename=%s, want screenshot.png", att.Filename)
	}
	if att.Size != int64(len(content)) {
		t.Errorf("size=%d, want %d", att.Size, len(content))
	}

	// Verify the content hash is correct.
	h := sha256.Sum256(content)
	wantHash := hex.EncodeToString(h[:])
	if att.Hash != wantHash {
		t.Errorf("hash=%s, want %s", att.Hash, wantHash)
	}

	// Verify the file was stored content-addressed.
	storedPath := filepath.Join(artDir, wantHash)
	if _, err := os.Stat(storedPath); err != nil {
		t.Errorf("artifact not stored at %s: %v", storedPath, err)
	}
}

func TestBacklog_OnlyAttachment(t *testing.T) {
	db, rec, artDir := testDB(t)
	bl := NewBacklog(db, rec, artDir, nil)

	attDir := t.TempDir()
	attPath := filepath.Join(attDir, "bug.png")
	if err := os.WriteFile(attPath, []byte("img"), 0o600); err != nil {
		t.Fatal(err)
	}

	// No description, only attachment — should succeed (§9.2).
	_, err := bl.Add(context.Background(), AddRequest{
		ProjectID:   "test-proj",
		Attachments: []AttachmentInput{{Path: attPath}},
	})
	if err != nil {
		t.Fatalf("Add with only attachment: %v", err)
	}
}

func TestBacklog_StateTransitions(t *testing.T) {
	db, rec, artDir := testDB(t)
	bl := NewBacklog(db, rec, artDir, nil)
	ctx := context.Background()

	task, _ := bl.Add(ctx, AddRequest{
		ProjectID:   "test-proj",
		Description: "do something",
	})

	// NEW -> INGESTED
	task, err := bl.Transition(ctx, task.ID, ActionIngest)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if task.State != StateIngested {
		t.Errorf("state=%s, want INGESTED", task.State)
	}

	// INGESTED -> PAUSED
	task, err = bl.Pause(ctx, task.ID)
	if err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if task.State != StatePaused {
		t.Errorf("state=%s, want PAUSED", task.State)
	}

	// PAUSED -> INGESTED (resume)
	task, err = bl.Transition(ctx, task.ID, ActionResume)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if task.State != StateIngested {
		t.Errorf("state=%s, want INGESTED", task.State)
	}

	// INGESTED -> CANCELLED
	task, err = bl.Cancel(ctx, task.ID)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if task.State != StateCancelled {
		t.Errorf("state=%s, want CANCELLED", task.State)
	}
}

func TestBacklog_InvalidTransitionRejected(t *testing.T) {
	db, rec, artDir := testDB(t)
	bl := NewBacklog(db, rec, artDir, nil)
	ctx := context.Background()

	task, _ := bl.Add(ctx, AddRequest{
		ProjectID:   "test-proj",
		Description: "test",
	})

	// Cancel it, then try to pause from CANCELLED.
	_, err := bl.Cancel(ctx, task.ID)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	_, err = bl.Pause(ctx, task.ID)
	if err == nil {
		t.Fatal("expected error: cannot pause from CANCELLED")
	}
}

func TestBacklog_ListByProject(t *testing.T) {
	db, rec, artDir := testDB(t)
	bl := NewBacklog(db, rec, artDir, nil)
	ctx := context.Background()

	bl.Add(ctx, AddRequest{ProjectID: "test-proj", Description: "task 1"})
	bl.Add(ctx, AddRequest{ProjectID: "test-proj", Description: "task 2"})

	tasks, err := bl.ListByProject(ctx, "test-proj")
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("tasks=%d, want 2", len(tasks))
	}
}

func TestBacklog_AuditRecorded(t *testing.T) {
	db, rec, artDir := testDB(t)
	bl := NewBacklog(db, rec, artDir, nil)
	ctx := context.Background()

	task, _ := bl.Add(ctx, AddRequest{
		ProjectID:   "test-proj",
		Description: "audit test",
	})

	events, err := rec.History(ctx, task.ID, 100)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	found := false
	for _, e := range events {
		if e.Type == "task.created" {
			found = true
			break
		}
	}
	if !found {
		t.Error("task.created audit event not found")
	}
}

// TestBacklog_AddCommitsTaskAttachmentAndAuditTogether is a regression test for
// MAJOR-1: a task with an attachment persists the task row, its attachment row,
// and the audit event as one atomic unit (spec §11.4, ADR-0003). There is no
// window in which the task exists without its audit trail or vice-versa.
func TestBacklog_AddCommitsTaskAttachmentAndAuditTogether(t *testing.T) {
	db, rec, artDir := testDB(t)
	bl := NewBacklog(db, rec, artDir, nil)
	ctx := context.Background()

	attDir := t.TempDir()
	attPath := filepath.Join(attDir, "spec.png")
	if err := os.WriteFile(attPath, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}

	task, err := bl.Add(ctx, AddRequest{
		ProjectID:   "test-proj",
		Description: "atomic add",
		Attachments: []AttachmentInput{{Path: attPath}},
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Task row durable.
	st, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("task not durable: %v", err)
	}
	if st.State != string(StateNew) {
		t.Errorf("state=%s, want NEW", st.State)
	}
	// Attachment row durable in the same commit.
	atts, err := db.ListAttachments(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListAttachments: %v", err)
	}
	if len(atts) != 1 {
		t.Fatalf("attachments=%d, want 1 (task+attachment must commit together)", len(atts))
	}
	// Audit event durable in the same commit.
	events, err := db.ListAuditEvents(ctx, storage.AuditFilter{ScopeID: task.ID, Type: "task.created"})
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("task.created audit events=%d, want 1 (task+audit must commit together)", len(events))
	}
}

// TestBacklog_TransitionCommitsStateAndAuditTogether asserts that a task
// lifecycle transition persists the new state and its audit event atomically.
func TestBacklog_TransitionCommitsStateAndAuditTogether(t *testing.T) {
	db, rec, artDir := testDB(t)
	bl := NewBacklog(db, rec, artDir, nil)
	ctx := context.Background()

	task, _ := bl.Add(ctx, AddRequest{ProjectID: "test-proj", Description: "x"})
	if _, err := bl.Transition(ctx, task.ID, ActionIngest); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	st, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if st.State != string(StateIngested) {
		t.Fatalf("state=%s, want INGESTED", st.State)
	}
	events, err := db.ListAuditEvents(ctx, storage.AuditFilter{ScopeID: task.ID, Type: "task.state_changed"})
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("state_changed audit events=%d, want 1", len(events))
	}
}
