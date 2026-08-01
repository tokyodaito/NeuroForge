package workspace

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// Git network subcommands that must NEVER be invoked by the workspace manager.
// Their presence here is explicit documentation: if a future contributor adds a
// subcommand, it must not be one of these (spec §4.2, §36.13, AC-7, ADR-0008).
var forbiddenGitSubcommands = map[string]string{
	"push":       "git push is forbidden in LOCAL_REVIEW (AC-7, §36.13)",
	"fetch":      "git fetch is a network operation forbidden in LOCAL_REVIEW (AC-7)",
	"pull":       "git pull is a network operation forbidden in LOCAL_REVIEW (AC-7)",
	"clone":      "git clone is a network operation forbidden in LOCAL_REVIEW (AC-7)",
	"ls-remote":  "git ls-remote is a network operation forbidden in LOCAL_REVIEW (AC-7)",
	"send-pack":  "git send-pack is a network operation forbidden in LOCAL_REVIEW (AC-7)",
	"fetch-pack": "git fetch-pack is a network operation forbidden in LOCAL_REVIEW (AC-7)",
	"archive":    "git archive over a remote is forbidden (AC-7)",
}

// allowedGitSubcommands is the positive allowlist of git subcommands the
// workspace manager may invoke. Anything not on this list is rejected. This is
// the structural enforcement of AC-7: LOCAL_REVIEW performs zero Git network
// operations, by construction rather than convention.
var allowedGitSubcommands = map[string]bool{
	"worktree":         true, // create/list/remove worktrees
	"rev-parse":        true, // resolve SHAs, branches
	"rev-list":         true, // list commits
	"add":              true, // stage files
	"commit":           true, // create checkpoint commits
	"branch":           true, // create/delete/list local branches
	"diff":             true, // show diffs
	"log":              true, // show history
	"status":           true, // show working tree status
	"show":             true, // show objects
	"format-patch":     true, // export patches
	"checkout":         true, // switch branches inside a worktree
	"symbolic-ref":     true, // inspect HEAD
	"config":           true, // local config (e.g. user identity for commits)
	"check-ref-format": true, // validate caller-supplied branch names (M4)
	"stash":            true, // list only (used for accept-into-branch checks)
	"merge-tree":       true, // merge without touching working tree
	"cat-file":         true, // read object contents
	"update-ref":       true, // create result branch refs
	"for-each-ref":     true, // enumerate refs
	"-C":               true, // change directory flag (handled specially)
}

// ErrGitNetworkForbidden is returned when a git network subcommand is requested.
// This should never happen because the allowlist is checked first, but the
// explicit check is defense-in-depth.
var ErrGitNetworkForbidden = errors.New("workspace: git network operation forbidden (AC-7)")

// gitRunner executes allowlisted git commands against a repository. It never
// performs a network operation: every invocation is validated against
// [allowedGitSubcommands] and explicitly rejected if it matches a forbidden
// network subcommand.
type gitRunner struct {
	// dir is the working directory for git -C. Empty means the process CWD.
	dir string
}

// run executes a git command and returns stdout. The first element of args is
// the git subcommand; it MUST be on the allowlist and MUST NOT be a forbidden
// network subcommand.
func (g gitRunner) run(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("workspace: git requires at least one argument")
	}
	sub := args[0]

	// Defense-in-depth: reject network subcommands even if they somehow appear.
	if reason, banned := forbiddenGitSubcommands[sub]; banned {
		return "", fmt.Errorf("%w: %s", ErrGitNetworkForbidden, reason)
	}
	// Positive allowlist enforcement.
	if !allowedGitSubcommands[sub] {
		return "", fmt.Errorf("workspace: git subcommand %q is not on the allowlist (AC-7)", sub)
	}

	full := args
	if g.dir != "" {
		full = append([]string{"-C", g.dir}, args...)
	}

	cmd := exec.CommandContext(ctx, "git", full...)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", &gitCmdError{args: args, stderr: string(ee.Stderr), dir: g.dir}
		}
		return "", fmt.Errorf("workspace: git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// runLines is like run but returns trimmed, split lines.
func (g gitRunner) runLines(ctx context.Context, args ...string) ([]string, error) {
	out, err := g.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

type gitCmdError struct {
	args   []string
	stderr string
	dir    string
}

func (e *gitCmdError) Error() string {
	loc := e.dir
	if loc == "" {
		loc = "(cwd)"
	}
	return fmt.Sprintf("git %s [%s]: %s", strings.Join(e.args, " "), loc, strings.TrimSpace(e.stderr))
}

// isValidBranchChar reports whether c is a valid character in a git branch name
// component (not a ref separator or glob char).
var invalidBranchChars = regexp.MustCompile(`[\s~^:?*\[\\]`)

// sanitizeBranchSegment ensures a segment (task id, work package id) is safe to
// embed in a branch name. It replaces characters that git treats specially in
// ref names.
func sanitizeBranchSegment(s string) string {
	s = invalidBranchChars.ReplaceAllString(s, "-")
	s = strings.Trim(s, "/.-")
	if s == "" {
		s = "x"
	}
	return s
}
