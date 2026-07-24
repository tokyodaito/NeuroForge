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
)

// ErrInvalidTransition is returned when a task state transition is not permitted.
type ErrInvalidTransition struct {
	From   State
	Action Action
}

func (e *ErrInvalidTransition) Error() string {
	return fmt.Sprintf("task: invalid transition: cannot %s from state %s", e.Action, e.From)
}

// validTransitions defines the legal task state transitions for M1.
// cancel is universally allowed from any non-terminal state (returns CANCELLED).
var validTransitions = map[State]map[Action]State{
	StateNew: {
		ActionIngest: StateIngested,
		ActionPause:  StatePaused,
	},
	StateIngested: {
		ActionPause: StatePaused,
	},
	StatePaused: {
		ActionResume: StateIngested,
	},
}

// terminalStates cannot be left.
var terminalStates = map[State]bool{
	StateCancelled: true,
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

// IsValidState reports whether s is a known M1 task state.
func IsValidState(s State) bool {
	switch s {
	case StateNew, StateIngested, StatePaused, StateCancelled:
		return true
	}
	return false
}

// IsTerminal reports whether the state is terminal.
func IsTerminal(s State) bool {
	return terminalStates[s]
}
