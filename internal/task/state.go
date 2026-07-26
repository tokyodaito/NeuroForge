package task

import "fmt"

// State is a task lifecycle state.
type State string

// Task states. M1 implements the durable backlog states (spec §9, M1-5).
// Additional states (COMPILED, READY, RUNNING, VERIFIED, etc.) are defined in
// STATE_MACHINES.md §2 and land with later milestones (M2+). Per rule §36.25
// these are explicitly documented, not hidden stubs.
const (
	// StateNew: task created with free-form text, not yet ingested.
	StateNew State = "NEW"
	// StateIngested: task accepted into the active backlog.
	StateIngested State = "INGESTED"
	// StatePaused: user paused the task.
	StatePaused State = "PAUSED"
	// StateCancelled: user cancelled the task (terminal).
	StateCancelled State = "CANCELLED"

	// M2+ states (not reachable in M1):
	StateCompiled  State = "COMPILED"
	StateReady     State = "READY"
	StateRunning   State = "RUNNING"
	StateVerified  State = "VERIFIED"
	StateCompleted State = "COMPLETED"
	StateRejected  State = "REJECTED"
	StateFailed    State = "FAILED"
)

// Action drives a task state transition.
type Action string

const (
	ActionIngest Action = "ingest"
	ActionPause  Action = "pause"
	ActionResume Action = "resume"
	ActionCancel Action = "cancel"
	// ActionDispatch transitions a NEW/INGESTED task into RUNNING (the minimal
	// `forge run` path uses this when it dispatches a task).
	ActionDispatch Action = "dispatch"
	// ActionComplete transitions a RUNNING task into COMPLETED (terminal).
	// Used by the run-app finalize step when the outcome is a verified
	// completed-* result.
	ActionComplete Action = "complete"
	// ActionFail transitions a RUNNING task into FAILED (terminal). Used by
	// the run-app finalize step for the failed / no-change / timed-out /
	// interrupted outcomes (STATE_MACHINE.md §4.2).
	ActionFail Action = "fail"
)

// ErrInvalidTransition is returned when a task state transition is not permitted.
type ErrInvalidTransition struct {
	From   State
	Action Action
}

func (e *ErrInvalidTransition) Error() string {
	return fmt.Sprintf("task: invalid transition: cannot %s from state %s", e.Action, e.From)
}

// validTransitions defines the legal task state transitions. cancel is
// universally allowed from any non-terminal state (returns CANCELLED). The
// minimal run's runtime terminal transitions (RUNNING → COMPLETED / FAILED)
// are added per STATE_MACHINE.md §4.2 (the M1 machine only reached CANCELLED
// as terminal — KF-06).
var validTransitions = map[State]map[Action]State{
	StateNew: {
		ActionIngest:   StateIngested,
		ActionPause:    StatePaused,
		ActionDispatch: StateRunning,
	},
	StateIngested: {
		ActionPause:    StatePaused,
		ActionDispatch: StateRunning,
	},
	StatePaused: {
		ActionResume: StateIngested,
	},
	StateRunning: {
		ActionComplete: StateCompleted,
		ActionFail:     StateFailed,
	},
}

// terminalStates cannot be left. COMPLETED, FAILED, CANCELLED are the runtime
// terminals the minimal run reaches (STATE_MACHINE.md §4.1).
var terminalStates = map[State]bool{
	StateCancelled: true,
	StateCompleted: true,
	StateFailed:    true,
}

// CanTransition reports whether the action is valid from the current state, and
// if so, returns the resulting state.
func CanTransition(from State, action Action) (State, error) {
	if terminalStates[from] {
		return "", &ErrInvalidTransition{From: from, Action: action}
	}
	if action == ActionCancel {
		return StateCancelled, nil
	}
	if transitions, ok := validTransitions[from]; ok {
		if to, ok := transitions[action]; ok {
			return to, nil
		}
	}
	return "", &ErrInvalidTransition{From: from, Action: action}
}

// IsValidState reports whether s is a known task state (M1 + the runtime
// terminal states the minimal run reaches).
func IsValidState(s State) bool {
	switch s {
	case StateNew, StateIngested, StatePaused, StateCancelled,
		StateRunning, StateCompleted, StateFailed:
		return true
	}
	return false
}

// ActionForOutcome maps a runapp outcome name to the task action the finalize
// step must apply (STATE_MACHINE.md §4.2). It is defined here (rather than in
// runapp) so the task package owns its own transition table. The outcome
// strings are the literals from OUTCOME_CONTRACT.md §1.1.
func ActionForOutcome(outcome string) Action {
	switch outcome {
	case "completed-with-commit", "completed-with-uncommitted-changes":
		return ActionComplete
	case "cancelled":
		return ActionCancel
	case "completed-no-changes", "failed", "timed-out", "interrupted":
		return ActionFail
	}
	// Unknown outcome: be conservative and fail the task. The finalize tx
	// will reject this when the task is already terminal (idempotent path),
	// otherwise it transitions safely to FAILED.
	return ActionFail
}

// IsTerminal reports whether the state is terminal.
func IsTerminal(s State) bool {
	return terminalStates[s]
}
