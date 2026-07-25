package scheduler

import (
	"context"
	"fmt"
	"time"

	"neuroforge/internal/audit"
	"neuroforge/internal/postmerge"
)

// MergeOutcome mirrors postmerge.MergeOutcome — the input to the post-merge
// sentinel. Kept here so callers (transport/CLI) do not need to import postmerge
// directly.
type MergeOutcome struct {
	TaskID     string
	CommitSHA  string
	BaseBranch string
	Number     int
	MergedAt   time.Time
}

// Reverter is the capability the post-merge sentinel uses to revert a merge via
// the single merge-authority chokepoint (§28, ADR-0017). The daemon injects a
// wrapper around merge.Authority.Revert.
type Reverter interface {
	Revert(ctx context.Context, taskID, commitSHA, baseBranch string, number int) (revertSHA string, err error)
}

// SmokeCheck is one deterministic post-merge verification step (spec §37). The
// daemon injects concrete checks (e.g. "does the target still build?").
type SmokeCheck interface {
	Name() string
	Run(ctx context.Context) SmokeResult
}

// SmokeResult is the outcome of one smoke check.
type SmokeResult struct {
	Name   string
	Status string // passed | failed | skipped | error
	Detail string
}

// PostMergeOptions configures one sentinel run.
type PostMergeOptions struct {
	// Checks injected by the caller. When empty, a single "merge-present" check
	// runs (did the merge actually land?).
	Checks []SmokeCheck
	// Reverter used when auto-revert is policy-enabled. When nil and a revert is
	// required, the sentinel downgrades to ALERT_ONLY (never silent).
	Reverter Reverter
}

// RunPostMerge executes the post-merge sentinel (spec §37, §4.4, ADR-0017). It
// is a structural no-op (DecisionSkipped) outside AUTONOMOUS — the merge would
// already have been refused by the Governor (AC-7). On a regression in
// AUTONOMOUS with auto_revert enabled, it reverts through the injected Reverter
// (the merge.Authority) and reopens the task idempotently. A failed revert
// downgrades to ALERT_ONLY and reopens for human review (never silent).
//
// The result is persisted durably (the §31 post_merge_checks table) via the
// PostMergeSink.
func (s *Scheduler) RunPostMerge(ctx context.Context, out MergeOutcome, opts PostMergeOptions) (PostMergeRecord, error) {
	pctx, err := s.resolver.Resolve(ctx, out.TaskID)
	if err != nil {
		return PostMergeRecord{}, fmt.Errorf("scheduler: resolve task for post-merge: %w", err)
	}

	checks := toPostmergeChecks(opts.Checks)
	rev := toPostmergeReverter(opts.Reverter)
	reo := &sentinelReopener{s: s, taskID: out.TaskID}

	sentinel := postmerge.NewSentinel(checks, rev, reo)
	pmOut := postmerge.MergeOutcome{
		TaskID:     out.TaskID,
		CommitSHA:  out.CommitSHA,
		BaseBranch: out.BaseBranch,
		Number:     out.Number,
		MergedAt:   out.MergedAt,
	}
	if pmOut.MergedAt.IsZero() {
		pmOut.MergedAt = s.now()
	}

	pmRes, runErr := sentinel.Run(ctx, pctx.Resolved, pmOut)

	// Persist the result durably (§31 post_merge_checks) — even on error so the
	// audit trail is complete (never silent, ADR-0017).
	rec := PostMergeRecord{
		TaskID:     pmRes.TaskID,
		CommitSHA:  pmRes.CommitSHA,
		BaseBranch: pmRes.BaseBranch,
		Decision:   string(pmRes.Decision),
		AllPassed:  pmRes.AllPassed,
		Reverted:   pmRes.Reverted,
		RevertSHA:  pmRes.RevertSHA,
		OccurredAt: pmRes.OccurredAt,
	}
	if s.postmerge != nil {
		if err := s.postmerge.RecordPostMerge(ctx, rec); err != nil {
			s.logger.Warn("scheduler: persist post-merge result failed", "task", out.TaskID, "err", err)
		}
	}

	s.auditf(ctx, "scheduler.postmerge", pctx.ProjectID, out.TaskID, audit.Payload(
		"decision", rec.Decision, "reverted", rec.Reverted, "revert_sha", rec.RevertSHA,
		"all_passed", rec.AllPassed))

	if runErr != nil {
		return rec, fmt.Errorf("scheduler: post-merge sentinel: %w", runErr)
	}
	return rec, nil
}

// Reopen reopens a task idempotently (§37). It implements postmerge.TaskReopener
// so the sentinel can call it; the daemon-backed TaskReopener performs the
// actual state transition. Reopening an already-open task is a no-op (the
// underlying task store treats it as idempotent).
func (s *Scheduler) Reopen(ctx context.Context, taskID, reason string) error {
	if s.reopener == nil {
		return nil
	}
	if err := s.reopener.Reopen(ctx, taskID, reason); err != nil {
		return fmt.Errorf("scheduler: reopen task %s: %w", taskID, err)
	}
	s.auditf(ctx, "scheduler.task_reopened", "", taskID, audit.Payload("reason", reason))
	return nil
}

// reopener is the daemon-injected task reopener (set via SetReopener).
type reopener = TaskReopener

// SetReopener injects the task reopener used by the post-merge sentinel. The
// daemon calls this after constructing the scheduler.
func (s *Scheduler) SetReopener(r TaskReopener) { s.reopener = r }

// --- adapters ---

func toPostmergeChecks(checks []SmokeCheck) []postmerge.SmokeCheck {
	out := make([]postmerge.SmokeCheck, 0, len(checks))
	for _, c := range checks {
		out = append(out, &smokeCheckAdapter{c: c})
	}
	return out
}

type smokeCheckAdapter struct{ c SmokeCheck }

func (a *smokeCheckAdapter) Name() string { return a.c.Name() }
func (a *smokeCheckAdapter) Run(ctx context.Context) postmerge.CheckResult {
	r := a.c.Run(ctx)
	return postmerge.CheckResult{Name: r.Name, Status: postmerge.SmokeStatus(r.Status), Detail: r.Detail}
}

func toPostmergeReverter(r Reverter) postmerge.Reverter {
	if r == nil {
		return nil
	}
	return &reverterAdapter{r: r}
}

type reverterAdapter struct{ r Reverter }

func (a *reverterAdapter) Revert(ctx context.Context, taskID, commitSHA, baseBranch string, number int) (string, error) {
	return a.r.Revert(ctx, taskID, commitSHA, baseBranch, number)
}

// sentinelReopener adapts the scheduler into a postmerge.TaskReopener.
type sentinelReopener struct {
	s      *Scheduler
	taskID string
}

func (r *sentinelReopener) Reopen(ctx context.Context, taskID, reason string) error {
	if r.s == nil {
		return nil
	}
	return r.s.Reopen(ctx, taskID, reason)
}
