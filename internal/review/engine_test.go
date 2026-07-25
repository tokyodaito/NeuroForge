package review

import (
	"context"
	"testing"

	"neuroforge/internal/policy"
)

func resolveReview(t *testing.T, profile policy.Profile, override *policy.Pipeline) policy.Resolved {
	t.Helper()
	proj := policy.NewProject(profile)
	res, vs := policy.Resolve(proj, policy.TaskContext{Override: override})
	if policy.Blocks(vs) {
		t.Logf("note: resolve produced blocks: %+v", vs)
	}
	return res
}

func TestEngine_AllReviewsOn_Approved(t *testing.T) {
	t.Parallel()
	rev := NewFakeReviewer(FakeScript{}) // no findings → approved
	eng := New(Options{Reviewer: rev})
	res := resolveReview(t, policy.ProfileLocalReview, nil)

	result, err := eng.Run(context.Background(), RunInput{
		Policy: res,
		Req:    ReviewRequest{Diff: "diff"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.OverallStatus() != StatusApproved {
		t.Errorf("status = %s, want approved", result.OverallStatus())
	}
	if len(result.RolesRun) != 3 {
		t.Errorf("expected 3 roles run, got %d (%v)", len(result.RolesRun), result.RolesRun)
	}
	if result.Label() != "REVIEWED" {
		t.Errorf("label = %q, want REVIEWED", result.Label())
	}
}

func TestEngine_ReviewDisabled_Skipped(t *testing.T) {
	t.Parallel()
	rev := NewFakeReviewer(FakeScript{})
	eng := New(Options{Reviewer: rev})

	over := policy.ProfileDefaults(policy.ProfileLocalReview)
	over.Review.AIReview = false
	over.Review.SecurityReview = policy.TriOff
	over.Review.ArchitectureReview = policy.TriOff
	res := resolveReview(t, policy.ProfileLocalReview, &over)

	result, err := eng.Run(context.Background(), RunInput{Policy: res})
	if err != nil {
		t.Fatal(err)
	}
	if result.OverallStatus() != StatusSkipped {
		t.Errorf("status = %s, want skipped", result.OverallStatus())
	}
	if result.IsReviewed() {
		t.Error("should not be reviewed")
	}
	if result.Label() != "NOT AI-REVIEWED" {
		t.Errorf("label = %q, want NOT AI-REVIEWED", result.Label())
	}
	if len(rev.Calls()) != 0 {
		t.Errorf("reviewer should not have been called, got %d calls", len(rev.Calls()))
	}
}

func TestEngine_BlockerFinding_ChangesRequested(t *testing.T) {
	t.Parallel()
	rev := NewFakeReviewer(FakeScript{
		PerRole: map[Role][]Finding{
			RoleSecurity: {
				{Role: RoleSecurity, Severity: SeverityBlocker, Title: "SQL injection", File: "db.go"},
			},
		},
	})
	eng := New(Options{Reviewer: rev})
	res := resolveReview(t, policy.ProfileLocalReview, nil)

	result, err := eng.Run(context.Background(), RunInput{Policy: res})
	if err != nil {
		t.Fatal(err)
	}
	if result.OverallStatus() != StatusChangesRequested {
		t.Errorf("status = %s, want changes_requested", result.OverallStatus())
	}
	if result.BlockerCount() != 1 {
		t.Errorf("blockers = %d, want 1", result.BlockerCount())
	}
}

func TestEngine_MajorFinding_ChangesRequested(t *testing.T) {
	t.Parallel()
	rev := NewFakeReviewer(FakeScript{
		PerRole: map[Role][]Finding{
			RoleCorrectness: {
				{Role: RoleCorrectness, Severity: SeverityMajor, Title: "Missing error handling"},
			},
		},
	})
	eng := New(Options{Reviewer: rev})
	res := resolveReview(t, policy.ProfileLocalReview, nil)

	result, _ := eng.Run(context.Background(), RunInput{Policy: res})
	if result.OverallStatus() != StatusChangesRequested {
		t.Errorf("status = %s, want changes_requested", result.OverallStatus())
	}
	if result.MajorCount() != 1 {
		t.Errorf("majors = %d, want 1", result.MajorCount())
	}
}

func TestEngine_MandatoryCannotBeDisabled(t *testing.T) {
	t.Parallel()
	rev := NewFakeReviewer(FakeScript{})
	eng := New(Options{Reviewer: rev})

	proj := policy.NewProject(policy.ProfileLocalReview)
	proj.Security.Mandatory = policy.MandatoryChecks{
		AIReview:           true,
		SecurityReview:     true,
		ArchitectureReview: true,
	}
	over := proj.Pipeline
	over.Review.AIReview = false
	over.Review.SecurityReview = policy.TriOff
	over.Review.ArchitectureReview = policy.TriOff

	res, _ := policy.Resolve(proj, policy.TaskContext{Override: &over})

	result, err := eng.Run(context.Background(), RunInput{Policy: res})
	if err != nil {
		t.Fatal(err)
	}
	// All three roles should have run because the mandatory enforcement
	// restored them despite the override.
	for _, role := range []Role{RoleCorrectness, RoleArchitecture, RoleSecurity} {
		found := false
		for _, r := range result.RolesRun {
			if r == role {
				found = true
			}
		}
		if !found {
			t.Errorf("mandatory role %s should have run despite override (AC-29)", role)
		}
	}
}

func TestEngine_PartialReview(t *testing.T) {
	t.Parallel()
	rev := NewFakeReviewer(FakeScript{})
	eng := New(Options{Reviewer: rev})

	over := policy.ProfileDefaults(policy.ProfileLocalReview)
	over.Review.AIReview = true
	over.Review.SecurityReview = policy.TriOff
	over.Review.ArchitectureReview = policy.TriOff
	res := resolveReview(t, policy.ProfileLocalReview, &over)

	result, err := eng.Run(context.Background(), RunInput{Policy: res})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.RolesRun) != 1 || result.RolesRun[0] != RoleCorrectness {
		t.Errorf("only correctness should run, got %v", result.RolesRun)
	}
	if len(result.RolesSkipped) != 2 {
		t.Errorf("expected 2 skipped roles, got %d", len(result.RolesSkipped))
	}
}

func TestFinding_SeverityHelpers(t *testing.T) {
	t.Parallel()
	blocker := Finding{Severity: SeverityBlocker}
	major := Finding{Severity: SeverityMajor}
	minor := Finding{Severity: SeverityMinor}
	if !blocker.IsBlocker() {
		t.Error("blocker should be blocker")
	}
	if !major.IsMajorOrWorse() {
		t.Error("major should be major or worse")
	}
	if !blocker.IsMajorOrWorse() {
		t.Error("blocker should be major or worse")
	}
	if minor.IsMajorOrWorse() {
		t.Error("minor should not be major or worse")
	}
}
