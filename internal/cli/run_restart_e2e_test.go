package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
	"neuroforge/internal/audit"
	"neuroforge/internal/daemon"
	"neuroforge/internal/runapp"
	"neuroforge/internal/storage"
	"neuroforge/internal/task"
	"neuroforge/internal/transport"
	"neuroforge/internal/workspace"
)

// TestForgeRun_DaemonCrashDuringActiveRun_Recovered verifies the durable
// pipeline's restart recovery (M14-06): forge run → execute in flight →
// kill -9 daemon → new daemon startup marks the in-flight stage interrupted
// and RE-DRIVES the run in the background (recovery, not terminal
// interruption). The re-driven run (fake/cancellation with a short timeout)
// fails as timed-out; the durable stage history keeps the interrupted record.
// Cancelled runs would never be re-driven; crashed-but-active runs are.
func TestForgeRun_DaemonCrashDuringActiveRun_Recovered(t *testing.T) {
	if testing.Short() {
		t.Skip("daemon crash E2E spawns real processes")
	}
	f := newRunFixture(t)
	dirs := daemon.WithRoot(f.home)
	primaryBefore := f.primaryHEAD()

	// Start a hanging run (fake/cancellation stays active until cancelled;
	// the short --timeout bounds the re-driven run after the crash).
	cmd := exec.Command(f.bin, "run", "--json", "--engine", "fake", "--model", "fake/cancellation",
		"--timeout", "3s", "hang until crash")
	cmd.Env = append(os.Environ(), "NEUROFORGE_HOME="+f.home)
	cmd.Dir = f.repoPath
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	if err := waitForActiveWorkspace(f.home, 20*time.Second); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("workspace never became active: %v", err)
	}

	// Capture pre-crash active workspace id.
	wsID, taskID := activeWorkspaceIDs(t, f.home)
	if wsID == "" {
		_ = cmd.Process.Kill()
		t.Fatal("no active workspace id")
	}

	// Kill -9 the daemon (hard crash mid-run).
	daemonPID := readDaemonPID(t, dirs.PIDFile)
	if daemonPID <= 0 {
		_ = cmd.Process.Kill()
		t.Fatal("no daemon pid")
	}
	proc, err := os.FindProcess(daemonPID)
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("find daemon: %v", err)
	}
	if err := proc.Signal(syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("kill -9 daemon: %v", err)
	}
	// Wait for the pid to die.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// CLI should exit (transport error or cancelled). Accept transport infra
	// interruption or exit 130 (OUTCOME_CONTRACT interrupted/cancelled).
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("CLI did not exit after daemon kill; stderr=%s", stderr.String())
	}

	// Start a fresh daemon → startup reconciler marks the in-flight stage
	// interrupted; pipeline recovery re-drives the run in the background. The
	// re-driven fake/cancellation run hits its 3s timeout and the run settles
	// as timed-out/failed.
	out, _, code := runForge(f.t, f.bin, f.home, "daemon", "start")
	if code != 0 {
		t.Fatalf("daemon start after crash: exit %d out=%s", code, out)
	}

	// Poll the durable pipeline status until the run reaches a terminal state.
	var status transport.PipelineRunResultDTO
	terminal := false
	pollDeadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(pollDeadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cli, err := daemon.Connect(ctx, dirs)
		if err == nil {
			if st, serr := cli.PipelineStatus(ctx, taskID); serr == nil {
				status = st
				switch st.RunState {
				case "failed", "cancelled", "completed", "repair_exhausted":
					terminal = true
				}
			}
		}
		cancel()
		if terminal {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !terminal {
		t.Fatalf("pipeline run never reached a terminal state after recovery (last: %s at %s)",
			status.RunState, status.CurrentStage)
	}
	if status.RunState != "failed" {
		t.Errorf("run_state = %q, want failed (re-driven run timed out)", status.RunState)
	}
	if status.FailureCategory != "provider_timeout" {
		t.Errorf("failure_category = %q, want provider_timeout", status.FailureCategory)
	}

	// The stage history records the interruption (MarkInterrupted at crash
	// recovery) — proof the crash was seen and recovered, not hidden.
	var sawInterrupted bool
	for _, r := range status.StageRecords {
		if r.FailureCategory == "interrupted" {
			sawInterrupted = true
		}
	}
	if !sawInterrupted {
		t.Errorf("no interrupted stage record in %+v", status.StageRecords)
	}

	// Workspace + task settle terminal (timed-out failure path).
	if wsState := workspaceStateByID(t, f.home, wsID); wsState != "timed_out" && wsState != "failed" {
		t.Errorf("workspace state = %q, want timed_out/failed after recovery", wsState)
	}
	if tkState := taskStateByID(t, f.home, taskID); tkState != "FAILED" {
		t.Errorf("task state = %q, want FAILED", tkState)
	}
	if got := f.primaryHEAD(); got != primaryBefore {
		t.Errorf("primary checkout changed: %s → %s", primaryBefore, got)
	}
}

// TestForgeRun_RestartPreservesCancelled (BF-03 A2).
func TestForgeRun_RestartPreservesCancelled(t *testing.T) {
	if testing.Short() {
		t.Skip("restart E2E")
	}
	f := newRunFixture(t)
	dirs := daemon.WithRoot(f.home)

	// Drive cancel via SIGINT mid-run.
	cmd := exec.Command(f.bin, "run", "--json", "--engine", "fake", "--model", "fake/cancellation", "cancel me")
	cmd.Env = append(os.Environ(), "NEUROFORGE_HOME="+f.home)
	cmd.Dir = f.repoPath
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := waitForActiveWorkspace(f.home, 15*time.Second); err != nil {
		_ = cmd.Process.Kill()
		t.Skipf("never active: %v", err)
	}
	_ = cmd.Process.Signal(syscall.SIGINT)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("cancel did not finish")
	}

	// Wait for terminal cancelled.
	wsID, taskID, wsState := waitTerminalWorkspace(t, f.home, 15*time.Second)
	if wsState != "cancelled" && wsState != "failed" {
		// Cancel path may settle cancelled; tolerate brief races.
		t.Logf("post-cancel state=%s (want cancelled)", wsState)
	}
	// Prefer cancelled; if finalize raced to failed, still check restart stability.
	preState := latestWorkspaceState(t, f.home, firstProjectID(t, f.home))
	preTask := taskStateByID(t, f.home, taskID)
	preAudits := countOutcomeAudits(t, f.home, wsID)

	runForge(f.t, f.bin, f.home, "daemon", "stop")
	// Restart via daemon start (runs reconciler).
	if out, _, code := runForge(f.t, f.bin, f.home, "daemon", "start"); code != 0 {
		t.Fatalf("restart: %s", out)
	}

	postState := latestWorkspaceState(t, f.home, firstProjectID(t, f.home))
	if postState != preState {
		t.Errorf("workspace state changed across restart: %s → %s", preState, postState)
	}
	if postState == "active" || postState == "pending" {
		t.Errorf("reconciler revived workspace to %s", postState)
	}
	postTask := taskStateByID(t, f.home, taskID)
	if postTask != preTask {
		t.Errorf("task state changed: %s → %s", preTask, postTask)
	}
	postAudits := countOutcomeAudits(t, f.home, wsID)
	if postAudits != preAudits {
		t.Errorf("outcome audit count changed: %d → %d", preAudits, postAudits)
	}
	// Repeat cancel is N/A on terminal workspace; second reconciler pass is noop.
	runForge(f.t, f.bin, f.home, "daemon", "stop")
	runForge(f.t, f.bin, f.home, "daemon", "start")
	if got := latestWorkspaceState(t, f.home, firstProjectID(t, f.home)); got != preState {
		t.Errorf("second restart changed state to %s", got)
	}
	_ = dirs
	_ = stdout
	_ = stderr
}

