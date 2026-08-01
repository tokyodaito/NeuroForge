// This file implements the pipeline driver (milestone M14-06): the
// deterministic loop that advances a durable run through its stages by
// invoking injected handlers. The Store owns persistence and transition
// legality; the Driver owns routing only — it never compiles, dispatches
// agents or verifies anything itself. Concrete handlers are supplied by the
// daemon.
//
// Routing table (handler outcome → store mutation):
//
//	compile/plan/ready  success            → CompleteStage, Transition to next stage
//	execute             success, files > 0 → CompleteStage, Transition to verify
//	execute             success, files = 0 → FailStage(no_code_changes), enter repair
//	verify              passed             → CompleteStage, Transition to review
//	verify              !passed            → FailStage(category), enter repair
//	review              approved           → CompleteStage, Transition to finalize
//	review              !approved          → FailStage(review_rejection), enter repair
//	repair              success            → CompleteStage, IncrementStageAttempt,
//	                                         Transition to verify (re-verification)
//	finalize            success            → CompleteStage, SetRunState(completed)
//	any handler         error              → FailStage(category), then:
//	                                         quota_exceeded / rate_limited from
//	                                         execute or verify → waiting_quota,
//	                                         anything else → failed
//
// "enter repair" always calls IncrementRepairAttempt FIRST: when it reports
// the budget exhausted (ok=false) the run goes to repair_exhausted instead of
// entering the repair stage.
//
// Crash semantics: the driver persists stage outcomes AFTER the handler
// returns, so a crash mid-stage leaves the run active with an open "entered"
// record — exactly the shape MarkInterrupted reconciles. A handler panic is
// treated as a process-level crash, not a stage outcome: the driver recovers
// it, leaves the store untouched and returns an error; the reconciler can
// then MarkInterrupted + re-drive.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// ErrEmergencyStopped is returned by Drive when the persisted emergency-stop
// flag is on. The run is NOT advanced — it stays active at its current stage
// and can be re-driven after the flag is cleared.
var ErrEmergencyStopped = errors.New("pipeline: emergency stop engaged")

// StageError lets a handler report a failure with its category so the driver
// can route it (quota waits, repair loops, terminal failure). Errors without
// a StageError — or with an unknown category — are routed as
// invariant_violation.
type StageError struct {
	Category FailureCategory
	Reason   string
	Err      error
}

func (e *StageError) Error() string {
	switch {
	case e.Reason != "":
		return fmt.Sprintf("pipeline: stage failed (%s): %s", e.Category, e.Reason)
	case e.Err != nil:
		return e.Err.Error()
	default:
		return fmt.Sprintf("pipeline: stage failed (%s)", e.Category)
	}
}

func (e *StageError) Unwrap() error { return e.Err }

// RunContext is the per-stage-attempt view handed to handlers.
type RunContext struct {
	TaskID        string
	ProjectID     string
	Stage         Stage
	Attempt       int // stage attempt (bumps on every repair loop)
	RepairAttempt int // repair attempts consumed so far
	Store         *Store
	Now           func() time.Time
}

// SimpleHandler runs a stage with a plain success/failure outcome
// (compile, plan, ready, finalize).
type SimpleHandler func(ctx context.Context, rc *RunContext) (evidenceRef string, err error)

// ExecuteHandler runs the execute stage. changedFiles == 0 on success routes
// the run to repair with failure category no_code_changes (or to
// repair_exhausted when the repair budget is spent).
type ExecuteHandler func(ctx context.Context, rc *RunContext) (evidenceRef string, changedFiles int, err error)

// VerifyHandler runs the verify stage. passed routes to review; !passed
// routes to repair (budget check first) with the returned category recorded
// on the failed stage.
type VerifyHandler func(ctx context.Context, rc *RunContext) (passed bool, evidenceRef string, category FailureCategory, err error)

// ReviewHandler runs the review stage. approved routes to finalize;
// !approved routes to repair (budget check first) with category
// review_rejection.
type ReviewHandler func(ctx context.Context, rc *RunContext) (approved bool, evidenceRef string, err error)

// RepairHandler performs ONE bounded repair attempt (e.g. re-run the agent
// with a repair prompt). Success routes repair → verify so verification
// reruns against the repaired state.
type RepairHandler func(ctx context.Context, rc *RunContext) (evidenceRef string, err error)

