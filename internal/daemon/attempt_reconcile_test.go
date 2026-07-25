package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"neuroforge/internal/audit"
	"neuroforge/internal/storage"
	"neuroforge/internal/workspace"
)

// newAttemptFixture builds a real workspace manager + a workspace backed by a
// real git repo so the attempt reconciler can exercise its decisions.
func newAttemptFixture(t *testing.T) (*workspace.Manager, *storage.DB, *audit.Recorder, workspace.Workspace, string) {
	t.Helper()
	dirs := WithRoot(t.TempDir())
	db, err := storage.Open(context.Background(), dirs.StateDB, &storage.Options{Logger: quietLogger2()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec := audit.NewRecorder(db, quietLogger2())
	wm := workspace.NewManager(db, rec, dirs.Root, quietLogger2())

	repoPath := filepath.Join(dirs.Root, "repo")
	runGitInDaemonTest(t, repoPath, "init", "-b", "main")
	runGitInDaemonTest(t, repoPath, "config", "user.email", "t@t.com")
	runGitInDaemonTest(t, repoPath, "config", "user.name", "T")
	mustWriteDaemonTest(t, filepath.Join(repoPath, "README.md"), "# x\n")
	runGitInDaemonTest(t, repoPath, "add", "-A")
	runGitInDaemonTest(t, repoPath, "commit", "-m", "init")

	now := "2026-07-25T00:00:00Z"
	mustExecDaemonTest(t, db, `INSERT INTO projects (id,name,path,remote,state,profile,created_at,updated_at) VALUES ('p','P',?,?,'IDLE','LOCAL_REVIEW',?,?)`, repoPath, "", now, now)
	mustExecDaemonTest(t, db, `INSERT INTO tasks (id,project_id,title,description,priority,state,created_at,updated_at) VALUES ('tk','p','T','d','NORMAL','NEW',?,?)`, now, now)
	ws, err := wm.Create(context.Background(), workspace.CreateRequest{
		ProjectID: "p", ProjectPath: repoPath, TaskID: "tk",
	})
	if err != nil {
		t.Fatal(err)
	}
	return wm, db, rec, ws, dirs.Root
}

func TestAttemptReconciler_ActiveWithPack_IsResumable(t *testing.T) {
	wm, db, rec, ws, _ := newAttemptFixture(t)

	// Simulate an interrupted run: a checkpoint + a continuation pack exist,
	// but the workspace is "active" (mid-run when the daemon died).
	mustWriteDaemonTest(t, filepath.Join(ws.Path, "edit.txt"), "edit\n")
	if _, err := wm.Checkpoint(context.Background(), ws.ID, workspace.MomentPreQuotaSwitch, "before failover"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateContinuationPack(context.Background(), storage.ContinuationPack{
		WorkspaceID: ws.ID, FilePath: "/tmp/pack.json", BaseSHA: ws.BaseSHA, CurrentSHA: ws.HeadSHA,
		CreatedAt: "2026-07-25T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	r := &attemptReconciler{wm: wm}
	tx := ReconcileTx{DB: db, Audit: rec, Logger: quietLogger2()}
	decisions, err := r.Reconcile(context.Background(), tx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	found := false
	for _, d := range decisions {
		if d.Entity == "workspace:"+ws.ID {
			found = true
			if d.Action != DecisionKept {
				t.Errorf("resumable active workspace: want kept, got %s (detail=%s)", d.Action, d.Detail)
			}
		}
	}
	if !found {
		t.Fatal("no decision for the workspace")
	}

	// The interrupted run was marked failed (so it is not treated as live).
	updated, _ := wm.Get(context.Background(), ws.ID)
	if updated.State != workspace.StateFailed {
		t.Errorf("state = %s, want failed (interrupted run is stale)", updated.State)
	}
}

func TestAttemptReconciler_WaitingQuota_KeptIfIntact(t *testing.T) {
	wm, db, rec, ws, _ := newAttemptFixture(t)
	if _, err := wm.SetState(context.Background(), ws.ID, workspace.StateWaitingQuota); err != nil {
		t.Fatal(err)
	}

	r := &attemptReconciler{wm: wm}
	tx := ReconcileTx{DB: db, Audit: rec, Logger: quietLogger2()}
	decisions, err := r.Reconcile(context.Background(), tx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	for _, d := range decisions {
		if d.Entity == "workspace:"+ws.ID {
			if d.Action != DecisionKept {
				t.Errorf("waiting_quota intact: want kept, got %s", d.Action)
			}
		}
	}
}

func TestAttemptReconciler_ActiveNoPack_MarkedStale(t *testing.T) {
	wm, db, rec, ws, _ := newAttemptFixture(t)
	// Active, no checkpoint, no pack: the run is lost.
	r := &attemptReconciler{wm: wm}
	tx := ReconcileTx{DB: db, Audit: rec, Logger: quietLogger2()}
	decisions, err := r.Reconcile(context.Background(), tx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	for _, d := range decisions {
		if d.Entity == "workspace:"+ws.ID {
			if d.Action != DecisionMarkedStale {
				t.Errorf("active no pack: want marked-stale, got %s", d.Action)
			}
		}
	}
	updated, _ := wm.Get(context.Background(), ws.ID)
	if updated.State != workspace.StateFailed {
		t.Errorf("state = %s, want failed", updated.State)
	}
}

// TestAttemptReconciler_InterruptedTransitionsTaskAndAudits verifies BF-03 /
// STATE_MACHINE.md §5.1 + §4.3 + OUTCOME_CONTRACT.md §1.1: when the reconciler
// finds an active workspace whose run was interrupted by the daemon restart, it
// (1) marks the workspace failed, (2) moves the owning RUNNING task to FAILED
// (task and workspace agree), and (3) records a durable `interrupted`
// run.outcome_decided audit event. Re-reconciling is idempotent: the task is
// not revived and no second interrupted event is required.
func TestAttemptReconciler_InterruptedTransitionsTaskAndAudits(t *testing.T) {
	wm, db, rec, ws, _ := newAttemptFixture(t)

	// The task was seeded NEW; an in-flight run would have it RUNNING.
	now := "2026-07-25T00:00:00Z"
	mustExecDaemonTest(t, db, `UPDATE tasks SET state = ?, updated_at = ? WHERE id = ?`, "RUNNING", now, ws.TaskID)

	r := &attemptReconciler{wm: wm}
	tx := ReconcileTx{DB: db, Audit: rec, Logger: quietLogger2()}
	if _, err := r.Reconcile(context.Background(), tx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// Workspace -> failed.
	updated, _ := wm.Get(context.Background(), ws.ID)
	if updated.State != workspace.StateFailed {
		t.Fatalf("workspace state = %s, want failed", updated.State)
	}
	// Task -> FAILED (agrees with the terminal workspace; never revived).
	tk, err := db.GetTask(context.Background(), ws.TaskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if tk.State != "FAILED" {
		t.Errorf("task state = %s, want FAILED (must agree with terminal workspace)", tk.State)
	}

	// An interrupted outcome audit event exists.
	events, err := db.ListAuditEvents(context.Background(), storage.AuditFilter{ScopeID: ws.ID})
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	foundInterrupted := false
	for _, e := range events {
		if e.Type == "run.outcome_decided" && auditContains(e.Payload, `"outcome":"interrupted"`) {
			foundInterrupted = true
		}
	}
	if !foundInterrupted {
		t.Errorf("no run.outcome_decided interrupted audit event recorded")
	}

	// Idempotency: re-reconcile does not revive the task and keeps the
	// workspace failed.
	if _, err := r.Reconcile(context.Background(), tx); err != nil {
		t.Fatalf("re-reconcile: %v", err)
	}
	tk2, _ := db.GetTask(context.Background(), ws.TaskID)
	if tk2.State != "FAILED" {
		t.Errorf("after re-reconcile task state = %s, want FAILED (revived?)", tk2.State)
	}
}

// auditContains is a substring check on the JSON payload (stable enough for the
// test's injected event).
func auditContains(payload, want string) bool {
	return strings.Contains(payload, want)
}
