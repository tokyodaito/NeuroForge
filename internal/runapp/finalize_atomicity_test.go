package runapp_test

import (
	"context"
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
// §3.4): when the result ref is created BEFORE the SQLite transaction and the
// transaction then FAILS, finalize MUST remove the just-created ref (compensating
// delete) so git and DB stay consistent — no orphan ref pointing at a result the
// DB never recorded. The workspace must remain non-terminal (rolled back). A
// retry with a healthy tx then succeeds and re-creates the ref.
//
// This is a real fault-injection test against real SQLite + real Git refs
// (not a mock): the ref lands in the worktree's shared object DB and is verified
// via for-each-ref.
func TestFinalize_Atomicity_CompensatesRefOnTxFailure(t *testing.T) {
	f := newFinalizeFixture(t)
	ctx := context.Background()

	// Agent committed in the worktree → completed-with-commit → ref is created.
	newHEAD := f.commitInWorktree("src/a.go", "package a\n", "feat: a")
	ins := workspace.Inspection{ActualHEAD: newHEAD, StatusPorcelain: "", ChangedFiles: []string{"src/a.go"}}

	wm := &faultWM{inner: f.wm}
	wm.failUpdateState.Store(true) // force the tx to fail after the ref is created
	svc := runapp.NewService(runapp.Options{
		Workspaces: wm, Tasks: f.bk, Audit: audit.NewRecorder(f.db, nil), DB: f.db,
	})

	// Finalize must fail (injected tx fault) AND compensate the ref.
	_, err := svc.Finalize(ctx, runapp.FinalizeRequest{
		WorkspaceID:   f.ws.ID,
		TaskID:        "task-1",
		TerminalEvent: terminalEvent(protocol.EventRunCompleted, ""),
		Inspection:    ins,
		Engine:        "opencode", Model: "m",
	})
	if err == nil {
		t.Fatalf("finalize unexpectedly succeeded with injected fault")
	}

	// The compensating DeleteResultRef must have run.
	if got := wm.deleteCalled.Load(); got != 1 {
		t.Fatalf("compensating DeleteResultRef called %d times, want 1 (orphan ref left?)", got)
	}
	// The ref must NOT exist (git and DB agree: no result recorded).
	refSHA, _ := f.wm.ResolveResultRef(ctx, f.ws.TaskID, f.ws.Path)
	if refSHA != "" {
		t.Errorf("orphan result ref survived tx failure: resolves to %q (must be deleted)", refSHA)
	}
	// The workspace must still be non-terminal (the tx rolled back).
	wsAfter, _ := f.wm.Get(ctx, f.ws.ID)
	if isMinimalTerminal(wsAfter.State) {
		t.Errorf("workspace state = %s after failed tx, want non-terminal (rolled back)", wsAfter.State)
	}

	// Retry with a healthy tx: finalize succeeds and re-creates the ref
	// (recovery to a consistent terminal state).
	wm.failUpdateState.Store(false)
	res, err := svc.Finalize(ctx, runapp.FinalizeRequest{
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
	if refSHA2 == "" {
		t.Errorf("result ref missing after successful retry finalize")
	}

	// Idempotency: a third finalize on the now-terminal workspace is a no-op
	// that does NOT call the compensating delete and does not error.
	before := wm.deleteCalled.Load()
	if _, err := svc.Finalize(ctx, runapp.FinalizeRequest{
		WorkspaceID:   f.ws.ID,
		TaskID:        "task-1",
		TerminalEvent: terminalEvent(protocol.EventRunCompleted, ""),
		Inspection:    ins,
	}); err != nil {
		t.Errorf("idempotent re-finalize errored: %v", err)
	}
	if got := wm.deleteCalled.Load(); got != before {
		t.Errorf("idempotent re-finalize triggered compensating delete (%d -> %d)", before, got)
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
func (r *refFailWM) UpdateStateTx(ctx context.Context, tx *storage.Tx, id string, state workspace.State, headSHA, resultBranch, resultSHA string) error {
	return r.inner.UpdateStateTx(ctx, tx, id, state, headSHA, resultBranch, resultSHA)
}