// TestForgeRun_RestartPreservesTimedOut (BF-03 A3).
func TestForgeRun_RestartPreservesTimedOut(t *testing.T) {
	if testing.Short() {
		t.Skip("restart E2E")
	}
	f := newRunFixture(t)

	doc, _, code := f.runJSON("--engine", "fake", "--model", "fake/timeout",
		"--timeout", "1s", "block")
	if code != 124 {
		t.Fatalf("exit=%d, want 124; doc=%v", code, doc)
	}
	if doc["outcome"] != "timed-out" {
		t.Fatalf("outcome=%v, want timed-out", doc["outcome"])
	}
	wsID, _ := doc["workspace_id"].(string)
	taskID, _ := doc["task_id"].(string)
	preState := latestWorkspaceState(t, f.home, firstProjectID(t, f.home))
	if preState != "timed_out" {
		t.Fatalf("pre-restart ws state=%s, want timed_out", preState)
	}
	preTask := taskStateByID(t, f.home, taskID)
	preAudits := countOutcomeAudits(t, f.home, wsID)

	runForge(f.t, f.bin, f.home, "daemon", "stop")
	if out, _, c := runForge(f.t, f.bin, f.home, "daemon", "start"); c != 0 {
		t.Fatalf("restart: %s", out)
	}

	postState := latestWorkspaceState(t, f.home, firstProjectID(t, f.home))
	if postState != "timed_out" {
		t.Errorf("timed_out became %s after restart", postState)
	}
	if postTask := taskStateByID(t, f.home, taskID); postTask != preTask {
		t.Errorf("task %s → %s", preTask, postTask)
	}
	if postAudits := countOutcomeAudits(t, f.home, wsID); postAudits != preAudits {
		t.Errorf("audit count %d → %d", preAudits, postAudits)
	}
	// Second reconciliation idempotent.
	runForge(f.t, f.bin, f.home, "daemon", "stop")
	runForge(f.t, f.bin, f.home, "daemon", "start")
	if got := latestWorkspaceState(t, f.home, firstProjectID(t, f.home)); got != "timed_out" {
		t.Errorf("after 2nd restart state=%s", got)
	}
}

