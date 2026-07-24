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

// makeTestGitRepo creates a real git repository in a temp dir and returns its path.
func makeTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// connectClient creates a transport.Client connected to the running daemon.
func connectClient(t *testing.T, home string) *transport.Client {
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

// TestM1_DemonstrableScenario is the automated end-to-end proof for milestone
// M1 (§36.20). It builds the real forge binary and exercises the full project
// and task lifecycle through the daemon API. No real AI providers.
func TestM1_DemonstrableScenario(t *testing.T) {
	bin := forgeBinary(t)
	home := t.TempDir()
	withDaemonCleanup(t, bin, home)

	step := func(name string, ok bool, detail string) {
		t.Helper()
		if !ok {
			t.Fatalf("M1 scenario FAILED at step %q: %s", name, detail)
		}
		t.Logf("M1 scenario ok: %s", name)
	}

	repoPath := makeTestGitRepo(t)

	// 1. Start daemon.
	out, _, code := runForge(t, bin, home, "daemon", "start")
	step("1.daemon-start", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))

	// 2. Add a project via CLI.
	out, _, code = runForge(t, bin, home, "project", "add", repoPath)
	step("2.project-add", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))

	// 3. List projects with --json and verify.
	out, _, code = runForge(t, bin, home, "project", "list", "--json")
	step("3a.project-list-json", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))
	var projects []transport.ProjectDTO
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &projects); err != nil {
		t.Fatalf("decode projects json: %v\n%s", err, out)
	}
	step("3b.project-list-nonempty", len(projects) >= 1, fmt.Sprintf("len=%d", len(projects)))
	projectID := projects[0].ID
	step("3c.project-state-disabled", projects[0].State == "DISABLED",
		fmt.Sprintf("state=%s", projects[0].State))

	// 4. Show project with --json.
	out, _, code = runForge(t, bin, home, "project", "show", projectID, "--json")
	step("4.project-show-json", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))

	// 5. Start the project (DISABLED -> IDLE).
	out, _, code = runForge(t, bin, home, "project", "start", projectID)
	step("5.project-start", code == ExitOK && strings.Contains(out, "IDLE"),
		fmt.Sprintf("exit=%d out=%s", code, out))

	// 6. Add a free-form task (AC-3).
	out, _, code = runForge(t, bin, home, "task", "add", "-p", projectID,
		"Fix the login screen — two progress indicators appear")
	step("6.task-add", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))

	// 7. Add a task with an attachment (AC-4).
	attDir := t.TempDir()
	attPath := filepath.Join(attDir, "screenshot.png")
	if err := os.WriteFile(attPath, []byte("fake-png"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, code = runForge(t, bin, home, "task", "add", "-p", projectID,
		"--title", "Bug fix", "-a", attPath, "See attached screenshot")
	step("7.task-add-attachment", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))

	// 8. List tasks with --json.
	out, _, code = runForge(t, bin, home, "task", "list", "--project", projectID, "--json")
	step("8a.task-list-json", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))
	var tasks []transport.TaskDTO
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &tasks); err != nil {
		t.Fatalf("decode tasks json: %v\n%s", err, out)
	}
	step("8b.task-list-count", len(tasks) == 2, fmt.Sprintf("len=%d want 2", len(tasks)))
	taskID := tasks[0].ID
	step("8c.task-state-new", tasks[0].State == "NEW", fmt.Sprintf("state=%s", tasks[0].State))

	// 9. Show task with --json.
	out, _, code = runForge(t, bin, home, "task", "show", taskID, "--json")
	step("9.task-show-json", code == ExitOK, fmt.Sprintf("exit=%d out=%s", code, out))

	// 10. Pause the task.
	out, _, code = runForge(t, bin, home, "task", "pause", taskID)
	step("10.task-pause", code == ExitOK && strings.Contains(out, "PAUSED"),
		fmt.Sprintf("exit=%d out=%s", code, out))

	// 11. Cancel the second task.
	out, _, code = runForge(t, bin, home, "task", "cancel", tasks[1].ID)
	step("11.task-cancel", code == ExitOK && strings.Contains(out, "CANCELLED"),
		fmt.Sprintf("exit=%d out=%s", code, out))

	// 12. Daemon restart persistence: stop, restart, verify state survives.
	runForge(t, bin, home, "daemon", "stop")
	runForge(t, bin, home, "daemon", "start")

	// 13. Projects still present and state persisted (IDLE).
	cli := connectClient(t, home)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	restartedProjects, err := cli.ListProjects(ctx)
	step("13a.projects-after-restart", err == nil && len(restartedProjects) == 1,
		fmt.Sprintf("err=%v len=%d", err, len(restartedProjects)))
	step("13b.project-state-persisted",
		len(restartedProjects) > 0 && restartedProjects[0].State == "IDLE",
		fmt.Sprintf("state=%v", restartedProjects))

	// 14. Tasks still present after restart.
	restartedTasks, err := cli.ListTasks(ctx, projectID)
	step("14.tasks-after-restart", err == nil && len(restartedTasks) == 2,
		fmt.Sprintf("err=%v len=%d", err, len(restartedTasks)))

	// 15. Audit trail contains project and task events.
	entries := auditEntries(t, daemon.WithRoot(home))
	hasProjectAdded := false
	hasTaskCreated := false
	for _, e := range entries {
		if e["type"] == "project.added" {
			hasProjectAdded = true
		}
		if e["type"] == "task.created" {
			hasTaskCreated = true
		}
	}
	step("15a.audit-project-added", hasProjectAdded, "project.added not in audit")
	step("15b.audit-task-created", hasTaskCreated, "task.created not in audit")

	t.Logf("M1 scenario: all steps passed ✓")
}

