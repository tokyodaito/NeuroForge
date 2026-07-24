package project

import "testing"

// TestProjectStateMachine_ValidTransitions verifies every legal M1 transition
// produces the expected target state.
func TestProjectStateMachine_ValidTransitions(t *testing.T) {
	tests := []struct {
		name   string
		from   State
		action Action
		want   State
	}{
		{"start from DISABLED", StateDisabled, ActionStart, StateIdle},
		{"pause from IDLE", StateIdle, ActionPause, StatePaused},
		{"resume from PAUSED", StatePaused, ActionResume, StateIdle},
		{"drain from IDLE", StateIdle, ActionDrain, StateDraining},
		{"drain from RUNNING", StateRunning, ActionDrain, StateDraining},
		{"pause from RUNNING", StateRunning, ActionPause, StatePaused},
		{"resume from DRAINING", StateDraining, ActionResume, StateIdle},
		{"recover from ERROR", StateError, ActionRecover, StateIdle},

		// stop is universal
		{"stop from IDLE", StateIdle, ActionStop, StateDisabled},
		{"stop from RUNNING", StateRunning, ActionStop, StateDisabled},
		{"stop from PAUSED", StatePaused, ActionStop, StateDisabled},
		{"stop from DRAINING", StateDraining, ActionStop, StateDisabled},
		{"stop from ERROR", StateError, ActionStop, StateDisabled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CanTransition(tt.from, tt.action)
			if err != nil {
				t.Fatalf("CanTransition(%s, %s): unexpected error: %v", tt.from, tt.action, err)
			}
			if got != tt.want {
				t.Errorf("CanTransition(%s, %s) = %s, want %s", tt.from, tt.action, got, tt.want)
			}
		})
	}
}

// TestProjectStateMachine_InvalidTransitions verifies that illegal transitions
// are rejected with ErrInvalidTransition.
func TestProjectStateMachine_InvalidTransitions(t *testing.T) {
	tests := []struct {
		name   string
		from   State
		action Action
	}{
		{"start from IDLE", StateIdle, ActionStart},
		{"start from RUNNING", StateRunning, ActionStart},
		{"pause from DISABLED", StateDisabled, ActionPause},
		{"pause from PAUSED", StatePaused, ActionPause},
		{"resume from IDLE", StateIdle, ActionResume},
		{"resume from RUNNING", StateRunning, ActionResume},
		{"drain from PAUSED", StatePaused, ActionDrain},
		{"drain from DISABLED", StateDisabled, ActionDrain},
		{"start from ERROR without recover", StateError, ActionStart},
		{"pause from ERROR", StateError, ActionPause},
		{"stop from DISABLED", StateDisabled, ActionStop},
		{"recover from IDLE", StateIdle, ActionRecover},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := CanTransition(tt.from, tt.action)
			if err == nil {
				t.Fatalf("CanTransition(%s, %s): expected error, got nil", tt.from, tt.action)
			}
			if _, ok := err.(*ErrInvalidTransition); !ok {
				t.Fatalf("CanTransition(%s, %s): expected ErrInvalidTransition, got %T: %v",
					tt.from, tt.action, err, err)
			}
		})
	}
}

// TestProjectStateMachine_IsValidState verifies state recognition.
func TestProjectStateMachine_IsValidState(t *testing.T) {
	for _, s := range []State{StateDisabled, StateIdle, StateRunning, StatePaused, StateDraining, StateError} {
		if !IsValidState(s) {
			t.Errorf("IsValidState(%s) = false, want true", s)
		}
	}
	for _, s := range []State{"BOGUS", "", "idle"} {
		if IsValidState(s) {
			t.Errorf("IsValidState(%q) = true, want false", s)
		}
	}
}

func TestProjectStateMachine_IsTerminal(t *testing.T) {
	if !IsTerminal(StateDisabled) {
		t.Errorf("DISABLED should be terminal")
	}
	if IsTerminal(StateIdle) {
		t.Errorf("IDLE should not be terminal")
	}
}
