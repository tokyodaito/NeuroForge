package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
	"neuroforge/internal/pipeline"
	"neuroforge/internal/runapp"
	"neuroforge/internal/storage"
	"neuroforge/internal/task"
	"neuroforge/internal/transport"
	"neuroforge/internal/workspace"
)

// Phase E, scenario 1 + 2 + 13: daemon restart (SIGKILL shape) between and
// during pipeline stages, and finalize-intent crash recovery.

// TestPipelineFault_RestartMidExecute_Recovers proves restart recovery from a
// kill while the coding agent was in flight: the open execute stage is
// recorded as interrupted (not completed), the restarted daemon re-drives the
// run, reuses the existing worktree (no worktree/attempt explosion) and the
// run reaches a genuine terminal state.
func TestPipelineFault_RestartMidExecute_Recovers(t *testing.T) {
	blocked := newScriptedCodingAdapter(blockUntilCancelledBehavior())
	env := newFaultEnv(t, faultDeps{adapter: blocked})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Start a run whose agent blocks in execute forever (until "killed").
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_, _ = env.svc.RunPipeline(ctx, transport.PipelineRunRequest{
			ProjectID:   env.projID,
			Description: "restart mid execute",
			Engine:      "fake",
			Model:       "fake/standard",
		})
	}()
	// The abandoned goroutine is the "dead daemon's" in-flight work. Release
	// it at the very end so it unwinds before the DB closes. By then the run
	// is terminal (completed by the restarted daemon), so its late cancel is
	// absorbed by the terminal-state guards.
	t.Cleanup(func() {
		blocked.cancelAll()
		select {
		case <-runDone:
		case <-time.After(15 * time.Second):
			t.Errorf("abandoned run goroutine did not unwind after cancel")
		}
	})

	// Discover the task and wait for the agent to be in flight.
	var taskID string
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && taskID == "" {
		tasks, err := env.tasks.ListByProject(ctx, env.projID)
		if err == nil && len(tasks) > 0 {
			taskID = tasks[len(tasks)-1].ID
		}
		time.Sleep(25 * time.Millisecond)
	}
	if taskID == "" {
		t.Fatal("run did not start")
	}
	env.waitStage(t, taskID, "execute", 30*time.Second)
	if n := len(env.workspaces(t, taskID)); n != 1 {
		t.Fatalf("workspaces before kill = %d, want 1", n)
	}

	// === KILL: abandon the blocked service; boot a fresh one on the same
	// NEUROFORGE_HOME. The restarted daemon's agent completes immediately. ===
	env.restart(t, faultDeps{adapter: newScriptedCodingAdapter(
		writeCommitBehavior(map[string]string{"RESULT.md": "recovered\n"}))})

	// Startup reconciliation marks the in-flight execute stage interrupted.
	decisions := env.reconcilePipeline(t)
	marked := false
	for _, d := range decisions {
		if d.Action == DecisionMarkedStale && strings.Contains(d.Entity, taskID) {
			marked = true
		}
	}
	if !marked {
		t.Errorf("reconciler did not mark the run stale: %+v", decisions)
	}
	st := env.status(t, taskID)
	interrupted := false
	for _, r := range stageRecords(st, "execute", "failed") {
		if r.FailureCategory == string(pipeline.FailureInterrupted) {
			interrupted = true
		}
	}
	if !interrupted {
		t.Errorf("no interrupted execute record after kill (records: %+v)", st.StageRecords)
	}
	if st.RunState != "active" {
		t.Errorf("run_state after kill = %s, want active (recoverable)", st.RunState)
	}

	// Recovery re-drives the run to a genuine terminal state.
	env.svc.ResumeActiveRuns(ctx)
	st = env.waitRunState(t, taskID, "completed", 90*time.Second)

	// No worktree explosion: the ready stage ran once and its worktree was
	// reused across the restart.
	if n := len(env.workspaces(t, taskID)); n != 1 {
		t.Errorf("workspaces after recovery = %d, want 1 (no duplicate worktree attempts)", n)
	}
	// The execute stage completed at the bumped attempt — exactly once.
	execCompleted := stageRecords(st, "execute", "completed")
	if len(execCompleted) != 1 || execCompleted[0].Attempt != 2 {
		t.Errorf("execute completed records = %+v, want exactly one at attempt 2", execCompleted)
	}
	// The result ref exists and the primary checkout is untouched.
	ref := "refs/heads/forge/result/" + taskID
	if sha := strings.TrimSpace(faultGitOut(t, env.repo, "rev-parse", "--verify", ref)); sha == "" {
		t.Errorf("result ref %s missing after recovery", ref)
	}
	if got := strings.TrimSpace(faultGitOut(t, env.repo, "rev-parse", "HEAD")); got != env.head {
		t.Errorf("primary HEAD changed: %s → %s", env.head, got)
	}
	tk, err := env.tasks.Get(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if tk.State != task.StateCompleted {
		t.Errorf("task state = %s, want COMPLETED", tk.State)
	}
}

