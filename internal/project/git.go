package project

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// GitInfo holds the discovered Git metadata for a repository path.
type GitInfo struct {
	Remote string // origin remote URL (empty if no remote)
	Branch string // current branch name
}

// ErrNotAGitRepo is returned when the path is not inside a Git work tree.
var ErrNotAGitRepo = errors.New("project: path is not a Git repository")

// ValidateGitRepo checks that path is a valid Git repository and returns its
// metadata. It performs read-only Git operations and never modifies the
// checkout (spec §17.1).
//
// The check uses `git rev-parse --is-inside-work-tree` which works for both
// standard and worktree checkouts. If git is not available on PATH, it falls
// back to checking for a .git entry in the directory.
func ValidateGitRepo(ctx context.Context, path string) (GitInfo, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return GitInfo{}, err
	}

	if !isGitRepo(ctx, abs) {
		return GitInfo{}, ErrNotAGitRepo
	}

	info := GitInfo{}
	info.Remote = gitRemoteURL(ctx, abs)
	info.Branch = gitCurrentBranch(ctx, abs)
	return info, nil
}

// isGitRepo reports whether the path is inside a Git work tree.
func isGitRepo(ctx context.Context, dir string) bool {
	out, err := runGit(ctx, dir, "rev-parse", "--is-inside-work-tree")
	if err == nil {
		return strings.TrimSpace(out) == "true"
	}
	// Fallback: check for .git file/directory in the path.
	return dirHasGit(dir)
}

func dirHasGit(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// gitRemoteURL returns the origin remote URL, or empty if none.
func gitRemoteURL(ctx context.Context, dir string) string {
	out, err := runGit(ctx, dir, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// gitCurrentBranch returns the current branch name, or empty on detached HEAD.
func gitCurrentBranch(ctx context.Context, dir string) string {
	out, err := runGit(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}
	b := strings.TrimSpace(out)
	if b == "HEAD" {
		return ""
	}
	return b
}

// runGit executes a git command in dir and returns its stdout. Errors include
// stderr for diagnostics.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", &gitError{cmd: strings.Join(args, " "), stderr: string(ee.Stderr)}
		}
		return "", err
	}
	return string(out), nil
}

type gitError struct {
	cmd    string
	stderr string
}

func (e *gitError) Error() string {
	return "git " + e.cmd + ": " + strings.TrimSpace(e.stderr)
}
