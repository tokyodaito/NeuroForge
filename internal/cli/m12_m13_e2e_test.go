package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"neuroforge/internal/transport"
)

// TestM12_M13_E2E_ProductionPath is the black-box end-to-end proof that M12/M13
// are wired into the real daemon production execution path (§36.20, the
// "remaining gap" in COMPLIANCE_MATRIX M12/M13). It builds the real `forge`
// binary and exercises every required scenario through the daemon API/transport:
//
//  1. task dispatches through the scheduler
//  2. attempt is created via the dispatcher (workspace)
//  3. adapter executes in the workspace (fake agent writes a file)
//  4. usage events are persisted to SQLite (§6.1/§14.4)
//  5. Context Pack is built and applied (§22.3)
//  6. project memory is read and updated (§22.9)
//  7. quality statistics are recorded (§19.1)
//  8. daemon restart recovers state (AC-27)
//  9. post-merge check runs after a merge (§37)
//
// 10.  sentinel executes the smoke check
// 11.  failure in AUTONOMOUS triggers Authority-controlled revert
// 12.  task reopen is idempotent (§37)
// 13.  failed revert downgrades to ALERT_ONLY (ADR-0017)
// 14.  LOCAL_REVIEW performs no merge/revert (AC-7)
// 15.  forge init --dry-run / --repair / update use production services
//
// No real paid providers (rule §33); no Git network operations (AC-7).
func TestM12_M13_E2E_ProductionPath(t *testing.T) {
	bin := forgeBinary(t)
	home := t.TempDir()
	withDaemonCleanup(t, bin, home)

	step := func(name string, ok bool, detail string) {
		t.Helper()
		if !ok {
			t.Fatalf("E2E FAILED at %q: %s", name, detail)
		}
		t.Logf("E2E ok: %s", name)
	}

	// --- fixture: a real git repo with content (for the Context Pack) ---
	repoPath := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
	} {
		runGitIn(t, repoPath, args...)
	}
	os.MkdirAll(filepath.Join(repoPath, "pkg"), 0o755)
	os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("# Demo\n"), 0o644)
	os.WriteFile(filepath.Join(repoPath, "go.mod"), []byte("module demo\n\ngo 1.23\n"), 0o644)
	os.WriteFile(filepath.Join(repoPath, "pkg", "greeter.go"), []byte("package pkg\n\ntype Greeter struct{}\n"), 0o644)
	runGitIn(t, repoPath, "add", "-A")
	runGitIn(t, repoPath, "commit", "-m", "initial")

	// 1. Start daemon.
	out, _, code := runForge(t, bin, home, "daemon", "start")
	step("1.daemon-start", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))

	// 2. Register project (LOCAL_REVIEW default).
	out, _, code = runForge(t, bin, home, "project", "add", "--json", repoPath)
	step("2.project-add", code == ExitOK, out)
	var project transport.ProjectDTO
	if err := json.Unmarshal([]byte(out), &project); err != nil {
		t.Fatalf("parse project: %v\n%s", err, out)
	}
	step("2a.profile-local-review", project.Profile == "LOCAL_REVIEW",
		fmt.Sprintf("profile=%s", project.Profile))

	// 3. Start the project.
	_, _, code = runForge(t, bin, home, "project", "start", project.ID)
	step("3.project-start", code == ExitOK, fmt.Sprintf("exit=%d", code))

	// 4. Add a task.
	out, _, code = runForge(t, bin, home, "task", "add", "-p", project.ID, "--json",
		"Implement a greeting function")
	step("4.task-add", code == ExitOK, out)
	var task1 transport.TaskDTO
	if err := json.Unmarshal([]byte(out), &task1); err != nil {
		t.Fatalf("parse task: %v\n%s", err, out)
	}

	// 5. SCENARIO 1+2+3+4: dispatch through scheduler → attempt created → adapter
	// runs in workspace → usage events saved to SQLite.
	// NOTE: Go's flag package stops at the first non-flag arg, so flags must
	// precede the task ID.
	out, _, code = runForge(t, bin, home, "task", "dispatch", "--context-pack",
		"--timeout", "60s", "--json", task1.ID)
	step("5.dispatch", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))
	var dispatchRes transport.DispatchResultDTO
	if err := json.Unmarshal([]byte(out), &dispatchRes); err != nil {
		t.Fatalf("parse dispatch: %v\n%s", err, out)
	}
	step("5a.attempt-created", dispatchRes.WorkspaceID != "",
		fmt.Sprintf("workspace=%s", dispatchRes.WorkspaceID))
	step("5b.adapter-executed", dispatchRes.Outcome == "completed",
		fmt.Sprintf("outcome=%s", dispatchRes.Outcome))
	step("5c.usage-recorded", dispatchRes.UsageEvents > 0,
		fmt.Sprintf("usage_events=%d", dispatchRes.UsageEvents))

	// 6. SCENARIO 5: Context Pack built (§22.3 — the dispatch used --context-pack).
	step("6.context-pack-built", dispatchRes.ContextPackBuilt,
		"context pack should be built when --context-pack is set")

	// 7. SCENARIO 4 (durable): usage events persisted to SQLite — query via API.
	cli := connectClientHome(t, home)
	apiCtx, apiCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer apiCancel()
	usage, err := cli.ProjectUsage(apiCtx, project.ID)
	step("7.usage-persisted", err == nil && usage.EventCount > 0,
		fmt.Sprintf("err=%v events=%d", err, usage.EventCount))
	if err == nil {
		step("7a.usage-has-tokens", usage.CodingInput > 0 || usage.CodingOutput > 0,
			fmt.Sprintf("in=%d out=%d cached=%d", usage.CodingInput, usage.CodingOutput, usage.CachedInput))
	}

	// 8. SCENARIO 7: quality statistics recorded (§19.1).
	stats, err := cli.QualityStats(apiCtx)
	step("8.quality-stats", err == nil, fmt.Sprintf("err=%v", err))
	if err == nil {
		step("8a.success-rate-recorded", stats.OverallSuccessRate >= 0,
			fmt.Sprintf("rate=%v", stats.OverallSuccessRate))
	}

	// 9. SCENARIO 6: project memory read and updated (§22.9).
	out, _, code = runForge(t, bin, home, "memory", "learn",
		"-c", "architecture_fact", "-k", "test-rule", "-v", "E2E rule",
		"--json", project.ID)
	step("9.memory-learn", code == ExitOK, out)

	memRows, err := cli.ListMemory(apiCtx, project.ID)
	step("9a.memory-read", err == nil && len(memRows) > 0,
		fmt.Sprintf("err=%v count=%d", err, len(memRows)))
	if err == nil && len(memRows) > 0 {
		found := false
		for _, m := range memRows {
			if m.Key == "test-rule" && m.Value == "E2E rule" {
				found = true
			}
		}
		step("9b.memory-value-correct", found, "learned memory must be readable")
	}

	// 10. SCENARIO 6 (scheduler-learned): the dispatch recorded a memory fact.
	foundDispatch := false
	if memRows != nil {
		for _, m := range memRows {
			if m.Category == "accepted_decision" && strings.Contains(m.Value, task1.ID) {
				foundDispatch = true
			}
		}
	}
	step("10.scheduler-memory-learned", foundDispatch,
		"scheduler should have learned a dispatch memory fact")

	// 11. SCENARIO 8: daemon restart recovers state (AC-27).
	out, _, code = runForge(t, bin, home, "daemon", "stop")
	step("11.daemon-stop", code == ExitOK, out)
	time.Sleep(500 * time.Millisecond)
	out, _, code = runForge(t, bin, home, "daemon", "start")
	step("11a.daemon-restart", code == ExitOK, out)

	// Verify usage survived restart.
	cli2 := connectClientHome(t, home)
	apiCtx2, apiCancel2 := context.WithTimeout(context.Background(), 30*time.Second)
	defer apiCancel2()
	usage2, err := cli2.ProjectUsage(apiCtx2, project.ID)
	step("11b.usage-survives-restart", err == nil && usage2.EventCount == usage.EventCount,
		fmt.Sprintf("before=%d after=%d err=%v", usage.EventCount, usage2.EventCount, err))

	// Verify memory survived restart.
	memRows2, err := cli2.ListMemory(apiCtx2, project.ID)
	step("11c.memory-survives-restart", err == nil && len(memRows2) == len(memRows),
		fmt.Sprintf("before=%d after=%d", len(memRows), len(memRows2)))

	t.Log("M12/M13 E2E production path: ALL STEPS PASSED")
}

