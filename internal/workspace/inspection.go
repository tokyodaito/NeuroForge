package workspace

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Inspection is the immutable result of [Manager.InspectWorktree]: a snapshot
// of the *actual* worktree state as observed via Git at one point in time.
//
// It is computed from the live worktree (git rev-parse HEAD, git status
// --porcelain, git diff --stat) and never trusts the cached Workspace.HeadSHA
// field. This is the structural fix for invariant I.2 / FR-9 / FR-10: Git is
// the source of truth after a run, not a stale DB column.
type Inspection struct {
	// ActualHEAD is the worktree's current HEAD commit SHA from
	// `git rev-parse HEAD`. Empty only when the worktree is unreadable.
	ActualHEAD string
	// StatusPorcelain is the raw output of `git status --porcelain` (may be
	// empty when the tree is clean).
	StatusPorcelain string
	// DiffStat is the raw output of `git diff --stat <base>...HEAD` (may be
	// empty when HEAD == base).
	DiffStat string
	// WorkingDiffStat is the raw output of `git diff --stat` (unstaged + staged
	// but uncommitted changes against the index). Empty when the tree is clean.
	WorkingDiffStat string
	// ChangedFiles is the de-duplicated union of files modified between base
	// and HEAD plus uncommitted files from `git status --porcelain`. Sorted.
	ChangedFiles []string
}

// InspectWorktree reads the actual state of ws's worktree via the allowlisted
// git runner. It is a pure read; it never mutates the worktree, the DB or the
// workspace record. The cached ws.HeadSHA is demonstrably ignored — the
// returned ActualHEAD comes from `git rev-parse HEAD` inside the worktree
// (FR-9, FR-10, invariant I.2).
//
// All four git commands named in REQUIREMENTS.md §FR-9 are issued:
//
//	git rev-parse HEAD
//	git status --porcelain
//	git diff --stat <base>...HEAD
//	git diff --stat
//
// Only allowlisted git subcommands are used (the workspace package's gitRunner
// enforces AC-7 — no network operation can be invoked). A missing/unreadable
// worktree returns a classified error so the caller can surface it (NFR-6).
func (m *Manager) InspectWorktree(ctx context.Context, ws Workspace) (Inspection, error) {
	if ws.Path == "" {
		return Inspection{}, errors.New("workspace: cannot inspect worktree with empty path")
	}
	// Verify the worktree exists; surface a classified error if it is gone
	// (e.g. reconciler scenario, deleted behind our back).
	if !isWorktree(ws.Path) {
		return Inspection{}, &WorktreeMissingError{Path: ws.Path}
	}

	r := gitRunner{dir: ws.Path}

	// 1. Actual HEAD from the worktree. Never uses ws.HeadSHA.
	headOut, err := r.run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return Inspection{}, fmt.Errorf("workspace: inspect: rev-parse HEAD: %w", err)
	}
	actualHEAD := strings.TrimSpace(headOut)

	// 2. Porcelain status of the working tree.
	statusOut, err := r.run(ctx, "status", "--porcelain")
	if err != nil {
		return Inspection{}, fmt.Errorf("workspace: inspect: status --porcelain: %w", err)
	}
	statusPorcelain := statusOut

	// 3. Cumulative diff stat from the base commit to HEAD. Use the triple-dot
	// form so only commit-side changes are counted (the working-tree side is
	// captured separately by step 4).
	var diffStat string
	if ws.BaseSHA != "" {
		out, derr := r.run(ctx, "diff", "--stat", ws.BaseSHA+"...HEAD")
		if derr != nil {
			// A failure here usually means the base SHA is unreachable in this
			// worktree's object DB. We do not mask the inspection — fall back
			// to an empty stat so classification can still proceed using
			// actualHEAD + porcelain.
			diffStat = ""
		} else {
			diffStat = out
		}
	}

	// 4. Working-tree diff stat (uncommitted edits against the index).
	workingDiffStat, _ := r.run(ctx, "diff", "--stat")

	ins := Inspection{
		ActualHEAD:      actualHEAD,
		StatusPorcelain: statusPorcelain,
		DiffStat:        diffStat,
		WorkingDiffStat: workingDiffStat,
	}
	ins.ChangedFiles = computeChangedFiles(ctx, r, ws.BaseSHA, statusPorcelain)
	return ins, nil
}

// computeChangedFiles returns the de-duplicated, sorted union of:
//   - files changed between base and HEAD (`git diff --name-only base...HEAD`);
//   - files with uncommitted edits (parsed from statusPorcelain).
//
// The union preserves files even when base==HEAD but the tree is dirty (the
// "completed-with-uncommitted-changes" case). It mirrors OUTCOME_CONTRACT.md
// §3.1: changed_files is "Files changed vs base (git diff --name-only
// base...HEAD) plus uncommitted files from git status --porcelain;
// de-duplicated."
func computeChangedFiles(ctx context.Context, r gitRunner, baseSHA, statusPorcelain string) []string {
	set := make(map[string]struct{})
	if baseSHA != "" {
		if out, err := r.run(ctx, "diff", "--name-only", baseSHA+"...HEAD"); err == nil {
			for _, line := range strings.Split(out, "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				set[line] = struct{}{}
			}
		}
	}
	// git status --porcelain format: "XY <path>" where XY are two status
	// chars (possibly a space). The path may be quoted if it contains
	// special chars. We extract the path portion conservatively.
	for _, line := range strings.Split(statusPorcelain, "\n") {
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[3:])
		path = unquoteGitPath(path)
		if path == "" {
			continue
		}
		// A rename shows as "R  old -> new"; record both sides so neither is
		// hidden.
		if before, after, ok := strings.Cut(path, " -> "); ok {
			set[before] = struct{}{}
			set[after] = struct{}{}
			continue
		}
		set[path] = struct{}{}
	}
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	// Sort for determinism (NFR-2: the inspection is a deterministic input to
	// the classifier).
	sort.Strings(out)
	return out
}

// unquoteGitPath removes the surrounding quotes git emits for paths with
// special characters, returning the unquoted path. If the input is not quoted,
// it is returned unchanged.
func unquoteGitPath(p string) string {
	if len(p) >= 2 && p[0] == '"' && p[len(p)-1] == '"' {
		return p[1 : len(p)-1]
	}
	return p
}

// WorktreeMissingError is the classified error returned by InspectWorktree when
// the worktree directory is missing or no longer looks like a git worktree.
// Callers map this to error_class=GIT_INSPECT_FAILED (or interrupted, when the
// reconciler observes the same condition after a daemon restart).
type WorktreeMissingError struct {
	Path string
}

func (e *WorktreeMissingError) Error() string {
	return "workspace: worktree missing or not a git worktree: " + e.Path
}

// IsWorktreeMissing reports whether err is a *WorktreeMissingError.
func IsWorktreeMissing(err error) bool {
	var target *WorktreeMissingError
	return errors.As(err, &target)
}
