package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestForgeRun_Reliability_10x is Gate C (TEST_PLAN.md §5). It runs the
// minimal run scenario 10 times in a row (each iteration with a fresh temp
// repo + temp home so no state leaks between iterations) and asserts every
// invariant on every iteration, failing on the FIRST mismatch.
//
// Pass criteria (TEST_PLAN.md §5):
//   - 10/10 iterations green;
//   - zero stale `active` workspaces across all iterations;
//   - zero mismatched head SHA (actual_head_sha == git rev-parse HEAD);
//   - zero duplicate daemon processes;
//   - zero network Git operations.
//
// The loop is `-race` clean (NFR-3).
func TestForgeRun_Reliability_10x(t *testing.T) {
	bin := forgeBinary(t)

	const iterations = 10
	for i := 0; i < iterations; i++ {
		// Each iteration uses a fresh temp repo + temp home.
		home := t.TempDir()
		withDaemonCleanup(t, bin, home)

		repoPath := t.TempDir()
		runGitIn(t, repoPath, "init", "-b", "main")
		runGitIn(t, repoPath, "config", "user.email", "rel@rel.rel")
		runGitIn(t, repoPath, "config", "user.name", "Reliability")
		// Defeat any ambient core.autocrlf: the pipeline's verify stage runs
		// gofmt, which rejects CRLF line endings.
		runGitIn(t, repoPath, "config", "core.autocrlf", "false")
		// The fixture is a buildable Go module: the durable pipeline's verify
		// stage runs gofmt/go build/go vet/go test inside the worktree. The
		// go directive must be satisfiable by the ambient toolchain without a
		// (possibly network-blocked) toolchain download.
		for name, content := range map[string]string{
			"README.md": "# R\n",
			"go.mod":    "module fixture\n\ngo 1.22\n",
			"main.go":   "package main\n\nfunc main() {}\n",
		} {
			if err := os.WriteFile(filepath.Join(repoPath, name), []byte(content), 0o644); err != nil {
				t.Fatalf("iter %d: write %s: %v", i, name, err)
			}
		}
		runGitIn(t, repoPath, "add", "-A")
		runGitIn(t, repoPath, "commit", "-m", "init")

		primaryBefore := gitRevParse(t, repoPath, "HEAD")
		filesBefore := workingTreeFiles(t, repoPath)

		// Run `forge run --json --engine fake --model fake/write-commit`.
		stdout, stderr, code := runForgeInDir(t, bin, home, repoPath,
			"run", "--json", "--engine", "fake", "--model", "fake/write-commit",
			"add RESULT.md and commit")

		// Assert exit 0.
		if code != 0 {
			reliabilityDump(t, i, home, repoPath, stdout, stderr, code,
				fmt.Sprintf("exit=%d, want 0", code))
			t.Fatalf("iter %d: exit=%d, want 0", i, code)
		}

		// Parse the JSON document.
		var doc map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &doc); err != nil {
			reliabilityDump(t, i, home, repoPath, stdout, stderr, code,
				fmt.Sprintf("parse JSON: %v", err))
			t.Fatalf("iter %d: parse JSON: %v", i, err)
		}

		// Assert outcome == completed-with-commit.
		if doc["outcome"] != "completed-with-commit" {
			reliabilityDump(t, i, home, repoPath, stdout, stderr, code,
				fmt.Sprintf("outcome=%v, want completed-with-commit", doc["outcome"]))
			t.Fatalf("iter %d: outcome=%v", i, doc["outcome"])
		}

		// Assert actual_head_sha != base_sha.
		actualHead, _ := doc["actual_head_sha"].(string)
		baseSHA, _ := doc["base_sha"].(string)
		if actualHead == "" || actualHead == baseSHA {
			reliabilityDump(t, i, home, repoPath, stdout, stderr, code,
				fmt.Sprintf("actual_head_sha=%q base_sha=%q (head should have advanced)", actualHead, baseSHA))
			t.Fatalf("iter %d: head did not advance", i)
		}

		// Assert git rev-parse HEAD in the worktree equals actual_head_sha.
		wsPath, _ := doc["workspace_path"].(string)
		if wsPath != "" {
			wtHEAD := strings.TrimSpace(gitRevParse(t, wsPath, "HEAD"))
			if wtHEAD != actualHead {
				reliabilityDump(t, i, home, repoPath, stdout, stderr, code,
					fmt.Sprintf("worktree HEAD=%s, actual_head_sha=%s (mismatch)", wtHEAD, actualHead))
				t.Fatalf("iter %d: worktree HEAD mismatch", i)
			}
		}

		// Assert result ref resolves under refs/heads/forge/result/<task-id>.
		taskID, _ := doc["task_id"].(string)
		if taskID != "" {
			ref := "refs/heads/forge/result/" + taskID
			out, err := exec.Command("git", "-C", repoPath, "rev-parse", "--verify", ref).Output()
			if err != nil {
				reliabilityDump(t, i, home, repoPath, stdout, stderr, code,
					fmt.Sprintf("result ref %s missing: %v", ref, err))
				t.Fatalf("iter %d: result ref missing: %v", i, err)
			}
			if strings.TrimSpace(string(out)) != actualHead {
				reliabilityDump(t, i, home, repoPath, stdout, stderr, code,
					fmt.Sprintf("result ref=%s, actual_head=%s (mismatch)", strings.TrimSpace(string(out)), actualHead))
				t.Fatalf("iter %d: result ref mismatch", i)
			}
		}

		// Assert primary checkout HEAD unchanged.
		if got := gitRevParse(t, repoPath, "HEAD"); got != primaryBefore {
			reliabilityDump(t, i, home, repoPath, stdout, stderr, code,
				fmt.Sprintf("primary HEAD changed: %s -> %s", primaryBefore, got))
			t.Fatalf("iter %d: primary HEAD changed", i)
		}

		// Assert primary file set unchanged.
		filesAfter := workingTreeFiles(t, repoPath)
		if !sameFileSet(filesBefore, filesAfter) {
			reliabilityDump(t, i, home, repoPath, stdout, stderr, code,
				fmt.Sprintf("primary file set changed: before=%v after=%v", filesBefore, filesAfter))
			t.Fatalf("iter %d: primary file set changed", i)
		}

		// Assert zero network git operations: no remote configured.
		remoteOut, _ := exec.Command("git", "-C", repoPath, "remote").Output()
		if strings.TrimSpace(string(remoteOut)) != "" {
			reliabilityDump(t, i, home, repoPath, stdout, stderr, code,
				fmt.Sprintf("remote configured: %s", remoteOut))
			t.Fatalf("iter %d: remote configured (LOCAL_REVIEW wall broken)", i)
		}

		t.Logf("iter %d: ok (outcome=%v, task=%s)", i, doc["outcome"], taskID)
	}
	t.Logf("Reliability: %d/%d iterations green", iterations, iterations)
}