// TestPipelineFault_RestartDuringVerify_Recovers kills the daemon while the
// verify stage is in flight (durable state built exactly as a kill between
// Drive transitions leaves it) and proves the restarted daemon re-verifies
// and completes the run honestly.
func TestPipelineFault_RestartDuringVerify_Recovers(t *testing.T) {
	env := newFaultEnv(t, faultDeps{})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	taskID, ws := env.setupKilledRun(t, pipeline.StageVerify, "killed during verify")

	env.restart(t, faultDeps{})
	decisions := env.reconcilePipeline(t)
	marked := false
	for _, d := range decisions {
		if d.Action == DecisionMarkedStale && strings.Contains(d.Entity, taskID) {
			marked = true
		}
	}
	if !marked {
		t.Fatalf("reconciler did not mark the verify-stage run stale: %+v", decisions)
	}
	st := env.status(t, taskID)
	interrupted := false
	for _, r := range stageRecords(st, "verify", "failed") {
		if r.FailureCategory == string(pipeline.FailureInterrupted) {
			interrupted = true
		}
	}
	if !interrupted {
		t.Fatalf("no interrupted verify record (records: %+v)", st.StageRecords)
	}

	env.svc.ResumeActiveRuns(ctx)
	st = env.waitRunState(t, taskID, "completed", 90*time.Second)

	// Verify genuinely re-ran at the bumped attempt; review and finalize ran
	// once each.
	verifyDone := stageRecords(st, "verify", "completed")
	if len(verifyDone) != 1 || verifyDone[0].Attempt != 2 {
		t.Errorf("verify completed records = %+v, want exactly one at attempt 2", verifyDone)
	}
	if n := len(stageRecords(st, "review", "completed")); n != 1 {
		t.Errorf("review completed records = %d, want 1", n)
	}
	if n := len(stageRecords(st, "finalize", "completed")); n != 1 {
		t.Errorf("finalize completed records = %d, want 1", n)
	}
	if n := len(env.workspaces(t, taskID)); n != 1 {
		t.Errorf("workspaces = %d, want 1", n)
	}
	ref := "refs/heads/forge/result/" + taskID
	sha := strings.TrimSpace(faultGitOut(t, env.repo, "rev-parse", "--verify", ref))
	wantSHA := strings.TrimSpace(faultGitOut(t, ws.Path, "rev-parse", "HEAD"))
	if sha == "" || sha != wantSHA {
		t.Errorf("result ref = %q, want worktree HEAD %q", sha, wantSHA)
	}
}

// TestPipelineFault_RestartDuringRepair_Recovers kills the daemon mid-repair
// and proves the restarted daemon re-enters the repair loop and converges.
func TestPipelineFault_RestartDuringRepair_Recovers(t *testing.T) {
	env := newFaultEnv(t, faultDeps{})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	taskID, _ := env.setupKilledRun(t, pipeline.StageRepair, "killed during repair")

	env.restart(t, faultDeps{})
	decisions := env.reconcilePipeline(t)
	marked := false
	for _, d := range decisions {
		if d.Action == DecisionMarkedStale && strings.Contains(d.Entity, taskID) {
			marked = true
		}
	}
	if !marked {
		t.Fatalf("reconciler did not mark the repair-stage run stale: %+v", decisions)
	}

	env.svc.ResumeActiveRuns(ctx)
	st := env.waitRunState(t, taskID, "completed", 90*time.Second)

	// The injected verify failure and one completed repair attempt are both
	// durable, and the run completed — the repair loop was honestly re-entered,
	// not skipped.
	verifyFailed := stageRecords(st, "verify", "failed")
	if len(verifyFailed) == 0 {
		t.Errorf("no failed verify record (the injected failure must stay durable)")
	} else if verifyFailed[0].FailureCategory != string(pipeline.FailureStaticAnalysis) {
		t.Errorf("verify failure category = %s, want static_analysis_failure", verifyFailed[0].FailureCategory)
	}
	if n := len(stageRecords(st, "repair", "completed")); n != 1 {
		t.Errorf("repair completed records = %d, want 1", n)
	}
	if n := len(stageRecords(st, "verify", "completed")); n != 1 {
		t.Errorf("verify completed records = %d, want 1 (re-verification after repair)", n)
	}
	if n := len(env.workspaces(t, taskID)); n != 1 {
		t.Errorf("workspaces = %d, want 1", n)
	}
}

