package daemon

import (
	"context"
	"fmt"
	"log/slog"

	"neuroforge/internal/audit"
	"neuroforge/internal/runapp"
	"neuroforge/internal/storage"
	"neuroforge/internal/task"
	"neuroforge/internal/workspace"
)

// finalizeIntentReconciler resumes crash-interrupted finalizations (BF-07).
// A durable finalize_intents row without a terminal workspace means the prior
// process died between intent/ref and the terminal DB commit. Recovery either
// completes the commit consistently or surfaces a conflict — never a false
// success and never a silent overwrite of a foreign result ref.
type finalizeIntentReconciler struct {
	svc *runapp.Service
}

func newFinalizeIntentReconciler(db *storage.DB, rec *audit.Recorder, wm *workspace.Manager, logger *slog.Logger) *finalizeIntentReconciler {
	bk := task.NewBacklog(db, rec, "", logger)
	svc := runapp.NewService(runapp.Options{
		Workspaces: wm,
		Tasks:      bk,
		Audit:      rec,
		DB:         db,
	})
	return &finalizeIntentReconciler{svc: svc}
}

func (r *finalizeIntentReconciler) Name() string { return "finalize-intents" }

func (r *finalizeIntentReconciler) Reconcile(ctx context.Context, tx ReconcileTx) ([]ReconcileDecision, error) {
	results, errs := r.svc.RecoverPendingFinalizations(ctx)
	var decisions []ReconcileDecision
	for _, res := range results {
		decisions = append(decisions, ReconcileDecision{
			Reconciler: r.Name(),
			Entity:     "workspace:" + res.WorkspaceID,
			Action:     DecisionRepaired,
			Detail:     fmt.Sprintf("resumed finalize intent → outcome=%s", res.Outcome),
		})
	}
	for _, err := range errs {
		decisions = append(decisions, ReconcileDecision{
			Reconciler: r.Name(),
			Entity:     "finalize-intent",
			Action:     DecisionMarkedStale,
			Detail:     err.Error(),
		})
		if tx.Logger != nil {
			tx.Logger.Warn("finalize intent recovery failed", "err", err)
		}
	}
	if len(decisions) == 0 {
		decisions = append(decisions, ReconcileDecision{
			Reconciler: r.Name(),
			Entity:     "finalize-intents",
			Action:     DecisionNoOp,
			Detail:     "no pending finalize intents",
		})
	}
	return decisions, nil
}
