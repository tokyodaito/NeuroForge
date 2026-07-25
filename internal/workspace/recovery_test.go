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

// setStateForce writes the workspace row's state directly (bypassing transition
// validation) so a test can seed a terminal state and then assert recovery
// cannot leave it. This models a workspace that was finalized in a prior run.
func setStateForce(t *testing.T, db *storage.DB, id string, state workspace.State) {
	t.Helper()
	now := "2026-07-25T00:00:00Z"
	mustExec(t, db, `UPDATE workspaces SET state = ?, updated_at = ? WHERE id = ?`, string(state), now, id)
}

// TestSetState_TerminalToActiveForbidden verifies BF-03 / STATE_MACHINE.md §3.3
// / invariant I.8: every minimal-run terminal state is absorbing — a recovery
// path (or a late event routed through SetState) may NOT move it back to
// active. This is the core state-machine guard against reviving finalized runs.
func TestSetState_TerminalToActiveForbidden(t *testing.T) {
	terminals := []workspace.State{
		workspace.StateCompleted,
		workspace.StateFailed,
		workspace.StateCancelled,
		workspace.StateTimedOut,
	}
	for _, term := range terminals {
		t.Run(string(term)+"->active", func(t *testing.T) {
			wm, db, tmp := newRecoveryManager(t)
			ws := seedWorkspace(t, wm, db, tmp)
			setStateForce(t, db, ws.ID, term)

			if _, err := wm.SetState(context.Background(), ws.ID, workspace.StateActive); err == nil {
				t.Fatalf("%s -> active was allowed (must be forbidden)", term)
			}
			// The state must be unchanged.
			got, err := wm.Get(context.Background(), ws.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.State != term {
				t.Errorf("state changed to %s, want to stay %s", got.State, term)
			}
		})
	}
}

// TestSetState_FailedSelfTransitionIdempotent verifies that re-reconciling an
// already-failed workspace (failed -> failed) is a harmless no-op (the
// reconciler must be idempotent — STATE_MACHINE.md §5.4).
func TestSetState_FailedSelfTransitionIdempotent(t *testing.T) {
	wm, db, tmp := newRecoveryManager(t)
	ws := seedWorkspace(t, wm, db, tmp)
	setStateForce(t, db, ws.ID, workspace.StateFailed)

	if _, err := wm.SetState(context.Background(), ws.ID, workspace.StateFailed); err != nil {
		t.Fatalf("failed -> failed (idempotent re-reconcile) should be allowed: %v", err)
	}
}
