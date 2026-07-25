package merge

import (
	"testing"

	"neuroforge/internal/evidence"
	"neuroforge/internal/policy"
	"neuroforge/internal/review"
	"neuroforge/internal/testengine"
)

func resolvePolicy(t *testing.T, profile policy.Profile, override *policy.Pipeline) policy.Resolved {
	t.Helper()
	proj := policy.NewProject(profile)
	res, vs := policy.Resolve(proj, policy.TaskContext{Override: override})
	if policy.Blocks(vs) {
		t.Logf("note: resolve produced blocks: %+v", vs)
	}
	return res
}

func passingInput(policy_ policy.Resolved) Input {
	return Input{
		Policy:                     policy_,
		SpecificationLocked:        true,
		ScopeValid:                 true,
		RequiredChecksPassed:       true,
		AcceptanceEvidenceComplete: true,
		BlockerFindings:            0,
		MajorFindings:              0,
		TargetAllowed:              true,
		BranchCurrent:              true,
		BudgetPolicySatisfied:      true,
		VisualPolicySatisfied:      true,
		Evidence: evidence.Set{Items: []evidence.Evidence{
			{CriterionID: "AC-1", Status: "passed"},
		}},
		TestSummary: testengine.Summary{
			TestsWereRun: true,
			Results:      []testengine.Result{{Level: testengine.LevelModule, Status: testengine.StatusPassed}},
		},
		ReviewResult: review.Result{RolesRun: []review.Role{review.RoleCorrectness}},
	}
}

func TestDecide_AllGatesPass_LocalReview_AllowsLocalResult(t *testing.T) {
	t.Parallel()
	res := resolvePolicy(t, policy.ProfileLocalReview, nil)
	in := passingInput(res)
	r := Decide(in)
	if r.Decision != DecisionAllowLocalResult {
		t.Errorf("decision = %s, want ALLOW_LOCAL_RESULT", r.Decision)
	}
}

func TestDecide_AllGatesPass_Autonomous_AllowsMerge(t *testing.T) {
	t.Parallel()
	res := resolvePolicy(t, policy.ProfileAutonomous, nil)
	in := passingInput(res)
	r := Decide(in)
	if r.Decision != DecisionAllowMerge {
		t.Errorf("decision = %s, want ALLOW_MERGE", r.Decision)
	}
}

func TestDecide_AllGatesPass_RemoteReview_AllowsChangeRequest(t *testing.T) {
	t.Parallel()
	res := resolvePolicy(t, policy.ProfileRemoteReview, nil)
	in := passingInput(res)
	r := Decide(in)
	if r.Decision != DecisionAllowChangeRequest {
		t.Errorf("decision = %s, want ALLOW_CHANGE_REQUEST", r.Decision)
	}
}

func TestDecide_BlockerFindings_RequireRepair(t *testing.T) {
	t.Parallel()
	res := resolvePolicy(t, policy.ProfileAutonomous, nil)
	in := passingInput(res)
	in.BlockerFindings = 1
	r := Decide(in)
	if r.Decision != DecisionRequireRepair {
		t.Errorf("decision = %s, want REQUIRE_REPAIR", r.Decision)
	}
}

func TestDecide_MajorFindings_RequireRepair(t *testing.T) {
	t.Parallel()
	res := resolvePolicy(t, policy.ProfileAutonomous, nil)
	in := passingInput(res)
	in.MajorFindings = 2
	r := Decide(in)
	if r.Decision != DecisionRequireRepair {
		t.Errorf("decision = %s, want REQUIRE_REPAIR", r.Decision)
	}
}

func TestDecide_BranchNotCurrent_RequireRebase(t *testing.T) {
	t.Parallel()
	res := resolvePolicy(t, policy.ProfileAutonomous, nil)
	in := passingInput(res)
	in.BranchCurrent = false
	r := Decide(in)
	if r.Decision != DecisionRequireRebase {
		t.Errorf("decision = %s, want REQUIRE_REBASE", r.Decision)
	}
}

func TestDecide_SpecNotLocked_PolicyBlocked(t *testing.T) {
	t.Parallel()
	res := resolvePolicy(t, policy.ProfileAutonomous, nil)
	in := passingInput(res)
	in.SpecificationLocked = false
	r := Decide(in)
	if r.Decision != DecisionPolicyBlocked {
		t.Errorf("decision = %s, want POLICY_BLOCKED", r.Decision)
	}
}

func TestDecide_ScopeInvalid_PolicyBlocked(t *testing.T) {
	t.Parallel()
	res := resolvePolicy(t, policy.ProfileAutonomous, nil)
	in := passingInput(res)
	in.ScopeValid = false
	r := Decide(in)
	if r.Decision != DecisionPolicyBlocked {
		t.Errorf("decision = %s, want POLICY_BLOCKED", r.Decision)
	}
}

func TestDecide_EvidenceIncomplete_PolicyBlocked(t *testing.T) {
	t.Parallel()
	res := resolvePolicy(t, policy.ProfileAutonomous, nil)
	in := passingInput(res)
	in.Evidence = evidence.Set{} // empty → incomplete
	r := Decide(in)
	if r.Decision != DecisionPolicyBlocked {
		t.Errorf("decision = %s, want POLICY_BLOCKED", r.Decision)
	}
}

