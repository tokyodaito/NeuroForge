// Package runapp is the thin application service that composes the minimal
// reliable run (`forge run`). It owns the single end-to-end sequence:
//
//   - launch one production coding-agent adapter via the supervisor;
//   - wait for the single terminal adapter event (run.completed/failed/cancelled);
//   - inspect the worktree's actual Git state (internal/workspace);
//   - classify the outcome deterministically;
//   - finalize workspace + task + audit atomically;
//   - create the local result ref (refs/heads/forge/result/<task-id>) when applicable;
//   - return a structured Result consumed by the CLI.
//
// It does NOT replace internal/scheduler, internal/postmerge, internal/review
// or internal/merge; the minimal run bypasses those subsystems (NFR-7). It does
// NOT import the daemon, the transport, or any adapter protocol type (it only
// consumes the supervisor's already-normalized terminal event).
//
// Slices S2..S7 of IMPLEMENTATION_SLICES.md land in this package.
package runapp

import (
	"strings"

	"neuroforge/internal/adapter/codingagent/protocol"
	"neuroforge/internal/workspace"
)

// Outcome is the disjoint, total result classification of a single run
// (OUTCOME_CONTRACT.md §1.1, invariant I.3). Every terminal run maps to
// exactly one Outcome.
type Outcome string

const (
	// OutcomeCompletedWithCommit: COMPLETED run, HEAD advanced past base.
	OutcomeCompletedWithCommit Outcome = "completed-with-commit"
	// OutcomeCompletedWithUncommittedChanges: COMPLETED run, HEAD == base but
	// the working tree is dirty.
	OutcomeCompletedWithUncommittedChanges Outcome = "completed-with-uncommitted-changes"
	// OutcomeCompletedNoChanges: COMPLETED run, HEAD == base and the tree is
	// clean. A FAILURE (invariant I.4: no-change run is a failure).
	OutcomeCompletedNoChanges Outcome = "completed-no-changes"
	// OutcomeFailed: adapter reported run.failed (non-timeout).
	OutcomeFailed Outcome = "failed"
	// OutcomeCancelled: user cancellation was the accepted terminal.
	OutcomeCancelled Outcome = "cancelled"
	// OutcomeTimedOut: hard wall-clock deadline fired.
	OutcomeTimedOut Outcome = "timed-out"
	// OutcomeInterrupted: daemon died mid-run; the reconciler marked the
	// workspace failed. Produced only by the reconciler, never by a live
	// forge run.
	OutcomeInterrupted Outcome = "interrupted"
)

// Terminal is the supervisor-side terminal label, derived from the adapter's
// normalized event stream. It is the "process outcome" component of the
// classifier (NOT the task outcome — see invariant I.1).
type Terminal string

const (
	TerminalCompleted Terminal = "COMPLETED"
	TerminalFailed    Terminal = "FAILED"
	TerminalCancelled Terminal = "CANCELLED"
)

// ClassifyInput is the immutable input to the classifier. All four fields are
// required: the supervisor terminal (process outcome), the workspace's base
// SHA, the actual HEAD read from `git rev-parse HEAD` (FR-9), and the porcelain
// status text read from `git status --porcelain` (FR-9). The classifier never
// trusts the cached workspace.HeadSHA.
type ClassifyInput struct {
	// Terminal is the run's process outcome label. Timeouts are reflected here
	// as TerminalFailed with a TIMEOUT class (the supervisor synthesizes this).
	Terminal Terminal
	// TimeoutClass is true when the supervisor's terminal event was a hard
	// timeout (run.failed with class TIMEOUT). It is the discriminating bit
	// between OutcomeFailed and OutcomeTimedOut (OUTCOME_CONTRACT.md §1.2).
	TimeoutClass bool
	// BaseSHA is the workspace's base commit SHA.
	BaseSHA string
	// ActualHEAD is `git rev-parse HEAD` inside the worktree after the run
	// (FR-9).
	ActualHEAD string
	// StatusPorcelain is `git status --porcelain` after the run (FR-9).
	StatusPorcelain string
}

