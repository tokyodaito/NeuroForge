package cli

import "testing"

// TestExitCodeFor_OutcomeContract verifies OUTCOME_CONTRACT.md §4 exit codes,
// including the BF-08 fix: `interrupted` → 130 (SIGINT-like), not 1.
func TestExitCodeFor_OutcomeContract(t *testing.T) {
	cases := []struct {
		outcome string
		want    int
	}{
		{"completed-with-commit", ExitOK},
		{"completed-with-uncommitted-changes", ExitOK},
		{"completed-no-changes", ExitErr},
		{"failed", ExitErr},
		{"cancelled", ExitCancelled},   // 130
		{"interrupted", ExitCancelled}, // 130 (BF-08)
		{"timed-out", ExitTimedOut},    // 124
		{"", ExitErr},                  // unknown → safe failure
		{"nonsense", ExitErr},
	}
	for _, tc := range cases {
		t.Run(tc.outcome, func(t *testing.T) {
			if got := exitCodeFor(tc.outcome); got != tc.want {
				t.Errorf("exitCodeFor(%q) = %d, want %d", tc.outcome, got, tc.want)
			}
		})
	}
}
