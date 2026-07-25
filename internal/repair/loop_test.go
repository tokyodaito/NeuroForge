package repair

import (
	"context"
	"errors"
	"testing"

	"neuroforge/internal/review"
	"neuroforge/internal/testengine"
)

func TestLoop_ResolvesOnFirstTry(t *testing.T) {
	t.Parallel()
	var repairCalls, verifyCalls int
	loop := New(Options{
		MaxIterations: 3,
		Repair: func(_ context.Context, _ RepairContext) error {
			repairCalls++
			return nil
		},
		Verify: func(_ context.Context) ([]Finding, error) {
			verifyCalls++
			return nil, nil // no remaining findings
		},
	})

	out, err := loop.Run(context.Background(), []Finding{
		{Source: "test", Severity: "fail", Title: "TestFoo"},
	}, "diff", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Resolved {
		t.Error("should be resolved")
	}
	if repairCalls != 1 || verifyCalls != 1 {
		t.Errorf("repair=%d verify=%d, want 1/1", repairCalls, verifyCalls)
	}
}

func TestLoop_ReachesMaxIterations(t *testing.T) {
	t.Parallel()
	loop := New(Options{
		MaxIterations: 2,
		Repair:        func(_ context.Context, _ RepairContext) error { return nil },
		Verify: func(_ context.Context) ([]Finding, error) {
			return []Finding{{Source: "test", Severity: "fail", Title: "TestFoo"}}, nil
		},
	})

	out, err := loop.Run(context.Background(), []Finding{
		{Source: "test", Severity: "fail", Title: "TestFoo"},
	}, "diff", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Resolved {
		t.Error("should not be resolved")
	}
	if out.IterationsRun != 2 {
		t.Errorf("iterations = %d, want 2", out.IterationsRun)
	}
	if len(out.RemainingFindings) != 1 {
		t.Errorf("remaining = %d, want 1", len(out.RemainingFindings))
	}
	if len(out.History) != 3 { // initial + 2 verify results
		t.Errorf("history = %d entries, want 3", len(out.History))
	}
}

func TestLoop_RepairErrorStops(t *testing.T) {
	t.Parallel()
	loop := New(Options{
		MaxIterations: 3,
		Repair: func(_ context.Context, _ RepairContext) error {
			return errors.New("agent crashed")
		},
		Verify: func(_ context.Context) ([]Finding, error) { return nil, nil },
	})

	_, err := loop.Run(context.Background(), []Finding{
		{Source: "test", Severity: "fail", Title: "TestFoo"},
	}, "diff", nil)
	if err == nil {
		t.Fatal("expected error from repair failure")
	}
}

func TestLoop_NoActionableFindings_Noop(t *testing.T) {
	t.Parallel()
	repairCalled := false
	loop := New(Options{
		MaxIterations: 3,
		Repair: func(_ context.Context, _ RepairContext) error {
			repairCalled = true
			return nil
		},
		Verify: func(_ context.Context) ([]Finding, error) { return nil, nil },
	})

	out, err := loop.Run(context.Background(), []Finding{
		{Source: "review", Severity: "minor", Title: "nit"},
	}, "diff", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Resolved {
		t.Error("should be resolved (no actionable findings)")
	}
	if repairCalled {
		t.Error("repair should not be called for non-actionable findings")
	}
}

func TestLoop_NewPanicsOnZeroIterations(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for zero max iterations")
		}
	}()
	New(Options{MaxIterations: 0})
}

func TestFinding_IsActionable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		f    Finding
		want bool
	}{
		{Finding{Source: "test", Severity: "fail"}, true},
		{Finding{Source: "review", Severity: "blocker"}, true},
		{Finding{Source: "review", Severity: "major"}, true},
		{Finding{Source: "review", Severity: "minor"}, false},
		{Finding{Source: "review", Severity: "info"}, false},
	}
	for _, c := range cases {
		if got := c.f.IsActionable(); got != c.want {
			t.Errorf("%+v.IsActionable() = %v, want %v", c.f, got, c.want)
		}
	}
}

func TestFromTestFailures(t *testing.T) {
	t.Parallel()
	results := []testengine.Result{
		{Failures: []testengine.TestFailure{{TestName: "TestA", Message: "fail"}}},
		{Failures: []testengine.TestFailure{{TestName: "TestB"}}},
	}
	findings := FromTestFailures(results)
	if len(findings) != 2 {
		t.Fatalf("got %d findings, want 2", len(findings))
	}
	if findings[0].Source != "test" || findings[0].Title != "TestA" {
		t.Errorf("unexpected finding: %+v", findings[0])
	}
}

func TestFromReviewFindings(t *testing.T) {
	t.Parallel()
	findings := FromReviewFindings([]review.Finding{
		{Role: review.RoleSecurity, Severity: review.SeverityBlocker, Title: "SQLi"},
	})
	if len(findings) != 1 || findings[0].Severity != "blocker" {
		t.Errorf("unexpected: %+v", findings)
	}
}

func TestRepairContext_Prompt(t *testing.T) {
	t.Parallel()
	rc := RepairContext{
		Iteration: 2,
		Findings: []Finding{
			{Source: "test", Title: "TestFoo", Description: "assertion failed", File: "foo.go", Line: 42},
		},
	}
	p := rc.Prompt()
	if p == "" {
		t.Error("prompt should not be empty")
	}
}
