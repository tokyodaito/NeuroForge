package postmerge_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"neuroforge/internal/policy"
	"neuroforge/internal/postmerge"
)

// --- fakes ---

type check struct {
	name   string
	status postmerge.SmokeStatus
	detail string
}

func (c check) Name() string { return c.name }
func (c check) Run(context.Context) postmerge.CheckResult {
	return postmerge.CheckResult{Name: c.name, Status: c.status, Detail: c.detail}
}

type fakeReverter struct {
	called     bool
	taskID     string
	commitSHA  string
	baseBranch string
	err        error
	revertSHA  string
}

func (f *fakeReverter) Revert(_ context.Context, taskID, commitSHA, baseBranch string, _ int) (string, error) {
	f.called = true
	f.taskID = taskID
	f.commitSHA = commitSHA
	f.baseBranch = baseBranch
	return f.revertSHA, f.err
}

type fakeReopener struct {
	called bool
	taskID string
	reason string
}

func (f *fakeReopener) Reopen(_ context.Context, taskID, reason string) error {
	f.called = true
	f.taskID = taskID
	f.reason = reason
	return nil
}

func autonomous(autoRevert bool) policy.Resolved {
	return policy.Resolved{
		Profile: policy.ProfileAutonomous,
		Pipeline: func() policy.Pipeline {
			p := policy.ProfileDefaults(policy.ProfileAutonomous)
			p.PostMerge = policy.PostMergeConfig{Enabled: true, AutoRevert: autoRevert}
			return p
		}(),
	}
}

func localReview() policy.Resolved {
	p := policy.ProfileDefaults(policy.ProfileLocalReview)
	return policy.Resolved{Profile: policy.ProfileLocalReview, Pipeline: p}
}

func mergeOutcome() postmerge.MergeOutcome {
	return postmerge.MergeOutcome{
		TaskID: "WORK-1", CommitSHA: "abc123", BaseBranch: "main", Number: 42, MergedAt: time.Now(),
	}
}

// --- tests ---

func TestHealthyStaysClosed(t *testing.T) {
	rev := &fakeReverter{revertSHA: "rev000"}
	reo := &fakeReopener{}
	s := postmerge.NewSentinel([]postmerge.SmokeCheck{
		check{"build", postmerge.SmokePassed, ""},
		check{"smoke-test", postmerge.SmokePassed, ""},
	}, rev, reo)
	res, err := s.Run(context.Background(), autonomous(true), mergeOutcome())
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != postmerge.DecisionHealthy {
		t.Errorf("decision = %s want HEALTHY", res.Decision)
	}
	if !res.AllPassed {
		t.Errorf("AllPassed should be true")
	}
	if rev.called {
		t.Errorf("reverter must not be called on healthy result")
	}
	if postmerge.ReopenState(res) != postmerge.TaskReopenKeepClosed {
		t.Errorf("task should stay closed when healthy")
	}
}

func TestSmokeFailureAutoRevertsAndReopens(t *testing.T) {
	rev := &fakeReverter{revertSHA: "revSHA"}
	reo := &fakeReopener{}
	s := postmerge.NewSentinel([]postmerge.SmokeCheck{
		check{"build", postmerge.SmokePassed, ""},
		check{"smoke-test", postmerge.SmokeFailed, "3 tests failed"},
	}, rev, reo)
	res, err := s.Run(context.Background(), autonomous(true), mergeOutcome())
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != postmerge.DecisionRevert {
		t.Errorf("decision = %s want REVERT", res.Decision)
	}
	if !res.Reverted || res.RevertSHA != "revSHA" {
		t.Errorf("revert not performed: %+v", res)
	}
	if !rev.called {
		t.Errorf("reverter was not invoked")
	}
	if rev.commitSHA != "abc123" || rev.baseBranch != "main" {
		t.Errorf("revert called with wrong args: sha=%s base=%s", rev.commitSHA, rev.baseBranch)
	}
	if !reo.called {
		t.Errorf("task was not reopened")
	}
	if postmerge.ReopenState(res) != postmerge.TaskReopenReopened {
		t.Errorf("task should be reopened after auto-revert")
	}
}

func TestSmokeFailureNoAutoRevertAlertsOnly(t *testing.T) {
	rev := &fakeReverter{}
	reo := &fakeReopener{}
	// AUTONOMOUS but auto_revert disabled.
	s := postmerge.NewSentinel([]postmerge.SmokeCheck{
		check{"smoke-test", postmerge.SmokeFailed, "fail"},
	}, rev, reo)
	res, err := s.Run(context.Background(), autonomous(false), mergeOutcome())
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != postmerge.DecisionAlertOnly {
		t.Errorf("decision = %s want ALERT_ONLY", res.Decision)
	}
	if rev.called {
		t.Errorf("reverter must not be called when auto_revert disabled")
	}
	if !reo.called {
		t.Errorf("task should still be reopened for human review")
	}
}

func TestRevertFailureDowngradesToAlertOnly(t *testing.T) {
	rev := &fakeReverter{err: errors.New("provider 500")}
	reo := &fakeReopener{}
	s := postmerge.NewSentinel([]postmerge.SmokeCheck{
		check{"smoke-test", postmerge.SmokeFailed, "fail"},
	}, rev, reo)
	res, err := s.Run(context.Background(), autonomous(true), mergeOutcome())
	if err == nil {
		t.Fatalf("expected error from failed revert")
	}
	if res.Decision != postmerge.DecisionAlertOnly {
		t.Errorf("decision = %s want ALERT_ONLY after revert failure", res.Decision)
	}
	if !reo.called {
		t.Errorf("task should be reopened when revert fails")
	}
}

func TestNoReverterDowngradesToAlertOnly(t *testing.T) {
	reo := &fakeReopener{}
	s := postmerge.NewSentinel([]postmerge.SmokeCheck{
		check{"smoke-test", postmerge.SmokeFailed, "fail"},
	}, nil, reo)
	res, err := s.Run(context.Background(), autonomous(true), mergeOutcome())
	if err == nil {
		t.Fatalf("expected error when auto-revert enabled but no reverter")
	}
	if res.Decision != postmerge.DecisionAlertOnly {
		t.Errorf("decision = %s want ALERT_ONLY", res.Decision)
	}
}

func TestPostMergeSkippedWhenPolicyDisabled(t *testing.T) {
	// LOCAL_REVIEW never has post-merge enabled (§4.2). The sentinel is a no-op.
	rev := &fakeReverter{}
	reo := &fakeReopener{}
	s := postmerge.NewSentinel([]postmerge.SmokeCheck{
		check{"smoke-test", postmerge.SmokeFailed, "fail"},
	}, rev, reo)
	res, err := s.Run(context.Background(), localReview(), mergeOutcome())
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != postmerge.DecisionSkipped {
		t.Errorf("decision = %s want SKIPPED for LOCAL_REVIEW", res.Decision)
	}
	if rev.called || reo.called {
		t.Errorf("no side effects should run when post-merge is disabled")
	}
}

func TestErroredCheckAlertsOnlyNeverReverts(t *testing.T) {
	rev := &fakeReverter{revertSHA: "x"}
	reo := &fakeReopener{}
	s := postmerge.NewSentinel([]postmerge.SmokeCheck{
		check{"flaky", postmerge.SmokeError, "timeout"},
	}, rev, reo)
	res, err := s.Run(context.Background(), autonomous(true), mergeOutcome())
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != postmerge.DecisionAlertOnly {
		t.Errorf("decision = %s want ALERT_ONLY on errored check", res.Decision)
	}
	if rev.called {
		t.Errorf("must not auto-revert on a check that errored (noise)")
	}
}
