package runapp_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"neuroforge/internal/adapter/codingagent/protocol"
	"neuroforge/internal/audit"
	"neuroforge/internal/runapp"
	"neuroforge/internal/storage"
	"neuroforge/internal/task"
	"neuroforge/internal/workspace"
)

// faultWM wraps a *workspace.Manager and lets a test (a) force the in-tx
// workspace-state update to fail AFTER the result ref was created, and (b)
// observe whether finalize's compensating DeleteResultRef ran. It implements
// runapp.WorkspaceManager.
type faultWM struct {
	inner           *workspace.Manager
	failUpdateState atomic.Bool
	deleteCalled    atomic.Int32
}

func (f *faultWM) Get(ctx context.Context, id string) (workspace.Workspace, error) {
	return f.inner.Get(ctx, id)
}
func (f *faultWM) InspectWorktree(ctx context.Context, ws workspace.Workspace) (workspace.Inspection, error) {
	return f.inner.InspectWorktree(ctx, ws)
}
func (f *faultWM) EnsureResultRef(ctx context.Context, ws workspace.Workspace, headSHA string) (string, error) {
	return f.inner.EnsureResultRef(ctx, ws, headSHA)
}
func (f *faultWM) DeleteResultRef(ctx context.Context, ws workspace.Workspace) error {
	f.deleteCalled.Add(1)
	return f.inner.DeleteResultRef(ctx, ws)
}
func (f *faultWM) ResolveResultRef(ctx context.Context, taskID, dir string) (string, error) {
	return f.inner.ResolveResultRef(ctx, taskID, dir)
}
func (f *faultWM) UpdateStateTx(ctx context.Context, tx *storage.Tx, id string, state workspace.State, headSHA, resultBranch, resultSHA string) error {
	if f.failUpdateState.Load() {
		return errInjectedFault
	}
	return f.inner.UpdateStateTx(ctx, tx, id, state, headSHA, resultBranch, resultSHA)
}

var errInjectedFault = errFault{}

type errFault struct{}

func (errFault) Error() string { return "injected finalize fault (BF-07)" }

// isMinimalTerminal reports whether a workspace state is a minimal-run terminal
// (must not be reached when a finalize is supposed to roll back).
func isMinimalTerminal(s workspace.State) bool {
	switch s {
	case workspace.StateCompleted, workspace.StateFailed, workspace.StateCancelled,
		workspace.StateTimedOut, workspace.StateKept, workspace.StateRejected, workspace.StateDeleted:
		return true
	}
	return false
}

// TestFinalize_Atomicity_CompensatesRefOnTxFailure (BF-07 / STATE_MACHINE.md
// §3.4): models process crash after result ref is created and before the
// terminal SQLite commit. Finalize leaves a durable intent + the ref. The
// workspace stays non-terminal (no false success). A retry RESUMES from the
// intent, commits the terminal state, and keeps exactly one result ref.
//
// Real SQLite + real Git refs (not a mock).
func TestFinalize_Atomicity_CompensatesRefOnTxFailure(t *testing.T) {
	f := newFinalizeFixture(t)
	ctx := context.Background()

	newHEAD := f.commitInWorktree("src/a.go", "package a\n", "feat: a")
	ins := workspace.Inspection{ActualHEAD: newHEAD, StatusPorcelain: "", ChangedFiles: []string{"src/a.go"}}

	svc := runapp.NewService(runapp.Options{
		Workspaces: f.wm, Tasks: f.bk, Audit: audit.NewRecorder(f.db, nil), DB: f.db,
	})
	// Crash after ref, before terminal DB commit (loss of process state).
	svc.SetTestHooks(nil, func() error { return errCrashAfterRef })

	_, err := svc.Finalize(ctx, runapp.FinalizeRequest{
		WorkspaceID:   f.ws.ID,
		TaskID:        "task-1",
		TerminalEvent: terminalEvent(protocol.EventRunCompleted, ""),
		Inspection:    ins,
		Engine:        "opencode", Model: "m",
	})
	if !errors.Is(err, errCrashAfterRef) {
		t.Fatalf("finalize err = %v, want crash-after-ref", err)
	}

	intent, ierr := f.db.GetFinalizeIntent(ctx, f.ws.ID)
	if ierr != nil {
		t.Fatalf("finalize intent missing after crash: %v", ierr)
	}
	if intent.Phase != storage.FinalizePhaseRefReady {
		t.Errorf("intent phase = %s, want ref_ready", intent.Phase)
	}
	refSHA, _ := f.wm.ResolveResultRef(ctx, f.ws.TaskID, f.ws.Path)
	if refSHA != newHEAD {
		t.Errorf("result ref after crash = %q, want %q (kept for resume)", refSHA, newHEAD)
	}
	wsAfter, _ := f.wm.Get(ctx, f.ws.ID)
	if isMinimalTerminal(wsAfter.State) {
		t.Errorf("workspace state = %s after crash, want non-terminal", wsAfter.State)
	}

	// Fresh service = process restart; resume completes.
	svc2 := runapp.NewService(runapp.Options{
		Workspaces: f.wm, Tasks: f.bk, Audit: audit.NewRecorder(f.db, nil), DB: f.db,
	})
	res, err := svc2.Finalize(ctx, runapp.FinalizeRequest{
		WorkspaceID:   f.ws.ID,
		TaskID:        "task-1",
		TerminalEvent: terminalEvent(protocol.EventRunCompleted, ""),
		Inspection:    ins,
		Engine:        "opencode", Model: "m",
	})
	if err != nil {
		t.Fatalf("retry finalize failed: %v", err)
	}
	if res.Outcome != runapp.OutcomeCompletedWithCommit {
		t.Errorf("retry outcome = %s, want completed-with-commit", res.Outcome)
	}
	refSHA2, _ := f.wm.ResolveResultRef(ctx, f.ws.TaskID, f.ws.Path)
	if refSHA2 != newHEAD {
		t.Errorf("result ref after retry = %q, want %q", refSHA2, newHEAD)
	}
	if _, err := f.db.GetFinalizeIntent(ctx, f.ws.ID); !errors.Is(err, storage.ErrFinalizeIntentNotFound) {
		t.Errorf("intent should be cleared after successful commit, got %v", err)
	}

	if _, err := svc2.Finalize(ctx, runapp.FinalizeRequest{
		WorkspaceID:   f.ws.ID,
		TaskID:        "task-1",
		TerminalEvent: terminalEvent(protocol.EventRunCompleted, ""),
		Inspection:    ins,
	}); err != nil {
		t.Errorf("idempotent re-finalize errored: %v", err)
	}
}

