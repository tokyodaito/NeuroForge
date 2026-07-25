package runapp_test

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"neuroforge/internal/adapter/codingagent/protocol"
	"neuroforge/internal/audit"
	"neuroforge/internal/runapp"
	"neuroforge/internal/storage"
	"neuroforge/internal/task"
	"neuroforge/internal/workspace"
)

// TestFinalize_Recovery_CrashAfterIntentBeforeRef (BF-07 B2):
// intent persisted → process crash → ref not yet created.
// After restart/retry: finalization continues, ref created exactly once,
// terminal DB committed, outcome/audit not duplicated.
func TestFinalize_Recovery_CrashAfterIntentBeforeRef(t *testing.T) {
	f := newFinalizeFixture(t)
	ctx := context.Background()
	newHEAD := f.commitInWorktree("src/a.go", "package a\n", "feat: a")
	ins := workspace.Inspection{ActualHEAD: newHEAD, StatusPorcelain: "", ChangedFiles: []string{"src/a.go"}}

	svc := runapp.NewService(runapp.Options{
		Workspaces: f.wm, Tasks: f.bk, Audit: audit.NewRecorder(f.db, nil), DB: f.db,
	})
	// Model process crash: lose in-memory state after intent is durable.
	svc.SetTestHooks(func() error {
		return errCrashAfterIntent
	}, nil)

	_, err := svc.Finalize(ctx, runapp.FinalizeRequest{
		WorkspaceID: f.ws.ID, TaskID: "task-1",
		TerminalEvent: terminalEvent(protocol.EventRunCompleted, ""),
		Inspection:    ins, Engine: "opencode", Model: "m", RunID: "run-crash-1",
	})
	if !errors.Is(err, errCrashAfterIntent) {
		t.Fatalf("want crash-after-intent, got %v", err)
	}

	// Intent durable, ref absent.
	intent, err := f.db.GetFinalizeIntent(ctx, f.ws.ID)
	if err != nil {
		t.Fatalf("intent missing: %v", err)
	}
	if intent.Phase != storage.FinalizePhasePending {
		t.Errorf("phase = %s, want pending", intent.Phase)
	}
	if sha, _ := f.wm.ResolveResultRef(ctx, "task-1", f.ws.Path); sha != "" {
		t.Errorf("ref should not exist yet, got %s", sha)
	}
	ws, _ := f.wm.Get(ctx, f.ws.ID)
	if isMinimalTerminal(ws.State) {
		t.Fatalf("workspace terminal after crash-before-ref: %s", ws.State)
	}

	// Fresh service (new process) resumes.
	svc2 := runapp.NewService(runapp.Options{
		Workspaces: f.wm, Tasks: f.bk, Audit: audit.NewRecorder(f.db, nil), DB: f.db,
	})
	res, err := svc2.Finalize(ctx, runapp.FinalizeRequest{
		WorkspaceID: f.ws.ID, TaskID: "task-1",
		TerminalEvent: terminalEvent(protocol.EventRunCompleted, ""),
		Inspection:    ins, Engine: "opencode", Model: "m", RunID: "run-crash-1",
	})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if res.Outcome != runapp.OutcomeCompletedWithCommit {
		t.Errorf("outcome = %s", res.Outcome)
	}
	if sha, _ := f.wm.ResolveResultRef(ctx, "task-1", f.ws.Path); sha != newHEAD {
		t.Errorf("ref = %q, want %q", sha, newHEAD)
	}
	ws2, _ := f.wm.Get(ctx, f.ws.ID)
	if ws2.State != workspace.StateCompleted {
		t.Errorf("ws state = %s, want completed", ws2.State)
	}
	events, _ := f.db.ListAuditEvents(ctx, storage.AuditFilter{ScopeID: f.ws.ID})
	if n := countEventType(events, "run.outcome_decided"); n != 1 {
		t.Errorf("outcome_decided count = %d, want 1", n)
	}
	if _, err := f.db.GetFinalizeIntent(ctx, f.ws.ID); !errors.Is(err, storage.ErrFinalizeIntentNotFound) {
		t.Errorf("intent should be cleared, got %v", err)
	}
}