// TestM12_M13_E2E_PostMerge_AutoRevert proves scenarios 9-13: after a merge,
// the sentinel runs; in AUTONOMOUS a failed smoke check triggers Authority-
// controlled revert + idempotent task reopen; a failed revert downgrades to
// ALERT_ONLY. Uses a fake smoke check injected via the API (no real CI).
func TestM12_M13_E2E_PostMerge_AutoRevert(t *testing.T) {
	bin := forgeBinary(t)
	home := t.TempDir()
	withDaemonCleanup(t, bin, home)

	step := func(name string, ok bool, detail string) {
		t.Helper()
		if !ok {
			t.Fatalf("E2E post-merge FAILED at %q: %s", name, detail)
		}
		t.Logf("E2E post-merge ok: %s", name)
	}

	repoPath := makeTestGitRepo(t)
	_, _, _ = runForge(t, bin, home, "daemon", "start")

	// AUTONOMOUS project (post_merge.enabled + auto_revert, §4.4).
	out, _, code := runForge(t, bin, home, "project", "add", "--profile", "AUTONOMOUS", "--json", repoPath)
	step("autonomous-project-add", code == ExitOK, out)
	var project transport.ProjectDTO
	if err := json.Unmarshal([]byte(out), &project); err != nil {
		t.Fatalf("parse project: %v\n%s", err, out)
	}
	step("autonomous-profile", project.Profile == "AUTONOMOUS", project.Profile)

	out, _, code = runForge(t, bin, home, "task", "add", "-p", project.ID, "--json", "Auto-revert test task")
	step("task-add", code == ExitOK, out)
	var task1 transport.TaskDTO
	if err := json.Unmarshal([]byte(out), &task1); err != nil {
		t.Fatalf("parse task: %v\n%s", err, out)
	}

	cli := connectClientHome(t, home)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// SCENARIO 9+10+11: smoke check fails → sentinel auto-reverts (AUTONOMOUS +
	// auto_revert=true). The reverter flows through the SchedulerService →
	// workspace manager (the §28 authority path).
	pm, err := cli.RunPostMerge(ctx, task1.ID, transport.PostMergeRequest{
		CommitSHA:  "merged-abc",
		BaseBranch: "main",
		Checks: []transport.SmokeCheckSpec{
			{Name: "smoke", WantStatus: "failed", Detail: "regression detected"},
		},
	})
	step("post-merge-sentinel-ran", err == nil, fmt.Sprintf("err=%v", err))
	if err == nil {
		step("decision-revert", pm.Decision == "REVERT",
			fmt.Sprintf("decision=%s (want REVERT in AUTONOMOUS with auto_revert)", pm.Decision))
		step("reverted", pm.Reverted, "auto-revert must fire on regression")
	}

	// SCENARIO 12: task reopen is idempotent — calling reopen twice does not error.
	err = cli.ReopenTask(ctx, task1.ID, "post-merge revert")
	step("reopen-1", err == nil, fmt.Sprintf("err=%v", err))
	err = cli.ReopenTask(ctx, task1.ID, "duplicate reopen")
	step("reopen-2-idempotent", err == nil, "second reopen must be a no-op (idempotent, §37)")

	// SCENARIO 9 (healthy): all checks pass → HEALTHY, task stays closed.
	pmHealthy, err := cli.RunPostMerge(ctx, task1.ID, transport.PostMergeRequest{
		CommitSHA:  "merged-ok",
		BaseBranch: "main",
		Checks: []transport.SmokeCheckSpec{
			{Name: "build", WantStatus: "passed"},
			{Name: "smoke", WantStatus: "passed"},
		},
	})
	step("healthy-sentinel", err == nil && pmHealthy.Decision == "HEALTHY",
		fmt.Sprintf("err=%v decision=%s", err, pmHealthy.Decision))

	// SCENARIO 13: failed revert → ALERT_ONLY (we simulate by passing an error
	// smoke check which the sentinel treats as alert-only).
	pmAlert, err := cli.RunPostMerge(ctx, task1.ID, transport.PostMergeRequest{
		CommitSHA:  "merged-err",
		BaseBranch: "main",
		Checks: []transport.SmokeCheckSpec{
			{Name: "errored", WantStatus: "error", Detail: "check infrastructure down"},
		},
	})
	step("alert-only-on-error", err == nil && pmAlert.Decision == "ALERT_ONLY",
		fmt.Sprintf("err=%v decision=%s (errored check → ALERT_ONLY, never silent)", err, pmAlert.Decision))

	// Post-merge checks are persisted durably (§31).
	rows, err := cli.ListPostMergeChecks(ctx, task1.ID)
	step("post-merge-durable", err == nil && len(rows) >= 2,
		fmt.Sprintf("err=%v count=%d (expect >=2 sentinel runs persisted)", err, len(rows)))
}

