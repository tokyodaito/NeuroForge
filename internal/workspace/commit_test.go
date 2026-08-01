package workspace_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"neuroforge/internal/storage"
	"neuroforge/internal/workspace"
)

// setupCommitWorkspace creates a repo + manager + one active workspace and
// returns the manager, the DB, the workspace and the repo path.
func setupCommitWorkspace(t *testing.T) (*workspace.Manager, *storage.DB, workspace.Workspace, string) {
	t.Helper()
	repo := setupTestRepo(t)
	mgr, db, _ := setupManager(t)
	ctx := context.Background()
	ws, err := mgr.Create(ctx, workspace.CreateRequest{
		ProjectID: "proj", ProjectPath: repo, TaskID: "task-1",
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	return mgr, db, ws, repo
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v in %s: %v", args, dir, err)
	}
	return strings.TrimSpace(string(out))
}

func TestCommitWorktreeChanges_DirtyTreeCommits(t *testing.T) {
	mgr, _, ws, repo := setupCommitWorkspace(t)
	ctx := context.Background()

	if err := os.MkdirAll(filepath.Join(ws.Path, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(ws.Path, "src", "hello.txt"), "hello\n")

	base := ws.HeadSHA
	sha, created, err := mgr.CommitWorktreeChanges(ctx, ws.ID, "forge: result for task task-1")
	if err != nil {
		t.Fatalf("CommitWorktreeChanges: %v", err)
	}
	if !created {
		t.Fatal("created = false, want true (tree was dirty)")
	}
	if sha == base {
		t.Errorf("commit SHA == base %s, want a new commit", base)
	}
	if got := gitOut(t, ws.Path, "rev-parse", "HEAD"); got != sha {
		t.Errorf("worktree HEAD = %s, want %s", got, sha)
	}
	if subj := gitOut(t, ws.Path, "log", "-1", "--format=%s"); subj != "forge: result for task task-1" {
		t.Errorf("commit subject = %q", subj)
	}
	// The commit contains the new file and the tree is clean afterwards.
	if names := gitOut(t, ws.Path, "show", "--name-only", "--format=", sha); !strings.Contains(names, "src/hello.txt") {
		t.Errorf("commit does not contain src/hello.txt: %q", names)
	}
	if status := gitOut(t, ws.Path, "status", "--porcelain"); status != "" {
		t.Errorf("worktree still dirty after commit: %q", status)
	}
	// The primary checkout is untouched.
	if got := readHeadSHA(t, repo); got != base {
		t.Errorf("primary HEAD changed: %s -> %s", base, got)
	}
}

func TestCommitWorktreeChanges_CleanTreeIsNoOpAndIdempotent(t *testing.T) {
	mgr, _, ws, _ := setupCommitWorkspace(t)
	ctx := context.Background()

	// Clean tree: no commit.
	sha, created, err := mgr.CommitWorktreeChanges(ctx, ws.ID, "forge: result for task task-1")
	if err != nil {
		t.Fatalf("CommitWorktreeChanges (clean): %v", err)
	}
	if created {
		t.Error("created = true on a clean tree, want false (no empty commit)")
	}
	if sha != ws.HeadSHA {
		t.Errorf("sha = %s, want current HEAD %s", sha, ws.HeadSHA)
	}

	// Dirty once, commit, then re-drive: the second call must not create a
	// duplicate commit.
	mustWrite(t, filepath.Join(ws.Path, "dirty.txt"), "dirty\n")
	first, created, err := mgr.CommitWorktreeChanges(ctx, ws.ID, "forge: result for task task-1")
	if err != nil || !created {
		t.Fatalf("first commit: created=%v err=%v", created, err)
	}
	second, created, err := mgr.CommitWorktreeChanges(ctx, ws.ID, "forge: result for task task-1")
	if err != nil {
		t.Fatalf("re-drive: %v", err)
	}
	if created {
		t.Error("re-drive created a duplicate commit")
	}
	if second != first {
		t.Errorf("re-drive SHA = %s, want %s", second, first)
	}
	if n := gitOut(t, ws.Path, "rev-list", "--count", ws.HeadSHA+"..HEAD"); n != "1" {
		t.Errorf("commits beyond base = %s, want exactly 1", n)
	}
}

func TestCommitWorktreeChanges_RefusesPrimaryCheckout(t *testing.T) {
	mgr, db, ws, repo := setupCommitWorkspace(t)
	ctx := context.Background()

	// Corrupt the workspace record to point at the primary checkout (a .git
	// DIRECTORY, not a linked worktree): the guard must refuse before any
	// git mutation.
	if _, err := db.Exec(ctx, `UPDATE workspaces SET path = ? WHERE id = ?`, repo, ws.ID); err != nil {
		t.Fatalf("point workspace at primary checkout: %v", err)
	}
	mustWrite(t, filepath.Join(repo, "dirty.txt"), "dirty\n")
	t.Cleanup(func() { _ = os.Remove(filepath.Join(repo, "dirty.txt")) })

	if _, _, err := mgr.CommitWorktreeChanges(ctx, ws.ID, "forge: result"); err == nil {
		t.Fatal("CommitWorktreeChanges on the primary checkout: want error, got nil")
	}
	if status := gitOut(t, repo, "status", "--porcelain"); !strings.Contains(status, "dirty.txt") {
		t.Errorf("primary checkout was mutated: status = %q", status)
	}
}