// Handlers are the stage implementations the driver routes between. All
// fields are required; NewDriver rejects nil handlers.
type Handlers struct {
	Compile  SimpleHandler
	Plan     SimpleHandler
	Ready    SimpleHandler
	Finalize SimpleHandler
	Execute  ExecuteHandler
	Verify   VerifyHandler
	Review   ReviewHandler
	Repair   RepairHandler
}

func (h Handlers) validate() error {
	switch {
	case h.Compile == nil:
		return fmt.Errorf("pipeline: driver: Compile handler is nil")
	case h.Plan == nil:
		return fmt.Errorf("pipeline: driver: Plan handler is nil")
	case h.Ready == nil:
		return fmt.Errorf("pipeline: driver: Ready handler is nil")
	case h.Finalize == nil:
		return fmt.Errorf("pipeline: driver: Finalize handler is nil")
	case h.Execute == nil:
		return fmt.Errorf("pipeline: driver: Execute handler is nil")
	case h.Verify == nil:
		return fmt.Errorf("pipeline: driver: Verify handler is nil")
	case h.Review == nil:
		return fmt.Errorf("pipeline: driver: Review handler is nil")
	case h.Repair == nil:
		return fmt.Errorf("pipeline: driver: Repair handler is nil")
	}
	return nil
}

// Driver is the deterministic pipeline loop. It is safe for concurrent use;
// Drive calls for the same task are serialised on a per-task mutex, and
// Store-level idempotency covers cross-restart re-drives.
type Driver struct {
	store    *Store
	handlers Handlers
	logger   *slog.Logger
	now      func() time.Time

	mu        sync.Mutex
	taskLocks map[string]*sync.Mutex
}

// NewDriver creates a Driver over store. All handlers must be non-nil. The
// logger may be nil (a quiet default is used).
func NewDriver(store *Store, h Handlers, logger *slog.Logger) (*Driver, error) {
	if store == nil {
		return nil, fmt.Errorf("pipeline: driver: store is nil")
	}
	if err := h.validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	}
	return &Driver{
		store:     store,
		handlers:  h,
		logger:    logger,
		now:       func() time.Time { return time.Now().UTC() },
		taskLocks: map[string]*sync.Mutex{},
	}, nil
}

// Drive advances the run for taskID through its stages until the run reaches
// a terminal or wait state, the emergency stop engages, or an unrecoverable
// store error occurs. It returns nil when the run reaches a terminal or wait
// state — the outcome lives in the store; callers read CurrentRun.
//
// Drive is idempotent: re-driving a finished run is a no-op, and concurrent
// Drive calls for the same task from one Driver are serialised. A store
// mutation that loses a race (ErrConcurrentModification) is retried once;
// after that the error is returned.
func (d *Driver) Drive(ctx context.Context, taskID string) error {
	if taskID == "" {
		return fmt.Errorf("pipeline: drive: task_id is required")
	}
	lock := d.taskLock(taskID)
	lock.Lock()
	defer lock.Unlock()

	retried := false
	for {
		done, err := d.iterate(ctx, taskID)
		if err != nil {
			switch {
			case errors.Is(err, ErrConcurrentModification) && !retried:
				// Another writer moved the run; re-read and re-decide once.
				retried = true
				continue
			case errors.Is(err, ErrRunTerminal):
				// A concurrent Cancel/SetRunState won the race while the
				// handler ran; the outcome is already durable.
				run, rerr := d.store.CurrentRun(ctx, taskID)
				if rerr == nil && IsTerminalRunState(run.State) {
					return nil
				}
				return err
			default:
				return err
			}
		}
		if done {
			return nil
		}
	}
}

func (d *Driver) taskLock(taskID string) *sync.Mutex {
	d.mu.Lock()
	defer d.mu.Unlock()
	m, ok := d.taskLocks[taskID]
	if !ok {
		m = &sync.Mutex{}
		d.taskLocks[taskID] = m
	}
	return m
}

