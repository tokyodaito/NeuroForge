package daemon

import (
	"context"
	"fmt"

	"neuroforge/internal/audit"
	"neuroforge/internal/storage"
	"neuroforge/internal/supervisor"
	"neuroforge/internal/workspace"
)

// attemptReconciler recovers in-flight agent attempts after a daemon restart
// (AC-27, spec §11.4). It inspects active/waiting workspaces, their checkpoints
// and continuation packs, and records a deterministic recovery decision for
// each — marking stale in-flight runs as failed and flagging pack-backed runs
// as resumable, so the scheduler (or an explicit re-run) can continue without
// double-spend and without losing the checkpoint (AC-15).
//
// Safety (spec §11.4, §36.13): the reconciler NEVER auto-resumes a delivery
// operation (push/PR/MR/merge). It only reconciles agent-attempt state.
type attemptReconciler struct {
	wm *workspace.Manager
}

func (r *attemptReconciler) Name() string { return "attempts" }

func (r *attemptReconciler) Reconcile(ctx context.Context, tx ReconcileTx) ([]ReconcileDecision, error) {
	all, err := r.wm.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("attempt reconciler: list workspaces: %w", err)
	}
	var decisions []ReconcileDecision
	for _, ws := range all {
		switch ws.State {
		case workspace.StateActive:
			decisions = append(decisions, r.reconcileActive(ctx, tx, ws))
		case workspace.StateWaitingQuota:
			decisions = append(decisions, r.reconcileWaitingQuota(ctx, tx, ws))
		case workspace.StateQuarantined:
			decisions = append(decisions, ReconcileDecision{
				Reconciler: r.Name(), Entity: "workspace:" + ws.ID,
				Action: DecisionNoOp,
				Detail: "quarantined; awaits human un-quarantine",
			})
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

// reconcileActive handles a workspace that was mid-run when the daemon died.
// An active workspace with an in-flight run_id represents interrupted work:
//   - if a continuation pack + checkpoint exist, the run is resumable from the
//     pack (AC-15, AC-27) — mark it failed-but-resumable so an explicit re-run
//     (or the scheduler) continues from the pack without double-spend;
//   - otherwise the in-flight run is lost — mark failed.
//
// The reconciler does NOT auto-start a new run: that would risk double-spend
// if the prior process is still alive elsewhere. It records a durable decision
// and leaves resumption to an explicit action.
func (r *attemptReconciler) reconcileActive(ctx context.Context, tx ReconcileTx, ws workspace.Workspace) ReconcileDecision {
	packs, err := tx.DB.ListContinuationPacks(ctx, ws.ID)
	if err != nil {
		return ReconcileDecision{
			Reconciler: r.Name(), Entity: "workspace:" + ws.ID,
			Action: DecisionMarkedStale,
			Detail: fmt.Sprintf("list continuation packs failed: %v", err),
		}
	}
	checkpoints, cpErr := tx.DB.ListCheckpoints(ctx, ws.ID)
	if cpErr != nil {
		return ReconcileDecision{
			Reconciler: r.Name(), Entity: "workspace:" + ws.ID,
			Action: DecisionMarkedStale,
			Detail: fmt.Sprintf("list checkpoints failed: %v", cpErr),
		}
	}
	exists, existErr := workspace.WorktreeExists(ws.Path)

	// Interrupted in-flight run: the process is gone (the daemon restarted), so
	// the recorded run_id is stale. Mark the workspace failed so it is not
	// treated as live. A pack + checkpoint mean the work is resumable.
	resumable := len(packs) > 0 && len(checkpoints) > 0
	if existErr != nil || !exists {
		// Worktree gone: the run cannot be resumed at all.
		if err := r.markFailed(ctx, ws.ID, "worktree missing after restart"); err != nil {
			tx.Logger.Warn("attempt reconciler: mark failed", "err", err)
		}
		return ReconcileDecision{
			Reconciler: r.Name(), Entity: "workspace:" + ws.ID,
			Action: DecisionMarkedStale,
			Detail: "active worktree missing after restart; marked failed",
		}
	}

	detail := fmt.Sprintf("interrupted run reconciled (run=%s); %d checkpoints, %d packs",
		ws.RunID, len(checkpoints), len(packs))
	if resumable {
		detail += "; resumable from continuation pack (AC-27)"
	}
	if err := r.markFailed(ctx, ws.ID, "interrupted by daemon restart"); err != nil {
		tx.Logger.Warn("attempt reconciler: mark failed", "err", err)
	}
	return ReconcileDecision{
		Reconciler: r.Name(), Entity: "workspace:" + ws.ID,
		Action: decisionForActive(resumable),
		Detail: detail,
	}
}

// reconcileWaitingQuota re-evaluates a parked workspace. It stays parked until
// an external signal (a route becomes available); the reconciler only confirms
// the worktree is still intact.
func (r *attemptReconciler) reconcileWaitingQuota(ctx context.Context, tx ReconcileTx, ws workspace.Workspace) ReconcileDecision {
	exists, err := workspace.WorktreeExists(ws.Path)
	if err != nil || !exists {
		if mErr := r.markFailed(ctx, ws.ID, "worktree missing while waiting for quota"); mErr != nil {
			tx.Logger.Warn("attempt reconciler: mark failed", "err", mErr)
		}
		return ReconcileDecision{
			Reconciler: r.Name(), Entity: "workspace:" + ws.ID,
			Action: DecisionMarkedStale,
			Detail: "worktree missing while WAITING_QUOTA; marked failed",
		}
	}
	return ReconcileDecision{
		Reconciler: r.Name(), Entity: "workspace:" + ws.ID,
		Action: DecisionKept,
		Detail: "WAITING_QUOTA intact; awaits route availability",
	}
}

func (r *attemptReconciler) markFailed(ctx context.Context, workspaceID, reason string) error {
	if _, err := r.wm.SetState(ctx, workspaceID, workspace.StateFailed); err != nil {
		return err
	}
	if r.wm != nil {
		// The workspace manager audits the transition; nothing more to do.
		_ = reason
	}
	return nil
}

func decisionForActive(resumable bool) DecisionAction {
	if resumable {
		// Resumable: keep the record so an explicit re-run can pick up the pack.
		return DecisionKept
	}
	return DecisionMarkedStale
}

// _ keeps the audit/supervisor/storage imports referenced for future per-
// workspace audit enrichment.
var (
	_ = audit.ScopeTask
	_ = supervisor.MomentPreQuotaSwitch
	_ storage.Workspace
)
