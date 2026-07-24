package workspace_test

import (
	"context"
	"path/filepath"
	"testing"

	"neuroforge/internal/audit"
	"neuroforge/internal/storage"
	"neuroforge/internal/workspace"
)

func newRecoveryManager(t *testing.T) (*workspace.Manager, *storage.DB, string) {
	t.Helper()
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "state.db")
	db, err := storage.Open(context.Background(), dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec := audit.NewRecorder(db, nil)
	wm := workspace.NewManager(db, rec, tmp, nil)
	return wm, db, tmp
}

// seedRepo creates a git repo + project + task + workspace and returns the
// workspace id.
func seedWorkspace(t *testing.T, wm *workspace.Manager, db *storage.DB, tmp string) workspace.Workspace {
	t.Helper()
	repoPath := filepath.Join(tmp, "repo")
	runGitIn(t, repoPath, "init", "-b", "main")
	runGitIn(t, repoPath, "config", "user.email", "t@t.com")
	runGitIn(t, repoPath, "config", "user.name", "T")
	mustWrite(t, filepath.Join(repoPath, "README.md"), "# x\n")
	runGitIn(t, repoPath, "add", "-A")
	runGitIn(t, repoPath, "commit", "-m", "init")

	now := "2026-07-25T00:00:00Z"
	mustExec(t, db, `INSERT INTO projects (id,name,path,remote,state,profile,created_at,updated_at) VALUES ('p','P',?,?,'IDLE','LOCAL_REVIEW',?,?)`, repoPath, "", now, now)
	mustExec(t, db, `INSERT INTO tasks (id,project_id,title,description,priority,state,created_at,updated_at) VALUES ('tk','p','T','d','NORMAL','NEW',?,?)`, now, now)

	ws, err := wm.Create(context.Background(), workspace.CreateRequest{
		ProjectID: "p", ProjectPath: repoPath, TaskID: "tk",
	})
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

func TestSetState_WaitingQuota(t *testing.T) {
	wm, db, tmp := newRecoveryManager(t)
	ws := seedWorkspace(t, wm, db, tmp)

	updated, err := wm.SetState(context.Background(), ws.ID, workspace.StateWaitingQuota)
	if err != nil {
		t.Fatalf("SetState waiting_quota: %v", err)
	}
	if updated.State != workspace.StateWaitingQuota {
		t.Errorf("state = %s, want waiting_quota", updated.State)
	}

	// waiting_quota -> active is legal (route becomes available).
	if _, err := wm.SetState(context.Background(), ws.ID, workspace.StateActive); err != nil {
		t.Fatalf("SetState active: %v", err)
	}
}

func TestSetState_Quarantined(t *testing.T) {
	wm, db, tmp := newRecoveryManager(t)
	ws := seedWorkspace(t, wm, db, tmp)

	updated, err := wm.SetState(context.Background(), ws.ID, workspace.StateQuarantined)
	if err != nil {
		t.Fatalf("SetState quarantined: %v", err)
	}
	if updated.State != workspace.StateQuarantined {
		t.Errorf("state = %s, want quarantined", updated.State)
	}
}

func TestSetState_RejectedIsTerminal(t *testing.T) {
	wm, db, tmp := newRecoveryManager(t)
	ws := seedWorkspace(t, wm, db, tmp)

	// Reject first.
	if _, err := wm.Review(context.Background(), ws.ID, workspace.ActionReject); err != nil {
		t.Fatal(err)
	}
	// A rejected workspace cannot transition to recovery states.
	if _, err := wm.SetState(context.Background(), ws.ID, workspace.StateWaitingQuota); err == nil {
		t.Error("expected error transitioning from rejected to waiting_quota")
	}
}

func TestRetainCheckpoints_PruneExcess(t *testing.T) {
	wm, db, tmp := newRecoveryManager(t)
	ws := seedWorkspace(t, wm, db, tmp)

	// Create 5 checkpoints.
	for i := 0; i < 5; i++ {
		mustWrite(t, filepath.Join(ws.Path, "f.txt"), string(rune('a'+i)))
		if _, err := wm.Checkpoint(context.Background(), ws.ID, workspace.MomentManual, "cp"); err != nil {
			t.Fatal(err)
		}
	}

	// Keep only the 2 most recent.
	cfg := workspace.RetentionConfig{KeepMostRecent: 2}
	pruned, err := wm.RetainCheckpoints(context.Background(), ws.ID, cfg)
	if err != nil {
		t.Fatalf("RetainCheckpoints: %v", err)
	}
	if pruned != 3 {
		t.Errorf("pruned = %d, want 3", pruned)
	}

	cps, err := wm.ListCheckpoints(context.Background(), ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cps) != 2 {
		t.Errorf("remaining checkpoints = %d, want 2", len(cps))
	}
}

func TestRetainCheckpoints_KeepAllWhenWithinLimit(t *testing.T) {
	wm, db, tmp := newRecoveryManager(t)
	ws := seedWorkspace(t, wm, db, tmp)

	mustWrite(t, filepath.Join(ws.Path, "f.txt"), "x")
	if _, err := wm.Checkpoint(context.Background(), ws.ID, workspace.MomentManual, "cp"); err != nil {
		t.Fatal(err)
	}
	pruned, err := wm.RetainCheckpoints(context.Background(), ws.ID, workspace.RetentionConfig{KeepMostRecent: 20})
	if err != nil {
		t.Fatal(err)
	}
	if pruned != 0 {
		t.Errorf("pruned = %d, want 0", pruned)
	}
}