// TestM1_DuplicateProjectRejected verifies that adding the same repo path twice
// is rejected (§8.1).
func TestM1_DuplicateProjectRejected(t *testing.T) {
	bin := forgeBinary(t)
	home := t.TempDir()
	withDaemonCleanup(t, bin, home)

	repoPath := makeTestGitRepo(t)

	// Start daemon and add project.
	runForge(t, bin, home, "daemon", "start")
	out, _, code := runForge(t, bin, home, "project", "add", repoPath)
	if code != ExitOK {
		t.Fatalf("first add: exit=%d out=%s", code, out)
	}

	// Second add must fail.
	out, errOut, code := runForge(t, bin, home, "project", "add", repoPath)
	if code == ExitOK {
		t.Fatalf("expected error for duplicate; got exit 0: %s", out)
	}
	combined := out + errOut
	if !strings.Contains(combined, "already registered") && !strings.Contains(strings.ToLower(combined), "already") {
		t.Errorf("expected 'already registered' error; got: %s", combined)
	}
}

// TestM1_NonexistentRepositoryRejected verifies that adding a non-Git directory
// is rejected.
func TestM1_NonexistentRepositoryRejected(t *testing.T) {
	bin := forgeBinary(t)
	home := t.TempDir()
	withDaemonCleanup(t, bin, home)

	plainDir := t.TempDir() // not a git repo

	runForge(t, bin, home, "daemon", "start")
	out, errOut, code := runForge(t, bin, home, "project", "add", plainDir)
	if code == ExitOK {
		t.Fatalf("expected error for non-git directory; got exit 0: %s", out)
	}
	combined := out + errOut
	if !strings.Contains(strings.ToLower(combined), "not a git") && !strings.Contains(strings.ToLower(combined), "not a git repository") {
		t.Errorf("expected 'not a Git repository' error; got: %s", combined)
	}
}

// TestM1_LocalAPI tests the transport client → daemon → storage round-trip
// directly through the API (no CLI command parsing).
func TestM1_LocalAPI(t *testing.T) {
	bin := forgeBinary(t)
	home := t.TempDir()
	withDaemonCleanup(t, bin, home)
	repoPath := makeTestGitRepo(t)

	runForge(t, bin, home, "daemon", "start")
	cli := connectClient(t, home)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Add project via API.
	p, err := cli.AddProject(ctx, transport.AddProjectRequest{Path: repoPath})
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	if p.State != "DISABLED" {
		t.Errorf("state=%s, want DISABLED", p.State)
	}

	// Start via API.
	p, err = cli.StartProject(ctx, p.ID)
	if err != nil {
		t.Fatalf("StartProject: %v", err)
	}
	if p.State != "IDLE" {
		t.Errorf("state=%s, want IDLE", p.State)
	}

	// Add task via API.
	task, err := cli.AddTask(ctx, transport.AddTaskRequest{
		ProjectID:   p.ID,
		Description: "API test task",
		Priority:    "HIGH",
	})
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if task.Priority != "HIGH" {
		t.Errorf("priority=%s, want HIGH", task.Priority)
	}

	// List tasks via API.
	tasks, err := cli.ListTasks(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("tasks=%d, want 1", len(tasks))
	}

	// Cancel via API.
	task, err = cli.CancelTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	if task.State != "CANCELLED" {
		t.Errorf("state=%s, want CANCELLED", task.State)
	}

	// Invalid transition: cancel already cancelled.
	_, err = cli.CancelTask(ctx, task.ID)
	if err == nil {
		t.Fatal("expected error: cancel from CANCELLED")
	}

	// Invalid transition: start already started project.
	_, err = cli.StartProject(ctx, p.ID)
	if err == nil {
		t.Fatal("expected error: start from IDLE")
	}
}
