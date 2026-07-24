package daemon

import (
	"context"
	"fmt"

	"neuroforge/internal/audit"
	"neuroforge/internal/workspace"
)

// workspaceReconciler reconciles workspace records against the filesystem at
// daemon startup. It verifies that active/pending worktrees still exist and
// marks stale ones, so the daemon does not operate on ghost workspaces after a
// crash or restart (AC-27, spec §11.4).
type workspaceReconciler struct {
	wm *workspace.Manager
}

func (r *workspaceReconciler) Name() string { return "workspaces" }

func (r *workspaceReconciler) Reconcile(ctx context.Context, tx ReconcileTx) ([]ReconcileDecision, error) {
	all, err := r.wm.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("workspace reconciler: list: %w", err)
	}

	var decisions []ReconcileDecision
	for _, ws := range all {
		switch ws.State {
		case workspace.StateActive, workspace.StatePending:
			// Verify the worktree still exists on disk. If the daemon crashed
			// mid-run, the worktree may be gone or the state may be stale.
			exists, err := workspace.WorktreeExists(ws.Path)
			if err != nil {
				decisions = append(decisions, ReconcileDecision{
					Reconciler: r.Name(), Entity: "workspace:" + ws.ID,
					Action: DecisionMarkedStale,
					Detail: fmt.Sprintf("stat error: %v", err),
				})
				continue
			}
			if !exists {
				// The worktree is gone. Mark as stale so the user knows. We do
				// NOT auto-delete: the user may want to inspect the record.
				decisions = append(decisions, ReconcileDecision{
					Reconciler: r.Name(), Entity: "workspace:" + ws.ID,
					Action: DecisionMarkedStale,
					Detail: fmt.Sprintf("worktree path %s no longer exists (state=%s); marked stale", ws.Path, ws.State),
				})
			} else {
				decisions = append(decisions, ReconcileDecision{
					Reconciler: r.Name(), Entity: "workspace:" + ws.ID,
					Action: DecisionKept,
					Detail: fmt.Sprintf("worktree intact (state=%s, branch=%s)", ws.State, ws.Branch),
				})
			}
		default:
			decisions = append(decisions, ReconcileDecision{
				Reconciler: r.Name(), Entity: "workspace:" + ws.ID,
				Action: DecisionNoOp,
				Detail: fmt.Sprintf("state=%s (no recovery needed)", ws.State),
			})
		}
	}
	return decisions, nil
}

// _ ensures audit is referenced (future use: record reconciliation per workspace).
var _ = audit.ScopeTask
