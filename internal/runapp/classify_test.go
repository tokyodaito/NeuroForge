package runapp_test

import (
	"testing"

	"neuroforge/internal/runapp"
	"neuroforge/internal/workspace"
)

// TestClassify_Table verifies every cell of OUTCOME_CONTRACT.md §1.1:
//
//	| Outcome                              | runTerminal | actualHEAD == baseSHA | gitStatus empty |
//	| completed-with-commit                | COMPLETED   | no                    | any             |
//	| completed-with-uncommitted-changes   | COMPLETED   | yes                   | no              |
//	| completed-no-changes                 | COMPLETED   | yes                   | yes             |
//	| failed                               | FAILED      | any                   | any             |
//	| cancelled                            | CANCELLED   | any                   | any             |
//	| timed-out                            | FAILED+TMO  | any                   | any             |
//
// It also asserts NFR-2 (determinism: same inputs ⇒ same outcome) and
// invariant I.3 (disjoint and total).
func TestClassify_Table(t *testing.T) {
	type tc struct {
		name       string
		in         runapp.ClassifyInput
		want       runapp.Outcome
		wantState  workspace.State
		createsRef bool
	}
	cases := []tc{
		{
			name: "completed-with-commit",
			in: runapp.ClassifyInput{
				Terminal:        runapp.TerminalCompleted,
				BaseSHA:         "abc",
				ActualHEAD:      "def",
				StatusPorcelain: "",
			},
			want:       runapp.OutcomeCompletedWithCommit,
			wantState:  workspace.StateCompleted,
			createsRef: true,
		},
		{
			name: "completed-with-commit-dirty-tree-still-with-commit",
			in: runapp.ClassifyInput{
				Terminal:        runapp.TerminalCompleted,
				BaseSHA:         "abc",
				ActualHEAD:      "def",
				StatusPorcelain: " M foo.txt\n",
			},
			want:       runapp.OutcomeCompletedWithCommit,
			wantState:  workspace.StateCompleted,
			createsRef: true,
		},
		{
			name: "completed-with-uncommitted-changes",
			in: runapp.ClassifyInput{
				Terminal:        runapp.TerminalCompleted,
				BaseSHA:         "abc",
				ActualHEAD:      "abc",
				StatusPorcelain: " M foo.txt\n",
			},
			want:       runapp.OutcomeCompletedWithUncommittedChanges,
			wantState:  workspace.StateCompleted,
			createsRef: true,
		},
		{
			name: "completed-no-changes",
			in: runapp.ClassifyInput{
				Terminal:        runapp.TerminalCompleted,
				BaseSHA:         "abc",
				ActualHEAD:      "abc",
				StatusPorcelain: "",
			},
			want:       runapp.OutcomeCompletedNoChanges,
			wantState:  workspace.StateFailed,
			createsRef: false,
		},
		{
			name: "failed-process-exit-1",
			in: runapp.ClassifyInput{
				Terminal:   runapp.TerminalFailed,
				BaseSHA:    "abc",
				ActualHEAD: "abc",
			},
			want:       runapp.OutcomeFailed,
			wantState:  workspace.StateFailed,
			createsRef: false,
		},
		{
			name: "failed-process-but-head-advanced-still-failed",
			in: runapp.ClassifyInput{
				Terminal:   runapp.TerminalFailed,
				BaseSHA:    "abc",
				ActualHEAD: "def", // commit was made but the run failed
			},
			want:       runapp.OutcomeFailed,
			wantState:  workspace.StateFailed,
			createsRef: false,
		},
		{
			name: "cancelled-beats-everything",
			in: runapp.ClassifyInput{
				Terminal:   runapp.TerminalCancelled,
				BaseSHA:    "abc",
				ActualHEAD: "def", // even with a commit, cancelled wins
			},
			want:       runapp.OutcomeCancelled,
			wantState:  workspace.StateCancelled,
			createsRef: false,
		},
		{
			name: "timed-out",
			in: runapp.ClassifyInput{
				Terminal:     runapp.TerminalFailed,
				TimeoutClass: true,
				BaseSHA:      "abc",
				ActualHEAD:   "abc",
			},
			want:       runapp.OutcomeTimedOut,
			wantState:  workspace.StateTimedOut,
			createsRef: false,
		},
		{
			name: "timeout-beats-cancel-via-failure-class",
			// OUTCOME_CONTRACT.md §1.2: when the hard deadline fired, the
			// terminal is FAILED+TIMEOUT ⇒ timed-out. This branch is reached
			// because the supervisor synthesizes run.failed(TIMEOUT) on a hard
			// deadline — it is NOT TerminalCancelled even if a cancel was
			// also requested (§1.3 of STATE_MACHINE.md).
			in: runapp.ClassifyInput{
				Terminal:     runapp.TerminalFailed,
				TimeoutClass: true,
			},
			want:       runapp.OutcomeTimedOut,
			wantState:  workspace.StateTimedOut,
			createsRef: false,
		},
	}

	seen := make(map[runapp.Outcome]bool, len(cases))
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := runapp.Classify(c.in)
			if got != c.want {
				t.Fatalf("Classify(%+v) = %q, want %q", c.in, got, c.want)
			}
			if state := got.WorkspaceState(); state != c.wantState {
				t.Errorf("WorkspaceState() = %q, want %q", state, c.wantState)
			}
			if ref := got.CreatesResultRef(); ref != c.createsRef {
				t.Errorf("CreatesResultRef() = %v, want %v", ref, c.createsRef)
			}
			if !got.IsTerminal() {
				t.Errorf("outcome %q should be terminal", got)
			}
			// Determinism: re-running with the same input yields the same
			// result (NFR-2).
			again := runapp.Classify(c.in)
			if again != got {
				t.Errorf("non-deterministic: first %q then %q", got, again)
			}
			seen[got] = true
		})
	}
	// Sanity: every "named" outcome except `interrupted` (which the
	// reconciler produces, not the live classifier) must be reachable.
	for _, want := range []runapp.Outcome{
		runapp.OutcomeCompletedWithCommit,
		runapp.OutcomeCompletedWithUncommittedChanges,
		runapp.OutcomeCompletedNoChanges,
		runapp.OutcomeFailed,
		runapp.OutcomeCancelled,
		runapp.OutcomeTimedOut,
	} {
		if !seen[want] {
			t.Errorf("outcome %q never produced by the table test", want)
		}
	}
}

// TestClassify_NoChangeIsNotSuccess verifies KF-01 / KF-05 / I.1 / I.4: a
// process that completed but produced no repository changes is classified as
// `completed-no-changes` and maps to the failed workspace state. It must NOT
// be confused with a successful run.
func TestClassify_NoChangeIsNotSuccess(t *testing.T) {
	in := runapp.ClassifyInput{
		Terminal:        runapp.TerminalCompleted,
		BaseSHA:         "abc",
		ActualHEAD:      "abc",
		StatusPorcelain: "",
	}
	got := runapp.Classify(in)
	if got != runapp.OutcomeCompletedNoChanges {
		t.Fatalf("got %q, want completed-no-changes", got)
	}
	if got.WorkspaceState() != workspace.StateFailed {
		t.Errorf("a no-change run must map to the failed workspace state, got %q", got.WorkspaceState())
	}
	if got.CreatesResultRef() {
		t.Errorf("a no-change run must not create a result ref")
	}
}
