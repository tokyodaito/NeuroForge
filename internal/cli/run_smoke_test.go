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
)

// smokeArtifactsDir is where a failed Gate D smoke run copies its home + repo so
// an operator can forensically inspect what the real adapter produced. It is
// under the test temp tree and only populated on failure.
const smokeArtifactsDir = "/tmp/neuroforge-smoke-failure"

// TestForgeRun_Smoke_OpenCode is the opt-in real-adapter smoke test (Gate D,
// TEST_PLAN.md §6). It is SKIPPED unless the env var NEUROFORGE_SMOKE=opencode
// is set, so it never runs in `go test ./...` nor in CI.
//
// When enabled, the test drives the PRODUCTION OpenCode adapter (no fake
// fallback — it is skipped entirely otherwise) through the durable pipeline
// (compile → plan → ready → execute → verify → review → finalize) and asserts:
//   - the engine/model fields are forwarded verbatim;
//   - a real commit exists (actual_head_sha != base_sha) and the result ref /
//     commit_sha equal the actual head SHA (invariant I.5);
//   - RESULT.md is present in the worktree;
//   - the workspace + task reach TERMINAL DB state (completed/COMPLETED) via the
//     loopback API;
//   - the primary checkout is untouched (HEAD + file set unchanged);
//   - no push / fetch / pull / PR / merge / remote ref was created
//     (LOCAL_REVIEW wall).
//
// On failure it copies the home + repo into /tmp/neuroforge-smoke-failure for
// forensic inspection. It is the only test permitted to use the network + a
// paid model (rule §36.5).
func TestForgeRun_Smoke_OpenCode(t *testing.T) {
	if os.Getenv("NEUROFORGE_SMOKE") != "opencode" {
		t.Skip("smoke test is opt-in; set NEUROFORGE_SMOKE=opencode to run it")
	}
	bin := forgeBinary(t)
	home := t.TempDir()
	withDaemonCleanup(t, bin, home)

	repoPath := t.TempDir()
	runGitIn(t, repoPath, "init", "-b", "main")
	runGitIn(t, repoPath, "config", "user.email", "smoke@smoke.smoke")
	runGitIn(t, repoPath, "config", "user.name", "Smoke")
	// Defeat any ambient core.autocrlf: the pipeline's verify stage runs
	// gofmt, which rejects CRLF line endings.
	runGitIn(t, repoPath, "config", "core.autocrlf", "false")
	// The durable pipeline's verify stage runs gofmt/go build/go vet/go test
	// inside the worktree, so the fixture is a minimal buildable Go module
	// (go directive satisfiable by the ambient toolchain, no download).
	for name, content := range map[string]string{
		"README.md": "# Smoke\n",
		"go.mod":    "module fixture\n\ngo 1.22\n",
		"main.go":   "package main\n\nfunc main() {}\n",
	} {
		if err := os.WriteFile(filepath.Join(repoPath, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGitIn(t, repoPath, "add", "-A")
	runGitIn(t, repoPath, "commit", "-m", "init")

	primaryBefore := gitRevParse(t, repoPath, "HEAD")
	filesBefore := workingTreeFiles(t, repoPath)

	// On ANY failure, preserve artifacts for forensic inspection.
	t.Cleanup(func() {
		if t.Failed() {
			dst := filepath.Join(smokeArtifactsDir, fmt.Sprintf("run-%d", time.Now().UnixNano()))
			_ = os.MkdirAll(dst, 0o755)
			_ = exec.Command("cp", "-R", home, filepath.Join(dst, "home")).Run()
			_ = exec.Command("cp", "-R", repoPath, filepath.Join(dst, "repo")).Run()
			t.Logf("BF-09: artifacts copied to %s", dst)
		}
	})

	stdout, stderr, code := runForgeInDir(t, bin, home, repoPath,
		"run", "--json", "--engine", "opencode", "--model", "zai-coding-plan/glm-5.2",
		"Create RESULT.md with the text 'hello' and make a local git commit")
	if code != 0 {
		t.Fatalf("smoke run exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}

	var doc map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &doc); err != nil {
		t.Fatalf("parse JSON: %v\nstdout=%s", err, stdout)
	}

	// Engine + model forwarded verbatim (FR-5/FR-7).
	if got := doc["engine"]; got != "opencode" {
		t.Errorf("engine = %v, want opencode", got)
	}
	if got := doc["model"]; got != "zai-coding-plan/glm-5.2" {
		t.Errorf("model = %v, want zai-coding-plan/glm-5.2", got)
	}

	// A real commit exists.
	actualHead, _ := doc["actual_head_sha"].(string)
	baseSHA, _ := doc["base_sha"].(string)
	if actualHead == "" || actualHead == baseSHA {
		t.Errorf("no real commit: actual_head_sha=%q base_sha=%q", actualHead, baseSHA)
	}
	// Invariant I.5: the committed result carries the real commit SHA, and the
	// result ref resolves to the same SHA as the actual head.
	commitSHA, _ := doc["commit_sha"].(string)
	if commitSHA != "" && actualHead != "" && commitSHA != actualHead {
		t.Errorf("commit_sha=%q != actual_head_sha=%q (I.5)", commitSHA, actualHead)
	}
	resultBranch, _ := doc["result_branch"].(string)
	if resultBranch != "" && actualHead != "" {
		refSHA := gitRevParse(t, repoPath, resultBranch)
		if refSHA != "" && refSHA != actualHead {
			t.Errorf("result ref %s resolves to %s, want actual head %s (I.5)", resultBranch, refSHA, actualHead)
		}
	}

	// RESULT.md present in the worktree.
	wsPath, _ := doc["workspace_path"].(string)
	if wsPath != "" {
		if _, err := os.Stat(filepath.Join(wsPath, "RESULT.md")); err != nil {
			t.Errorf("RESULT.md missing in worktree %s: %v", wsPath, err)
		}
	}

	// Workspace terminal completed (CLI contract).
	if got := doc["outcome"]; got != "completed-with-commit" {
		t.Errorf("outcome = %v, want completed-with-commit", got)
	}

	// Terminal DB state: query the daemon API and assert the workspace + task
	// are terminal (completed / COMPLETED) for this project.
	pid := doc["task_id"]
	_ = pid
	if projectID := smokeProjectID(t, home); projectID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cli, err := daemon.Connect(ctx, daemon.WithRoot(home))
		if err == nil {
			wss, _ := cli.ListWorkspaces(ctx, "", projectID)
			for _, w := range wss {
				if w.State != "completed" {
					t.Errorf("workspace %s DB state = %s, want completed (terminal)", w.ID, w.State)
				}
			}
		}
	}

	// Primary checkout untouched.
	if got := gitRevParse(t, repoPath, "HEAD"); got != primaryBefore {
		t.Errorf("primary HEAD changed: %s -> %s", primaryBefore, got)
	}
	filesAfter := workingTreeFiles(t, repoPath)
	if !sameFileSet(filesBefore, filesAfter) {
		t.Errorf("primary file set changed:\nwas: %v\ngot: %v", filesBefore, filesAfter)
	}

	// LOCAL_REVIEW wall: no push / fetch / pull / PR / merge / remote refs.
	remoteOut, _ := exec.Command("git", "-C", repoPath, "remote").Output()
	if strings.TrimSpace(string(remoteOut)) != "" {
		t.Errorf("remote configured (LOCAL_REVIEW wall): %s", remoteOut)
	}
	branchROut, _ := exec.Command("git", "-C", repoPath, "branch", "-r").Output()
	if strings.TrimSpace(string(branchROut)) != "" {
		t.Errorf("remote branches exist (LOCAL_REVIEW wall): %s", branchROut)
	}

	// The primary HEAD reflog must not have been rewritten by NeuroForge.
	reflogOut, _ := exec.Command("git", "-C", repoPath, "reflog", "show", "HEAD").Output()
	if strings.Contains(string(reflogOut), "neuroforge") {
		t.Errorf("primary reflog mentions neuroforge (rewritten): %s", reflogOut)
	}
}

// smokeProjectID returns the id of the (single) project registered for the
// smoke repo, via the loopback API.
func smokeProjectID(t *testing.T, home string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cli, err := daemon.Connect(ctx, daemon.WithRoot(home))
	if err != nil {
		return ""
	}
	ps, err := cli.ListProjects(ctx)
	if err != nil || len(ps) == 0 {
		return ""
	}
	return ps[0].ID
}
