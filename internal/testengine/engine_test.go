package testengine

import (
	"context"
	"testing"

	"neuroforge/internal/policy"
)

func resolved(t *testing.T, profile policy.Profile, override *policy.Pipeline) policy.Resolved {
	t.Helper()
	proj := policy.NewProject(profile)
	res, vs := policy.Resolve(proj, policy.TaskContext{Override: override})
	if policy.Blocks(vs) {
		t.Logf("note: resolve produced blocks: %+v", vs)
	}
	return res
}

func TestEngine_VerifyTestsEnabled_AllPass(t *testing.T) {
	t.Parallel()
	runner := NewFakeRunner(FakeScript{Result: Result{Status: StatusPassed, Passed: 5}})
	eng := New(Options{Runner: runner})
	res := resolved(t, policy.ProfileLocalReview, nil)

	summary, err := eng.Verify(context.Background(), VerifyInput{
		Policy:       res,
		ChangedFiles: []policy.FileChange{{Path: "src/main.go"}},
		MaxLevel:     LevelModule,
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.OverallStatus() != StatusPassed {
		t.Errorf("overall = %s, want passed", summary.OverallStatus())
	}
	if summary.HighestCompletedLevel != LevelModule {
		t.Errorf("highest = %s, want module", summary.HighestCompletedLevel)
	}
	if !summary.TestsWereRun {
		t.Error("expected TestsWereRun=true")
	}
}

func TestEngine_VerifyTestsDisabled_SkipsTestLevels(t *testing.T) {
	t.Parallel()
	runner := NewFakeRunner(FakeScript{Result: Result{Status: StatusPassed}})
	eng := New(Options{Runner: runner})

	over := policy.ProfileDefaults(policy.ProfileLocalReview)
	over.Tests.Generate = false
	over.Tests.RunExisting = false
	over.Tests.RunGenerated = false
	res := resolved(t, policy.ProfileLocalReview, &over)

	summary, err := eng.Verify(context.Background(), VerifyInput{
		Policy:   res,
		MaxLevel: LevelFull,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Syntax + compile may run; test levels must be skipped.
	for _, r := range summary.Results {
		if r.Level >= LevelTargeted && r.Status != StatusSkipped {
			t.Errorf("level %s should be skipped when tests disabled, got %s", r.Level, r.Status)
		}
	}
	if summary.TestsWereRun {
		t.Error("TestsWereRun should be false when tests disabled")
	}
	if summary.OverallStatus() == StatusFailed {
		t.Error("should not fail when tests are just skipped")
	}
}

func TestEngine_VerifyLevelFails_StopsDeeper(t *testing.T) {
	t.Parallel()
	script := FakeScript{
		PerLevel: map[VerificationLevel]Result{
			LevelSyntax:   {Status: StatusPassed},
			LevelCompile:  {Status: StatusPassed},
			LevelTargeted: {Status: StatusFailed, Failed: 1, Failures: []TestFailure{{TestName: "TestFoo", Message: "assertion failed"}}},
		},
	}
	runner := NewFakeRunner(script)
	eng := New(Options{Runner: runner})
	res := resolved(t, policy.ProfileLocalReview, nil)

	summary, err := eng.Verify(context.Background(), VerifyInput{
		Policy:   res,
		MaxLevel: LevelFull,
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.OverallStatus() != StatusFailed {
		t.Fatalf("overall = %s, want failed", summary.OverallStatus())
	}
	if !summary.HasTestFailures() {
		t.Error("expected test failures")
	}
	// Module and full should be not_run.
	moduleRun := findResult(summary, LevelModule)
	if moduleRun == nil || moduleRun.Status != StatusNotRun {
		t.Errorf("module should be not_run after targeted failure")
	}
	// Full should not have been invoked.
	if runner.CallCount(LevelModule) != 0 {
		t.Error("module level should not have been called")
	}
}

func TestEngine_ProgressiveStopsAfterCompile(t *testing.T) {
	t.Parallel()
	script := FakeScript{
		PerLevel: map[VerificationLevel]Result{
			LevelSyntax:  {Status: StatusPassed},
			LevelCompile: {Status: StatusFailed},
		},
	}
	eng := New(Options{Runner: NewFakeRunner(script)})
	res := resolved(t, policy.ProfileLocalReview, nil)

	summary, _ := eng.Verify(context.Background(), VerifyInput{
		Policy:   res,
		MaxLevel: LevelFull,
	})
	if summary.OverallStatus() != StatusFailed {
		t.Fatalf("overall = %s, want failed", summary.OverallStatus())
	}
	if summary.TestsWereRun {
		t.Error("no tests should have run after compile failure")
	}
}

func TestScopeValidator_GenerateDisabled(t *testing.T) {
	t.Parallel()
	p := policy.Pipeline{Tests: policy.TestsConfig{Generate: false}}
	sv := NewScopeValidator(p)
	err := sv.Validate([]policy.FileChange{{Path: "foo_test.go", IsNew: true}})
	if err == nil {
		t.Fatal("expected scope violation for test path when generate disabled")
	}
	// Non-test path is fine.
	if err := sv.Validate([]policy.FileChange{{Path: "src/main.go"}}); err != nil {
		t.Errorf("non-test path should not violate: %v", err)
	}
}

func TestScopeValidator_GenerateEnabled(t *testing.T) {
	t.Parallel()
	p := policy.Pipeline{Tests: policy.TestsConfig{Generate: true, ModifyExisting: true}}
	sv := NewScopeValidator(p)
	if err := sv.Validate([]policy.FileChange{{Path: "foo_test.go", IsNew: true}}); err != nil {
		t.Errorf("new test should be allowed: %v", err)
	}
	if err := sv.Validate([]policy.FileChange{{Path: "foo_test.go", IsNew: false}}); err != nil {
		t.Errorf("modify existing test should be allowed: %v", err)
	}
}

func TestSummary_Empty(t *testing.T) {
	t.Parallel()
	s := Summary{}
	if s.OverallStatus() != StatusSkipped {
		t.Errorf("empty summary = %s, want skipped", s.OverallStatus())
	}
}

func findResult(s Summary, lvl VerificationLevel) *Result {
	for i := range s.Results {
		if s.Results[i].Level == lvl {
			return &s.Results[i]
		}
	}
	return nil
}
