package task

import "testing"

// TestTaskStateMachine_ValidTransitions verifies every legal M1 transition.
func TestTaskStateMachine_ValidTransitions(t *testing.T) {
	tests := []struct {
		name   string
		from   State
		action Action
		want   State
	}{
		{"ingest from NEW", StateNew, ActionIngest, StateIngested},
		{"pause from NEW", StateNew, ActionPause, StatePaused},
		{"pause from INGESTED", StateIngested, ActionPause, StatePaused},
		{"resume from PAUSED", StatePaused, ActionResume, StateIngested},

		// cancel is universal from non-terminal
		{"cancel from NEW", StateNew, ActionCancel, StateCancelled},
		{"cancel from INGESTED", StateIngested, ActionCancel, StateCancelled},
		{"cancel from PAUSED", StatePaused, ActionCancel, StateCancelled},
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

// TestTaskStateMachine_InvalidTransitions verifies that illegal transitions
// are rejected.
func TestTaskStateMachine_InvalidTransitions(t *testing.T) {
	tests := []struct {
		name   string
		from   State
		action Action
	}{
		{"ingest from INGESTED", StateIngested, ActionIngest},
		{"ingest from PAUSED", StatePaused, ActionIngest},
		{"pause from CANCELLED", StateCancelled, ActionPause},
		{"cancel from CANCELLED", StateCancelled, ActionCancel},
		{"resume from NEW", StateNew, ActionResume},
		{"resume from INGESTED", StateIngested, ActionResume},
		{"resume from CANCELLED", StateCancelled, ActionResume},
		{"ingest from CANCELLED", StateCancelled, ActionIngest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := CanTransition(tt.from, tt.action)
			if err == nil {
				t.Fatalf("CanTransition(%s, %s): expected error, got nil", tt.from, tt.action)
			}
		})
	}
}

func TestTaskStateMachine_IsTerminal(t *testing.T) {
	if !IsTerminal(StateCancelled) {
		t.Error("CANCELLED should be terminal")
	}
	if IsTerminal(StateNew) {
		t.Error("NEW should not be terminal")
	}
	if IsTerminal(StateIngested) {
		t.Error("INGESTED should not be terminal")
	}
}

func TestTaskStateMachine_IsValidState(t *testing.T) {
	for _, s := range []State{StateNew, StateIngested, StatePaused, StateCancelled} {
		if !IsValidState(s) {
			t.Errorf("IsValidState(%s) = false, want true", s)
		}
	}
	for _, s := range []State{"BOGUS", ""} {
		if IsValidState(s) {
			t.Errorf("IsValidState(%q) = true, want false", s)
		}
	}
}