// TestPipelineFault_RestartDuringFinalize_Recovers kills the daemon with the
// run active at the finalize stage (no finalize intent yet) and proves the
// restarted daemon runs finalize exactly once: one result ref, one finalize
// record, and idempotent re-drive afterwards.
func TestPipelineFault_RestartDuringFinalize_Recovers(t *testing.T) {
	env := newFaultEnv(t, faultDeps{})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	taskID, ws := env.setupKilledRun(t, pipeline.StageFinalize, "killed during finalize")

	env.restart(t, faultDeps{})
	env.reconcilePipeline(t)
	env.svc.ResumeActiveRuns(ctx)
	st := env.waitRunState(t, taskID, "completed", 90*time.Second)

	if n := len(stageRecords(st, "finalize", "completed")); n != 1 {
		t.Errorf("finalize completed records = %d, want 1", n)
	}
	ref := "refs/heads/forge/result/" + taskID
	sha := strings.TrimSpace(faultGitOut(t, env.repo, "rev-parse", "--verify", ref))
	wantSHA := strings.TrimSpace(faultGitOut(t, ws.Path, "rev-parse", "HEAD"))
	if sha == "" || sha != wantSHA {
		t.Fatalf("result ref = %q, want worktree HEAD %q", sha, wantSHA)
	}
	if st.ResultRef != ref {
		t.Errorf("run result_ref = %q, want %q", st.ResultRef, ref)
	}
	// No finalize intent is left behind.
	if _, err := env.db.GetFinalizeIntent(ctx, ws.ID); !errors.Is(err, storage.ErrFinalizeIntentNotFound) {
		t.Errorf("finalize intent left behind: %v", err)
	}

	// Re-drive of the recovered, completed run is an idempotent no-op: no new
	// stage records, no ref movement, no extra commits.
	recordsBefore := len(st.StageRecords)
	if err := env.svc.driver.Drive(ctx, taskID); err != nil {
		t.Fatalf("re-drive completed run: %v", err)
	}
	st2 := env.status(t, taskID)
	if len(st2.StageRecords) != recordsBefore {
		t.Errorf("stage records grew on re-drive: %d → %d", recordsBefore, len(st2.StageRecords))
	}
	if sha2 := strings.TrimSpace(faultGitOut(t, env.repo, "rev-parse", ref)); sha2 != sha {
		t.Errorf("result ref moved on re-drive: %s → %s", sha, sha2)
	}
	if got := strings.TrimSpace(faultGitOut(t, env.repo, "rev-list", "--count", ref)); got != "2" {
		t.Errorf("commits on result branch = %s, want 2 (init + agent)", got)
	}
}

