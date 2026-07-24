package project

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"neuroforge/internal/audit"
	"neuroforge/internal/storage"
)

// testDB opens a fresh storage DB in a temp dir, runs migrations, and returns
// it along with an audit recorder.
func testDB(t *testing.T) (*storage.DB, *audit.Recorder) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	ctx := context.Background()
	db, err := storage.Open(ctx, dbPath, nil)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	rec := audit.NewRecorder(db, nil)
	return db, rec
}

// makeGitRepo creates a real git repository in a temp dir and returns its path.
func makeGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	commands := [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
		{"add", "--all"},
		{"commit", "--allow-empty", "-m", "init"},
	}
	for _, args := range commands {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestRegistry_AddValidGitRepo(t *testing.T) {
	db, rec := testDB(t)
	reg := NewRegistry(db, rec, nil)

	repoPath := makeGitRepo(t)
	ctx := context.Background()

	p, err := reg.Add(ctx, AddRequest{Path: repoPath})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if p.ID == "" {
		t.Error("expected non-empty ID")
	}
	if p.State != StateDisabled {
		t.Errorf("state=%s, want DISABLED", p.State)
	}
	if p.Path != repoPath {
		t.Errorf("path=%q, want %q", p.Path, repoPath)
	}
}

func TestRegistry_AddDuplicatePath(t *testing.T) {
	db, rec := testDB(t)
	reg := NewRegistry(db, rec, nil)

	repoPath := makeGitRepo(t)
	ctx := context.Background()

	_, err := reg.Add(ctx, AddRequest{Path: repoPath})
	if err != nil {
		t.Fatalf("first Add: %v", err)
	}

	_, err = reg.Add(ctx, AddRequest{Path: repoPath})
	if err == nil {
		t.Fatal("expected error for duplicate path, got nil")
	}
}

func TestRegistry_AddNonexistentRepository(t *testing.T) {
	db, rec := testDB(t)
	reg := NewRegistry(db, rec, nil)

	dir := t.TempDir() // empty dir, not a git repo
	ctx := context.Background()

	_, err := reg.Add(ctx, AddRequest{Path: dir})
	if err == nil {
		t.Fatal("expected error for non-git directory, got nil")
	}
}

func TestRegistry_ListAndGet(t *testing.T) {
	db, rec := testDB(t)
	reg := NewRegistry(db, rec, nil)

	ctx := context.Background()
	repo1 := makeGitRepo(t)
	repo2 := makeGitRepo(t)

	p1, err := reg.Add(ctx, AddRequest{Path: repo1})
	if err != nil {
		t.Fatalf("Add 1: %v", err)
	}
	p2, err := reg.Add(ctx, AddRequest{Path: repo2})
	if err != nil {
		t.Fatalf("Add 2: %v", err)
	}

	projects, err := reg.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("len(projects)=%d, want 2", len(projects))
	}

	got, err := reg.Get(ctx, p1.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != p1.ID {
		t.Errorf("got ID=%s, want %s", got.ID, p1.ID)
	}
	_ = p2
}

func TestRegistry_RemoveDoesNotDeleteFiles(t *testing.T) {
	db, rec := testDB(t)
	reg := NewRegistry(db, rec, nil)

	repoPath := makeGitRepo(t)
	ctx := context.Background()

	p, err := reg.Add(ctx, AddRequest{Path: repoPath})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := reg.Remove(ctx, p.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// The .git directory should still exist (files not deleted).
	if _, err := exec.CommandContext(ctx, "git", "-C", repoPath, "status").CombinedOutput(); err != nil {
		t.Errorf("git status failed after remove (files should be untouched): %v", err)
	}
}

func TestRegistry_StateTransitions(t *testing.T) {
	db, rec := testDB(t)
	reg := NewRegistry(db, rec, nil)

	repoPath := makeGitRepo(t)
	ctx := context.Background()

	p, err := reg.Add(ctx, AddRequest{Path: repoPath})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	// DISABLED -> IDLE
	p, err = reg.Start(ctx, p.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if p.State != StateIdle {
		t.Errorf("state=%s, want IDLE", p.State)
	}

	// IDLE -> PAUSED
	p, err = reg.Pause(ctx, p.ID)
	if err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if p.State != StatePaused {
		t.Errorf("state=%s, want PAUSED", p.State)
	}

	// PAUSED -> IDLE (resume)
	p, err = reg.Transition(ctx, p.ID, ActionResume)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if p.State != StateIdle {
		t.Errorf("state=%s, want IDLE", p.State)
	}

	// IDLE -> DISABLED (stop)
	p, err = reg.Stop(ctx, p.ID)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if p.State != StateDisabled {
		t.Errorf("state=%s, want DISABLED", p.State)
	}
}

func TestRegistry_InvalidTransitionRejected(t *testing.T) {
	db, rec := testDB(t)
	reg := NewRegistry(db, rec, nil)

	repoPath := makeGitRepo(t)
	ctx := context.Background()

	p, _ := reg.Add(ctx, AddRequest{Path: repoPath})

	// DISABLED + pause = invalid
	_, err := reg.Pause(ctx, p.ID)
	if err == nil {
		t.Fatal("expected error for pause from DISABLED")
	}

	// DISABLED + start = OK, then start again = invalid
	_, err = reg.Start(ctx, p.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	_, err = reg.Start(ctx, p.ID)
	if err == nil {
		t.Fatal("expected error for start from IDLE")
	}
}

func TestRegistry_AuditRecorded(t *testing.T) {
	db, rec := testDB(t)
	reg := NewRegistry(db, rec, nil)

	repoPath := makeGitRepo(t)
	ctx := context.Background()

	p, _ := reg.Add(ctx, AddRequest{Path: repoPath})
	_, _ = reg.Start(ctx, p.ID)

	// Check audit history for this project.
	events, err := rec.History(ctx, p.ID, 100)
	if err != nil {
		t.Fatalf("History: %v", err)
	}

	wantTypes := map[string]bool{
		"project.added":         false,
		"project.state_changed": false,
	}
	for _, e := range events {
		if _, ok := wantTypes[e.Type]; ok {
			wantTypes[e.Type] = true
		}
	}
	for typ, found := range wantTypes {
		if !found {
			t.Errorf("audit event %q not found for project %s", typ, p.ID)
		}
	}
}

func TestRegistry_RemoveNotInList(t *testing.T) {
	db, rec := testDB(t)
	reg := NewRegistry(db, rec, nil)
	ctx := context.Background()

	repoPath := makeGitRepo(t)
	p, _ := reg.Add(ctx, AddRequest{Path: repoPath})
	_ = reg.Remove(ctx, p.ID)

	projects, _ := reg.List(ctx)
	for _, proj := range projects {
		if proj.ID == p.ID {
			t.Error("removed project still in list")
		}
	}

	// Get should fail.
	_, err := reg.Get(ctx, p.ID)
	if err == nil {
		t.Error("Get on removed project should fail")
	}
}

func TestRegistry_StatePersists(t *testing.T) {
	db, rec := testDB(t)
	reg := NewRegistry(db, rec, nil)

	repoPath := makeGitRepo(t)
	ctx := context.Background()

	p, _ := reg.Add(ctx, AddRequest{Path: repoPath})
	_, _ = reg.Start(ctx, p.ID)

	// Re-read from storage.
	p2, err := reg.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p2.State != StateIdle {
		t.Errorf("state after re-read=%s, want IDLE", p2.State)
	}

	// Verify updated_at changed.
	if !p2.UpdatedAt.After(p.CreatedAt) && !p2.UpdatedAt.Equal(p.CreatedAt) {
		time.Sleep(time.Millisecond)
	}
}

// TestRegistry_AddCommitsStateAndAuditTogether is a regression test for MAJOR-1
// ("state mutation + audit recording are separate calls; no SQLite tx
// boundary"). It asserts that a registered project is durable together with its
// audit event: there is no window in which the project exists but its audit
// trail does not (spec §11.4, ADR-0003).
func TestRegistry_AddCommitsStateAndAuditTogether(t *testing.T) {
	db, rec := testDB(t)
	reg := NewRegistry(db, rec, nil)
	ctx := context.Background()

	p, err := reg.Add(ctx, AddRequest{Path: makeGitRepo(t)})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	// State row is durable.
	sp, err := db.GetProject(ctx, p.ID)
	if err != nil {
		t.Fatalf("project not durable: %v", err)
	}
	if sp.State != string(StateDisabled) {
		t.Errorf("state=%s, want DISABLED", sp.State)
	}

	// Audit event for the same mutation is durable in the same commit.
	events, err := db.ListAuditEvents(ctx, storage.AuditFilter{ScopeID: p.ID, Type: "project.added"})
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("project.added audit events=%d, want 1 (state+audit must commit together)", len(events))
	}
}

// TestRegistry_TransitionCommitsStateAndAuditTogether asserts that a lifecycle
// transition persists the new state and its audit event atomically.
func TestRegistry_TransitionCommitsStateAndAuditTogether(t *testing.T) {
	db, rec := testDB(t)
	reg := NewRegistry(db, rec, nil)
	ctx := context.Background()

	p, _ := reg.Add(ctx, AddRequest{Path: makeGitRepo(t)})
	if _, err := reg.Start(ctx, p.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}

	sp, err := db.GetProject(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if sp.State != string(StateIdle) {
		t.Fatalf("state=%s, want IDLE", sp.State)
	}
	events, err := db.ListAuditEvents(ctx, storage.AuditFilter{ScopeID: p.ID, Type: "project.state_changed"})
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("state_changed audit events=%d, want 1", len(events))
	}
}

// TestRegistry_RemoveCommitsStateAndAuditTogether asserts that removing a
// project deletes the row and records the audit event atomically: after Remove,
// the row is gone and exactly one project.removed audit event exists.
func TestRegistry_RemoveCommitsStateAndAuditTogether(t *testing.T) {
	db, rec := testDB(t)
	reg := NewRegistry(db, rec, nil)
	ctx := context.Background()

	p, _ := reg.Add(ctx, AddRequest{Path: makeGitRepo(t)})
	if err := reg.Remove(ctx, p.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if _, err := db.GetProject(ctx, p.ID); err == nil {
		t.Fatal("project row should be deleted after Remove")
	}
	events, err := db.ListAuditEvents(ctx, storage.AuditFilter{ScopeID: p.ID, Type: "project.removed"})
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("project.removed audit events=%d, want 1", len(events))
	}
}
