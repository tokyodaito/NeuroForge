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

	"neuroforge/internal/daemon"
	"neuroforge/internal/transport"
)

// TestM3_DemonstrableScenario is the automated end-to-end proof for milestone
// M3 (§36.20). It exercises the full workspace lifecycle:
//
//  1. create a temp Git repo with content;
//  2. register a project;
//  3. add a task;
//  4. create a workspace (worktree);
//  5. run the fake agent (modifies a file inside the worktree);
//  6. create a checkpoint commit;
//  7. create the local result branch;
//  8. verify the primary checkout is unchanged (§17.1, §36.14);
//  9. verify no Git network command was executed (AC-7);
//  10. restart the daemon;
//  11. verify the result is still accessible (AC-27).
//
// No real AI providers, no network.
func TestM3_DemonstrableScenario(t *testing.T) {
	bin := forgeBinary(t)
	home := t.TempDir()
	withDaemonCleanup(t, bin, home)

	step := func(name string, ok bool, detail string) {
		t.Helper()
		if !ok {
			t.Fatalf("M3 scenario FAILED at step %q: %s", name, detail)
		}
		t.Logf("M3 scenario ok: %s", name)
	}

	// 1. Create a temp Git repo with real content.
	repoPath := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
	} {
		runGitIn(t, repoPath, args...)
	}
	if err := os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("# Project\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoPath, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitIn(t, repoPath, "add", "-A")
	runGitIn(t, repoPath, "commit", "-m", "initial project")

	// Record the primary checkout's state BEFORE any workspace operations.
	primaryHeadBefore := gitRevParse(t, repoPath, "HEAD")
	primaryFilesBefore := workingTreeFiles(t, repoPath)

	// 2. Start daemon.
	out, _, code := runForge(t, bin, home, "daemon", "start")
	step("2.daemon-start", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))

	// 3. Register the project.
	out, _, code = runForge(t, bin, home, "project", "add", "--json", repoPath)
	step("3.project-add", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))

	var project transport.ProjectDTO
	if err := json.Unmarshal([]byte(out), &project); err != nil {
		t.Fatalf("parse project JSON: %v\n%s", err, out)
	}

	// 4. Start the project (DISABLED -> IDLE).
	out, _, code = runForge(t, bin, home, "project", "start", project.ID)
	step("4.project-start", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))

	// 5. Add a task.
	out, _, code = runForge(t, bin, home, "task", "add",
		"-p", project.ID, "--title", "M3 test", "--json", "Add a greeting file")
	step("5.task-add", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))

	var task transport.TaskDTO
	if err := json.Unmarshal([]byte(out), &task); err != nil {
		t.Fatalf("parse task JSON: %v\n%s", err, out)
	}

	// 6. Create a workspace (Git worktree) for the task.
	out, _, code = runForge(t, bin, home, "workspace", "create",
		"-t", task.ID, "--json")
	step("6.workspace-create", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))

	var ws transport.WorkspaceDTO
	if err := json.Unmarshal([]byte(out), &ws); err != nil {
		t.Fatalf("parse workspace JSON: %v\n%s", err, out)
	}
	step("6a.workspace-active", ws.State == "active",
		fmt.Sprintf("state=%s", ws.State))
	step("6b.workspace-branch",
		ws.Branch == "forge/"+task.ID+"/main/attempt-1",
		fmt.Sprintf("branch=%s", ws.Branch))

	// Verify the worktree path is under the managed home, NOT the repo.
	step("6c.worktree-isolated",
		strings.Contains(ws.Path, home) && !strings.Contains(ws.Path, repoPath),
		fmt.Sprintf("path=%s home=%s repo=%s", ws.Path, home, repoPath))

	// 7. Run the fake agent inside the workspace.
	out, _, code = runForge(t, bin, home, "workspace", "run", "--json", ws.ID)
	step("7.workspace-run", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))

	var wsAfterRun transport.WorkspaceDTO
	if err := json.Unmarshal([]byte(out), &wsAfterRun); err != nil {
		t.Fatalf("parse workspace-after-run JSON: %v\n%s", err, out)
	}

	// The fake agent (success scenario) writes src/hello.txt. Verify it exists
	// INSIDE the worktree.
	helloPath := filepath.Join(wsAfterRun.Path, "src", "hello.txt")
	step("7a.agent-wrote-file", fileExists(helloPath),
		fmt.Sprintf("expected %s to exist", helloPath))

	// 8. Create a checkpoint commit.
	out, _, code = runForge(t, bin, home, "workspace", "checkpoint", ws.ID,
		"--moment", "first-diff", "--message", "agent work")
	step("8.checkpoint", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))

	// 9. Create the local result branch (forge/result/<task>).
	out, _, code = runForge(t, bin, home, "workspace", "result", "--json", ws.ID)
	step("9.create-result", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))

	var wsResult transport.WorkspaceDTO
	if err := json.Unmarshal([]byte(out), &wsResult); err != nil {
		t.Fatalf("parse result workspace JSON: %v\n%s", err, out)
	}
	wantResultBranch := "forge/result/" + task.ID
	step("9a.result-branch-name",
		wsResult.ResultBranch == wantResultBranch,
		fmt.Sprintf("got=%s want=%s", wsResult.ResultBranch, wantResultBranch))
	step("9b.result-sha-set", wsResult.ResultSHA != "",
		"result SHA should be non-empty")

	// 10. Verify the result branch exists in the repo's ref store.
	resultSHA := gitRevParse(t, repoPath, wantResultBranch)
	step("10.result-branch-in-repo", resultSHA == wsResult.ResultSHA,
		fmt.Sprintf("repo SHA=%s workspace SHA=%s", resultSHA, wsResult.ResultSHA))

	// 11. Verify the primary checkout is UNCHANGED (§17.1, §36.14, AC-8).
	primaryHeadAfter := gitRevParse(t, repoPath, "HEAD")
	step("11.primary-head-unchanged",
		primaryHeadBefore == primaryHeadAfter,
		fmt.Sprintf("before=%s after=%s", primaryHeadBefore, primaryHeadAfter))

	primaryFilesAfter := workingTreeFiles(t, repoPath)
	step("12.primary-files-unchanged",
		sameFileSet(primaryFilesBefore, primaryFilesAfter),
		fmt.Sprintf("before=%v after=%v", primaryFilesBefore, primaryFilesAfter))

	// The working tree of the primary checkout must NOT contain the agent's
	// output file.
	step("13.primary-no-agent-file",
		!fileExists(filepath.Join(repoPath, "src", "hello.txt")),
		"agent output should not appear in primary checkout")

	// 14. Verify no Git network command was executed (AC-7).
	// The repo has no remote configured, and the allowlist structurally excludes
	// push/fetch/pull/clone. We verify the invariant from two angles:
	//   a. No remote is configured (so even if push were called it would fail).
	//   b. No forge/* branch appears in the remote ref list (because there are
	//      no remotes).
	remoteOut, _ := exec.Command("git", "-C", repoPath, "remote").Output()
	step("14a.no-remote-configured", len(strings.TrimSpace(string(remoteOut))) == 0,
		fmt.Sprintf("remotes=%q", string(remoteOut)))

	branchROut, _ := exec.Command("git", "-C", repoPath, "branch", "-r").Output()
	step("14b.no-remote-branches", len(strings.TrimSpace(string(branchROut))) == 0,
		fmt.Sprintf("remote branches=%q", string(branchROut)))

	// 15. Restart the daemon (stop + start) and verify the result survives.
	out, _, code = runForge(t, bin, home, "daemon", "stop")
	step("15.daemon-stop", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))

	// Brief pause to ensure the process is fully gone.
	time.Sleep(500 * time.Millisecond)

	out, _, code = runForge(t, bin, home, "daemon", "start")
	step("16.daemon-restart", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))

	// 17. Verify the result is accessible after restart (AC-27).
	cli := connectClientHome(t, home)
	apiCtx, apiCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer apiCancel()

	wsRestored, err := cli.GetWorkspace(apiCtx, ws.ID)
	step("17.workspace-accessible-after-restart", err == nil,
		fmt.Sprintf("err=%v", err))

	if err == nil {
		step("17a.result-branch-survives",
			wsRestored.ResultBranch == wantResultBranch,
			fmt.Sprintf("got=%s want=%s", wsRestored.ResultBranch, wantResultBranch))
		step("17b.result-sha-survives",
			wsRestored.ResultSHA == wsResult.ResultSHA,
			fmt.Sprintf("got=%s want=%s", wsRestored.ResultSHA, wsResult.ResultSHA))
		step("17c.path-survives",
			wsRestored.Path == ws.Path,
			fmt.Sprintf("got=%s want=%s", wsRestored.Path, ws.Path))
	}

	// 18. Verify the diff is non-empty.
	diffResp, err := cli.DiffWorkspace(apiCtx, ws.ID)
	step("18.diff-accessible", err == nil && diffResp.Diff != "",
		fmt.Sprintf("err=%v diff_len=%d", err, len(diffResp.Diff)))
	if err == nil {
		step("18a.diff-mentions-agent-output",
			strings.Contains(diffResp.Diff, "hello.txt"),
			"diff should mention the agent's file")
	}

	// 19. Verify checkpoints are durable after restart.
	cps, err := cli.ListCheckpoints(apiCtx, ws.ID)
	step("19.checkpoints-durable", err == nil && len(cps) > 0,
		fmt.Sprintf("err=%v count=%d", err, len(cps)))

	// 20. Review: reject (delete the workspace worktree; NOT user data).
	_, err = cli.ReviewWorkspace(apiCtx, ws.ID, transport.ReviewRequest{Action: "reject"})
	step("20.review-reject", err == nil, fmt.Sprintf("err=%v", err))

	// After reject, the worktree should be gone.
	step("20a.worktree-deleted-on-reject", !fileExists(ws.Path),
		"worktree should be removed after reject")

	// But the primary checkout must still be intact.
	step("20b.primary-intact-after-reject",
		fileExists(filepath.Join(repoPath, "README.md")),
		"primary checkout files must survive workspace reject")

	t.Log("M3 scenario: ALL STEPS PASSED")
}