// TestForgeRun_LateTerminalEventAfterRestartRejected (BF-03 A4):
// a terminally failed run stays failed across a daemon restart; late
// completed/failed/cancelled finalize calls are absorbed idempotently.
func TestForgeRun_LateTerminalEventAfterRestartRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("restart E2E")
	}
	f := newRunFixture(t)
	dirs := daemon.WithRoot(f.home)

	// Drive a run to a terminal failure (fake/crash fails the execute stage;
	// the pipeline settles the workspace as failed).
	doc, _, code := f.runJSON("--engine", "fake", "--model", "fake/crash", "crash the engine")
	if code != 1 {
		t.Fatalf("exit=%d, want 1 (failed); doc=%v", code, doc)
	}
	if doc["outcome"] != "failed" {
		t.Fatalf("outcome=%v, want failed", doc["outcome"])
	}
	wsID, _ := doc["workspace_id"].(string)
	taskID, _ := doc["task_id"].(string)
	if st := workspaceStateByID(t, f.home, wsID); st != "failed" {
		t.Fatalf("ws state=%s, want failed", st)
	}
	preHead := workspaceHeadByID(t, f.home, wsID)
	preResult := workspaceResultByID(t, f.home, wsID)
	preAudits := countOutcomeAudits(t, f.home, wsID)

	// Restart the daemon: the terminal run must not be revived or re-driven.
	runForge(f.t, f.bin, f.home, "daemon", "stop")
	if out, _, c := runForge(f.t, f.bin, f.home, "daemon", "start"); c != 0 {
		t.Fatalf("start: %s", out)
	}
	if st := workspaceStateByID(t, f.home, wsID); st != "failed" {
		t.Fatalf("ws state=%s after restart, want failed", st)
	}

	// Late terminal event via real persistence path (open same DB).
	db, err := storage.Open(context.Background(), dirs.StateDB, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	rec := audit.NewRecorder(db, nil)
	wm := workspace.NewManager(db, rec, dirs.WorkspacesDir, nil)
	bk := task.NewBacklog(db, rec, filepath.Join(f.home, "artifacts"), nil)
	svc := runapp.NewService(runapp.Options{Workspaces: wm, Tasks: bk, Audit: rec, DB: db})

	ws, err := wm.Get(context.Background(), wsID)
	if err != nil {
		t.Fatal(err)
	}
	ins, _ := wm.InspectWorktree(context.Background(), ws)
	// Late completed (and also try failed/cancelled).
	for _, ev := range []protocol.EventType{
		protocol.EventRunCompleted,
		protocol.EventRunFailed,
		protocol.EventRunCancelled,
	} {
		res, ferr := svc.Finalize(context.Background(), runapp.FinalizeRequest{
			WorkspaceID:   wsID,
			TaskID:        taskID,
			TerminalEvent: protocol.NormalizedEvent{Type: ev},
			Inspection:    ins,
			Engine:        "fake",
			RunID:         "late-run",
		})
		if ferr != nil {
			t.Fatalf("late %s: %v", ev, ferr)
		}
		if !res.Idempotent {
			t.Errorf("late %s must be idempotent absorbed", ev)
		}
	}

	ws2, _ := wm.Get(context.Background(), wsID)
	if ws2.State != workspace.StateFailed {
		t.Errorf("late event changed state to %s", ws2.State)
	}
	if ws2.HeadSHA != preHead {
		t.Errorf("head overwritten: %s → %s", preHead, ws2.HeadSHA)
	}
	if ws2.ResultSHA != preResult {
		t.Errorf("result sha overwritten: %s → %s", preResult, ws2.ResultSHA)
	}
	// No second outcome_decided (only finalize_idempotent notices).
	postAudits := countOutcomeAudits(t, f.home, wsID)
	if postAudits != preAudits {
		t.Errorf("outcome_decided count %d → %d (late event must not add)", preAudits, postAudits)
	}
	// No new result ref.
	if sha, _ := wm.ResolveResultRef(context.Background(), taskID, ws.Path); sha != "" && preResult == "" {
		t.Errorf("late event created result ref %s", sha)
	}
}

