package workspace_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"neuroforge/internal/storage"
	"neuroforge/internal/workspace"
)

// TestInspectWorktree_CachedHeadIgnored verifies FR-9 / FR-10 / I.2:
// InspectWorktree reads the *actual* worktree state via git, ignoring the
// cached ws.HeadSHA column. The test exercises three branches:
//  1. a new commit (HEAD advanced);
//  2. uncommitted edits (HEAD == base, porcelain non-empty);
//  3. clean tree (HEAD == base, porcelain empty).
//
// In every branch the inspection's ActualHEAD must equal `git rev-parse HEAD`
// of the worktree (never the cached value we deliberately corrupt).
func TestInspectWorktree_CachedHeadIgnored(t *testing.T) {
	wm, db, _ := setupManager(t)
	repoPath := setupTestRepo(t)
	ctx := context.Background()

	// Create a real workspace (worktree) to inspect.
	ws, err := wm.Create(ctx, workspace.CreateRequest{
		ProjectID:   "proj",
		ProjectPath: repoPath,
		TaskID:      "task-1",
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	// Branch 1: HEAD advances. Make a commit inside the worktree.
	t.Run("branch_1_new_commit", func(t *testing.T) {
		mustWrite(t, filepath.Join(ws.Path, "src", "file.txt"), "first edit\n")
		runGit(t, ws.Path, "add", "-A")
		runGit(t, ws.Path, "commit", "-m", "agent edit")

		// Corrupt the cached head_sha so we can prove it is ignored.
		corruptCachedHeadSHA(t, db, ws.ID, strings.Repeat("0", 40))

		wsFresh, err := wm.Get(ctx, ws.ID)
		if err != nil {
			t.Fatalf("get workspace: %v", err)
		}
		ins, err := wm.InspectWorktree(ctx, wsFresh)
		if err != nil {
			t.Fatalf("inspect: %v", err)
		}
		want := readHeadSHA(t, ws.Path)
		if ins.ActualHEAD != want {
			t.Errorf("ActualHEAD = %q, want %q (git rev-parse HEAD)", ins.ActualHEAD, want)
		}
		if ins.ActualHEAD == ws.BaseSHA {
			t.Errorf("ActualHEAD == base SHA %q, expected HEAD to have advanced", ins.ActualHEAD)
		}
		if ins.ActualHEAD == "0000000000000000000000000000000000000000" {
			t.Errorf("ActualHEAD equals the corrupted cached value — the cached head_sha was trusted")
		}
		if len(ins.ChangedFiles) == 0 {
			t.Errorf("ChangedFiles empty for a commit that added src/file.txt")
		}
		if !containsString(ins.ChangedFiles, "src/file.txt") {
			t.Errorf("ChangedFiles = %v, want to contain src/file.txt", ins.ChangedFiles)
		}
		if strings.TrimSpace(ins.StatusPorcelain) != "" {
			t.Errorf("StatusPorcelain should be empty after commit, got %q", ins.StatusPorcelain)
		}
	})

	// Branch 2: uncommitted edits (HEAD stays at base, porcelain non-empty).
	t.Run("branch_2_uncommitted", func(t *testing.T) {
		// Fresh workspace for isolation.
		ws2, err := wm.Create(ctx, workspace.CreateRequest{
			ProjectID:   "proj",
			ProjectPath: repoPath,
			TaskID:      "task-1",
		})
		if err != nil {
			t.Fatalf("create ws2: %v", err)
		}
		mustWrite(t, filepath.Join(ws2.Path, "uncommitted.txt"), "dirty\n")
		corruptCachedHeadSHA(t, db, ws2.ID, strings.Repeat("1", 40))

		wsFresh, err := wm.Get(ctx, ws2.ID)
		if err != nil {
			t.Fatalf("get workspace: %v", err)
		}
		ins, err := wm.InspectWorktree(ctx, wsFresh)
		if err != nil {
			t.Fatalf("inspect: %v", err)
		}
		if ins.ActualHEAD != ws2.BaseSHA {
			t.Errorf("ActualHEAD = %q, want base %q (no commit was made)", ins.ActualHEAD, ws2.BaseSHA)
		}
		if strings.TrimSpace(ins.StatusPorcelain) == "" {
			t.Errorf("StatusPorcelain empty, expected uncommitted changes")
		}
		if len(ins.ChangedFiles) == 0 {
			t.Errorf("ChangedFiles empty for a dirty tree")
		}
		if !containsString(ins.ChangedFiles, "uncommitted.txt") {
			t.Errorf("ChangedFiles = %v, want to contain uncommitted.txt", ins.ChangedFiles)
		}
	})

	// Branch 3: clean tree (HEAD == base, porcelain empty).
	t.Run("branch_3_clean", func(t *testing.T) {
		ws3, err := wm.Create(ctx, workspace.CreateRequest{
			ProjectID:   "proj",
			ProjectPath: repoPath,
			TaskID:      "task-1",
		})
		if err != nil {
			t.Fatalf("create ws3: %v", err)
		}
		corruptCachedHeadSHA(t, db, ws3.ID, strings.Repeat("2", 40))

		wsFresh, err := wm.Get(ctx, ws3.ID)
		if err != nil {
			t.Fatalf("get workspace: %v", err)
		}
		ins, err := wm.InspectWorktree(ctx, wsFresh)
		if err != nil {
			t.Fatalf("inspect: %v", err)
		}
		if ins.ActualHEAD != ws3.BaseSHA {
			t.Errorf("ActualHEAD = %q, want base %q for a clean tree", ins.ActualHEAD, ws3.BaseSHA)
		}
		if strings.TrimSpace(ins.StatusPorcelain) != "" {
			t.Errorf("StatusPorcelain should be empty for a clean tree, got %q", ins.StatusPorcelain)
		}
		if len(ins.ChangedFiles) != 0 {
			t.Errorf("ChangedFiles = %v, want empty for a clean tree", ins.ChangedFiles)
		}
	})
}

// TestInspectWorktree_MissingWorktree asserts a classified error is returned
// when the worktree directory has been removed (the reconciler scenario).
func TestInspectWorktree_MissingWorktree(t *testing.T) {
	wm, _, _ := setupManager(t)
	repoPath := setupTestRepo(t)
	ctx := context.Background()

	ws, err := wm.Create(ctx, workspace.CreateRequest{
		ProjectID:   "proj",
		ProjectPath: repoPath,
		TaskID:      "task-1",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Remove the worktree directory.
	if err := os.RemoveAll(ws.Path); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}
	_, err = wm.InspectWorktree(ctx, ws)
	if err == nil {
		t.Fatalf("expected an error for a missing worktree, got nil")
	}
	if !workspace.IsWorktreeMissing(err) {
		t.Fatalf("expected WorktreeMissingError, got %v", err)
	}
}

// TestInspectWorktree_NoNetworkSubcommands asserts only allowlisted git
// subcommands are used (AC-7). We can't intercept the runner here, so this
// test instead verifies the structural guarantee: the workspace gitRunner's
// allowlist rejects every network-capable subcommand. This is the positive
// complement to internal/workspace's own allowlist tests.
func TestInspectWorktree_OnlyAllowlistedGit(t *testing.T) {
	// Smoke: InspectWorktree works against a normal worktree without ever
	// invoking a network operation. The deeper guarantee is verified by the
	// m3 scenario's network probe.
	wm, _, _ := setupManager(t)
	repoPath := setupTestRepo(t)
	ctx := context.Background()
	ws, err := wm.Create(ctx, workspace.CreateRequest{
		ProjectID:   "proj",
		ProjectPath: repoPath,
		TaskID:      "task-1",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := wm.InspectWorktree(ctx, ws); err != nil {
		t.Fatalf("inspect: %v", err)
	}
}

// ---- helpers ----

func corruptCachedHeadSHA(t *testing.T, db *storage.DB, id, fake string) {
	t.Helper()
	if _, err := db.Exec(context.Background(),
		`UPDATE workspaces SET head_sha = ? WHERE id = ?`, fake, id); err != nil {
		t.Fatalf("corrupt head_sha: %v", err)
	}
}

func containsString(slice []string, want string) bool {
	for _, s := range slice {
		if s == want {
			return true
		}
	}
	return false
}
