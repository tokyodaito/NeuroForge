package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// CommitWorktreeChanges stages every change in the workspace worktree
// (modified, untracked, deleted) and creates ONE commit on the attempt branch
// with the given message. It exists for the pipeline finalize path: the
// factory owns the managed worktree, so a result commit is produced
// deterministically instead of relying on the agent to commit.
//
// Guarantees:
//   - The commit happens INSIDE the managed worktree only. A path that is not
//     a linked worktree (e.g. the user's primary checkout, which has a .git
//     directory instead of a .git file) is rejected before any git mutation
//     (§17.1, AC-8).
//   - Nothing to commit → no empty commit is created; created is false and
//     the current HEAD is returned. This makes re-driven finalizes
//     idempotent (no duplicate commits).
//   - Only allowlisted git subcommands are used (gitRunner enforces AC-7).
//
// The method is git-only: it writes no DB rows and no audit events. The
// caller (the finalize chokepoint) persists the resulting head and audits the
// decision in its own terminal transaction.
func (m *Manager) CommitWorktreeChanges(ctx context.Context, workspaceID, message string) (commitSHA string, created bool, err error) {
	ws, err := m.Get(ctx, workspaceID)
	if err != nil {
		return "", false, err
	}
	if ws.State == StateDeleted || ws.State == StateRejected {
		return "", false, fmt.Errorf("workspace: cannot commit in %q (state=%s)", workspaceID, ws.State)
	}
	if ws.Path == "" {
		return "", false, errors.New("workspace: no worktree path")
	}
	if !isWorktree(ws.Path) {
		// Structural primary-checkout guard: a managed worktree has a .git
		// FILE; a primary checkout has a .git directory. Never commit there.
		return "", false, fmt.Errorf("workspace: %q is not a managed worktree; refusing to commit", ws.Path)
	}

	r := gitRunner{dir: ws.Path}

	// Stage all changes (new, modified, deleted).
	if _, err := r.run(ctx, "add", "-A"); err != nil {
		return "", false, fmt.Errorf("workspace: stage changes: %w", err)
	}

	// diff --cached --quiet exits 1 when there are staged changes, 0 when
	// clean; the runner maps non-zero exit to an error.
	hasChanges := true
	if _, err := r.run(ctx, "diff", "--cached", "--quiet"); err == nil {
		hasChanges = false
	}
	if !hasChanges {
		// Belt and braces: confirm with porcelain status.
		if out, sErr := r.run(ctx, "status", "--porcelain"); sErr == nil && strings.TrimSpace(out) != "" {
			hasChanges = true
		}
	}

	if !hasChanges {
		out, err := r.run(ctx, "rev-parse", "HEAD")
		if err != nil {
			return "", false, fmt.Errorf("workspace: read HEAD: %w", err)
		}
		return strings.TrimSpace(out), false, nil
	}

	// Commit with a deterministic identity so the result commit never needs
	// the user's git config (same pattern as Checkpoint).
	if _, err := r.run(ctx, "commit", "-m", message,
		"--author=NeuroForge <neuroforge@local>"); err != nil {
		return "", false, fmt.Errorf("workspace: result commit: %w", err)
	}
	out, err := r.run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", false, fmt.Errorf("workspace: read result commit SHA: %w", err)
	}
	commitSHA = strings.TrimSpace(out)
	m.logger.Info("result commit created", "workspace", workspaceID, "sha", commitSHA)
	return commitSHA, true, nil
}
