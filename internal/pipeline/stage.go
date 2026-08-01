package pipeline

import "fmt"

// Stage is one step of the production pipeline a task flows through.
type Stage string

// Pipeline stages for the minimal local path (see doc.go for the full map).
const (
	StageCompile  Stage = "compile"
	StagePlan     Stage = "plan"
	StageReady    Stage = "ready"
	StageExecute  Stage = "execute"
	StageVerify   Stage = "verify"
	StageReview   Stage = "review"
	StageFinalize Stage = "finalize"
	// StageRepair is the loop stage entered from verify/review failure. A
	// repair stage performs ONE bounded repair attempt (agent re-run with
	// repair context); it exits to verify (re-verification, the default) or
	// back to execute (full re-execution, if a repair policy chooses it).
	StageRepair Stage = "repair"
)

// RunState is the lifecycle state of a pipeline run.
type RunState string

// Run states. waiting_quota and blocked are non-terminal wait states; the
// rest of the non-active values are terminal outcomes.
const (
	RunActive          RunState = "active"
	RunCompleted       RunState = "completed"
	RunFailed          RunState = "failed"
	RunCancelled       RunState = "cancelled"
	RunRepairExhausted RunState = "repair_exhausted"
	RunWaitingQuota    RunState = "waiting_quota"
	RunBlocked         RunState = "blocked"
)

// FailureCategory classifies why a stage or run failed. The taxonomy is
// fixed; unknown categories are rejected by the Store so history stays
// machine-aggregable.
type FailureCategory string

// Failure categories (spec taxonomy; FailureInterrupted is used by the
// startup reconciler for stages that were in flight when the process died).
const (
	FailureCompile              FailureCategory = "compile_failure"
	FailureTest                 FailureCategory = "test_failure"
	FailureStaticAnalysis       FailureCategory = "static_analysis_failure"
	FailureReviewRejection      FailureCategory = "review_rejection"
	FailureAgentUnavailable     FailureCategory = "agent_unavailable"
	FailureAgentAuthUnavailable FailureCategory = "agent_auth_unavailable"
	FailureProviderTimeout      FailureCategory = "provider_timeout"
	FailureQuotaExceeded        FailureCategory = "quota_exceeded"
	FailureRateLimited          FailureCategory = "rate_limited"
	FailureInvalidAgentOutput   FailureCategory = "invalid_agent_output"
	FailureNoCodeChanges        FailureCategory = "no_code_changes"
	FailureWorktree             FailureCategory = "worktree_failure"
	FailureGit                  FailureCategory = "git_failure"
	FailureDatabase             FailureCategory = "database_failure"
	FailureLeaseLost            FailureCategory = "lease_lost"
	FailureCancelled            FailureCategory = "cancelled"
	FailurePolicyRejection      FailureCategory = "policy_rejection"
	FailureInvariantViolation   FailureCategory = "invariant_violation"
	FailureInterrupted          FailureCategory = "interrupted"
)

// ErrInvalidTransition is returned when a stage transition is not permitted
// from the run's current stage/state.
type ErrInvalidTransition struct {
	TaskID   string
	RunState RunState
	From     Stage
	To       Stage
}

func (e *ErrInvalidTransition) Error() string {
	return fmt.Sprintf("pipeline: invalid stage transition %s -> %s (task %s, run state %s)",
		e.From, e.To, e.TaskID, e.RunState)
}

// ErrInvalidRunStateTransition is returned when a run-state change is not
// permitted (e.g. leaving a terminal state).
type ErrInvalidRunStateTransition struct {
	TaskID string
	From   RunState
	To     RunState
}

func (e *ErrInvalidRunStateTransition) Error() string {
	return fmt.Sprintf("pipeline: invalid run-state transition %s -> %s (task %s)",
		e.From, e.To, e.TaskID)
}

// stageTransitions defines the legal stage-to-stage transitions while a run
// is active. Mirrors the transition-table style of task.state
// (validTransitions). Documented in doc.go.
var stageTransitions = map[Stage][]Stage{
	StageCompile:  {StagePlan},
	StagePlan:     {StageReady},
	StageReady:    {StageExecute},
	StageExecute:  {StageVerify, StageRepair},
	StageVerify:   {StageReview, StageRepair},
	StageReview:   {StageFinalize, StageRepair},
	StageRepair:   {StageVerify, StageExecute},
	StageFinalize: {},
}

// terminalRunStates cannot be left.
var terminalRunStates = map[RunState]bool{
	RunCompleted:       true,
	RunFailed:          true,
	RunCancelled:       true,
	RunRepairExhausted: true,
}

// waitRunStates are the non-terminal wait states. From a wait state the only
// legal stage transition is to StageReady (resume re-dispatch), and the only
// legal run-state exits are active / failed / cancelled.
var waitRunStates = map[RunState]bool{
	RunWaitingQuota: true,
	RunBlocked:      true,
}

// IsValidStage reports whether s is a known pipeline stage.
func IsValidStage(s Stage) bool {
	_, ok := stageTransitions[s]
	return ok
}

// IsValidRunState reports whether s is a known run state.
func IsValidRunState(s RunState) bool {
	switch s {
	case RunActive, RunCompleted, RunFailed, RunCancelled,
		RunRepairExhausted, RunWaitingQuota, RunBlocked:
		return true
	}
	return false
}

// IsValidFailureCategory reports whether c is a known failure category.
func IsValidFailureCategory(c FailureCategory) bool {
	switch c {
	case FailureCompile, FailureTest, FailureStaticAnalysis,
		FailureReviewRejection, FailureAgentUnavailable,
		FailureAgentAuthUnavailable, FailureProviderTimeout,
		FailureQuotaExceeded, FailureRateLimited, FailureInvalidAgentOutput,
		FailureNoCodeChanges, FailureWorktree, FailureGit, FailureDatabase,
		FailureLeaseLost, FailureCancelled, FailurePolicyRejection,
		FailureInvariantViolation, FailureInterrupted:
		return true
	}
	return false
}

// IsTerminalRunState reports whether the run state is terminal (cannot be
// left, excluded from startup recovery).
func IsTerminalRunState(s RunState) bool { return terminalRunStates[s] }

// IsWaitState reports whether the run state is a non-terminal wait state.
func IsWaitState(s RunState) bool { return waitRunStates[s] }

// CanTransitionStage reports whether moving the stage cursor from → to is
// legal given the run's current run state. Pure function — no I/O.
//
// Rules: terminal runs never transition; from a wait state the only legal
// move is to StageReady (resume re-dispatch, which also re-activates the
// run); from active, the stageTransitions table applies.
func CanTransitionStage(runState RunState, from, to Stage) bool {
	if IsTerminalRunState(runState) {
		return false
	}
	if IsWaitState(runState) {
		return to == StageReady
	}
	for _, next := range stageTransitions[from] {
		if next == to {
			return true
		}
	}
	return false
}

// CanTransitionRunState reports whether a run-state change from → to is
// legal. Pure function — no I/O. Terminal states have no exits; wait states
// may only resume (active) or terminate (failed, cancelled). Note that
// Store.SetRunState applies additional stage-dependent restrictions (e.g.
// waiting_quota is only reachable from execute/verify, completed only from
// finalize) that this pure function cannot see.
func CanTransitionRunState(from, to RunState) bool {
	switch from {
	case RunActive:
		switch to {
		case RunWaitingQuota, RunBlocked, RunFailed, RunCancelled,
			RunRepairExhausted, RunCompleted:
			return true
		}
	case RunWaitingQuota, RunBlocked:
		switch to {
		case RunActive, RunFailed, RunCancelled:
			return true
		}
	}
	return false
}
