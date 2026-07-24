package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"neuroforge/internal/audit"
	"neuroforge/internal/storage"
)

// DecisionAction classifies what a reconciler did with an entity.
type DecisionAction string

const (
	DecisionNoOp        DecisionAction = "no-op"        // nothing to do; state already consistent
	DecisionKept        DecisionAction = "kept"         // entity verified live/valid and left as-is
	DecisionReclaimed   DecisionAction = "reclaimed"    // stale/obsolete entity removed
	DecisionRepaired    DecisionAction = "repaired"     // corrupted entity fixed without data loss
	DecisionMarkedStale DecisionAction = "marked-stale" // entity recorded as stale for later handling
	DecisionConflict    DecisionAction = "conflict"     // a live owner exists; startup must abort
)

// ReconcileDecision is one outcome of reconciling a daemon-owned entity.
type ReconcileDecision struct {
	Reconciler string
	Entity     string
	Action     DecisionAction
	Detail     string
}

// ReconcileTx is the operational context handed to a [Reconciler] at startup.
type ReconcileTx struct {
	DB     *storage.DB
	Audit  *audit.Recorder
	Dirs   Dirs
	Logger *slog.Logger
}

// Reconciler reconciles one category of daemon-owned state against OS reality
// at startup. It must be deterministic and idempotent: reconciling an already
// consistent state yields only no-op/kept decisions.
//
// This is the extension point for future milestones. M0 registers reconcilers
// for the runtime entities that exist today (daemon runtime files + DB schema
// health). Agent-attempt / work-package / worktree reconcilers will be added in
// M2/M3 once those entities exist — they must not be faked here (rule §36.25).
type Reconciler interface {
	Name() string
	Reconcile(ctx context.Context, tx ReconcileTx) ([]ReconcileDecision, error)
}

// ErrConcurrentDaemon is returned when reconciliation finds a live daemon still
// owning the runtime dir; startup must abort to avoid a duplicate daemon.
var ErrConcurrentDaemon = errors.New("daemon: another daemon owns this runtime dir")

// DefaultReconcilers returns the M0 set: runtime-file ownership + DB health.
func DefaultReconcilers() []Reconciler {
	return []Reconciler{
		&runtimeFilesReconciler{},
		&databaseHealthReconciler{},
	}
}

// Reconcile runs every reconciler in order, audits each decision, and returns
// the aggregate. If a reconciler returns an error (e.g. a live-owner conflict),
// Reconcile stops and returns that error along with the decisions so far.
// Reconciliation never causes silent data loss: corrupted ephemeral runtime
// state is repaired/removed, but durable data is only verified, never destroyed.
func Reconcile(ctx context.Context, tx ReconcileTx, reconcilers []Reconciler) ([]ReconcileDecision, error) {
	var all []ReconcileDecision
	for _, r := range reconcilers {
		decisions, err := r.Reconcile(ctx, tx)
		for _, d := range decisions {
			all = append(all, d)
			if tx.Audit != nil {
				if _, aErr := tx.Audit.Record(ctx, audit.Event{
					Type:  "reconcile.decision",
					Actor: audit.ActorDaemon,
					Payload: audit.Payload(
						"reconciler", d.Reconciler,
						"entity", d.Entity,
						"action", string(d.Action),
						"detail", d.Detail,
					),
				}); aErr != nil {
					tx.Logger.Warn("audit reconcile.decision failed", "err", aErr)
				}
			}
			tx.Logger.Info("reconcile decision",
				"reconciler", d.Reconciler, "entity", d.Entity, "action", d.Action, "detail", d.Detail)
		}
		if err != nil {
			return all, err
		}
	}
	if tx.Audit != nil {
		if _, err := tx.Audit.Record(ctx, audit.Event{
			Type:    "reconcile.complete",
			Actor:   audit.ActorDaemon,
			Payload: audit.Payload("decisions", len(all)),
		}); err != nil {
			tx.Logger.Warn("audit reconcile.complete failed", "err", err)
		}
	}
	return all, nil
}

// --- runtimeFilesReconciler ---

type runtimeFilesReconciler struct{}

func (r *runtimeFilesReconciler) Name() string { return "runtime-files" }

func (r *runtimeFilesReconciler) Reconcile(ctx context.Context, tx ReconcileTx) ([]ReconcileDecision, error) {
	pid, err := readPID(tx.Dirs)
	if errors.Is(err, os.ErrNotExist) || (err == nil && pid == 0) {
		return []ReconcileDecision{{r.Name(), "runtime-files", DecisionNoOp, "no prior pid file; clean start"}}, nil
	}
	if err != nil {
		// Corrupted pid file: ephemeral runtime state — repair by removing it.
		cleanRuntimeFiles(tx.Dirs)
		return []ReconcileDecision{{
			Reconciler: r.Name(), Entity: "runtime-files", Action: DecisionRepaired,
			Detail: "removed unparseable pid file: " + err.Error(),
		}}, nil
	}

	if processAlive(pid) {
		// A process still claims this home. Do NOT touch its files (that would
		// break the running daemon). Abort startup to avoid a duplicate.
		return []ReconcileDecision{{
			Reconciler: r.Name(), Entity: "runtime-files", Action: DecisionConflict,
			Detail: fmt.Sprintf("pid %d is alive; another daemon owns this runtime dir", pid),
		}}, ErrConcurrentDaemon
	}

	// Stale: the recorded owner is dead. Reclaim the ephemeral runtime files.
	cleanRuntimeFiles(tx.Dirs)
	return []ReconcileDecision{{
		Reconciler: r.Name(), Entity: "runtime-files", Action: DecisionReclaimed,
		Detail: fmt.Sprintf("removed stale runtime files (pid %d not alive)", pid),
	}}, nil
}

// --- databaseHealthReconciler ---

type databaseHealthReconciler struct{}

func (r *databaseHealthReconciler) Name() string { return "database-health" }

func (r *databaseHealthReconciler) Reconcile(ctx context.Context, tx ReconcileTx) ([]ReconcileDecision, error) {
	v, err := tx.DB.CurrentVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("read schema version: %w", err)
	}
	n, err := tx.DB.CountAuditEvents(ctx)
	if err != nil {
		// Never silently swallow a durability problem.
		return nil, fmt.Errorf("count audit events: %w", err)
	}
	return []ReconcileDecision{{
		Reconciler: r.Name(), Entity: "database", Action: DecisionKept,
		Detail: fmt.Sprintf("schema v%d, %d audit rows present", v, n),
	}}, nil
}
