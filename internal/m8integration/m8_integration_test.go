// Package m8integration contains the M8 integration tests that exercise the full
// configurable-tests-and-review pipeline composition (spec §5, §24, §25, §27, §28).
//
// These tests compose the M8 domain packages (policy → testengine → review →
// evidence → repair → merge) with deterministic fake agents (rule §36.5: no real
// paid models in CI). They cover all combinations of the main pipeline flags and
// verify the critical M8 invariants:
//
//   - test generation off ⇒ test paths forbidden (§24.2)
//   - task override cannot weaken mandatory project security policy (AC-29)
//   - local result may be NOT TESTED and NOT REVIEWED (§24.4/§25.1)
//   - automatic merge cannot bypass mandatory project checks (§24.5)
//   - pipeline status explicitly shows skipped stages
//   - repair loop resolves findings (§25/§22.5)
//   - verification evidence confidence is lowered when tests disabled (§27)
package m8integration

import (
	"context"
	"testing"

	"neuroforge/internal/evidence"
	"neuroforge/internal/merge"
	"neuroforge/internal/policy"
	"neuroforge/internal/repair"
	"neuroforge/internal/review"
	"neuroforge/internal/testengine"
)

// flagCombo is one combination of the main pipeline flags. Fields left zero use
// the profile defaults.
type flagCombo struct {
	name    string
	profile policy.Profile

	// Test flags (nil = use profile default).
	genTests     *bool
	modifyTests  *bool
	runExisting  *bool
	runGenerated *bool

	// Review flags.
	aiReview   *bool
	secReview  *policy.TriState
	archReview *policy.TriState

	// Delivery flags.
	push   *bool
	cr     *bool
	merge_ *bool

	// Whether tests/reviews are expected to run (scripted by the fakes).
	testsPass     bool
	reviewApprove bool

	// Expected merge-governor decision.
	wantDecision merge.Decision
	// Expected local-result labels (if local result).
	wantTested   bool
	wantReviewed bool
}

func boolp(v bool) *bool                      { return &v }
func trip(v policy.TriState) *policy.TriState { return &v }

func (c flagCombo) buildProject() policy.Project {
	proj := policy.NewProject(c.profile)
	if c.genTests != nil || c.modifyTests != nil || c.runExisting != nil ||
		c.runGenerated != nil || c.aiReview != nil || c.secReview != nil ||
		c.archReview != nil || c.push != nil || c.cr != nil || c.merge_ != nil {
		over := proj.Pipeline
		if c.genTests != nil {
			over.Tests.Generate = *c.genTests
		}
		if c.modifyTests != nil {
			over.Tests.ModifyExisting = *c.modifyTests
		}
		if c.runExisting != nil {
			over.Tests.RunExisting = *c.runExisting
		}
		if c.runGenerated != nil {
			over.Tests.RunGenerated = *c.runGenerated
		}
		if c.aiReview != nil {
			over.Review.AIReview = *c.aiReview
		}
		if c.secReview != nil {
			over.Review.SecurityReview = *c.secReview
		}
		if c.archReview != nil {
			over.Review.ArchitectureReview = *c.archReview
		}
		if c.push != nil {
			over.Git.Push = *c.push
		}
		if c.cr != nil {
			over.ChangeRequest.Create = *c.cr
		}
		if c.merge_ != nil {
			over.Merge = *c.merge_
		}
		// Use the override as the project pipeline directly (we're testing the
		// flag resolution, not the override clamp here).
		proj.Pipeline, _ = policy.Normalize(over)
	}
	return proj
}