// TestFinalize_Recovery_CrashAfterRefBeforeDBCommit (BF-07 B3):
// intent + result ref created → process crash → terminal tx not committed.
// After restart: reconciler/resume completes commit; one terminal outcome;
// no orphan; no false success before resume.
func TestFinalize_Recovery_CrashAfterRefBeforeDBCommit(t *testing.T) {
	f := newFinalizeFixture(t)
	ctx := context.Background()
	newHEAD := f.commitInWorktree("src/b.go", "package b\n", "feat: b")
	ins := workspace.Inspection{ActualHEAD: newHEAD, StatusPorcelain: "", ChangedFiles: []string{"src/b.go"}}

	svc := runapp.NewService(runapp.Options{
		Workspaces: f.wm, Tasks: f.bk, Audit: audit.NewRecorder(f.db, nil), DB: f.db,
	})
	svc.SetTestHooks(nil, func() error { return errCrashAfterRef })

	_, err := svc.Finalize(ctx, runapp.FinalizeRequest{
		WorkspaceID: f.ws.ID, TaskID: "task-1",
		TerminalEvent: terminalEvent(protocol.EventRunCompleted, ""),
		Inspection:    ins, Engine: "opencode", Model: "m", RunID: "run-crash-2",
	})
	if !errors.Is(err, errCrashAfterRef) {
		t.Fatalf("want crash-after-ref, got %v", err)
	}

	// Partial: intent ref_ready + ref present + workspace still active.
	intent, err := f.db.GetFinalizeIntent(ctx, f.ws.ID)
	if err != nil {
		t.Fatalf("intent missing: %v", err)
	}
	if intent.Phase != storage.FinalizePhaseRefReady {
		t.Errorf("phase = %s, want ref_ready", intent.Phase)
	}
	if sha, _ := f.wm.ResolveResultRef(ctx, "task-1", f.ws.Path); sha != newHEAD {
		t.Errorf("ref after crash = %q, want %q", sha, newHEAD)
	}
	ws, _ := f.wm.Get(ctx, f.ws.ID)
	if isMinimalTerminal(ws.State) {
		t.Fatalf("false success: workspace terminal after crash-after-ref: %s", ws.State)
	}
	tk, _ := f.db.GetTask(ctx, "task-1")
	if tk.State == string(task.StateCompleted) {
		t.Fatalf("false success: task COMPLETED after crash-after-ref")
	}

	// RecoverPendingFinalizations (daemon startup path).
	svc2 := runapp.NewService(runapp.Options{
		Workspaces: f.wm, Tasks: f.bk, Audit: audit.NewRecorder(f.db, nil), DB: f.db,
	})
	results, errs := svc2.RecoverPendingFinalizations(ctx)
	if len(errs) != 0 {
		t.Fatalf("recover errs: %v", errs)
	}
	if len(results) != 1 || results[0].Outcome != runapp.OutcomeCompletedWithCommit {
		t.Fatalf("recover results = %+v", results)
	}
	ws2, _ := f.wm.Get(ctx, f.ws.ID)
	if ws2.State != workspace.StateCompleted {
		t.Errorf("ws state = %s after recover", ws2.State)
	}
	if sha, _ := f.wm.ResolveResultRef(ctx, "task-1", f.ws.Path); sha != newHEAD {
		t.Errorf("ref after recover = %q", sha)
	}
	events, _ := f.db.ListAuditEvents(ctx, storage.AuditFilter{ScopeID: f.ws.ID})
	if n := countEventType(events, "run.outcome_decided"); n != 1 {
		t.Errorf("outcome_decided = %d, want 1", n)
	}
}

// TestFinalize_ConcurrentSameRun (BF-07 B4): two concurrent finalizers for the
// same run — exactly one terminal decision, one result ref, one outcome audit;
// second caller is idempotent; no race under -race.
func TestFinalize_ConcurrentSameRun(t *testing.T) {
	f := newFinalizeFixture(t)
	ctx := context.Background()
	newHEAD := f.commitInWorktree("src/c.go", "package c\n", "feat: c")
	ins := workspace.Inspection{ActualHEAD: newHEAD, StatusPorcelain: "", ChangedFiles: []string{"src/c.go"}}
	svc := runapp.NewService(runapp.Options{
		Workspaces: f.wm, Tasks: f.bk, Audit: audit.NewRecorder(f.db, nil), DB: f.db,
	})
	req := runapp.FinalizeRequest{
		WorkspaceID: f.ws.ID, TaskID: "task-1",
		TerminalEvent: terminalEvent(protocol.EventRunCompleted, ""),
		Inspection:    ins, Engine: "opencode", Model: "m", RunID: "run-conc",
	}

	const n = 8
	var wg sync.WaitGroup
	var okCount atomic.Int32
	var outcomes sync.Map
	errs := make(chan error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			res, err := svc.Finalize(ctx, req)
			if err != nil {
				errs <- err
				return
			}
			okCount.Add(1)
			outcomes.Store(string(res.Outcome), true)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent finalize err: %v", err)
	}
	if okCount.Load() != n {
		t.Errorf("ok count = %d, want %d", okCount.Load(), n)
	}
	ws, _ := f.wm.Get(ctx, f.ws.ID)
	if ws.State != workspace.StateCompleted {
		t.Errorf("ws state = %s", ws.State)
	}
	if sha, _ := f.wm.ResolveResultRef(ctx, "task-1", f.ws.Path); sha != newHEAD {
		t.Errorf("ref = %q, want %q", sha, newHEAD)
	}
	events, _ := f.db.ListAuditEvents(ctx, storage.AuditFilter{ScopeID: f.ws.ID})
	if nOut := countEventType(events, "run.outcome_decided"); nOut != 1 {
		t.Errorf("outcome_decided = %d, want 1", nOut)
	}
	// Exactly one distinct outcome.
	count := 0
	outcomes.Range(func(_, _ any) bool { count++; return true })
	if count != 1 {
		t.Errorf("distinct outcomes = %d, want 1", count)
	}
}

