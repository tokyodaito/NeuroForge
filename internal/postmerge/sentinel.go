// Package postmerge implements the post-merge sentinel, smoke checks,
// auto-revert and task reopening (spec §4.4, §37, milestone M12).
//
// STATUS: implemented for milestone M12.
//
// Scope: after the Merge Governor (internal/merge) has merged a change in an
// AUTONOMOUS project, the post-merge sentinel runs a configurable set of smoke
// checks against the integrated target. If a smoke check fails (a regression),
// the sentinel:
//
//   - emits a PostMergeCheckResult recording the failure (audit §29.4);
//   - if and only if the resolved policy enables post_merge.auto_revert
//     (only ever true for AUTONOMOUS, §4.4), instructs the Authority to Revert
//     the merge via the change-request provider (§17.6);
//   - reopens the task so a repair loop can run again (§37 full pipeline).
//
// Auto-revert is a delivery mutation; it MUST flow through the single merge
// authority (the Authority) and is never performed by an agent process (AC-28).
// Post-merge smoke checks are never run in a network-locked profile — the
// Governor would already have refused the merge (AC-7).
//
// The package is pure domain logic with deterministic decision functions. It
// never calls an LLM (rule §22.6).
package postmerge

import (
	"context"
	"errors"
	"fmt"
	"time"

	"neuroforge/internal/policy"
)

// SmokeStatus is the outcome of one smoke check.
type SmokeStatus string

const (
	SmokePassed  SmokeStatus = "passed"
	SmokeFailed  SmokeStatus = "failed"
	SmokeSkipped SmokeStatus = "skipped"
	SmokeError   SmokeStatus = "error"
)

// CheckResult records the outcome of a single post-merge smoke check.
type CheckResult struct {
	Name     string
	Status   SmokeStatus
	Detail   string
	Duration time.Duration
}

// Passed reports whether the check succeeded.
func (c CheckResult) Passed() bool { return c.Status == SmokePassed }

// SmokeCheck is one deterministic post-merge verification step. Examples: build
// the target, run a fast smoke-test subset, hit a health endpoint. Checks are
// read-only against the integrated target.
type SmokeCheck interface {
	Name() string
	Run(ctx context.Context) CheckResult
}

// MergeOutcome describes the merge that was just performed (input to the
// sentinel). It is what the Authority returned.
type MergeOutcome struct {
	TaskID     string
	CommitSHA  string
	BaseBranch string
	Number     int // PR/MR number, 0 for local merge
	MergedAt   time.Time
}

// SentinelDecision is what the sentinel resolved to do.
type SentinelDecision string

const (
	// DecisionHealthy: all smoke checks passed; the task stays closed.
	DecisionHealthy SentinelDecision = "HEALTHY"
	// DecisionRevert: smoke failed AND auto-revert is policy-enabled; the merge
	// will be reverted through the Authority and the task reopened.
	DecisionRevert SentinelDecision = "REVERT"
	// DecisionAlertOnly: smoke failed but auto-revert is disabled (or the
	// provider cannot revert); the task is reopened and a human is alerted.
	DecisionAlertOnly SentinelDecision = "ALERT_ONLY"
	// DecisionSkipped: post-merge checks are disabled in policy; nothing runs.
	DecisionSkipped SentinelDecision = "SKIPPED"
)

// PostMergeCheckResult is the durable record of the post-merge sentinel run
// (persisted + audited). It maps to the §31 post_merge_checks table.
type PostMergeCheckResult struct {
	TaskID     string
	CommitSHA  string
	BaseBranch string
	Decision   SentinelDecision
	Checks     []CheckResult
	AllPassed  bool
	Reverted   bool
	RevertSHA  string
	OccurredAt time.Time
}

// Reverter is the capability the sentinel uses to revert a merge. In production
// this is the merge.Authority (which gates every delivery mutation). Keeping it
// an interface keeps this package free of a merge import and lets tests supply
// a deterministic fake.
type Reverter interface {
	// Revert reverts the merged commit. Returns the revert SHA on success.
	Revert(ctx context.Context, taskID, commitSHA, baseBranch string, number int) (revertSHA string, err error)
}

// TaskReopener reopens a task in the backlog so a repair loop can run again
// after an auto-revert (§37). It is the minimal interface the sentinel needs.
type TaskReopener interface {
	Reopen(ctx context.Context, taskID, reason string) error
}

// Sentinel runs post-merge smoke checks and, on regression, drives auto-revert
// and task reopening (§4.4, §37). It is deterministic: same inputs ⇒ same
// decision (rule §36.6).
type Sentinel struct {
	checks   []SmokeCheck
	reverter Reverter
	reopener TaskReopener
	now      func() time.Time
}

// NewSentinel builds a sentinel. checks may be empty (a "did it merge?" check
// is then the only one). If autoRevert is triggered and reverter is nil, the
// sentinel falls back to ALERT_ONLY (never silently does nothing).
func NewSentinel(checks []SmokeCheck, reverter Reverter, reopener TaskReopener) *Sentinel {
	return &Sentinel{
		checks:   checks,
		reverter: reverter,
		reopener: reopener,
		now:      func() time.Time { return time.Now().UTC() },
	}
}