// TestFinalize_Atomicity_RefFailureRollsBack (BF-07): when the result ref
// creation ITSELF fails, finalize returns an error and never opens the tx, so
// no DB state is partially written.
func TestFinalize_Atomicity_RefFailureRollsBack(t *testing.T) {
	f := newFinalizeFixture(t)
	ctx := context.Background()
	newHEAD := f.commitInWorktree("src/b.go", "package b\n", "feat: b")
	ins := workspace.Inspection{ActualHEAD: newHEAD, StatusPorcelain: ""}

	// Wrap so EnsureResultRef fails (ref cannot be created).
	wm := &refFailWM{inner: f.wm}
	svc := runapp.NewService(runapp.Options{
		Workspaces: wm, Tasks: f.bk, Audit: audit.NewRecorder(f.db, nil), DB: f.db,
	})
	_, err := svc.Finalize(ctx, runapp.FinalizeRequest{
		WorkspaceID:   f.ws.ID,
		TaskID:        "task-1",
		TerminalEvent: terminalEvent(protocol.EventRunCompleted, ""),
		Inspection:    ins,
	})
	if err == nil {
		t.Fatalf("finalize unexpectedly succeeded when ref creation failed")
	}
	wsAfter, _ := f.wm.Get(ctx, f.ws.ID)
	if isMinimalTerminal(wsAfter.State) {
		t.Errorf("workspace became terminal despite ref failure (state=%s)", wsAfter.State)
	}
	tk, _ := f.db.GetTask(ctx, "task-1")
	if tk.State == string(task.StateCompleted) {
		t.Errorf("task became COMPLETED despite ref failure")
	}
}

type refFailWM struct {
	inner *workspace.Manager
}

func (r *refFailWM) Get(ctx context.Context, id string) (workspace.Workspace, error) {
	return r.inner.Get(ctx, id)
}
func (r *refFailWM) InspectWorktree(ctx context.Context, ws workspace.Workspace) (workspace.Inspection, error) {
	return r.inner.InspectWorktree(ctx, ws)
}
func (r *refFailWM) EnsureResultRef(context.Context, workspace.Workspace, string) (string, error) {
	return "", errInjectedFault
}
func (r *refFailWM) DeleteResultRef(ctx context.Context, ws workspace.Workspace) error {
	return r.inner.DeleteResultRef(ctx, ws)
}
func (r *refFailWM) ResolveResultRef(ctx context.Context, taskID, dir string) (string, error) {
	return r.inner.ResolveResultRef(ctx, taskID, dir)
}
func (r *refFailWM) UpdateStateTx(ctx context.Context, tx *storage.Tx, id string, state workspace.State, headSHA, resultBranch, resultSHA string) error {
	return r.inner.UpdateStateTx(ctx, tx, id, state, headSHA, resultBranch, resultSHA)
}