// runPipeline executes the full M8 pipeline for a flag combination and returns
// the merge-governor decision, test summary, review result, and evidence.
func runPipeline(t *testing.T, c flagCombo) (merge.Result, testengine.Summary, review.Result, evidence.Set) {
	t.Helper()
	ctx := context.Background()
	proj := c.buildProject()
	res, vs := policy.Resolve(proj, policy.TaskContext{})
	if policy.Blocks(vs) {
		t.Logf("note: blocks present: %+v", vs)
	}

	// 1. Test engine.
	script := testengine.FakeScript{
		PerLevel: map[testengine.VerificationLevel]testengine.Result{
			testengine.LevelSyntax:  {Status: testengine.StatusPassed},
			testengine.LevelCompile: {Status: testengine.StatusPassed},
		},
	}
	if c.testsPass {
		script.PerLevel[testengine.LevelTargeted] = testengine.Result{Status: testengine.StatusPassed, Passed: 5}
		script.PerLevel[testengine.LevelModule] = testengine.Result{Status: testengine.StatusPassed, Passed: 5}
	} else {
		script.PerLevel[testengine.LevelTargeted] = testengine.Result{
			Status:   testengine.StatusFailed,
			Failed:   1,
			Failures: []testengine.TestFailure{{TestName: "TestFoo", Message: "assertion failed"}},
		}
	}
	testRunner := testengine.NewFakeRunner(script)
	testEng := testengine.New(testengine.Options{Runner: testRunner})
	testSummary, err := testEng.Verify(ctx, testengine.VerifyInput{
		Policy:       res,
		ChangedFiles: []policy.FileChange{{Path: "src/main.go"}},
		MaxLevel:     testengine.LevelModule,
	})
	if err != nil {
		t.Fatalf("test engine: %v", err)
	}

	// 2. Review engine.
	revFindings := []review.Finding{}
	if !c.reviewApprove {
		revFindings = append(revFindings, review.Finding{
			Role: review.RoleCorrectness, Severity: review.SeverityMajor,
			Title: "needs work",
		})
	}
	rev := review.NewFakeReviewer(review.FakeScript{DefaultFindings: revFindings})
	revEng := review.New(review.Options{Reviewer: rev})
	revResult, err := revEng.Run(ctx, review.RunInput{
		Policy: res,
		Req:    review.ReviewRequest{Diff: "diff"},
	})
	if err != nil {
		t.Fatalf("review engine: %v", err)
	}

	// 3. Evidence (§27).
	ev := evidence.Set{}
	ev.Add(evidence.Evidence{
		CriterionID: "AC-1", Status: "passed",
		Type:         evidence.EvidenceTest,
		Confidence:   evidence.ConfidenceHigh,
		TestsWereRun: testSummary.TestsWereRun && testSummary.OverallStatus() == testengine.StatusPassed,
	})
	if !testSummary.TestsWereRun {
		ev = ev.LowerForDisabledTests()
	}

	// 4. Merge Governor.
	govIn := merge.Input{
		Policy:                     res,
		SpecificationLocked:        true,
		ScopeValid:                 true,
		RequiredChecksPassed:       true,
		AcceptanceEvidenceComplete: true,
		BlockerFindings:            revResult.BlockerCount(),
		MajorFindings:              revResult.MajorCount(),
		TargetAllowed:              true,
		BranchCurrent:              true,
		BudgetPolicySatisfied:      true,
		VisualPolicySatisfied:      true,
		Evidence:                   ev,
		TestSummary:                testSummary,
		ReviewResult:               revResult,
	}
	govResult := merge.Decide(govIn)

	return govResult, testSummary, revResult, ev
}