// iterate performs one loop step: re-read the run, decide, dispatch at most
// one handler and apply its routing. done=true means Drive must stop.
func (d *Driver) iterate(ctx context.Context, taskID string) (done bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			// A panicking handler is a process-level crash, not a stage
			// outcome: leave the store as the handler left it so the
			// reconciler can MarkInterrupted + re-drive.
			done = true
			err = fmt.Errorf("pipeline: handler panic (task %s): %v", taskID, r)
		}
	}()

	if err := ctx.Err(); err != nil {
		return true, err
	}
	run, err := d.store.CurrentRun(ctx, taskID)
	if err != nil {
		return true, err
	}
	if IsTerminalRunState(run.State) || IsWaitState(run.State) {
		// Nothing to drive: terminal runs are done, and wait states resume
		// only via an explicit Transition to ready.
		return true, nil
	}
	on, reason, err := d.store.EmergencyStop(ctx)
	if err != nil {
		return true, err
	}
	if on {
		if reason == "" {
			reason = "no reason recorded"
		}
		return true, fmt.Errorf("%w: %s", ErrEmergencyStopped, reason)
	}

	rc := &RunContext{
		TaskID:        run.TaskID,
		ProjectID:     run.ProjectID,
		Stage:         run.CurrentStage,
		Attempt:       run.StageAttempt,
		RepairAttempt: run.RepairAttempt,
		Store:         d.store,
		Now:           d.now,
	}
	switch run.CurrentStage {
	case StageCompile:
		return false, d.runSimple(ctx, rc, d.handlers.Compile, StagePlan)
	case StagePlan:
		return false, d.runSimple(ctx, rc, d.handlers.Plan, StageReady)
	case StageReady:
		return false, d.runSimple(ctx, rc, d.handlers.Ready, StageExecute)
	case StageExecute:
		return false, d.runExecute(ctx, rc)
	case StageVerify:
		return false, d.runVerify(ctx, rc)
	case StageReview:
		return false, d.runReview(ctx, rc)
	case StageRepair:
		return false, d.runRepair(ctx, rc)
	case StageFinalize:
		return false, d.runFinalize(ctx, rc)
	default:
		return true, fmt.Errorf("pipeline: no handler routing for stage %q (task %s)", run.CurrentStage, taskID)
	}
}

// runSimple drives compile/plan/ready: success completes the stage and
// transitions to next.
func (d *Driver) runSimple(ctx context.Context, rc *RunContext, h SimpleHandler, next Stage) error {
	evidenceRef, err := h(ctx, rc)
	if err != nil {
		return d.failStage(ctx, rc, err)
	}
	if err := d.store.CompleteStage(ctx, rc.TaskID, rc.Stage, evidenceRef); err != nil {
		return err
	}
	_, err = d.store.Transition(ctx, rc.TaskID, next, "", "")
	return err
}

func (d *Driver) runFinalize(ctx context.Context, rc *RunContext) error {
	evidenceRef, err := d.handlers.Finalize(ctx, rc)
	if err != nil {
		return d.failStage(ctx, rc, err)
	}
	if err := d.store.CompleteStage(ctx, rc.TaskID, rc.Stage, evidenceRef); err != nil {
		return err
	}
	return d.store.SetRunState(ctx, rc.TaskID, RunStateChange{To: RunCompleted, ResultRef: evidenceRef})
}

func (d *Driver) runExecute(ctx context.Context, rc *RunContext) error {
	evidenceRef, changedFiles, err := d.handlers.Execute(ctx, rc)
	if err != nil {
		return d.failStage(ctx, rc, err)
	}
	if changedFiles < 0 {
		return d.failStage(ctx, rc, &StageError{
			Category: FailureInvariantViolation,
			Reason:   fmt.Sprintf("execute handler returned negative changedFiles %d", changedFiles),
		})
	}
	if changedFiles == 0 {
		const reason = "execute produced no code changes"
		if err := d.store.FailStage(ctx, rc.TaskID, rc.Stage, FailureNoCodeChanges, reason, evidenceRef); err != nil {
			return err
		}
		return d.enterRepair(ctx, rc, reason)
	}
	if err := d.store.CompleteStage(ctx, rc.TaskID, rc.Stage, evidenceRef); err != nil {
		return err
	}
	_, err = d.store.Transition(ctx, rc.TaskID, StageVerify, "", "")
	return err
}