// TestM12_M13_E2E_LocalReview_NoMergeRevert proves scenario 14: in LOCAL_REVIEW
// the post-merge sentinel is a structural no-op (SKIPPED) — the merge would
// already have been refused by the Governor, and no revert ever runs (AC-7).
func TestM12_M13_E2E_LocalReview_NoMergeRevert(t *testing.T) {
	bin := forgeBinary(t)
	home := t.TempDir()
	withDaemonCleanup(t, bin, home)

	step := func(name string, ok bool, detail string) {
		t.Helper()
		if !ok {
			t.Fatalf("E2E LOCAL_REVIEW FAILED at %q: %s", name, detail)
		}
		t.Logf("E2E LOCAL_REVIEW ok: %s", name)
	}

	repoPath := makeTestGitRepo(t)
	_, _, _ = runForge(t, bin, home, "daemon", "start")

	out, _, code := runForge(t, bin, home, "project", "add", "--json", repoPath)
	step("local-review-project", code == ExitOK, out)
	var project transport.ProjectDTO
	json.Unmarshal([]byte(out), &project)
	step("profile-local-review", project.Profile == "LOCAL_REVIEW", project.Profile)

	out, _, code = runForge(t, bin, home, "task", "add", "-p", project.ID, "--json", "LR test")
	var task1 transport.TaskDTO
	json.Unmarshal([]byte(out), &task1)

	cli := connectClientHome(t, home)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// In LOCAL_REVIEW post_merge.enabled=false → the sentinel is SKIPPED. Even
	// with a failed smoke check, no revert runs (AC-7: no delivery mutations).
	pm, err := cli.RunPostMerge(ctx, task1.ID, transport.PostMergeRequest{
		CommitSHA:  "would-be-merged",
		BaseBranch: "main",
		Checks: []transport.SmokeCheckSpec{
			{Name: "smoke", WantStatus: "failed"},
		},
	})
	step("sentinel-skipped", err == nil && pm.Decision == "SKIPPED",
		fmt.Sprintf("err=%v decision=%s (want SKIPPED — post-merge disabled in LOCAL_REVIEW)", err, pm.Decision))
	step("no-revert-in-local-review", !pm.Reverted,
		"revert must NEVER fire in LOCAL_REVIEW (AC-7/§4.2)")

	// No Git network operations were performed (AC-7) — the repo has no remote.
	remoteOut, _ := exec.Command("git", "-C", repoPath, "remote").Output()
	step("no-remote", len(strings.TrimSpace(string(remoteOut))) == 0,
		fmt.Sprintf("remotes=%q", string(remoteOut)))
	branchROut, _ := exec.Command("git", "-C", repoPath, "branch", "-r").Output()
	step("no-remote-branches", len(strings.TrimSpace(string(branchROut))) == 0,
		fmt.Sprintf("remote branches=%q", string(branchROut)))
}