// TestFinalize_RetryAfterPartialCompletion (BF-07 B5): deterministic recovery
// for each partial state.
func TestFinalize_RetryAfterPartialCompletion(t *testing.T) {
	t.Run("intent-no-ref", func(t *testing.T) {
		TestFinalize_Recovery_CrashAfterIntentBeforeRef(t)
	})
	t.Run("ref-no-terminal-db", func(t *testing.T) {
		TestFinalize_Recovery_CrashAfterRefBeforeDBCommit(t)
	})
	t.Run("terminal-db-complete-retry-idempotent", func(t *testing.T) {
		f := newFinalizeFixture(t)
		ctx := context.Background()
		newHEAD := f.commitInWorktree("src/d.go", "package d\n", "feat: d")
		ins := workspace.Inspection{ActualHEAD: newHEAD, StatusPorcelain: "", ChangedFiles: []string{"src/d.go"}}
		svc := runapp.NewService(runapp.Options{
			Workspaces: f.wm, Tasks: f.bk, Audit: audit.NewRecorder(f.db, nil), DB: f.db,
		})
		req := runapp.FinalizeRequest{
			WorkspaceID: f.ws.ID, TaskID: "task-1",
			TerminalEvent: terminalEvent(protocol.EventRunCompleted, ""),
			Inspection:    ins, Engine: "opencode", RunID: "run-done",
		}
		first, err := svc.Finalize(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		second, err := svc.Finalize(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if !second.Idempotent {
			t.Error("second should be idempotent")
		}
		if second.Outcome != first.Outcome || second.ResultBranch != first.ResultBranch {
			t.Errorf("second diverged: %+v vs %+v", second, first)
		}
		if sha, _ := f.wm.ResolveResultRef(ctx, "task-1", f.ws.Path); sha != newHEAD {
			t.Errorf("ref changed on retry: %s", sha)
		}
	})
	t.Run("ref-already-at-expected-sha", func(t *testing.T) {
		f := newFinalizeFixture(t)
		ctx := context.Background()
		newHEAD := f.commitInWorktree("src/e.go", "package e\n", "feat: e")
		// Pre-create the ref at the expected SHA (partial: ref done, no intent).
		if _, err := f.wm.EnsureResultRef(ctx, f.ws, newHEAD); err != nil {
			t.Fatal(err)
		}
		ins := workspace.Inspection{ActualHEAD: newHEAD, StatusPorcelain: "", ChangedFiles: []string{"src/e.go"}}
		svc := runapp.NewService(runapp.Options{
			Workspaces: f.wm, Tasks: f.bk, Audit: audit.NewRecorder(f.db, nil), DB: f.db,
		})
		res, err := svc.Finalize(ctx, runapp.FinalizeRequest{
			WorkspaceID: f.ws.ID, TaskID: "task-1",
			TerminalEvent: terminalEvent(protocol.EventRunCompleted, ""),
			Inspection:    ins,
		})
		if err != nil {
			t.Fatal(err)
		}
		if res.Outcome != runapp.OutcomeCompletedWithCommit {
			t.Errorf("outcome = %s", res.Outcome)
		}
		if sha, _ := f.wm.ResolveResultRef(ctx, "task-1", f.ws.Path); sha != newHEAD {
			t.Errorf("ref = %s", sha)
		}
	})
	t.Run("ref-points-to-other-sha-conflict", func(t *testing.T) {
		TestFinalize_ResultRefConflict(t)
	})
}

// TestFinalize_ResultRefConflict (BF-07 B6): existing result ref at a different
// SHA must not be overwritten; conflict is explicit; run not marked completed.
func TestFinalize_ResultRefConflict(t *testing.T) {
	f := newFinalizeFixture(t)
	ctx := context.Background()
	newHEAD := f.commitInWorktree("src/f.go", "package f\n", "feat: f")
	// Foreign ref at a different SHA (the base).
	foreign := f.ws.BaseSHA
	if foreign == "" {
		foreign = readHeadSHA(t, f.repoPath)
	}
	// Point result ref at base (not newHEAD) via raw git — models a foreign owner.
	ref := workspace.FullyQualifiedResultBranch("task-1")
	cmd := exec.Command("git", "-C", f.repoPath, "update-ref", ref, foreign)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("seed foreign ref: %v\n%s", err, out)
	}

	ins := workspace.Inspection{ActualHEAD: newHEAD, StatusPorcelain: "", ChangedFiles: []string{"src/f.go"}}
	svc := runapp.NewService(runapp.Options{
		Workspaces: f.wm, Tasks: f.bk, Audit: audit.NewRecorder(f.db, nil), DB: f.db,
	})
	_, err := svc.Finalize(ctx, runapp.FinalizeRequest{
		WorkspaceID: f.ws.ID, TaskID: "task-1",
		TerminalEvent: terminalEvent(protocol.EventRunCompleted, ""),
		Inspection:    ins, Engine: "opencode",
	})
	if err == nil {
		t.Fatal("expected result ref conflict, got nil")
	}
	var conf *runapp.ErrResultRefConflict
	if !errors.As(err, &conf) {
		t.Fatalf("want ErrResultRefConflict, got %T %v", err, err)
	}
	// Foreign ref untouched.
	got, _ := f.wm.ResolveResultRef(ctx, "task-1", f.repoPath)
	if got != foreign {
		t.Errorf("foreign ref overwritten: got %s, want %s", got, foreign)
	}
	// Workspace not completed.
	ws, _ := f.wm.Get(ctx, f.ws.ID)
	if ws.State == workspace.StateCompleted {
		t.Error("workspace must not be completed on conflict")
	}
	// Retry is deterministic (same conflict).
	_, err2 := svc.Finalize(ctx, runapp.FinalizeRequest{
		WorkspaceID: f.ws.ID, TaskID: "task-1",
		TerminalEvent: terminalEvent(protocol.EventRunCompleted, ""),
		Inspection:    ins,
	})
	if !errors.As(err2, &conf) {
		t.Errorf("retry want conflict, got %v", err2)
	}
}

// TestFinalize_NoChangeAndUncommittedOutcomes (BF-07 B7): no misleading refs.
func TestFinalize_NoChangeAndUncommittedOutcomes(t *testing.T) {
	t.Run("no-change-no-ref", func(t *testing.T) {
		f := newFinalizeFixture(t)
		ctx := context.Background()
		ins := f.inspect()
		svc := f.svc
		res, err := svc.Finalize(ctx, runapp.FinalizeRequest{
			WorkspaceID: f.ws.ID, TaskID: "task-1",
			TerminalEvent: terminalEvent(protocol.EventRunCompleted, ""),
			Inspection:    ins,
		})
		if err != nil {
			t.Fatal(err)
		}
		if res.Outcome != runapp.OutcomeCompletedNoChanges {
			t.Errorf("outcome = %s", res.Outcome)
		}
		if res.ResultBranch != "" {
			t.Errorf("no-change must not create result ref, got %s", res.ResultBranch)
		}
		if sha, _ := f.wm.ResolveResultRef(ctx, "task-1", f.ws.Path); sha != "" {
			t.Errorf("orphan ref for no-change: %s", sha)
		}
	})
	t.Run("uncommitted-ref-at-base-no-commit-sha", func(t *testing.T) {
		f := newFinalizeFixture(t)
		ctx := context.Background()
		f.writeUncommitted("dirty.txt", "x\n")
		ins := f.inspect()
		res, err := f.svc.Finalize(ctx, runapp.FinalizeRequest{
			WorkspaceID: f.ws.ID, TaskID: "task-1",
			TerminalEvent: terminalEvent(protocol.EventRunCompleted, ""),
			Inspection:    ins,
		})
		if err != nil {
			t.Fatal(err)
		}
		if res.Outcome != runapp.OutcomeCompletedWithUncommittedChanges {
			t.Errorf("outcome = %s", res.Outcome)
		}
		if res.CommitSHA != "" {
			t.Errorf("commit_sha must be empty for uncommitted, got %s", res.CommitSHA)
		}
		if res.ResultBranch == "" {
			t.Error("uncommitted should still get a result ref handle")
		}
		if res.WorkspacePath == "" {
			t.Error("workspace path must be preserved")
		}
		if len(res.ChangedFiles) == 0 {
			t.Error("changed files must be preserved")
		}
		// Ref points at HEAD (== base), not a fake "commit with changes".
		sha, _ := f.wm.ResolveResultRef(ctx, "task-1", f.ws.Path)
		if sha != f.ws.BaseSHA && sha != ins.ActualHEAD {
			t.Errorf("ref sha = %s, want base/actual %s/%s", sha, f.ws.BaseSHA, ins.ActualHEAD)
		}
	})
}

// TestFinalize_TerminalToActiveForbiddenPersistence verifies the full terminal
// → active matrix on the real persistence path (not only a pure function).
func TestFinalize_TerminalToActiveForbiddenPersistence(t *testing.T) {
	terminals := []struct {
		name  string
		setup func(f *finalizeFixture)
		state workspace.State
	}{
		{"completed", func(f *finalizeFixture) {
			f.commitInWorktree("x.go", "x\n", "c")
			_, _ = f.svc.Finalize(context.Background(), runapp.FinalizeRequest{
				WorkspaceID: f.ws.ID, TaskID: "task-1",
				TerminalEvent: terminalEvent(protocol.EventRunCompleted, ""),
				Inspection:    f.inspect(),
			})
		}, workspace.StateCompleted},
		{"failed", func(f *finalizeFixture) {
			_, _ = f.svc.Finalize(context.Background(), runapp.FinalizeRequest{
				WorkspaceID: f.ws.ID, TaskID: "task-1",
				TerminalEvent: terminalEvent(protocol.EventRunFailed, protocol.FailureInternalError),
				Inspection:    f.inspect(),
			})
		}, workspace.StateFailed},
		{"cancelled", func(f *finalizeFixture) {
			_, _ = f.svc.Finalize(context.Background(), runapp.FinalizeRequest{
				WorkspaceID: f.ws.ID, TaskID: "task-1",
				TerminalEvent: terminalEvent(protocol.EventRunCancelled, ""),
				Inspection:    f.inspect(),
			})
		}, workspace.StateCancelled},
		{"timed_out", func(f *finalizeFixture) {
			_, _ = f.svc.Finalize(context.Background(), runapp.FinalizeRequest{
				WorkspaceID: f.ws.ID, TaskID: "task-1",
				TerminalEvent: terminalEvent(protocol.EventRunFailed, protocol.FailureTimeout),
				Inspection:    f.inspect(),
			})
		}, workspace.StateTimedOut},
		{"interrupted-as-failed", func(f *finalizeFixture) {
			// interrupted maps to workspace failed (OUTCOME_CONTRACT §1.3).
			_, _ = f.wm.SetState(context.Background(), f.ws.ID, workspace.StateFailed)
		}, workspace.StateFailed},
	}
	for _, tc := range terminals {
		t.Run(tc.name, func(t *testing.T) {
			f := newFinalizeFixture(t)
			ctx := context.Background()
			tc.setup(f)
			ws, _ := f.wm.Get(ctx, f.ws.ID)
			if ws.State != tc.state {
				t.Fatalf("precondition state = %s, want %s", ws.State, tc.state)
			}
			// Late completed event must NOT revive to active / change outcome.
			res, err := f.svc.Finalize(ctx, runapp.FinalizeRequest{
				WorkspaceID: f.ws.ID, TaskID: "task-1",
				TerminalEvent: terminalEvent(protocol.EventRunCompleted, ""),
				Inspection:    f.inspect(),
			})
			if err != nil {
				t.Fatalf("late finalize err: %v", err)
			}
			if !res.Idempotent {
				t.Error("late finalize must be idempotent")
			}
			ws2, _ := f.wm.Get(ctx, f.ws.ID)
			if ws2.State != tc.state {
				t.Errorf("state changed %s → %s (terminal→active forbidden)", tc.state, ws2.State)
			}
			if ws2.State == workspace.StateActive {
				t.Error("terminal revived to active")
			}
		})
	}
}

var (
	errCrashAfterIntent = errors.New("injected crash after intent (BF-07 B2)")
	errCrashAfterRef    = errors.New("injected crash after ref (BF-07 B3)")
)

// silence unused import if strings is not used in some builds
var _ = strings.TrimSpace