// TestPipelineFault_FinalizeCrash_RecoveredExactlyOnce interrupts the
// finalize crash protocol between the durable intent and the result ref
// (runapp test hook — the process-death shape of BF-07 B2) and proves the
// daemon's startup finalize reconciler completes it exactly once: ref
// created, terminal DB committed, intent cleared, and a second reconcile is
// a pure no-op.
func TestPipelineFault_FinalizeCrash_RecoveredExactlyOnce(t *testing.T) {
	env := newFaultEnv(t, faultDeps{})
	ctx := context.Background()

	tk, err := env.tasks.Add(ctx, task.AddRequest{ProjectID: env.projID, Description: "finalize crash"})
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
	mustWriteDaemonTest(t, filepath.Join(ws.Path, "RESULT.md"), "crash recovery\n")
	runGitInDaemonTest(t, ws.Path, "add", "-A")
	runGitInDaemonTest(t, ws.Path, "commit", "-m", "agent work",
		"--author=NeuroForge Fake <fake@neuroforge.local>")
	head := strings.TrimSpace(faultGitOut(t, ws.Path, "rev-parse", "HEAD"))
	insp, err := env.wm.InspectWorktree(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}

	// Crash after the intent is durable, before the result ref exists.
	crashErr := errors.New("test: simulated crash after intent")
	env.svc.fin.SetTestHooks(func() error { return crashErr }, nil)
	_, err = env.svc.fin.Finalize(ctx, runapp.FinalizeRequest{
		WorkspaceID:   ws.ID,
		TaskID:        tk.ID,
		TerminalEvent: protocol.NormalizedEvent{Type: protocol.EventRunCompleted},
		Inspection:    insp,
		Engine:        "fake",
		Model:         "fake/write-commit",
		RunID:         "run-fault-finalize",
	})
	if !errors.Is(err, crashErr) {
		t.Fatalf("want crash-after-intent, got %v", err)
	}
	intent, err := env.db.GetFinalizeIntent(ctx, ws.ID)
	if err != nil {
		t.Fatalf("intent missing after crash: %v", err)
	}
	if intent.Phase != storage.FinalizePhasePending {
		t.Errorf("intent phase = %s, want pending", intent.Phase)
	}
	if sha, _ := env.wm.ResolveResultRef(ctx, tk.ID, ws.Path); sha != "" {
		t.Errorf("result ref must not exist yet, got %s", sha)
	}
	if wsNow, _ := env.wm.Get(ctx, ws.ID); isWorkspaceTerminalState(wsNow.State) {
		t.Fatalf("false success: workspace terminal (%s) before recovery", wsNow.State)
	}

	// === restart: the daemon's finalize-intent reconciler resumes it ===
	reconciler := newFinalizeIntentReconciler(env.db, env.rec, env.wm, quietLogger())
	decisions, err := reconciler.Reconcile(ctx, ReconcileTx{
		DB: env.db, Audit: env.rec, Dirs: env.dirs, Logger: quietLogger(),
	})
	if err != nil {
		t.Fatalf("finalize reconcile: %v", err)
	}
	repaired := 0
	for _, d := range decisions {
		if d.Action == DecisionRepaired {
			repaired++
		}
	}
	if repaired != 1 {
		t.Errorf("repaired decisions = %d, want 1 (%+v)", repaired, decisions)
	}

	ref := "refs/heads/forge/result/" + tk.ID
	if sha := strings.TrimSpace(faultGitOut(t, env.repo, "rev-parse", "--verify", ref)); sha != head {
		t.Errorf("recovered result ref = %q, want %q", sha, head)
	}
	if _, err := env.db.GetFinalizeIntent(ctx, ws.ID); !errors.Is(err, storage.ErrFinalizeIntentNotFound) {
		t.Errorf("intent should be cleared after recovery, got %v", err)
	}
	wsAfter, _ := env.wm.Get(ctx, ws.ID)
	if wsAfter.State != workspace.StateCompleted {
		t.Errorf("workspace state = %s, want completed", wsAfter.State)
	}
	tkAfter, err := env.tasks.Get(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tkAfter.State != task.StateCompleted {
		t.Errorf("task state = %s, want COMPLETED", tkAfter.State)
	}

	// Exactly once: a second startup reconcile finds nothing pending and the
	// outcome audit event was written a single time.
	decisions2, err := reconciler.Reconcile(ctx, ReconcileTx{
		DB: env.db, Audit: env.rec, Dirs: env.dirs, Logger: quietLogger(),
	})
	if err != nil {
		t.Fatalf("second finalize reconcile: %v", err)
	}
	for _, d := range decisions2 {
		if d.Action == DecisionRepaired {
			t.Errorf("second reconcile repaired again: %+v", d)
		}
	}
	if sha := strings.TrimSpace(faultGitOut(t, env.repo, "rev-parse", ref)); sha != head {
		t.Errorf("result ref moved on second reconcile: %s", sha)
	}
	events, err := env.db.ListAuditEvents(ctx, storage.AuditFilter{ScopeID: ws.ID})
	if err != nil {
		t.Fatal(err)
	}
	if n := faultCountEventType(events, "run.outcome_decided"); n != 1 {
		t.Errorf("run.outcome_decided events = %d, want exactly 1", n)
	}
}

func faultCountEventType(events []storage.AuditEvent, eventType string) int {
	n := 0
	for _, e := range events {
		if e.Type == eventType {
			n++
		}
	}
	return n
}