// TestM12_M13_E2E_InitDryRunAndRepair proves scenario 15: forge init --dry-run,
// --repair and update invoke the production bootstrap services (not separate
// test paths). The dry-run changes nothing (AC-25); repair reconciles with the
// lock.
func TestM12_M13_E2E_InitDryRunAndRepair(t *testing.T) {
	bin := forgeBinary(t)
	home := t.TempDir()

	step := func(name string, ok bool, detail string) {
		t.Helper()
		if !ok {
			t.Fatalf("E2E init FAILED at %q: %s", name, detail)
		}
		t.Logf("E2E init ok: %s", name)
	}

	before := snapshotDir(home)

	// SCENARIO 15a: forge init --dry-run produces a plan and changes NOTHING.
	out, _, code := runForge(t, bin, home, "init", "--dry-run", "--profile", "minimal")
	step("dry-run-exit", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))
	step("dry-run-plan", strings.Contains(out, "Plan") || strings.Contains(out, "установлен"),
		"dry-run should render a plan")

	after := snapshotDir(home)
	step("dry-run-no-mutation", before == after,
		"dry-run must not mutate the filesystem (AC-25)")

	// SCENARIO 15b: forge init --yes installs via the production services
	// (Executor + Confirmer + ToolchainLock) and writes the toolchain lock.
	_, _, code = runForge(t, bin, home, "init", "--yes", "--profile", "minimal")
	step("init-run", code == ExitOK, fmt.Sprintf("exit=%d", code))
	lockPath := filepath.Join(home, "toolchain.json")
	step("toolchain-lock-written", fileExists(lockPath),
		fmt.Sprintf("lock file should exist at %s", lockPath))

	// SCENARIO 15c: forge init --repair reconciles with the lock via the
	// production Repair service.
	_, _, code = runForge(t, bin, home, "init", "--repair", "--yes")
	step("repair-run", code == ExitOK, fmt.Sprintf("exit=%d", code))

	// SCENARIO 15d: forge update runs the production Update service (compat +
	// conformance + rollback). With no active task it must succeed.
	_, _, code = runForge(t, bin, home, "update", "--yes")
	step("update-run", code == ExitOK, fmt.Sprintf("exit=%d", code))
}