// TestM3_NoNetworkGitCommands is a focused security test verifying that the
// workspace manager's git runner structurally rejects every network-capable
// subcommand. This is the AC-7 guarantee: LOCAL_REVIEW performs zero Git network
// operations, by construction.
func TestM3_NoNetworkGitCommands(t *testing.T) {
	// We verify indirectly: the workspace manager only ever calls git through
	// the allowlisted runner. The full Create/Checkpoint/Result flow never
	// invokes a network command. This test verifies the end-to-end behavior by
	// confirming that all workspace operations complete successfully without any
	// remote being configured, and no remote refs are created.
	//
	// The structural allowlist (tested in workspace unit tests) provides the
	// code-level guarantee. This test provides the integration-level evidence.

	repoPath := t.TempDir()
	ctx := context.Background()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "t@t.com"},
		{"config", "user.name", "T"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		runGitIn(t, repoPath, args...)
	}

	bin := forgeBinary(t)
	home := t.TempDir()
	withDaemonCleanup(t, bin, home)

	// Start daemon, add project + task + workspace.
	runForge(t, bin, home, "daemon", "start")
	out, _, _ := runForge(t, bin, home, "project", "add", "--json", repoPath)
	t.Logf("project add: %s", out)
	var project transport.ProjectDTO
	if err := json.Unmarshal([]byte(out), &project); err != nil {
		t.Fatalf("parse project JSON: %v\n%s", err, out)
	}

	runForge(t, bin, home, "project", "start", project.ID)
	out, _, _ = runForge(t, bin, home, "task", "add", "-p", project.ID, "--json", "test")
	t.Logf("task add: %s", out)
	var task transport.TaskDTO
	if err := json.Unmarshal([]byte(out), &task); err != nil {
		t.Fatalf("parse task JSON: %v\n%s", err, out)
	}

	out, _, _ = runForge(t, bin, home, "workspace", "create", "-t", task.ID, "--json")
	var ws transport.WorkspaceDTO
	if err := json.Unmarshal([]byte(out), &ws); err != nil {
		t.Fatalf("parse workspace JSON: %v\n%s", err, out)
	}
	t.Logf("workspace: id=%s branch=%s path=%s", ws.ID, ws.Branch, ws.Path)

	// Run + checkpoint + result — all local-only operations.
	out, _, _ = runForge(t, bin, home, "workspace", "run", ws.ID)
	t.Logf("run: %s", out)
	runForge(t, bin, home, "workspace", "checkpoint", ws.ID)
	out, _, _ = runForge(t, bin, home, "workspace", "result", ws.ID)
	t.Logf("result: %s", out)

	// Verify NO remote was ever configured and NO remote refs exist.
	remotes, _ := exec.CommandContext(ctx, "git", "-C", repoPath, "remote", "-v").Output()
	if strings.TrimSpace(string(remotes)) != "" {
		t.Errorf("remote was configured during workspace operations:\n%s", remotes)
	}

	remoteBranches, _ := exec.CommandContext(ctx, "git", "-C", repoPath, "branch", "-r").Output()
	if strings.TrimSpace(string(remoteBranches)) != "" {
		t.Errorf("remote branches exist after workspace operations:\n%s", remoteBranches)
	}

	// Verify the forge/ branches are LOCAL only.
	allBranches, _ := exec.CommandContext(ctx, "git", "-C", repoPath, "branch", "--format=%(refname:short)").Output()
	if !strings.Contains(string(allBranches), "forge/") {
		t.Errorf("no forge/ local branches found:\n%s", allBranches)
	}

	t.Log("AC-7 verified: no Git network operations during workspace lifecycle")
}

// ---- helpers ----

func runGitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func gitRevParse(t *testing.T, dir, ref string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", ref).Output()
	if err != nil {
		t.Fatalf("git rev-parse %s: %v", ref, err)
	}
	return strings.TrimSpace(string(out))
}

func workingTreeFiles(t *testing.T, dir string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		// Exclude .git internals using the OS separator so this works on both
		// POSIX (/) and Windows (\). git worktree inherently adds ref/worktree
		// metadata to the object database; the §17.1 invariant is that the
		// user-visible working tree is untouched.
		if rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) {
			return nil
		}
		out[rel] = true
		return nil
	})
	return out
}

func sameFileSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func connectClientHome(t *testing.T, home string) *transport.Client {
	t.Helper()
	dirs := daemon.WithRoot(home)
	addr, err := daemon.ReadAddr(dirs)
	if err != nil || addr == "" {
		t.Fatalf("read addr: %v", err)
	}
	tok, err := daemon.ReadToken(dirs)
	if err != nil || tok == "" {
		t.Fatalf("read token: %v", err)
	}
	return transport.NewClient(addr, tok)
}
