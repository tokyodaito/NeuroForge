package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// FullyQualifiedResultBranch returns the fully-qualified ref name for a task's
// local result branch: refs/heads/forge/result/<task-id> (FR-14, I.7, KF-08).
// The ref is ALWAYS created under refs/heads/ — never as a short name that
// relies on git's ref-resolution rules.
func FullyQualifiedResultBranch(taskID string) string {
	return "refs/heads/forge/result/" + sanitizeBranchSegment(taskID)
}

// EnsureResultRef creates or updates the local result ref at the
// fully-qualified path refs/heads/forge/result/<task-id> so it points at
// headSHA. It is idempotent (re-running moves the ref to the new HEAD; a
// second call does not error and does not create a duplicate). It performs no
// network operation (AC-7) and never modifies the user's currently checked-out
// branch (§17.1, §36.14). Existing non-standard refs are never deleted.
//
// The implementation passes the literal fully-qualified ref name to
// `git update-ref`, pinning the explicit refs/heads/forge/result/<task-id>
// form the spec requires (KF-08).
func (m *Manager) EnsureResultRef(ctx context.Context, ws Workspace, headSHA string) (string, error) {
	if ws.Path == "" {
		return "", errors.New("workspace: no worktree path for result ref")
	}
	if headSHA == "" {
		return "", errors.New("workspace: headSHA is required for result ref")
	}
	ref := FullyQualifiedResultBranch(ws.TaskID)

	// Use the worktree's own object database. The workspace shares the
	// primary checkout's object DB (git worktree), so update-ref writes to
	// the shared ref store. Run from the worktree dir for consistency.
	r := gitRunner{dir: ws.Path}
	// We must run update-ref against the common git dir (the primary ref
	// store), not the per-worktree one. resolveCommonDir finds it.
	commonDir := resolveCommonDir(ctx, r)
	primaryRunner := gitRunner{dir: commonDir}

	// update-ref with the fully-qualified ref name. We pass the literal ref
	// (refs/heads/...) so git does not apply its short-name resolution rules.
	if _, err := primaryRunner.run(ctx, "update-ref", ref, headSHA); err != nil {
		return "", fmt.Errorf("workspace: update-ref %s: %w", ref, err)
	}
	return ref, nil
}

// ResolveResultRef returns the SHA the result ref points at, or "" with a
// nil error when the ref does not exist yet. dir selects the repository to
// resolve in (any path inside the workspace's shared object DB works — the
// worktree path, the primary checkout, or the common git dir). Used by tests
// and the reconciler to verify the ref landed in the right place (P-05).
func (m *Manager) ResolveResultRef(ctx context.Context, taskID, dir string) (string, error) {
	ref := FullyQualifiedResultBranch(taskID)
	r := gitRunner{dir: dir}
	out, err := r.run(ctx, "for-each-ref", "--format=%(objectname)", ref)
	if err != nil {
		// for-each-ref returns no rows (empty output) when the ref does not
		// exist; an exit error here means the ref is genuinely missing.
		if strings.Contains(err.Error(), "exit") {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// DeleteResultRef removes the local result ref refs/heads/forge/result/<task-id>
// via `git update-ref -d`. It is the COMPENSATING action used by finalize when
// the SQLite transaction that should record the matching result_branch fails
// AFTER the ref was created (BF-07): deleting the ref restores git/DB
// consistency so no orphan ref is left pointing at a result the DB never
// recorded. Idempotent: deleting a non-existent ref is not an error. It performs
// no network operation and never touches the checked-out branch.
func (m *Manager) DeleteResultRef(ctx context.Context, ws Workspace) error {
	if ws.Path == "" {
		return errors.New("workspace: no worktree path for result ref delete")
	}
	ref := FullyQualifiedResultBranch(ws.TaskID)
	r := gitRunner{dir: ws.Path}
	commonDir := resolveCommonDir(ctx, r)
	primaryRunner := gitRunner{dir: commonDir}
	// update-ref -d is idempotent: a missing ref exits 0 (or a benign error we
	// tolerate so a compensating delete never masks the original tx failure).
	if _, err := primaryRunner.run(ctx, "update-ref", "-d", ref); err != nil {
		return fmt.Errorf("workspace: delete result ref %s: %w", ref, err)
	}
	return nil
}