func (d *Driver) runVerify(ctx context.Context, rc *RunContext) error {
	passed, evidenceRef, category, err := d.handlers.Verify(ctx, rc)
	if err != nil {
		return d.failStage(ctx, rc, err)
	}
	if passed {
		if err := d.store.CompleteStage(ctx, rc.TaskID, rc.Stage, evidenceRef); err != nil {
			return err
		}
		_, err = d.store.Transition(ctx, rc.TaskID, StageReview, "", "")
		return err
	}
	if !IsValidFailureCategory(category) {
		category = FailureInvariantViolation
	}
	reason := fmt.Sprintf("verification failed: %s", category)
	if err := d.store.FailStage(ctx, rc.TaskID, rc.Stage, category, reason, evidenceRef); err != nil {
		return err
	}
	return d.enterRepair(ctx, rc, reason)
}

func (d *Driver) runReview(ctx context.Context, rc *RunContext) error {
	approved, evidenceRef, err := d.handlers.Review(ctx, rc)
	if err != nil {
		return d.failStage(ctx, rc, err)
	}
	if approved {
		if err := d.store.CompleteStage(ctx, rc.TaskID, rc.Stage, evidenceRef); err != nil {
			return err
		}
		_, err = d.store.Transition(ctx, rc.TaskID, StageFinalize, "", "")
		return err
	}
	const reason = "review rejected the changes"
	if err := d.store.FailStage(ctx, rc.TaskID, rc.Stage, FailureReviewRejection, reason, evidenceRef); err != nil {
		return err
	}
	return d.enterRepair(ctx, rc, reason)
}

func (d *Driver) runRepair(ctx context.Context, rc *RunContext) error {
	evidenceRef, err := d.handlers.Repair(ctx, rc)
	if err != nil {
		return d.failStage(ctx, rc, err)
	}
	if err := d.store.CompleteStage(ctx, rc.TaskID, rc.Stage, evidenceRef); err != nil {
		return err
	}
	// Re-entering verify re-runs a previously-executed stage: bump the stage
	// attempt first so the new entries are not absorbed by the idempotency
	// constraint at the old attempt.
	if _, err := d.store.IncrementStageAttempt(ctx, rc.TaskID); err != nil {
		return err
	}
	_, err = d.store.Transition(ctx, rc.TaskID, StageVerify, "repair applied; re-verify", "")
	return err
}

// failStage records a handler error on the current stage and routes the run:
// quota/rate-limit exhaustion from execute or verify parks the run in
// waiting_quota; anything else fails it.
func (d *Driver) failStage(ctx context.Context, rc *RunContext, herr error) error {
	category, reason := categorizeError(herr)
	if err := d.store.FailStage(ctx, rc.TaskID, rc.Stage, category, reason, ""); err != nil {
		return err
	}
	to := RunFailed
	// waiting_quota is only reachable from execute/verify (Store invariant);
	// a quota error anywhere else fails the run.
	if (category == FailureQuotaExceeded || category == FailureRateLimited) &&
		(rc.Stage == StageExecute || rc.Stage == StageVerify) {
		to = RunWaitingQuota
	}
	return d.store.SetRunState(ctx, rc.TaskID, RunStateChange{To: to, Reason: reason})
}

// enterRepair consumes one repair attempt and moves the run to the repair
// stage, or — when the budget is spent — parks the run in repair_exhausted.
func (d *Driver) enterRepair(ctx context.Context, rc *RunContext, reason string) error {
	ok, attempt, err := d.store.IncrementRepairAttempt(ctx, rc.TaskID)
	if err != nil {
		return err
	}
	if !ok {
		d.logger.Info("pipeline: repair budget exhausted", "task", rc.TaskID, "repair_attempt", attempt)
		return d.store.SetRunState(ctx, rc.TaskID, RunStateChange{
			To:     RunRepairExhausted,
			Reason: fmt.Sprintf("repair budget exhausted after %d attempts: %s", attempt, reason),
		})
	}
	_, err = d.store.Transition(ctx, rc.TaskID, StageRepair, reason, "")
	return err
}

// categorizeError extracts a failure category from a handler error. Errors
// without a StageError — or with an unknown category — become
// invariant_violation.
func categorizeError(err error) (FailureCategory, string) {
	var se *StageError
	if errors.As(err, &se) {
		reason := se.Reason
		if reason == "" {
			reason = se.Error()
		}
		if IsValidFailureCategory(se.Category) {
			return se.Category, reason
		}
		return FailureInvariantViolation, reason
	}
	return FailureInvariantViolation, err.Error()
}
