package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"neuroforge/internal/adapter/codingagent/protocol"
	"neuroforge/internal/runapp"
	"neuroforge/internal/storage"
	"neuroforge/internal/task"
	"neuroforge/internal/workspace"
)

// TestReconcileOrder_FinalizeIntentBeforeAttempts is the M3 regression test:
// a legacy runapp run that crashed mid-finalize leaves an ACTIVE workspace
// with a durable finalize intent. The startup reconcilers must run the
// finalize-intent reconciler BEFORE the attempt reconciler — otherwise the
// attempt reconciler marks the workspace failed and the intent is deleted
// uncompleted. Run in the production order, the intent completes the
// workspace and the attempt reconciler leaves it alone.
func TestReconcileOrder_FinalizeIntentBeforeAttempts(t *testing.T) {
	env := newFaultEnv(t, faultDeps{})
	ctx := context.Background()

	tk, err := env.tasks.Add(ctx, task.AddRequest{ProjectID: env.projID, Description: "reconcile order"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.tasks.Transition(ctx, tk.ID, task.ActionDispatch); err != nil {
		t.Fatal(err)
	}
	ws, err := env.wm.Create(ctx, workspace.CreateRequest{
		ProjectID:   env.projID,
		ProjectPath: env.repo,
		TaskID:      tk.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	mustWriteDaemonTest(t, filepath.Join(ws.Path, "RESULT.md"), "reconcile order\n")
	runGitInDaemonTest(t, ws.Path, "add", "-A")
	runGitInDaemonTest(t, ws.Path, "commit", "-m", "agent work",
		"--author=NeuroForge Fake <fake@neuroforge.local>")
	insp, err := env.wm.InspectWorktree(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}

	// Crash after the intent is durable, before the terminal commit — the
	// workspace stays ACTIVE, exactly the shape that confused the attempt
	// reconciler before the M3 reorder.
	crashErr := errors.New("test: simulated crash after intent")
	env.svc.fin.SetTestHooks(func() error { return crashErr }, nil)
	if _, err := env.svc.fin.Finalize(ctx, runapp.FinalizeRequest{
		WorkspaceID:   ws.ID,
		TaskID:        tk.ID,
		TerminalEvent: protocol.NormalizedEvent{Type: protocol.EventRunCompleted},
		Inspection:    insp,
		Engine:        "fake",
		Model:         "fake/write-commit",
		RunID:         "run-reconcile-order",
	}); !errors.Is(err, crashErr) {
		t.Fatalf("want crash-after-intent, got %v", err)
	}
	if wsNow, _ := env.wm.Get(ctx, ws.ID); wsNow.State != workspace.StateActive {
		t.Fatalf("workspace state = %s, want active (crashed mid-finalize)", wsNow.State)
	}

	// Production order (daemon.go): finalize intents first, then attempts.
	tx := ReconcileTx{DB: env.db, Audit: env.rec, Dirs: env.dirs, Logger: quietLogger()}
	finReconciler := newFinalizeIntentReconciler(env.db, env.rec, env.wm, quietLogger())
	if _, err := finReconciler.Reconcile(ctx, tx); err != nil {
		t.Fatalf("finalize reconcile: %v", err)
	}
	attemptDecisions, err := (&attemptReconciler{wm: env.wm}).Reconcile(ctx, tx)
	if err != nil {
		t.Fatalf("attempt reconcile: %v", err)
	}

	// The intent was completed, not deleted uncompleted.
	if _, err := env.db.GetFinalizeIntent(ctx, ws.ID); !errors.Is(err, storage.ErrFinalizeIntentNotFound) {
		t.Errorf("intent should be cleared after recovery, got %v", err)
	}
	wsAfter, err := env.wm.Get(ctx, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wsAfter.State != workspace.StateCompleted {
		t.Errorf("workspace state = %s, want completed (the M3 bug marked it failed)", wsAfter.State)
	}
	ref := "refs/heads/forge/result/" + tk.ID
	if sha := strings.TrimSpace(faultGitOut(t, env.repo, "rev-parse", "--verify", ref)); sha == "" {
		t.Errorf("result ref %s missing after ordered reconcile", ref)
	}

	// The attempt reconciler must NOT have touched the completed workspace.
	for _, d := range attemptDecisions {
		if d.Entity == "workspace:"+ws.ID && d.Action != DecisionNoOp {
			t.Errorf("attempt reconciler acted on the finalized workspace: %+v", d)
		}
	}
}