func TestDecide_AC29_TestsRequiredForMerge_CannotBypassByOverride(t *testing.T) {
	t.Parallel()
	// AUTONOMOUS project with RequireForRemoteMerge. A task override tries to
	// disable ALL tests. The Governor must still block the merge because tests
	// are mandatory for merge (§24.5).
	proj := policy.NewProject(policy.ProfileAutonomous)
	over := proj.Pipeline
	over.Tests.Generate = false
	over.Tests.RunExisting = false
	over.Tests.RunGenerated = false
	// RequireForRemoteMerge stays true (project-level; not something the
	// override can turn off because it's not in the only-restrict merge set —
	// it's a merge requirement, not a capability toggle).

	res, _ := policy.Resolve(proj, policy.TaskContext{Override: &over})

	in := passingInput(res)
	// Tests were NOT run (the override disabled them).
	in.TestSummary = testengine.Summary{TestsWereRun: false}

	r := Decide(in)
	if r.Decision != DecisionPolicyBlocked {
		t.Errorf("decision = %s, want POLICY_BLOCKED (§24.5: override cannot bypass mandatory merge tests)", r.Decision)
	}
	// Verify the specific gate exists and failed.
	foundGate := false
	for _, g := range r.Gates {
		if g.Name == "tests_required_for_merge" && !g.Passed {
			foundGate = true
		}
	}
	if !foundGate {
		t.Error("expected tests_required_for_merge gate to be present and failing")
	}
}

func TestDecide_AC29_TestsPassDespiteOverride_AllowsMerge(t *testing.T) {
	t.Parallel()
	// Same setup, but tests DID run and pass (e.g. existing tests still ran).
	proj := policy.NewProject(policy.ProfileAutonomous)
	over := proj.Pipeline
	over.Tests.Generate = false // generation off, but existing tests still run

	res, _ := policy.Resolve(proj, policy.TaskContext{Override: &over})

	in := passingInput(res)
	// Existing tests ran and passed.
	in.TestSummary = testengine.Summary{
		TestsWereRun: true,
		Results:      []testengine.Result{{Level: testengine.LevelTargeted, Status: testengine.StatusPassed}},
	}
	r := Decide(in)
	if r.Decision != DecisionAllowMerge {
		t.Errorf("decision = %s, want ALLOW_MERGE (tests passed)", r.Decision)
	}
}

func TestDecide_BlockerReviewFinding_PolicyBlocked(t *testing.T) {
	t.Parallel()
	res := resolvePolicy(t, policy.ProfileAutonomous, nil)
	in := passingInput(res)
	in.ReviewResult = review.Result{
		RolesRun: []review.Role{review.RoleSecurity},
		Findings: []review.Finding{
			{Role: review.RoleSecurity, Severity: review.SeverityBlocker, Title: "SQLi"},
		},
	}
	// The blocker finding also sets the blocker_findings gate via the count.
	in.BlockerFindings = 1
	r := Decide(in)
	if r.Decision != DecisionRequireRepair {
		t.Errorf("decision = %s, want REQUIRE_REPAIR", r.Decision)
	}
}

func TestLocalResultLabels_AllOff(t *testing.T) {
	t.Parallel()
	res := resolvePolicy(t, policy.ProfileLocalReview, nil)
	in := passingInput(res)
	in.TestSummary = testengine.Summary{TestsWereRun: false}
	in.ReviewResult = review.Result{} // no roles run
	labels := LocalResultLabels(in)
	want := []string{"IMPLEMENTED", "NOT TESTED", "NOT REVIEWED", "LOCAL BRANCH ONLY"}
	for i, w := range want {
		if i >= len(labels) || labels[i] != w {
			t.Errorf("label[%d] = %q, want %q", i, safeGet(labels, i), w)
		}
	}
}

func TestLocalResultLabels_AllOn(t *testing.T) {
	t.Parallel()
	res := resolvePolicy(t, policy.ProfileLocalReview, nil)
	in := passingInput(res)
	labels := LocalResultLabels(in)
	want := []string{"IMPLEMENTED", "TESTED", "REVIEWED", "LOCAL BRANCH ONLY"}
	for i, w := range want {
		if i >= len(labels) || labels[i] != w {
			t.Errorf("label[%d] = %q, want %q", i, safeGet(labels, i), w)
		}
	}
}

func TestDecision_IsAllow(t *testing.T) {
	t.Parallel()
	allows := []Decision{DecisionAllowLocalResult, DecisionAllowPush, DecisionAllowChangeRequest, DecisionAllowMerge}
	for _, d := range allows {
		if !d.IsAllow() {
			t.Errorf("%s should be allow", d)
		}
	}
	denies := []Decision{DecisionRequireRebase, DecisionRequireRepair, DecisionPolicyBlocked, DecisionQuarantine}
	for _, d := range denies {
		if d.IsAllow() {
			t.Errorf("%s should not be allow", d)
		}
	}
}

func safeGet(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return ""
}