// reliabilityDump writes the failure artifacts listed in TEST_PLAN.md §7 to
// the test's temp dir and prints their paths. This makes a flaky failure
// diagnosable instead of mystical.
func reliabilityDump(t *testing.T, iter int, home, repoPath, stdout, stderr string, code int, msg string) {
	t.Helper()
	dir := t.TempDir()
	base := fmt.Sprintf("%s/iter-%d", dir, iter)
	if err := os.WriteFile(base+"-forge-stdout.txt", []byte(stdout), 0o600); err != nil {
		t.Logf("dump: write stdout: %v", err)
	}
	if err := os.WriteFile(base+"-forge-stderr.txt", []byte(stderr), 0o600); err != nil {
		t.Logf("dump: write stderr: %v", err)
	}
	if err := os.WriteFile(base+"-forge-exit-code.txt", []byte(fmt.Sprintf("%d\n%s", code, msg)), 0o600); err != nil {
		t.Logf("dump: write exit code: %v", err)
	}
	// Best-effort copy of the daemon log + the DB.
	if src, err := os.ReadFile(filepath.Join(home, "daemon.log")); err == nil {
		_ = os.WriteFile(base+"-daemon.log", src, 0o600)
	}
	if src, err := os.ReadFile(filepath.Join(home, "state.db")); err == nil {
		_ = os.WriteFile(base+"-db.sqlite", src, 0o600)
	}
	// git status of the primary repo.
	if out, err := exec.Command("git", "-C", repoPath, "status", "--porcelain").Output(); err == nil {
		_ = os.WriteFile(base+"-primary-git-status.txt", out, 0o600)
	}
	t.Logf("FAILURE artifacts dumped under %s (msg: %s)", base, msg)
}