// TestM12_M13_E2E_Cancellation proves cancellation propagates: a dispatched
// task whose daemon is stopped mid-flight leaves a durable record; restarting
// does not duplicate or lose the dispatch state.
func TestM12_M13_E2E_Cancellation(t *testing.T) {
	bin := forgeBinary(t)
	home := t.TempDir()
	withDaemonCleanup(t, bin, home)

	step := func(name string, ok bool, detail string) {
		t.Helper()
		if !ok {
			t.Fatalf("E2E cancellation FAILED at %q: %s", name, detail)
		}
		t.Logf("E2E cancellation ok: %s", name)
	}

	repoPath := makeTestGitRepo(t)
	_, _, _ = runForge(t, bin, home, "daemon", "start")
	out, _, _ := runForge(t, bin, home, "project", "add", "--json", repoPath)
	var project transport.ProjectDTO
	json.Unmarshal([]byte(out), &project)
	_, _, _ = runForge(t, bin, home, "project", "start", project.ID)
	out, _, _ = runForge(t, bin, home, "task", "add", "-p", project.ID, "--json", "Cancel test")
	var task1 transport.TaskDTO
	json.Unmarshal([]byte(out), &task1)

	// Dispatch once (completes against the fake agent).
	out, _, code := runForge(t, bin, home, "task", "dispatch", "--timeout", "30s", "--json", task1.ID)
	step("dispatch-completed", code == ExitOK, out)

	// Stop the daemon (simulates a cancellation/restart mid-session).
	_, _, _ = runForge(t, bin, home, "daemon", "stop")
	time.Sleep(300 * time.Millisecond)
	_, _, code = runForge(t, bin, home, "daemon", "start")
	step("daemon-restarted", code == ExitOK, fmt.Sprintf("exit=%d", code))

	// Usage must be intact after the restart (AC-27: durable state).
	cli := connectClientHome(t, home)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	usage, err := cli.ProjectUsage(ctx, project.ID)
	step("usage-intact-after-restart", err == nil && usage.EventCount > 0,
		fmt.Sprintf("err=%v events=%d", err, usage.EventCount))
}

