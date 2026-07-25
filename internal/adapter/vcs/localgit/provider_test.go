package localgit

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"neuroforge/internal/adapter/vcs"
)

func setupRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "t@t.com")
	runGit(t, dir, "config", "user.name", "T")
	writeFile(t, filepath.Join(dir, "README.md"), "# R\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "init")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// addResultBranch commits a change on a new branch off HEAD and returns the
// branch's head SHA. Leaves the repo back on main.
func addResultBranch(t *testing.T, dir, branch string) string {
	t.Helper()
	runGit(t, dir, "branch", branch, "main")
	runGit(t, dir, "checkout", branch)
	writeFile(t, filepath.Join(dir, "feature.txt"), "feature\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "feature")
	sha := rev(t, dir, "HEAD")
	runGit(t, dir, "checkout", "main")
	return sha
}

func TestCapabilities_LocalNotNetwork(t *testing.T) {
	t.Parallel()
	p := New("/tmp")
	caps := p.Capabilities()
	if caps.IsNetwork {
		t.Fatal("local-git must NOT be a network provider (AC-7)")
	}
	if !caps.Merge || !caps.Revert {
		t.Fatal("local-git must support Merge + Revert")
	}
	if caps.PushBranch || caps.CreateChangeRequest || caps.EnableAutoMerge {
		t.Fatal("local-git must not advertise remote capabilities")
	}
}

func TestUnsupported_RemoteOps(t *testing.T) {
	t.Parallel()
	p := New("/tmp")
	ctx := context.Background()
	if _, err := p.PushBranch(ctx, vcs.PushBranchRequest{}); !errors.Is(err, vcs.ErrUnsupported) {
		t.Errorf("PushBranch: want ErrUnsupported, got %v", err)
	}
	if _, err := p.CreateChangeRequest(ctx, vcs.CreateChangeRequestRequest{}); !errors.Is(err, vcs.ErrUnsupported) {
		t.Errorf("CreateChangeRequest: want ErrUnsupported, got %v", err)
	}
	if _, err := p.GetChecks(ctx, vcs.GetChecksRequest{}); !errors.Is(err, vcs.ErrUnsupported) {
		t.Errorf("GetChecks: want ErrUnsupported, got %v", err)
	}
	if err := p.EnableAutoMerge(ctx, vcs.EnableAutoMergeRequest{}); !errors.Is(err, vcs.ErrUnsupported) {
		t.Errorf("EnableAutoMerge: want ErrUnsupported, got %v", err)
	}
}

func TestMerge_Squash_CreatesBackupAndCommit(t *testing.T) {
	t.Parallel()
	dir := setupRepo(t)
	addResultBranch(t, dir, "forge/result/W-1")
	mainBefore := rev(t, dir, "main")

	p := New(dir)
	ctx := context.Background()
	res, err := p.Merge(ctx, vcs.MergeRequest{
		TaskID:     "W-1",
		HeadBranch: "forge/result/W-1",
		BaseBranch: "main",
		Method:     vcs.MergeMethodSquash,
	})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !res.Merged {
		t.Fatal("expected Merged=true")
	}
	// The squash commit must contain the feature change.
	if !fileContains(t, filepath.Join(dir, "feature.txt"), "feature") {
		t.Fatal("feature.txt missing after squash merge")
	}
	// A backup ref must exist pointing at the pre-merge HEAD.
	backup := p.backupRef("W-1", mainBefore)
	if got := rev(t, dir, backup); got != mainBefore {
		t.Errorf("backup ref: want %s, got %s", mainBefore, got)
	}
	// HEAD advanced beyond mainBefore.
	if rev(t, dir, "HEAD") == mainBefore {
		t.Fatal("HEAD did not advance")
	}
}

func TestMerge_CherryPick_AppliesOnlyResultCommits(t *testing.T) {
	t.Parallel()
	dir := setupRepo(t)
	addResultBranch(t, dir, "forge/result/W-2")
	mainBefore := rev(t, dir, "main")

	p := New(dir)
	ctx := context.Background()
	res, err := p.Merge(ctx, vcs.MergeRequest{
		TaskID:     "W-2",
		HeadBranch: "forge/result/W-2",
		BaseBranch: "main",
		Method:     vcs.MergeMethodCherryPick,
	})
	if err != nil {
		t.Fatalf("cherry-pick: %v", err)
	}
	if !res.Merged || res.CommitSHA == "" {
		t.Fatalf("bad result: %+v", res)
	}
	if !fileContains(t, filepath.Join(dir, "feature.txt"), "feature") {
		t.Fatal("feature.txt missing after cherry-pick")
	}
	_ = mainBefore
}

func TestMerge_MergeCommit_NoFF(t *testing.T) {
	t.Parallel()
	dir := setupRepo(t)
	addResultBranch(t, dir, "forge/result/W-3")

	p := New(dir)
	ctx := context.Background()
	res, err := p.Merge(ctx, vcs.MergeRequest{
		TaskID:        "W-3",
		HeadBranch:    "forge/result/W-3",
		Method:        vcs.MergeMethodMerge,
		CommitMessage: "merge W-3",
	})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !res.Merged {
		t.Fatal("expected Merged=true")
	}
	// A merge commit must exist (two parents).
	out, err := exec.Command("git", "-C", dir, "rev-list", "--merges", "-n1", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) == "" {
		t.Fatal("expected a merge commit on HEAD")
	}
}

func TestMerge_RefusesDirtyCheckout(t *testing.T) {
	t.Parallel()
	dir := setupRepo(t)
	addResultBranch(t, dir, "forge/result/W-4")
	// Introduce an uncommitted change.
	writeFile(t, filepath.Join(dir, "README.md"), "# dirty\n")

	p := New(dir)
	_, err := p.Merge(context.Background(), vcs.MergeRequest{
		TaskID: "W-4", HeadBranch: "forge/result/W-4", Method: vcs.MergeMethodSquash,
	})
	if err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("want dirty-checkout refusal, got %v", err)
	}
}

func TestMerge_RejectsNetworkSubcommand(t *testing.T) {
	t.Parallel()
	dir := setupRepo(t)
	p := New(dir)
	_, err := p.run(context.Background(), "push", "origin", "main")
	if !errors.Is(err, vcs.ErrNetworkLocked) {
		t.Errorf("want ErrNetworkLocked, got %v", err)
	}
}

func TestRevert_CreatesRevertCommit(t *testing.T) {
	t.Parallel()
	dir := setupRepo(t)
	runGit(t, dir, "branch", "forge/result/W-5", "main")
	runGit(t, dir, "checkout", "forge/result/W-5")
	writeFile(t, filepath.Join(dir, "x.txt"), "x\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "add x")
	sha := rev(t, dir, "HEAD")
	runGit(t, dir, "checkout", "main")
	runGit(t, dir, "merge", "--ff-only", "forge/result/W-5")

	p := New(dir)
	res, err := p.Revert(context.Background(), vcs.RevertRequest{
		TaskID: "W-5", CommitSHA: sha,
	})
	if err != nil {
		t.Fatalf("revert: %v", err)
	}
	if !res.Reverted || res.RevertSHA == "" {
		t.Fatalf("bad revert result: %+v", res)
	}
	if fileExists(t, filepath.Join(dir, "x.txt")) {
		t.Fatal("x.txt should be gone after revert")
	}
}

// --- helpers ---

func rev(t *testing.T, dir, ref string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", ref).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func fileContains(t *testing.T, path, want string) bool {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(b), want)
}

func fileExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.ReadFile(path)
	return err == nil
}