// ErrPolicyDisabled is returned when a caller tries to run post-merge checks
// that the resolved policy does not enable.
var ErrPolicyDisabled = errors.New("postmerge: post-merge checks disabled by policy")

// Run executes the post-merge sentinel. It is a no-op (DecisionSkipped) when the
// resolved policy disables post_merge (which is the case for every profile
// except AUTONOMOUS). This is the structural guarantee that smoke checks never
// run in LOCAL_REVIEW (AC-7 — the merge would already have been refused).
func (s *Sentinel) Run(ctx context.Context, resolved policy.Resolved, out MergeOutcome) (PostMergeCheckResult, error) {
	res := PostMergeCheckResult{
		TaskID:     out.TaskID,
		CommitSHA:  out.CommitSHA,
		BaseBranch: out.BaseBranch,
		OccurredAt: s.now(),
	}
	if !resolved.Pipeline.PostMerge.Enabled {
		res.Decision = DecisionSkipped
		res.AllPassed = true // not a failure; just not configured
		return res, nil
	}

	// Run smoke checks deterministically.
	res.AllPassed = true
	for _, c := range s.checks {
		r := c.Run(ctx)
		res.Checks = append(res.Checks, r)
		if r.Status == SmokeError {
			// An errored check is treated as a failure we cannot act on
			// deterministically → alert only, never auto-revert on noise.
			res.AllPassed = false
			res.Decision = s.decide(resolved, false, true)
			return s.finalise(ctx, res, out)
		}
		if !r.Passed() {
			res.AllPassed = false
		}
	}

	if res.AllPassed {
		res.Decision = DecisionHealthy
		return res, nil
	}

	// Smoke failed. Decide whether to auto-revert.
	res.Decision = s.decide(resolved, false, false)
	return s.finalise(ctx, res, out)
}

// decide maps the smoke outcome + policy to a sentinel decision.
func (s *Sentinel) decide(resolved policy.Resolved, _, errored bool) SentinelDecision {
	if errored {
		// Never auto-revert on a check that itself errored (avoid noise).
		return DecisionAlertOnly
	}
	if resolved.Pipeline.PostMerge.Enabled && resolved.Pipeline.PostMerge.AutoRevert {
		return DecisionRevert
	}
	return DecisionAlertOnly
}

// finalise performs the side-effect for non-healthy decisions: revert (via the
// Authority) and/or reopen the task. It never lets a revert failure pass
// silently — a failed revert downgrades to ALERT_ONLY and reopens the task.
func (s *Sentinel) finalise(ctx context.Context, res PostMergeCheckResult, out MergeOutcome) (PostMergeCheckResult, error) {
	if res.Decision == DecisionRevert {
		if s.reverter == nil {
			res.Decision = DecisionAlertOnly
			s.reopen(ctx, res.TaskID, "auto-revert requested but no reverter configured; smoke check failed")
			return res, fmt.Errorf("postmerge: auto-revert enabled but no reverter wired")
		}
		sha, err := s.reverter.Revert(ctx, out.TaskID, out.CommitSHA, out.BaseBranch, out.Number)
		if err != nil {
			// Revert failed: downgrade to ALERT_ONLY and reopen for human review.
			res.Decision = DecisionAlertOnly
			s.reopen(ctx, res.TaskID, fmt.Sprintf("auto-revert failed: %v", err))
			return res, fmt.Errorf("postmerge: revert of %s failed: %w", out.CommitSHA, err)
		}
		res.Reverted = true
		res.RevertSHA = sha
		// Reopen the task so a repair loop can run again (§37).
		s.reopen(ctx, res.TaskID, "merged change regressed smoke checks; reverted and reopened for repair")
		return res, nil
	}
	if res.Decision == DecisionAlertOnly {
		s.reopen(ctx, res.TaskID, "post-merge smoke check failed; task reopened for human review")
	}
	return res, nil
}

func (s *Sentinel) reopen(ctx context.Context, taskID, reason string) {
	if s.reopener == nil || taskID == "" {
		return
	}
	_ = s.reopener.Reopen(ctx, taskID, reason)
}

// TaskReopenState models the task lifecycle effect of an auto-revert (§37). A
// merged task that regresses reverts to a state where a fresh repair attempt can
// run. The mapping is deterministic and pure.
type TaskReopenState string

const (
	TaskReopenReopened   TaskReopenState = "REOPENED"
	TaskReopenKeepClosed TaskReopenState = "MERGED" // healthy: stays closed
)

// ReopenState returns the deterministic task-lifecycle effect for a sentinel
// result. Only a healthy result keeps the task closed.
func ReopenState(r PostMergeCheckResult) TaskReopenState {
	if r.Decision == DecisionHealthy || r.Decision == DecisionSkipped {
		return TaskReopenKeepClosed
	}
	return TaskReopenReopened
}
