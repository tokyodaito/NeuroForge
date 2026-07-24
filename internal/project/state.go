package project

import "fmt"

// State is a project lifecycle state (spec §8.4).
type State string

// Project states per spec §8.4. M1 implements the six states marked below.
// States marked "M2+" exist in the type system so the state machine is
// complete, but they are not reachable via user commands in M1 (they require
// agent activity). Per rule §36.25 this is explicitly documented, not hidden.
const (
	// StateDisabled: project registered but not active (initial state on add).
	StateDisabled State = "DISABLED"
	// StateIdle: project started, no active task work. Reachable via start.
	StateIdle State = "IDLE"
	// StateRunning: at least one task is actively executing. M2+: entered by
	// the scheduler when work begins; in M1 it is a valid state in the machine
	// but not entered by any CLI command.
	StateRunning State = "RUNNING"
	// StatePaused: user paused the factory for this project.
	StatePaused State = "PAUSED"
	// StateDraining: factory is finishing in-flight work, then returns to IDLE.
	// M2+: entered by drain; in M1 it exists in the machine but is a no-op
	// (no active work to drain).
	StateDraining State = "DRAINING"
	// StateError: unrecoverable internal failure.
	StateError State = "ERROR"

	// M2+ states (not reachable in M1, defined for completeness):
	StateStarting State = "STARTING" // transient: DISABLED -> STARTING -> IDLE
	StatePausing  State = "PAUSING"  // transient: RUNNING -> PAUSING -> PAUSED
	StateDegraded State = "DEGRADED" // provider unavailable, work continues
	StateBlocked  State = "BLOCKED"  // hard policy/budget block
)

// Action is a lifecycle action that drives a state transition.
type Action string

const (
	ActionStart   Action = "start"
	ActionStop    Action = "stop"
	ActionPause   Action = "pause"
	ActionResume  Action = "resume"
	ActionDrain   Action = "drain"
	ActionRecover Action = "recover"
)

// ErrInvalidTransition is returned when a state transition is not permitted.
type ErrInvalidTransition struct {
	From   State
	Action Action
}

func (e *ErrInvalidTransition) Error() string {
	return fmt.Sprintf("project: invalid transition: cannot %s from state %s", e.Action, e.From)
}

// validTransitions defines the legal state transitions for each (state, action)
// pair. The result is the target state. This table is the single source of
// truth for the project state machine (see docs/architecture/STATE_MACHINES.md).
//
// stop is universally allowed from any non-DISABLED state (it returns to
// DISABLED). This is handled specially in CanTransition so the table stays
// compact.
var validTransitions = map[State]map[Action]State{
	StateDisabled: {
		ActionStart: StateIdle,
	},
	StateIdle: {
		ActionPause: StatePaused,
		ActionDrain: StateDraining,
	},
	StateRunning: {
		ActionPause: StatePaused,
		ActionDrain: StateDraining,
	},
	StatePaused: {
		ActionResume: StateIdle,
	},
	StateDraining: {
		ActionResume: StateIdle,
	},
	StateError: {
		ActionRecover: StateIdle,
	},
}

// CanTransition reports whether the action is valid from the current state, and
// if so, returns the resulting state.
func CanTransition(from State, action Action) (State, error) {
	if action == ActionStop && from != StateDisabled {
		return StateDisabled, nil
	}
	if transitions, ok := validTransitions[from]; ok {
		if to, ok := transitions[action]; ok {
			return to, nil
		}
	}
	return "", &ErrInvalidTransition{From: from, Action: action}
}

// MustTransition panics on invalid transition. Intended for tests only.
func MustTransition(from State, action Action) State {
	to, err := CanTransition(from, action)
	if err != nil {
		panic(err)
	}
	return to
}

// ValidStates is the set of states implemented in M1.
var validStatesM1 = map[State]bool{
	StateDisabled: true,
	StateIdle:     true,
	StateRunning:  true,
	StatePaused:   true,
	StateDraining: true,
	StateError:    true,
}

// IsValidState reports whether s is a known project state.
func IsValidState(s State) bool {
	return validStatesM1[s]
}

// IsTerminal reports whether the state is terminal (no further work possible
// without user intervention).
func IsTerminal(s State) bool {
	return s == StateDisabled
}