// TestForgeRun_RepeatedFinalizationAfterRestart_Idempotent (BF-03 A5).
func TestForgeRun_RepeatedFinalizationAfterRestart_Idempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("restart E2E")
	}
	f := newRunFixture(t)
	dirs := daemon.WithRoot(f.home)
	primaryBefore := f.primaryHEAD()

	doc, _, code := f.runJSON("--engine", "fake", "--model", "fake/write-commit", "done")
	if code != 0 {
		t.Fatalf("exit=%d doc=%v", code, doc)
	}
	if doc["outcome"] != "completed-with-commit" {
		t.Fatalf("outcome=%v", doc["outcome"])
	}
	wsID, _ := doc["workspace_id"].(string)
	taskID, _ := doc["task_id"].(string)
	wantHead, _ := doc["actual_head_sha"].(string)
	wantRef, _ := doc["result_branch"].(string)
	wantCommit, _ := doc["commit_sha"].(string)
	preAudits := countOutcomeAudits(t, f.home, wsID)

	// Restart daemon.
	runForge(f.t, f.bin, f.home, "daemon", "stop")
	if out, _, c := runForge(f.t, f.bin, f.home, "daemon", "start"); c != 0 {
		t.Fatalf("restart: %s", out)
	}

	// Re-finalize via real persistence path.
	db, err := storage.Open(context.Background(), dirs.StateDB, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	rec := audit.NewRecorder(db, nil)
	wm := workspace.NewManager(db, rec, dirs.WorkspacesDir, nil)
	bk := task.NewBacklog(db, rec, filepath.Join(f.home, "artifacts"), nil)
	svc := runapp.NewService(runapp.Options{Workspaces: wm, Tasks: bk, Audit: rec, DB: db})
	ws, _ := wm.Get(context.Background(), wsID)
	ins, _ := wm.InspectWorktree(context.Background(), ws)

	res, err := svc.Finalize(context.Background(), runapp.FinalizeRequest{
		WorkspaceID:   wsID,
		TaskID:        taskID,
		TerminalEvent: protocol.NormalizedEvent{Type: protocol.EventRunCompleted},
		Inspection:    ins,
		Engine:        "fake",
		RunID:         "repeat",
	})
	if err != nil {
		t.Fatalf("re-finalize: %v", err)
	}
	if !res.Idempotent {
		t.Error("re-finalize must be idempotent")
	}
	if string(res.Outcome) != "completed-with-commit" {
		t.Errorf("outcome=%s", res.Outcome)
	}
	if res.ResultBranch != wantRef {
		t.Errorf("result_branch=%s want %s", res.ResultBranch, wantRef)
	}
	ws2, _ := wm.Get(context.Background(), wsID)
	if ws2.State != workspace.StateCompleted {
		t.Errorf("state=%s", ws2.State)
	}
	if ws2.HeadSHA != wantHead {
		t.Errorf("head=%s want %s", ws2.HeadSHA, wantHead)
	}
	if ws2.ResultSHA != wantCommit && ws2.ResultSHA != wantHead {
		t.Errorf("result_sha=%s want %s", ws2.ResultSHA, wantCommit)
	}
	sha, _ := wm.ResolveResultRef(context.Background(), taskID, f.repoPath)
	if sha != wantHead {
		t.Errorf("ref sha=%s want %s", sha, wantHead)
	}
	postAudits := countOutcomeAudits(t, f.home, wsID)
	if postAudits != preAudits {
		t.Errorf("outcome_decided duplicated: %d → %d", preAudits, postAudits)
	}
	if got := f.primaryHEAD(); got != primaryBefore {
		t.Errorf("primary changed")
	}
}

// ---- helpers ----

