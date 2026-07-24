package daemon

import (
	"context"
	"path/filepath"
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
