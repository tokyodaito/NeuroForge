package daemon

import (
	"context"
	"fmt"
	"time"

	"neuroforge/internal/audit"
	"neuroforge/internal/pipeline"
	"neuroforge/internal/storage"
	"neuroforge/internal/supervisor"
	"neuroforge/internal/task"
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
//
// Ownership (M14-06): a workspace whose task has a NON-TERMINAL durable
// pipeline run is owned by the pipeline recovery (MarkInterrupted + re-drive)
// and is skipped here — finalizing it as interrupted would race the re-drive
// and could terminally fail a run the pipeline is about to resume.
func (r *attemptReconciler) reconcileActive(ctx context.Context, tx ReconcileTx, ws workspace.Workspace) ReconcileDecision {
	if r.pipelineOwns(ctx, tx, ws.TaskID) {
		return ReconcileDecision{
			Reconciler: r.Name(), Entity: "workspace:" + ws.ID,
			Action: DecisionNoOp,
			Detail: "task has an active pipeline run; pipeline recovery owns the outcome",
		}
	}
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
		if err := r.markFailed(ctx, tx, ws, "worktree missing after restart"); err != nil {
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
	if err := r.markFailed(ctx, tx, ws, "interrupted by daemon restart"); err != nil {
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
	if r.pipelineOwns(ctx, tx, ws.TaskID) {
		return ReconcileDecision{
			Reconciler: r.Name(), Entity: "workspace:" + ws.ID,
			Action: DecisionNoOp,
			Detail: "task has an active pipeline run; pipeline recovery owns the outcome",
		}
	}
	exists, err := workspace.WorktreeExists(ws.Path)
	if err != nil || !exists {
		if mErr := r.markFailed(ctx, tx, ws, "worktree missing while waiting for quota"); mErr != nil {
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

// markFailed transitions an interrupted workspace to failed AND keeps the
// owning task in agreement: a non-terminal task is moved to FAILED (BF-03,
// STATE_MACHINE.md §5.1 / §4.3 — a task whose workspace is terminal is itself
// terminal). The `interrupted` outcome (OUTCOME_CONTRACT.md §1.1) is recorded
// in the audit log with the reason so the interruption is durable and visible.
// The change is idempotent: re-reconciling an already-failed workspace is a
// no-op (allowedRecoveryTransition permits failed->failed) and a terminal task
// is never revived.
func (r *attemptReconciler) markFailed(ctx context.Context, tx ReconcileTx, ws workspace.Workspace, reason string) error {
	if _, err := r.wm.SetState(ctx, ws.ID, workspace.StateFailed); err != nil {
		return fmt.Errorf("workspace %s -> failed: %w", ws.ID, err)
	}
	// Keep the task in agreement with the terminal workspace. Only a
	// non-terminal task is moved; a terminal task stays (it must not be
	// revived — invariant I.8 / BF-03).
	if ws.TaskID != "" {
		if t, err := tx.DB.GetTask(ctx, ws.TaskID); err == nil && !isTerminalTask(task.State(t.State)) {
			now := time.Now().UTC().Format(time.RFC3339Nano)
			if err := tx.DB.UpdateTaskState(ctx, ws.TaskID, string(task.StateFailed), now); err != nil {
				tx.Logger.Warn("attempt reconciler: task -> failed", "task", ws.TaskID, "err", err)
			}
		}
	}
	// Record the interrupted outcome so it is durable and distinct from a
	// normal failure (OUTCOME_CONTRACT.md §1.1: interrupted is produced only by
	// the reconciler).
	if tx.Audit != nil {
		if _, err := tx.Audit.Record(ctx, audit.Event{
			Type:    "run.outcome_decided",
			Scope:   audit.ScopeTask,
			ScopeID: ws.ID,
			Actor:   audit.ActorDaemon,
			Payload: audit.Payload(
				"outcome", "interrupted",
				"workspace_id", ws.ID,
				"task_id", ws.TaskID,
				"run_id", ws.RunID,
				"reason", reason,
			),
		}); err != nil {
			tx.Logger.Warn("attempt reconciler: audit interrupted outcome", "err", err)
		}
	}
	return nil
}

// pipelineOwns reports whether the task has a non-terminal durable pipeline
// run; such workspaces are recovered by the pipeline recovery path, not by
// this reconciler.
func (r *attemptReconciler) pipelineOwns(ctx context.Context, tx ReconcileTx, taskID string) bool {
	if taskID == "" {
		return false
	}
	run, err := pipeline.NewStore(tx.DB, nil).CurrentRun(ctx, taskID)
	if err != nil {
		return false
	}
	return !pipeline.IsTerminalRunState(run.State)
}

// isTerminalTask reports whether a task state is terminal (COMPLETED / FAILED /
// CANCELLED / REJECTED) so the reconciler never revives a terminal task.
func isTerminalTask(s task.State) bool {
	switch s {
	case task.StateCompleted, task.StateFailed, task.StateCancelled, task.StateRejected:
		return true
	}
	return false
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
