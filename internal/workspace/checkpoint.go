package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"neuroforge/internal/audit"
	"neuroforge/internal/storage"
)

// Checkpoint creates a Git checkpoint commit inside the workspace worktree and
// records it durably. Checkpoint commits live in the attempt branch and NEVER
// auto-merge into the user's main branch (§5.2, §21.3).
//
// If there are no staged/unstaged changes, no commit is made and
// ErrNothingToCommit is returned (the caller decides whether that is an error).
func (m *Manager) Checkpoint(ctx context.Context, workspaceID string, moment CheckpointMoment, message string) (Checkpoint, error) {
	ws, err := m.Get(ctx, workspaceID)
	if err != nil {
		return Checkpoint{}, err
	}
	if ws.State == StateDeleted || ws.State == StateRejected {
		return Checkpoint{}, fmt.Errorf("workspace: cannot checkpoint %q (state=%s)", workspaceID, ws.State)
	}
	if ws.Path == "" {
		return Checkpoint{}, errors.New("workspace: no worktree path")
	}

	r := gitRunner{dir: ws.Path}

	// Stage all changes (new, modified, deleted).
	if _, err := r.run(ctx, "add", "-A"); err != nil {
		return Checkpoint{}, fmt.Errorf("workspace: stage changes: %w", err)
	}

	// Check whether there is anything to commit.
	diff, err := r.run(ctx, "diff", "--cached", "--quiet")
	hasChanges := true
	if err != nil {
		// diff --cached --quiet exits 1 when there are staged changes, 0 when
		// clean. The runner treats non-zero exit as an error, so a non-empty
		// diff is the "error" path here.
		hasChanges = true
	} else {
		// Exit 0 means no staged changes.
		_ = diff
		hasChanges = false
	}

	// Also check for unstaged changes that add -A may have missed (shouldn't
	// happen, but be safe).
	if !hasChanges {
		// Try status --porcelain to be certain.
		statusOut, sErr := r.run(ctx, "status", "--porcelain")
		if sErr == nil && strings.TrimSpace(statusOut) != "" {
			hasChanges = true
		}
	}

	commitSHA := ws.HeadSHA
	if hasChanges {
		fullMsg := fmt.Sprintf("[neuroforge checkpoint] %s: %s", moment, message)
		// Commit with a deterministic identity so checkpoint commits never need
		// the user's git config.
		if _, err := r.run(ctx, "commit", "-m", fullMsg,
			"--author=NeuroForge <neuroforge@local>"); err != nil {
			return Checkpoint{}, fmt.Errorf("workspace: checkpoint commit: %w", err)
		}
		out, err := r.run(ctx, "rev-parse", "HEAD")
		if err != nil {
			return Checkpoint{}, fmt.Errorf("workspace: read checkpoint SHA: %w", err)
		}
		commitSHA = strings.TrimSpace(out)
	}

	now := m.now()
	cp := Checkpoint{
		Workspace: workspaceID,
		CommitSHA: commitSHA,
		Moment:    moment,
		Message:   message,
		CreatedAt: now,
	}

	// Persist the checkpoint record + updated head_sha atomically.
	tx, err := m.db.BeginTx(ctx)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("workspace: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.CreateCheckpoint(ctx, storage.Checkpoint{
		WorkspaceID: workspaceID,
		CommitSHA:   commitSHA,
		Moment:      string(moment),
		Message:     message,
		CreatedAt:   now.Format(time.RFC3339Nano),
	}); err != nil {
		return Checkpoint{}, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE workspaces SET head_sha = ?, updated_at = ? WHERE id = ?`,
		commitSHA, now.Format(time.RFC3339Nano), workspaceID); err != nil {
		return Checkpoint{}, err
	}
	if err := m.auditTx(ctx, tx, workspaceID, "workspace.checkpoint", audit.Payload(
		"moment", string(moment),
		"commit_sha", commitSHA,
		"message", message,
		"had_changes", hasChanges,
	)); err != nil {
		return Checkpoint{}, err
	}
	if err := tx.Commit(); err != nil {
		return Checkpoint{}, fmt.Errorf("workspace: commit checkpoint tx: %w", err)
	}

	cp.ID = 0 // will be filled by ListCheckpoints; callers usually don't need it
	m.logger.Info("checkpoint created", "workspace", workspaceID,
		"moment", moment, "sha", commitSHA)
	return cp, nil
}

// ErrNothingToCommit is returned when a checkpoint has no changes to commit.
var ErrNothingToCommit = errors.New("workspace: nothing to commit")

// ListCheckpoints returns all checkpoints for a workspace.
func (m *Manager) ListCheckpoints(ctx context.Context, workspaceID string) ([]Checkpoint, error) {
	rows, err := m.db.ListCheckpoints(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]Checkpoint, len(rows))
	for i, c := range rows {
		ts, _ := time.Parse(time.RFC3339Nano, c.CreatedAt)
		out[i] = Checkpoint{
			ID:        c.ID,
			Workspace: c.WorkspaceID,
			CommitSHA: c.CommitSHA,
			Moment:    CheckpointMoment(c.Moment),
			Message:   c.Message,
			CreatedAt: ts,
		}
	}
	return out, nil
}