// TestPipeline_FlagCombinations is the master table-driven test covering all
// main flag combinations (AC-11, AC-12, AC-13, AC-14).
func TestPipeline_FlagCombinations(t *testing.T) {
	cases := []flagCombo{
		{
			name:      "full-pipeline-autonomous-all-on",
			profile:   policy.ProfileAutonomous,
			testsPass: true, reviewApprove: true,
			wantDecision: merge.DecisionAllowMerge,
			wantTested:   true, wantReviewed: true,
		},
		{
			name:     "no-tests-no-review-local",
			profile:  policy.ProfileLocalReview,
			genTests: boolp(false), runExisting: boolp(false), runGenerated: boolp(false),
			aiReview: boolp(false), secReview: trip(policy.TriOff), archReview: trip(policy.TriOff),
			testsPass: false, reviewApprove: true, // approve doesn't matter; reviews are off
			wantDecision: merge.DecisionAllowLocalResult,
			wantTested:   false, wantReviewed: false,
		},
		{
			name:     "tests-on-review-off-local",
			profile:  policy.ProfileLocalReview,
			aiReview: boolp(false), secReview: trip(policy.TriOff), archReview: trip(policy.TriOff),
			testsPass: true, reviewApprove: false,
			wantDecision: merge.DecisionAllowLocalResult,
			wantTested:   true, wantReviewed: false,
		},
		{
			name:     "tests-off-review-on-local",
			profile:  policy.ProfileLocalReview,
			genTests: boolp(false), runExisting: boolp(false), runGenerated: boolp(false),
			testsPass: false, reviewApprove: true,
			wantDecision: merge.DecisionAllowLocalResult,
			wantTested:   false, wantReviewed: true,
		},
		{
			name:      "remote-review-push-only-no-merge",
			profile:   policy.ProfileRemoteReview,
			testsPass: true, reviewApprove: true,
			wantDecision: merge.DecisionAllowChangeRequest,
			wantTested:   true, wantReviewed: true,
		},
		{
			name:      "autonomous-tests-fail-blocks-merge",
			profile:   policy.ProfileAutonomous,
			testsPass: false, reviewApprove: true,
			wantDecision: merge.DecisionRequireRepair,
			wantTested:   false, wantReviewed: true,
		},
		{
			name:      "autonomous-review-rejects-blocks-merge",
			profile:   policy.ProfileAutonomous,
			testsPass: true, reviewApprove: false,
			wantDecision: merge.DecisionRequireRepair,
			wantTested:   true, wantReviewed: true,
		},
		{
			name:     "gen-off-run-generated-auto-disabled",
			profile:  policy.ProfileLocalReview,
			genTests: boolp(false),
			// runGenerated stays true in the override, but normalisation forces
			// it off (R7). Tests that were "run" are only existing tests.
			testsPass: true, reviewApprove: true,
			wantDecision: merge.DecisionAllowLocalResult,
			wantTested:   true, wantReviewed: true,
		},
		{
			name:     "only-security-review-on",
			profile:  policy.ProfileLocalReview,
			aiReview: boolp(false), archReview: trip(policy.TriOff),
			secReview: trip(policy.TriOn),
			testsPass: true, reviewApprove: true,
			wantDecision: merge.DecisionAllowLocalResult,
			wantTested:   true, wantReviewed: true,
		},
		{
			name:       "architecture-review-auto",
			profile:    policy.ProfileLocalReview,
			archReview: trip(policy.TriAuto),
			testsPass:  true, reviewApprove: true,
			wantDecision: merge.DecisionAllowLocalResult,
			wantTested:   true, wantReviewed: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			govResult, testSummary, revResult, _ := runPipeline(t, c)

			if govResult.Decision != c.wantDecision {
				t.Errorf("decision = %s, want %s (reason: %s)",
					govResult.Decision, c.wantDecision, govResult.Reason)
			}

			// Verify tested/reviewed labels match expectations.
			tested := testSummary.TestsWereRun && testSummary.OverallStatus() == testengine.StatusPassed
			if tested != c.wantTested {
				t.Errorf("tested = %v (ran=%v overall=%s), want %v",
					tested, testSummary.TestsWereRun, testSummary.OverallStatus(), c.wantTested)
			}

			reviewed := revResult.IsReviewed()
			if reviewed != c.wantReviewed {
				t.Errorf("reviewed = %v (roles=%v), want %v",
					reviewed, revResult.RolesRun, c.wantReviewed)
			}
		})
	}
}

// TestCritical_TestGenerationOff_TestPathsForbidden verifies §24.2: when test
// generation is disabled, test paths become forbidden.
func TestCritical_TestGenerationOff_TestPathsForbidden(t *testing.T) {
	t.Parallel()
	proj := policy.NewProject(policy.ProfileLocalReview)
	over := proj.Pipeline
	over.Tests.Generate = false
	proj.Pipeline, _ = policy.Normalize(over)
	res, _ := policy.Resolve(proj, policy.TaskContext{})

	sv := testengine.NewScopeValidator(res.Pipeline)

	// Test file rejected.
	err := sv.Validate([]policy.FileChange{{Path: "src/foo_test.go", IsNew: true}})
	if err == nil {
		t.Error("test file must be rejected when generation is off (§24.2)")
	}

	// Non-test file accepted.
	err = sv.Validate([]policy.FileChange{{Path: "src/main.go"}})
	if err != nil {
		t.Errorf("non-test file should be accepted: %v", err)
	}

	// When generation is ON, test files are accepted.
	proj2 := policy.NewProject(policy.ProfileLocalReview)
	res2, _ := policy.Resolve(proj2, policy.TaskContext{})
	sv2 := testengine.NewScopeValidator(res2.Pipeline)
	if err := sv2.Validate([]policy.FileChange{{Path: "src/foo_test.go", IsNew: true}}); err != nil {
		t.Errorf("test file should be accepted when generation is on: %v", err)
	}
}