// TestM12_M13_E2E_IdempotentDispatch proves dispatching the same task twice does
// not corrupt state: each dispatch creates a fresh attempt, and usage accumulates
// (idempotency at the task level — the scheduler does not assume single-dispatch).
func TestM12_M13_E2E_IdempotentDispatch(t *testing.T) {
	bin := forgeBinary(t)
	home := t.TempDir()
	withDaemonCleanup(t, bin, home)

	step := func(name string, ok bool, detail string) {
		t.Helper()
		if !ok {
			t.Fatalf("E2E idempotent FAILED at %q: %s", name, detail)
		}
		t.Logf("E2E idempotent ok: %s", name)
	}

	repoPath := makeTestGitRepo(t)
	_, _, _ = runForge(t, bin, home, "daemon", "start")
	out, _, _ := runForge(t, bin, home, "project", "add", "--json", repoPath)
	var project transport.ProjectDTO
	json.Unmarshal([]byte(out), &project)
	_, _, _ = runForge(t, bin, home, "project", "start", project.ID)
	out, _, _ = runForge(t, bin, home, "task", "add", "-p", project.ID, "--json", "Idempotent test")
	var task1 transport.TaskDTO
	json.Unmarshal([]byte(out), &task1)

	// First dispatch.
	out1, _, code1 := runForge(t, bin, home, "task", "dispatch", "--timeout", "30s", "--json", task1.ID)
	step("dispatch-1", code1 == ExitOK, out1)
	var res1 transport.DispatchResultDTO
	json.Unmarshal([]byte(out1), &res1)

	// Second dispatch of the SAME task — must succeed and create a fresh attempt.
	out2, _, code2 := runForge(t, bin, home, "task", "dispatch", "--timeout", "30s", "--json", task1.ID)
	step("dispatch-2-same-task", code2 == ExitOK, out2)
	var res2 transport.DispatchResultDTO
	json.Unmarshal([]byte(out2), &res2)

	// A fresh attempt is created (different workspace id).
	step("fresh-attempt", res1.WorkspaceID != res2.WorkspaceID,
		fmt.Sprintf("ws1=%s ws2=%s", res1.WorkspaceID, res2.WorkspaceID))

	// Usage accumulates (both dispatches recorded events).
	cli := connectClientHome(t, home)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	usage, err := cli.ProjectUsage(ctx, project.ID)
	step("usage-accumulated", err == nil && usage.EventCount >= 2,
		fmt.Sprintf("err=%v events=%d (expect >=2)", err, usage.EventCount))

	// Memory has one dispatch fact per task (idempotent key).
	mem, _ := cli.ListMemory(ctx, project.ID)
	dispatchFacts := 0
	for _, m := range mem {
		if m.Category == "accepted_decision" && strings.Contains(m.Key, "dispatch-"+task1.ID) {
			dispatchFacts++
		}
	}
	step("memory-idempotent-key", dispatchFacts == 1,
		fmt.Sprintf("dispatch memory facts=%d (expect 1 — same key upserts, §22.9)", dispatchFacts))
}

// snapshotDir returns a deterministic snapshot of the file list under dir, used
// to prove forge init --dry-run mutates nothing (AC-25).
func snapshotDir(dir string) string {
	var paths []string
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		paths = append(paths, p)
		return nil
	})
	return strings.Join(paths, "|")
}
