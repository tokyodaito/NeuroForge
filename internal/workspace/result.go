package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"neuroforge/internal/audit"
)

// CreateResult creates the final local result branch (forge/result/<task-id>)
// pointing at the workspace's current HEAD. The result branch is a local-only
// ref that NEVER gets pushed (§17.4, AC-8, ADR-0007).
//
// If the result branch already exists, it is fast-forwarded to the new HEAD (a
// result branch accumulates across attempts for the same task).
func (m *Manager) CreateResult(ctx context.Context, workspaceID string) (Workspace, error) {
	ws, err := m.Get(ctx, workspaceID)
	if err != nil {
		return Workspace{}, err
	}
	if ws.State == StateDeleted || ws.State == StateRejected {
		return Workspace{}, fmt.Errorf("workspace: cannot create result for %q (state=%s)", workspaceID, ws.State)
	}
	if ws.Path == "" {
		return Workspace{}, errors.New("workspace: no worktree path")
	}

	r := gitRunner{dir: ws.Path}
	// Read the current HEAD of the worktree.
	headOut, err := r.run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return Workspace{}, fmt.Errorf("workspace: read HEAD for result: %w", err)
	}
	headSHA := strings.TrimSpace(headOut)

	resultBranch := ResultBranch(ws.TaskID)

	// Create or update the result branch ref to point at headSHA. We use
	// update-ref because it works from inside the worktree's repo (which shares
	// the object database with the primary checkout) without checking out the
	// branch. This is a LOCAL ref operation — no push (AC-7).
	primaryRunner := gitRunner{dir: resolveCommonDir(ctx, r)}
	if _, err := primaryRunner.run(ctx, "update-ref", resultBranch, headSHA); err != nil {
		return Workspace{}, fmt.Errorf("workspace: create result branch %q: %w", resultBranch, err)
	}

	ws.ResultBranch = resultBranch
	ws.ResultSHA = headSHA
	ws.State = StateCompleted
	ws.HeadSHA = headSHA
	ws.UpdatedAt = m.now()

	if err := m.db.SetWorkspaceResult(ctx, workspaceID, resultBranch, headSHA,
		ws.UpdatedAt.Format(time.RFC3339Nano)); err != nil {
		return Workspace{}, err
	}
	if err := m.updateState(ctx, workspaceID, StateCompleted, headSHA, ws.RunID, ws.SessionID); err != nil {
		return Workspace{}, err
	}
	if err := m.auditEvent(ctx, workspaceID, "workspace.result_created", audit.Payload(
		"result_branch", resultBranch,
		"result_sha", headSHA,
		"base_sha", ws.BaseSHA,
	)); err != nil {
		m.logger.Warn("audit result_created failed", "err", err)
	}

	m.logger.Info("result branch created", "workspace", workspaceID,
		"branch", resultBranch, "sha", headSHA)
	return ws, nil
}

// resolveCommonDir finds the common git dir (the main repository, not the
// worktree) so update-ref writes to the shared ref store. Falls back to the
// worktree dir if rev-parse fails.
func resolveCommonDir(ctx context.Context, r gitRunner) string {
	out, err := r.run(ctx, "rev-parse", "--git-common-dir")
	if err != nil {
		return r.dir
	}
	cd := strings.TrimSpace(out)
	if cd == "" {
		return r.dir
	}
	return cd
}

// Diff returns the diff between the base SHA and the workspace HEAD (the full
// result of the agent's work).
func (m *Manager) Diff(ctx context.Context, workspaceID string) (string, error) {
	ws, err := m.Get(ctx, workspaceID)
	if err != nil {
		return "", err
	}
	if ws.Path == "" {
		return "", errors.New("workspace: no worktree path")
	}
	r := gitRunner{dir: ws.Path}
	out, err := r.run(ctx, "diff", ws.BaseSHA, "HEAD")
	if err != nil {
		return "", fmt.Errorf("workspace: diff: %w", err)
	}
	return out, nil
}

// ExportPatch returns the patch (git format-patch --stdout) for the changes
// between the base SHA and the workspace HEAD.
func (m *Manager) ExportPatch(ctx context.Context, workspaceID string) (string, error) {
	ws, err := m.Get(ctx, workspaceID)
	if err != nil {
		return "", err
	}
	if ws.Path == "" {
		return "", errors.New("workspace: no worktree path")
	}
	r := gitRunner{dir: ws.Path}
	out, err := r.run(ctx, "format-patch", "--stdout", ws.BaseSHA+"..HEAD")
	if err != nil {
		return "", fmt.Errorf("workspace: export patch: %w", err)
	}
	return out, nil
}

// ReviewAction is the user's disposition of a completed workspace result.
type ReviewAction string

const (
	ActionKeep   ReviewAction = "keep"   // keep the result; retain workspace
	ActionReject ReviewAction = "reject" // reject; delete worktree (NOT user data)
	ActionAsk    ReviewAction = "ask"    // ask for changes; retain workspace for next attempt
)