// TestCritical_OverrideCannotWeakenMandatoryPolicy verifies AC-29: a task
// override cannot disable mandatory project security policy.
func TestCritical_OverrideCannotWeakenMandatoryPolicy(t *testing.T) {
	t.Parallel()
	proj := policy.NewProject(policy.ProfileLocalReview)
	proj.Security.Mandatory = policy.MandatoryChecks{
		AIReview:           true,
		SecurityReview:     true,
		ArchitectureReview: true,
	}

	// Override tries to disable everything.
	over := proj.Pipeline
	over.Review.AIReview = false
	over.Review.SecurityReview = policy.TriOff
	over.Review.ArchitectureReview = policy.TriOff

	res, vs := policy.Resolve(proj, policy.TaskContext{Override: &over})

	// Mandatory checks restored.
	if !res.Pipeline.Review.AIReview {
		t.Error("mandatory AI review was disabled by override (AC-29)")
	}
	if res.Pipeline.Review.SecurityReview == policy.TriOff {
		t.Error("mandatory security review was disabled by override (AC-29)")
	}
	if res.Pipeline.Review.ArchitectureReview == policy.TriOff {
		t.Error("mandatory architecture review was disabled by override (AC-29)")
	}

	// The review engine would run all three roles despite the override.
	rev := review.NewFakeReviewer(review.FakeScript{})
	revEng := review.New(review.Options{Reviewer: rev})
	result, _ := revEng.Run(context.Background(), review.RunInput{Policy: res})
	if len(result.RolesRun) != 3 {
		t.Errorf("all 3 mandatory roles should run despite override, got %v", result.RolesRun)
	}

	// Violations were recorded.
	hasAC29 := false
	for _, v := range vs {
		if v.Rule == "ac29.mandatory.ai_review" || v.Rule == "ac29.mandatory.security_review" || v.Rule == "ac29.mandatory.architecture_review" {
			hasAC29 = true
		}
	}
	if !hasAC29 {
		t.Error("expected AC-29 mandatory violation(s) to be recorded")
	}
}

// TestCritical_AutomaticMergeCannotBypassMandatoryChecks verifies §24.5: a task
// override disabling tests cannot bypass the mandatory merge rule.
func TestCritical_AutomaticMergeCannotBypassMandatoryChecks(t *testing.T) {
	t.Parallel()
	proj := policy.NewProject(policy.ProfileAutonomous)

	// Override disables all tests.
	over := proj.Pipeline
	over.Tests.Generate = false
	over.Tests.RunExisting = false
	over.Tests.RunGenerated = false

	res, _ := policy.Resolve(proj, policy.TaskContext{Override: &over})

	// Tests were NOT run (override disabled them).
	govResult := merge.Decide(merge.Input{
		Policy:                     res,
		SpecificationLocked:        true,
		ScopeValid:                 true,
		RequiredChecksPassed:       true,
		AcceptanceEvidenceComplete: true,
		TargetAllowed:              true,
		BranchCurrent:              true,
		BudgetPolicySatisfied:      true,
		VisualPolicySatisfied:      true,
		Evidence: evidence.Set{Items: []evidence.Evidence{
			{CriterionID: "AC-1", Status: "passed"},
		}},
		TestSummary:  testengine.Summary{TestsWereRun: false},
		ReviewResult: review.Result{RolesRun: []review.Role{review.RoleCorrectness}},
	})

	if govResult.Decision != merge.DecisionPolicyBlocked {
		t.Errorf("decision = %s, want POLICY_BLOCKED (§24.5: override cannot bypass mandatory merge tests)",
			govResult.Decision)
	}

	// Verify the specific gate.
	found := false
	for _, g := range govResult.Gates {
		if g.Name == "tests_required_for_merge" && !g.Passed {
			found = true
		}
	}
	if !found {
		t.Error("expected tests_required_for_merge gate to fail")
	}
}

// TestCritical_PipelineStatusShowsSkippedStages verifies the pipeline status
// explicitly shows which stages are skipped.
func TestCritical_PipelineStatusShowsSkippedStages(t *testing.T) {
	t.Parallel()
	proj := policy.NewProject(policy.ProfileLocalReview)
	over := proj.Pipeline
	over.Tests.Generate = false
	over.Tests.RunExisting = false
	over.Tests.RunGenerated = false
	over.Review.AIReview = false
	over.Review.SecurityReview = policy.TriOff
	over.Review.ArchitectureReview = policy.TriOff
	proj.Pipeline, _ = policy.Normalize(over)

	res, _ := policy.Resolve(proj, policy.TaskContext{})
	status := res.StageStatus()

	// Must show skipped stages.
	s := status.String()
	for _, want := range []string{"test_generation", "run_tests", "ai_review", "skipped"} {
		if !contains(s, want) {
			t.Errorf("status string missing %q:\n%s", want, s)
		}
	}

	// Local result labels must show NOT TESTED and NOT REVIEWED.
	labels := status.LocalResultLabels()
	if !containsSlice(labels, "NOT TESTED") {
		t.Errorf("labels missing NOT TESTED: %v", labels)
	}
	if !containsSlice(labels, "NOT REVIEWED") {
		t.Errorf("labels missing NOT REVIEWED: %v", labels)
	}
}