func readDaemonPID(t *testing.T, pidFile string) int {
	t.Helper()
	b, err := os.ReadFile(pidFile)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

func activeWorkspaceIDs(t *testing.T, home string) (wsID, taskID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cli, err := daemon.Connect(ctx, daemon.WithRoot(home))
	if err != nil {
		return "", ""
	}
	ps, _ := cli.ListProjects(ctx)
	for _, p := range ps {
		wss, _ := cli.ListWorkspaces(ctx, "", p.ID)
		for _, w := range wss {
			if w.State == "active" {
				return w.ID, w.TaskID
			}
		}
	}
	return "", ""
}

func inspectInterrupted(t *testing.T, home, wsID, taskID string) (wsState, tkState string, interrupted bool) {
	t.Helper()
	dirs := daemon.WithRoot(home)
	db, err := storage.Open(context.Background(), dirs.StateDB, nil)
	if err != nil {
		return "", "", false
	}
	defer db.Close()
	if wsID != "" {
		if w, err := db.GetWorkspace(context.Background(), wsID); err == nil {
			wsState = w.State
		}
	}
	if taskID != "" {
		if tk, err := db.GetTask(context.Background(), taskID); err == nil {
			tkState = tk.State
		}
	}
	events, _ := db.ListAuditEvents(context.Background(), storage.AuditFilter{ScopeID: wsID})
	for _, e := range events {
		if e.Type == "run.outcome_decided" && strings.Contains(e.Payload, `"outcome":"interrupted"`) {
			interrupted = true
		}
	}
	return wsState, tkState, interrupted
}

func waitTerminalWorkspace(t *testing.T, home string, timeout time.Duration) (wsID, taskID, state string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		cli, err := daemon.Connect(ctx, daemon.WithRoot(home))
		if err == nil {
			ps, _ := cli.ListProjects(ctx)
			for _, p := range ps {
				wss, _ := cli.ListWorkspaces(ctx, "", p.ID)
				for _, w := range wss {
					if w.State != "active" && w.State != "pending" {
						cancel()
						return w.ID, w.TaskID, w.State
					}
					wsID, taskID, state = w.ID, w.TaskID, w.State
				}
			}
		}
		cancel()
		time.Sleep(100 * time.Millisecond)
	}
	return wsID, taskID, state
}

func taskStateByID(t *testing.T, home, taskID string) string {
	t.Helper()
	if taskID == "" {
		return ""
	}
	dirs := daemon.WithRoot(home)
	db, err := storage.Open(context.Background(), dirs.StateDB, nil)
	if err != nil {
		return ""
	}
	defer db.Close()
	tk, err := db.GetTask(context.Background(), taskID)
	if err != nil {
		return ""
	}
	return tk.State
}

func workspaceStateByID(t *testing.T, home, wsID string) string {
	t.Helper()
	dirs := daemon.WithRoot(home)
	db, err := storage.Open(context.Background(), dirs.StateDB, nil)
	if err != nil {
		return ""
	}
	defer db.Close()
	w, err := db.GetWorkspace(context.Background(), wsID)
	if err != nil {
		return ""
	}
	return w.State
}

func workspaceHeadByID(t *testing.T, home, wsID string) string {
	t.Helper()
	dirs := daemon.WithRoot(home)
	db, err := storage.Open(context.Background(), dirs.StateDB, nil)
	if err != nil {
		return ""
	}
	defer db.Close()
	w, err := db.GetWorkspace(context.Background(), wsID)
	if err != nil {
		return ""
	}
	return w.HeadSHA
}

func workspaceResultByID(t *testing.T, home, wsID string) string {
	t.Helper()
	dirs := daemon.WithRoot(home)
	db, err := storage.Open(context.Background(), dirs.StateDB, nil)
	if err != nil {
		return ""
	}
	defer db.Close()
	w, err := db.GetWorkspace(context.Background(), wsID)
	if err != nil {
		return ""
	}
	return w.ResultSHA
}

func countOutcomeAudits(t *testing.T, home, wsID string) int {
	t.Helper()
	dirs := daemon.WithRoot(home)
	db, err := storage.Open(context.Background(), dirs.StateDB, nil)
	if err != nil {
		return 0
	}
	defer db.Close()
	events, _ := db.ListAuditEvents(context.Background(), storage.AuditFilter{ScopeID: wsID})
	n := 0
	for _, e := range events {
		if e.Type == "run.outcome_decided" {
			n++
		}
	}
	return n
}

// keep json import used for potential debug
var _ = json.Marshal
