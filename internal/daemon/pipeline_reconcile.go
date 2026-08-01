package daemon

import (
	"context"
	"fmt"

	"neuroforge/internal/pipeline"
)

// pipelineReconciler marks every active pipeline run's in-flight stage as
// interrupted at daemon startup (the process died mid-stage; the open
// "entered" record must not look like a stage outcome). The actual re-drive
// happens later via PipelineService.ResumeActiveRuns, once the driver and the
// stage dependencies (supervisor, workspace manager, …) exist — reconciliation
// runs before those are built. Cancelled runs are terminal and therefore
// never listed: a durable cancel is never resumed.
type pipelineReconciler struct {
	store *pipeline.Store
}

func (r *pipelineReconciler) Name() string { return "pipeline-runs" }

func (r *pipelineReconciler) Reconcile(ctx context.Context, tx ReconcileTx) ([]ReconcileDecision, error) {
	runs, err := r.store.ListActiveRuns(ctx)
	if err != nil {
		return nil, fmt.Errorf("pipeline reconcile: list active runs: %w", err)
	}
	if len(runs) == 0 {
		return []ReconcileDecision{{
			Reconciler: r.Name(), Entity: "pipeline-runs",
			Action: DecisionNoOp, Detail: "no active pipeline runs",
		}}, nil
	}
	// While the emergency stop is engaged, active runs are PARKED, not crashed:
	// marking their in-flight stage interrupted on every restart would accrue
	// spurious interrupted records (review finding L2). They are re-driven (and
	// marked interrupted exactly once) by ResumeActiveRuns after the stop
	// clears.
	estopOn, estopReason, err := r.store.EmergencyStop(ctx)
	if err != nil {
		return nil, fmt.Errorf("pipeline reconcile: read estop: %w", err)
	}
	decisions := make([]ReconcileDecision, 0, len(runs))
	if estopOn {
		for _, run := range runs {
			decisions = append(decisions, ReconcileDecision{
				Reconciler: r.Name(), Entity: "pipeline-run/" + run.TaskID,
				Action: DecisionKept,
				Detail: fmt.Sprintf("emergency stop engaged (%s); run parked at stage %s without an interrupted record",
					estopReason, run.CurrentStage),
			})
		}
		return decisions, nil
	}
	for _, run := range runs {
		if pipeline.IsWaitState(run.State) {
			// Wait states have no in-flight stage to interrupt; resume (via
			// Transition to ready) happens in ResumeActiveRuns.
			decisions = append(decisions, ReconcileDecision{
				Reconciler: r.Name(), Entity: "pipeline-run/" + run.TaskID,
				Action: DecisionKept, Detail: fmt.Sprintf("wait state %s at stage %s; resume deferred", run.State, run.CurrentStage),
			})
			continue
		}
		if err := r.store.MarkInterrupted(ctx, run.TaskID, "daemon restarted mid-stage"); err != nil {
			return decisions, fmt.Errorf("pipeline reconcile: mark interrupted for task %s: %w", run.TaskID, err)
		}
		decisions = append(decisions, ReconcileDecision{
			Reconciler: r.Name(), Entity: "pipeline-run/" + run.TaskID,
			Action: DecisionMarkedStale,
			Detail: fmt.Sprintf("stage %s (attempt %d) marked interrupted; re-drive deferred to pipeline recovery", run.CurrentStage, run.StageAttempt),
		})
	}
	return decisions, nil
}