// TestCritical_RepairLoop resolves test failures through a bounded loop.
func TestCritical_RepairLoop(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Initial findings: two test failures.
	initial := repair.FromTestFailures([]testengine.Result{
		{Failures: []testengine.TestFailure{
			{TestName: "TestA", Message: "fail"},
			{TestName: "TestB", Message: "fail"},
		}},
	})

	repairCount := 0
	loop := repair.New(repair.Options{
		MaxIterations: 3,
		Repair: func(_ context.Context, _ repair.RepairContext) error {
			repairCount++
			return nil
		},
		Verify: func(_ context.Context) ([]repair.Finding, error) {
			// First verify: one failure fixed, one remains. Second: all fixed.
			if repairCount == 1 {
				return repair.FromTestFailures([]testengine.Result{
					{Failures: []testengine.TestFailure{{TestName: "TestB", Message: "fail"}}},
				}), nil
			}
			return nil, nil
		},
	})

	out, err := loop.Run(ctx, initial, "diff", []string{"a.go", "b.go"})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Resolved {
		t.Error("repair loop should have resolved all findings")
	}
	if repairCount != 2 {
		t.Errorf("repair called %d times, want 2", repairCount)
	}
}

// TestCritical_EvidenceConfidenceLoweredWhenTestsDisabled verifies §27: when
// tests are disabled, evidence confidence is lowered.
func TestCritical_EvidenceConfidenceLoweredWhenTestsDisabled(t *testing.T) {
	t.Parallel()
	ev := evidence.Set{}
	ev.Add(evidence.Evidence{
		CriterionID: "AC-1", Status: "passed",
		Type: evidence.EvidenceTest, Confidence: evidence.ConfidenceHigh,
		TestsWereRun: false, // tests were NOT run
	})

	lowered := ev.LowerForDisabledTests()
	if lowered.AggregateConfidence() != evidence.ConfidenceLow {
		t.Errorf("confidence = %s, want low (§27)", lowered.AggregateConfidence())
	}
}

// TestCritical_IndependentPushPRMerge verifies AC-14: push, PR/MR and merge are
// independently switchable (subject to dependency rules).
func TestCritical_IndependentPushPRMerge(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		push, cr, mg bool
		// Expected: what the gate allows after normalisation.
		allowPush  bool
		allowCR    bool
		allowMerge bool
	}{
		{"all-off", false, false, false, false, false, false},
		{"push-only", true, false, false, true, false, false},
		{"push-and-cr", true, true, false, true, true, false},
		{"push-cr-merge", true, true, true, true, true, true},
		{"merge-without-push-forces-off", false, false, true, false, false, false}, // R2
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := policy.Pipeline{
				Git:           policy.GitConfig{Push: c.push},
				ChangeRequest: policy.ChangeRequestConfig{Create: c.cr},
				Merge:         c.mg,
			}
			normalised, _ := policy.Normalize(p)
			proj := policy.Project{
				Profile:  policy.ProfileCustom,
				Pipeline: normalised,
				Security: policy.Security{},
			}
			res, _ := policy.Resolve(proj, policy.TaskContext{})

			if d := res.Allows(policy.ActPush); d.Allow != c.allowPush {
				t.Errorf("push: allow=%v want %v (%s)", d.Allow, c.allowPush, d.Reason)
			}
			if d := res.Allows(policy.ActCreateChangeRequest); d.Allow != c.allowCR {
				t.Errorf("CR: allow=%v want %v (%s)", d.Allow, c.allowCR, d.Reason)
			}
			if d := res.Allows(policy.ActMerge); d.Allow != c.allowMerge {
				t.Errorf("merge: allow=%v want %v (%s)", d.Allow, c.allowMerge, d.Reason)
			}
		})
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func containsSlice(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