// Review applies the user's review decision to a completed workspace.
//
// Security: Reject deletes ONLY the managed worktree and its attempt branch.
// It NEVER deletes user data outside the managed workspace, and NEVER touches
// the primary checkout (§17.1). Every action is audited (§29.4).
func (m *Manager) Review(ctx context.Context, workspaceID string, action ReviewAction) (Workspace, error) {
	ws, err := m.Get(ctx, workspaceID)
	if err != nil {
		return Workspace{}, err
	}

	switch action {
	case ActionKeep:
		ws.State = StateKept
		if err := m.updateState(ctx, workspaceID, StateKept, ws.HeadSHA, ws.RunID, ws.SessionID); err != nil {
			return Workspace{}, err
		}
		if err := m.auditEvent(ctx, workspaceID, "workspace.result_kept", audit.Payload(
			"result_branch", ws.ResultBranch, "result_sha", ws.ResultSHA)); err != nil {
			m.logger.Warn("audit result_kept failed", "err", err)
		}
		m.logger.Info("result kept", "workspace", workspaceID)

	case ActionAsk:
		// Keep the workspace active so the user can create another attempt.
		if err := m.auditEvent(ctx, workspaceID, "workspace.changes_requested", audit.Payload(
			"result_branch", ws.ResultBranch)); err != nil {
			m.logger.Warn("audit changes_requested failed", "err", err)
		}
		m.logger.Info("changes requested", "workspace", workspaceID)

	case ActionReject:
		// Delete the managed worktree and attempt branch. The result branch is
		// also removed (it pointed only at this workspace's work). The primary
		// checkout is NEVER touched (§17.1).
		if err := m.deleteWorktree(ctx, ws); err != nil {
			m.logger.Warn("delete worktree on reject failed", "err", err)
		}
		ws.State = StateRejected
		if err := m.updateState(ctx, workspaceID, StateRejected, ws.HeadSHA, ws.RunID, ws.SessionID); err != nil {
			return Workspace{}, err
		}
		if err := m.auditEvent(ctx, workspaceID, "workspace.result_rejected", audit.Payload(
			"path", ws.Path, "branch", ws.Branch, "result_branch", ws.ResultBranch)); err != nil {
			m.logger.Warn("audit result_rejected failed", "err", err)
		}
		m.logger.Info("result rejected, worktree deleted", "workspace", workspaceID)

	default:
		return Workspace{}, fmt.Errorf("workspace: unknown review action %q", action)
	}
	return ws, nil
}

// Delete removes a workspace: deletes the managed worktree, the attempt branch,
// and the workspace record. It NEVER deletes files outside the managed
// workspace path or touches the primary checkout (§17.1).
func (m *Manager) Delete(ctx context.Context, workspaceID string) error {
	ws, err := m.Get(ctx, workspaceID)
	if err != nil {
		return err
	}
	if err := m.deleteWorktree(ctx, ws); err != nil {
		m.logger.Warn("delete worktree failed", "err", err)
	}
	if err := m.updateState(ctx, workspaceID, StateDeleted, ws.HeadSHA, ws.RunID, ws.SessionID); err != nil {
		return err
	}
	if err := m.auditEvent(ctx, workspaceID, "workspace.deleted", audit.Payload(
		"path", ws.Path, "branch", ws.Branch)); err != nil {
		m.logger.Warn("audit deleted failed", "err", err)
	}
	m.logger.Info("workspace deleted", "id", workspaceID)
	return nil
}

// deleteWorktree removes the git worktree and its attempt branch. This operates
// only on the managed path and the attempt branch — never the primary checkout.
func (m *Manager) deleteWorktree(ctx context.Context, ws Workspace) error {
	if ws.Path == "" {
		return nil
	}
	// Find the common git dir (main repo) to remove the worktree via
	// `git worktree remove`.
	primaryRunner := gitRunner{dir: resolveCommonDir(ctx, gitRunner{dir: ws.Path})}
	// Remove the worktree directory. --force allows removing even with
	// uncommitted changes (the user explicitly rejected/deleted).
	if _, err := primaryRunner.run(ctx, "worktree", "remove", "--force", ws.Path); err != nil {
		// Fall back to pruning if the directory was already gone.
		_, _ = primaryRunner.run(ctx, "worktree", "prune")
	}
	// Delete the attempt branch (local only — no push, ever).
	if ws.Branch != "" {
		_, _ = primaryRunner.run(ctx, "branch", "-D", ws.Branch)
	}
	// Delete the result branch only if it points at this workspace's SHA (a
	// later attempt may have moved it). This is a local ref deletion.
	if ws.ResultBranch != "" && ws.ResultSHA != "" {
		curSHA, err := primaryRunner.run(ctx, "rev-parse", ws.ResultBranch)
		if err == nil && strings.TrimSpace(curSHA) == ws.ResultSHA {
			_, _ = primaryRunner.run(ctx, "branch", "-D", ws.ResultBranch)
		}
	}
	return nil
}

// ListOrphaned returns worktree paths that exist on disk but have no matching
// workspace record (or whose record is stale). Used by `forge doctor`.
func (m *Manager) ListOrphaned(ctx context.Context) ([]string, error) {
	all, err := m.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(all))
	for _, w := range all {
		known[w.Path] = true
	}
	// Walk the workspaces root looking for directories that look like worktrees
	// (contain a .git file).
	root := m.workspacesRoot + "/workspaces"
	var orphans []string
	_ = filepathWalk(root, func(path string) bool {
		if known[path] {
			return false
		}
		if isWorktree(path) {
			orphans = append(orphans, path)
		}
		return false
	})
	return orphans, nil
}
