package runapp_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
	"neuroforge/internal/audit"
	"neuroforge/internal/runapp"
	"neuroforge/internal/storage"
	"neuroforge/internal/task"
	"neuroforge/internal/workspace"
)

// finalizeFixture wires a runapp.Service against an in-process DB + workspace
// manager + task backlog rooted at a temp home + a temp git repo. The workspace
// is created and (optionally) seeded with a fake agent run's file changes.
type finalizeFixture struct {
	t        *testing.T
	home     string
	db       *storage.DB
	rec      *audit.Recorder
	wm       *workspace.Manager
	bk       *task.Backlog
	svc      *runapp.Service
	repoPath string
	ws       workspace.Workspace
}

func newFinalizeFixture(t *testing.T) *finalizeFixture {
	t.Helper()
	home := t.TempDir()
	dbPath := filepath.Join(home, "state.db")
	ctx := context.Background()
	db, err := storage.Open(ctx, dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := db.CreateProject(ctx, storage.Project{
		ID: "proj", Name: "Test", Path: "/tmp/test", State: "IDLE",
		Profile: "LOCAL_REVIEW", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(ctx, storage.Task{
		ID: "task-1", ProjectID: "proj", Description: "test", State: "NEW",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	// Build a real temp git repo with one commit so worktree creation works.
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "test@test.local")
	runGit(t, repo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# T\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "init")

	rec := audit.NewRecorder(db, nil)
	wm := workspace.NewManager(db, rec, home, nil)
	bk := task.NewBacklog(db, rec, filepath.Join(home, "artifacts"), nil)
	svc := runapp.NewService(runapp.Options{
		Workspaces: wm,
		Tasks:      bk,
		Audit:      rec,
		DB:         db,
	})

	ws, err := wm.Create(ctx, workspace.CreateRequest{
		ProjectID:   "proj",
		ProjectPath: repo,
		TaskID:      "task-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Pre-transition task to RUNNING (the run-app Run path normally does this).
	if _, err := bk.Transition(ctx, "task-1", task.ActionDispatch); err != nil {
		t.Fatal(err)
	}

	return &finalizeFixture{
		t: t, home: home, db: db, rec: rec, wm: wm, bk: bk, svc: svc,
		repoPath: repo, ws: ws,
	}
}

// commitInWorktree writes a file and commits it inside the workspace's
// worktree, returning the new HEAD SHA. Simulates a "completed-with-commit"
// agent run.
func (f *finalizeFixture) commitInWorktree(path, content, msg string) string {
	f.t.Helper()
	full := filepath.Join(f.ws.Path, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	runGit(f.t, f.ws.Path, "add", "-A")
	runGit(f.t, f.ws.Path, "commit", "-m", msg)
	return readHeadSHA(f.t, f.ws.Path)
}

// writeUncommitted simulates an agent that wrote a file but did not commit.
func (f *finalizeFixture) writeUncommitted(path, content string) {
	f.t.Helper()
	full := filepath.Join(f.ws.Path, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// inspect reads the actual worktree state via the manager.
func (f *finalizeFixture) inspect() workspace.Inspection {
	f.t.Helper()
	ws, err := f.wm.Get(context.Background(), f.ws.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	ins, err := f.wm.InspectWorktree(context.Background(), ws)
	if err != nil {
		f.t.Fatal(err)
	}
	return ins
}

func terminalEvent(t protocol.EventType, class protocol.FailureClass) protocol.NormalizedEvent {
	ev := protocol.NormalizedEvent{Type: t}
	if class != "" {
		ev.Failure = &protocol.FailurePayload{Class: class}
	}
	return ev
}

// TestFinalize_CompletedWithCommit exercises the happy path: agent committed,
// outcome is completed-with-commit, workspace → completed, task → COMPLETED,
// result ref created at the fully-qualified path, audit row written — all in
// one atomic tx (S3 + S5, FR-12/13/14, I.5/I.7/I.8).
func TestFinalize_CompletedWithCommit(t *testing.T) {
	f := newFinalizeFixture(t)
	ctx := context.Background()
	newHEAD := f.commitInWorktree("RESULT.md", "hello\n", "agent work")

	ins := f.inspect()
	res, err := f.svc.Finalize(ctx, runapp.FinalizeRequest{
		WorkspaceID:   f.ws.ID,
		TaskID:        "task-1",
		TerminalEvent: terminalEvent(protocol.EventRunCompleted, ""),
		Inspection:    ins,
		Engine:        "opencode",
		Model:         "zai-coding-plan/glm-5.2",
		RunID:         "run-1",
	})
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if res.Outcome != runapp.OutcomeCompletedWithCommit {
		t.Errorf("outcome = %q, want completed-with-commit", res.Outcome)
	}
	if res.ActualHEAD != newHEAD {
		t.Errorf("ActualHEAD = %q, want %q", res.ActualHEAD, newHEAD)
	}
	if res.CommitSHA != newHEAD {
		t.Errorf("CommitSHA = %q, want %q (the real commit)", res.CommitSHA, newHEAD)
	}
	wantRef := "refs/heads/forge/result/task-1"
	if res.ResultBranch != wantRef {
		t.Errorf("ResultBranch = %q, want %q", res.ResultBranch, wantRef)
	}

	// Workspace must now be terminal: completed.
	ws, _ := f.wm.Get(ctx, f.ws.ID)
	if ws.State != workspace.StateCompleted {
		t.Errorf("workspace state = %q, want completed", ws.State)
	}
	if ws.HeadSHA != newHEAD {
		t.Errorf("workspace head_sha = %q, want %q (FR-10)", ws.HeadSHA, newHEAD)
	}
	if ws.ResultBranch != wantRef {
		t.Errorf("workspace result_branch = %q, want %q", ws.ResultBranch, wantRef)
	}
	if ws.ResultSHA != newHEAD {
		t.Errorf("workspace result_sha = %q, want %q", ws.ResultSHA, newHEAD)
	}

	// Task must be terminal: COMPLETED.
	tk, _ := f.bk.Get(ctx, "task-1")
	if tk.State != task.StateCompleted {
		t.Errorf("task state = %q, want COMPLETED", tk.State)
	}

	// One run.outcome_decided audit row must exist.
	events, _ := f.db.ListAuditEvents(ctx, storage.AuditFilter{ScopeID: f.ws.ID})
	if countEventType(events, "run.outcome_decided") != 1 {
		t.Errorf("expected exactly one run.outcome_decided audit event, got %d", countEventType(events, "run.outcome_decided"))
	}

	// The result ref must resolve under refs/heads/forge/result/<task-id>.
	refSHA, err := f.wm.ResolveResultRef(ctx, "task-1", f.repoPath)
	if err != nil {
		t.Fatalf("resolve ref: %v", err)
	}
	if refSHA != newHEAD {
		t.Errorf("ref resolved to %q, want %q", refSHA, newHEAD)
	}
	// Verify by running git directly against the primary repo.
	directSHA := gitOutput(t, "git", "-C", f.repoPath, "rev-parse", "--verify", wantRef)
	if strings.TrimSpace(directSHA) != newHEAD {
		t.Errorf("git rev-parse %s = %q, want %q", wantRef, directSHA, newHEAD)
	}
}

// TestFinalize_NoChanges verifies KF-01/KF-05/I.4: a process that completed
// without producing changes is classified completed-no-changes, the workspace
// ends up failed, the task ends up FAILED, no result ref is created.
func TestFinalize_NoChanges(t *testing.T) {
	f := newFinalizeFixture(t)
	ctx := context.Background()
	ins := f.inspect()

	res, err := f.svc.Finalize(ctx, runapp.FinalizeRequest{
		WorkspaceID:   f.ws.ID,
		TaskID:        "task-1",
		TerminalEvent: terminalEvent(protocol.EventRunCompleted, ""),
		Inspection:    ins,
		Engine:        "fake",
	})
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if res.Outcome != runapp.OutcomeCompletedNoChanges {
		t.Errorf("outcome = %q, want completed-no-changes", res.Outcome)
	}
	ws, _ := f.wm.Get(ctx, f.ws.ID)
	if ws.State != workspace.StateFailed {
		t.Errorf("state = %q, want failed (a no-change run is a failure)", ws.State)
	}
	tk, _ := f.bk.Get(ctx, "task-1")
	if tk.State != task.StateFailed {
		t.Errorf("task state = %q, want FAILED (KF-06)", tk.State)
	}
	if res.ResultBranch != "" {
		t.Errorf("no result ref should be created, got %q", res.ResultBranch)
	}
}

// TestFinalize_Failed verifies the failed adapter path.
func TestFinalize_Failed(t *testing.T) {
	f := newFinalizeFixture(t)
	ctx := context.Background()
	ins := f.inspect()
	res, err := f.svc.Finalize(ctx, runapp.FinalizeRequest{
		WorkspaceID:   f.ws.ID,
		TaskID:        "task-1",
		TerminalEvent: terminalEvent(protocol.EventRunFailed, protocol.FailureInternalError),
		Inspection:    ins,
	})
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if res.Outcome != runapp.OutcomeFailed {
		t.Errorf("outcome = %q, want failed", res.Outcome)
	}
	ws, _ := f.wm.Get(ctx, f.ws.ID)
	if ws.State != workspace.StateFailed {
		t.Errorf("state = %q, want failed", ws.State)
	}
}

// TestFinalize_Cancelled verifies the cancelled path.
func TestFinalize_Cancelled(t *testing.T) {
	f := newFinalizeFixture(t)
	ctx := context.Background()
	ins := f.inspect()
	res, err := f.svc.Finalize(ctx, runapp.FinalizeRequest{
		WorkspaceID:   f.ws.ID,
		TaskID:        "task-1",
		TerminalEvent: terminalEvent(protocol.EventRunCancelled, ""),
		Inspection:    ins,
	})
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if res.Outcome != runapp.OutcomeCancelled {
		t.Errorf("outcome = %q, want cancelled", res.Outcome)
	}
	ws, _ := f.wm.Get(ctx, f.ws.ID)
	if ws.State != workspace.StateCancelled {
		t.Errorf("state = %q, want cancelled", ws.State)
	}
}

// TestFinalize_TimedOut verifies the timeout path (S6 outcome path; the
// supervisor's terminal event carries class TIMEOUT).
func TestFinalize_TimedOut(t *testing.T) {
	f := newFinalizeFixture(t)
	ctx := context.Background()
	ins := f.inspect()
	res, err := f.svc.Finalize(ctx, runapp.FinalizeRequest{
		WorkspaceID:   f.ws.ID,
		TaskID:        "task-1",
		TerminalEvent: terminalEvent(protocol.EventRunFailed, protocol.FailureTimeout),
		Inspection:    ins,
	})
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if res.Outcome != runapp.OutcomeTimedOut {
		t.Errorf("outcome = %q, want timed-out", res.Outcome)
	}
	ws, _ := f.wm.Get(ctx, f.ws.ID)
	if ws.State != workspace.StateTimedOut {
		t.Errorf("state = %q, want timed_out", ws.State)
	}
}

// TestFinalize_IdempotentDoubleCall verifies OUTCOME_CONTRACT.md §6 / S4:
// calling Finalize twice on the same workspace is a no-op. The second call
// returns the recorded outcome, creates no duplicate result ref, and emits at
// most one run.finalize_idempotent notice (no second run.outcome_decided).
func TestFinalize_IdempotentDoubleCall(t *testing.T) {
	f := newFinalizeFixture(t)
	ctx := context.Background()
	newHEAD := f.commitInWorktree("RESULT.md", "hello\n", "agent work")
	ins := f.inspect()
	req := runapp.FinalizeRequest{
		WorkspaceID:   f.ws.ID,
		TaskID:        "task-1",
		TerminalEvent: terminalEvent(protocol.EventRunCompleted, ""),
		Inspection:    ins,
		Engine:        "opencode",
		RunID:         "run-1",
	}
	first, err := f.svc.Finalize(ctx, req)
	if err != nil {
		t.Fatalf("first Finalize: %v", err)
	}

	// Re-read inspection after first finalize (state changed).
	second, err := f.svc.Finalize(ctx, req)
	if err != nil {
		t.Fatalf("second Finalize: %v", err)
	}
	if !second.Idempotent {
		t.Errorf("second Finalize.Idempotent = false, want true")
	}
	if second.Outcome != first.Outcome {
		t.Errorf("second outcome = %q, want %q", second.Outcome, first.Outcome)
	}
	if second.CommitSHA != first.CommitSHA {
		t.Errorf("second CommitSHA = %q, want %q", second.CommitSHA, first.CommitSHA)
	}
	if second.ResultBranch != first.ResultBranch {
		t.Errorf("second ResultBranch = %q, want %q", second.ResultBranch, first.ResultBranch)
	}

	// Exactly one run.outcome_decided event (no duplicate).
	events, _ := f.db.ListAuditEvents(ctx, storage.AuditFilter{ScopeID: f.ws.ID})
	if n := countEventType(events, "run.outcome_decided"); n != 1 {
		t.Errorf("run.outcome_decided count = %d, want 1 (idempotent finalize)", n)
	}
	// At most one dedup notice.
	if n := countEventType(events, "run.finalize_idempotent"); n > 1 {
		t.Errorf("run.finalize_idempotent count = %d, want <= 1", n)
	}

	// The result ref still resolves to the recorded HEAD; no duplicate.
	refSHA, _ := f.wm.ResolveResultRef(ctx, "task-1", f.repoPath)
	if refSHA != newHEAD {
		t.Errorf("ref = %q, want %q", refSHA, newHEAD)
	}
}

// TestFinalize_TerminalAbsorbing verifies STATE_MACHINE.md §3.3 / I.8:
// a completed workspace may not be revived by a late event. The finalize
// path is idempotent (OUTCOME_CONTRACT.md §6): the late event is absorbed
// silently and the recorded outcome is returned unchanged.
func TestFinalize_TerminalAbsorbing(t *testing.T) {
	f := newFinalizeFixture(t)
	ctx := context.Background()
	f.commitInWorktree("RESULT.md", "x\n", "first")

	ins := f.inspect()
	first, err := f.svc.Finalize(ctx, runapp.FinalizeRequest{
		WorkspaceID:   f.ws.ID,
		TaskID:        "task-1",
		TerminalEvent: terminalEvent(protocol.EventRunCompleted, ""),
		Inspection:    ins,
	})
	if err != nil {
		t.Fatalf("first Finalize: %v", err)
	}
	if first.Outcome != runapp.OutcomeCompletedWithCommit {
		t.Fatalf("first outcome = %q", first.Outcome)
	}

	// A late run.failed event arrives. The workspace is already completed;
	// the idempotent path MUST NOT transition it to failed (invariant I.8).
	late, err := f.svc.Finalize(ctx, runapp.FinalizeRequest{
		WorkspaceID:   f.ws.ID,
		TaskID:        "task-1",
		TerminalEvent: terminalEvent(protocol.EventRunFailed, protocol.FailureInternalError),
		Inspection:    ins,
	})
	if err != nil {
		t.Fatalf("late Finalize: %v", err)
	}
	if !late.Idempotent {
		t.Errorf("late Finalize.Idempotent = false; the workspace is terminal")
	}
	// The recorded outcome (completed) is preserved — a late failure must
	// not overwrite it (STATE_MACHINE.md §3.3 / I.8).
	ws, _ := f.wm.Get(ctx, f.ws.ID)
	if ws.State != workspace.StateCompleted {
		t.Errorf("workspace state = %q, want completed (terminal is absorbing)", ws.State)
	}
}

// TestFinalize_IllegalPendingToCompleted verifies STATE_MACHINE.md §3.3:
// pending → completed is forbidden (must pass through active). We seed a
// pending workspace and try to finalize it directly into a completed-*
// outcome. The service refuses the transition.
func TestFinalize_IllegalPendingToCompleted(t *testing.T) {
	f := newFinalizeFixture(t)
	ctx := context.Background()
	// Manually reset the workspace to pending via storage. The worktree
	// still exists (wm.Create made it), so InspectWorktree succeeds.
	if _, err := f.db.Exec(ctx,
		`UPDATE workspaces SET state = 'pending' WHERE id = ?`, f.ws.ID); err != nil {
		t.Fatal(err)
	}
	// Add a commit so the classifier yields completed-with-commit (target
	// state = completed, which is forbidden from pending).
	f.commitInWorktree("RESULT.md", "x\n", "agent work")
	_, err := f.svc.Finalize(ctx, runapp.FinalizeRequest{
		WorkspaceID:   f.ws.ID,
		TaskID:        "task-1",
		TerminalEvent: terminalEvent(protocol.EventRunCompleted, ""),
		Inspection:    f.inspect(),
	})
	if err == nil {
		t.Fatalf("expected ErrIllegalTransition, got nil")
	}
	var it *runapp.ErrIllegalTransition
	if !errors.As(err, &it) {
		t.Fatalf("expected ErrIllegalTransition, got %v", err)
	}
}

// TestFinalize_Atomicity_SavesAuditAndStateTogether verifies STATE_MACHINE.md
// §3.4: the audit row and the workspace state commit atomically. We trigger
// the audit append path indirectly (recorder is non-nil) and verify BOTH the
// state and the audit row are present after a successful finalize.
func TestFinalize_Atomicity_SavesAuditAndStateTogether(t *testing.T) {
	f := newFinalizeFixture(t)
	ctx := context.Background()
	f.commitInWorktree("RESULT.md", "y\n", "agent work")
	if _, err := f.svc.Finalize(ctx, runapp.FinalizeRequest{
		WorkspaceID:   f.ws.ID,
		TaskID:        "task-1",
		TerminalEvent: terminalEvent(protocol.EventRunCompleted, ""),
		Inspection:    f.inspect(),
	}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	ws, _ := f.wm.Get(ctx, f.ws.ID)
	if ws.State != workspace.StateCompleted {
		t.Fatalf("workspace state = %q, want completed", ws.State)
	}
	events, _ := f.db.ListAuditEvents(ctx, storage.AuditFilter{ScopeID: f.ws.ID})
	if countEventType(events, "run.outcome_decided") != 1 {
		t.Errorf("expected exactly one run.outcome_decided event")
	}
}

// TestFinalize_UncommittedChanges verifies OUTCOME_CONTRACT.md §1.1 /
// I.6: agent wrote a file but did not commit. Outcome is
// completed-with-uncommitted-changes, workspace → completed, task → COMPLETED,
// changed_files non-empty, commit_sha equals actual head (which equals base),
// a result ref IS created at base.
func TestFinalize_UncommittedChanges(t *testing.T) {
	f := newFinalizeFixture(t)
	ctx := context.Background()
	f.writeUncommitted("scratch.txt", "dirty\n")
	ins := f.inspect()
	res, err := f.svc.Finalize(ctx, runapp.FinalizeRequest{
		WorkspaceID:   f.ws.ID,
		TaskID:        "task-1",
		TerminalEvent: terminalEvent(protocol.EventRunCompleted, ""),
		Inspection:    ins,
	})
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if res.Outcome != runapp.OutcomeCompletedWithUncommittedChanges {
		t.Errorf("outcome = %q, want completed-with-uncommitted-changes", res.Outcome)
	}
	ws, _ := f.wm.Get(ctx, f.ws.ID)
	if ws.State != workspace.StateCompleted {
		t.Errorf("workspace state = %q, want completed", ws.State)
	}
	if len(res.ChangedFiles) == 0 {
		t.Errorf("changed_files should be non-empty for uncommitted changes")
	}
	if !containsStr(res.ChangedFiles, "scratch.txt") {
		t.Errorf("changed_files = %v, want scratch.txt", res.ChangedFiles)
	}
	if res.ResultBranch != "refs/heads/forge/result/task-1" {
		t.Errorf("result_branch = %q, want refs/heads/forge/result/task-1", res.ResultBranch)
	}
}

// ---- helpers ----

func countEventType(events []storage.AuditEvent, eventType string) int {
	n := 0
	for _, e := range events {
		if e.Type == eventType {
			n++
		}
	}
	return n
}

func containsStr(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}