// Classify implements OUTCOME_CONTRACT.md §1.2 as a pure, deterministic
// function (NFR-2). Same inputs ⇒ same outcome, always. The function performs
// NO I/O and uses no time/rand.
//
// Applied in order; the first match wins:
//
//  1. Terminal == CANCELLED ⇒ cancelled.   (I.9: cancellation precedence)
//  2. Terminal == FAILED with TIMEOUT class ⇒ timed-out.
//  3. Terminal == FAILED (any other class) ⇒ failed.
//  4. Terminal == COMPLETED:
//     4a. actualHEAD != baseSHA ⇒ completed-with-commit.
//     4b. actualHEAD == baseSHA AND porcelain non-empty ⇒ completed-with-uncommitted-changes.
//     4c. actualHEAD == baseSHA AND porcelain empty ⇒ completed-no-changes.
//
// Classify is the structural fix for KF-01/KF-05: process success alone never
// yields a "completed-*" outcome — Git is the source of truth (invariant I.1).
func Classify(in ClassifyInput) Outcome {
	// 1. Cancellation precedence.
	if in.Terminal == TerminalCancelled {
		return OutcomeCancelled
	}
	// 2/3. Failure (timeout vs other).
	if in.Terminal == TerminalFailed {
		if in.TimeoutClass {
			return OutcomeTimedOut
		}
		return OutcomeFailed
	}
	// 4. Process completed — classify by the actual git result.
	if in.ActualHEAD != in.BaseSHA {
		return OutcomeCompletedWithCommit
	}
	if strings.TrimSpace(in.StatusPorcelain) != "" {
		return OutcomeCompletedWithUncommittedChanges
	}
	return OutcomeCompletedNoChanges
}

// ClassifyFromEvent is a convenience adapter that builds a ClassifyInput from
// the supervisor's terminal protocol.NormalizedEvent + the workspace
// inspection. The supervisor's event stream is the source of Terminal +
// TimeoutClass; the inspection is the source of BaseSHA / ActualHEAD /
// StatusPorcelain.
//
// If the terminal event is zero-valued (no terminal observed — the
// "interrupted" reconciler case), the caller should construct ClassifyInput
// directly with Terminal = "" and handle the interrupted outcome at the
// application-service layer.
func ClassifyFromEvent(term protocol.NormalizedEvent, ins workspace.Inspection, baseSHA string) ClassifyInput {
	in := ClassifyInput{
		BaseSHA:         baseSHA,
		ActualHEAD:      ins.ActualHEAD,
		StatusPorcelain: ins.StatusPorcelain,
	}
	switch term.Type {
	case protocol.EventRunCompleted:
		in.Terminal = TerminalCompleted
	case protocol.EventRunCancelled:
		in.Terminal = TerminalCancelled
	case protocol.EventRunFailed:
		in.Terminal = TerminalFailed
		if term.Failure != nil && term.Failure.Class == protocol.FailureTimeout {
			in.TimeoutClass = true
		}
	}
	return in
}

// WorkspaceState returns the durable workspace state matching the outcome
// (OUTCOME_CONTRACT.md §1.3 / STATE_MACHINE.md §3.1). The state is always a
// terminal one for the minimal run (FR-12).
func (o Outcome) WorkspaceState() workspace.State {
	switch o {
	case OutcomeCompletedWithCommit, OutcomeCompletedWithUncommittedChanges:
		return workspace.StateCompleted
	case OutcomeCompletedNoChanges, OutcomeFailed, OutcomeInterrupted:
		return workspace.StateFailed
	case OutcomeCancelled:
		return workspace.StateCancelled
	case OutcomeTimedOut:
		return workspace.StateTimedOut
	}
	return workspace.StateFailed
}

// CreatesResultRef reports whether the outcome should produce a local result
// ref at refs/heads/forge/result/<task-id> (FR-14, I.7, I.5).
func (o Outcome) CreatesResultRef() bool {
	switch o {
	case OutcomeCompletedWithCommit, OutcomeCompletedWithUncommittedChanges:
		return true
	}
	return false
}

// IsTerminal reports whether the outcome is one of the absorbing terminal
// outcomes (every outcome defined above is terminal for the minimal run).
func (o Outcome) IsTerminal() bool {
	switch o {
	case OutcomeCompletedWithCommit,
		OutcomeCompletedWithUncommittedChanges,
		OutcomeCompletedNoChanges,
		OutcomeFailed,
		OutcomeCancelled,
		OutcomeTimedOut,
		OutcomeInterrupted:
		return true
	}
	return false
}
