package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"neuroforge/internal/pipeline"
	"neuroforge/internal/testengine"
	"neuroforge/internal/transport"
)

// The module level (`go test ./...`) can fail on timing-sensitive tests that
// pass on an idle machine. The verify handler must re-run exactly the failing
// packages ONCE instead of burning a repair cycle on infrastructure flakes.

// flakeModuleFailure is a package-attributed module-level test failure — the
// shape parseTestOutput produces for `--- FAIL: TestFlaky` + `FAIL <pkg>`.
func flakeModuleFailure() testengine.Result {
	return testengine.Result{
		Level:  testengine.LevelModule,
		Status: testengine.StatusFailed,
		Failed: 1,
		Failures: []testengine.TestFailure{{
			TestName: "TestFlaky",
			Package:  "fixture",
			Message:  "timing sensitive: deadline exceeded",
		}},
	}
}

// readVerifyEvidence loads the FIRST verify stage record with the given
// status ("completed"/"failed") and unmarshals its evidence artifact.
func readVerifyEvidence(t *testing.T, env *faultEnv, dto transport.PipelineRunResultDTO, status string) verifyEvidence {
	t.Helper()
	recs := stageRecords(dto, "verify", status)
	if len(recs) == 0 {
		t.Fatalf("no verify %s record (all: %+v)", status, dto.StageRecords)
	}
	ref := recs[0].EvidenceRef
	if !strings.HasPrefix(ref, "artifact:") {
		t.Fatalf("verify evidence ref = %q, want artifact ref", ref)
	}
	b, err := env.svc.artifacts.Read(strings.TrimPrefix(ref, "artifact:"))
	if err != nil {
		t.Fatalf("read evidence artifact: %v", err)
	}
	var ev verifyEvidence
	if err := json.Unmarshal(b, &ev); err != nil {
		t.Fatalf("unmarshal verify evidence: %v", err)
	}
	return ev
}

// TestPipelineVerify_FlakeRetryPasses: the module level fails once with a
// package-attributed (flake-shaped) failure and the single package re-run
// passes — the stage passes, no repair is burned, and the evidence carries a
// flake_retry record with the retried packages and the original failure
// messages.
func TestPipelineVerify_FlakeRetryPasses(t *testing.T) {
	runner := testengine.NewFakeRunner(testengine.FakeScript{
		PerLevel: map[testengine.VerificationLevel]testengine.Result{
			testengine.LevelModule: flakeModuleFailure(),
		},
		// RetryResult nil → the package re-run passes.
	})
	env := newFaultEnv(t, faultDeps{runner: runner})

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dto, err := env.svc.RunPipeline(ctx, transport.PipelineRunRequest{
		ProjectID:   env.projID,
		Description: "flake retry passes",
		Engine:      "fake",
		Model:       "fake/write-commit",
	})
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if dto.RunState != "completed" {
		t.Fatalf("run_state = %s, want completed (failure %s: %s)", dto.RunState, dto.FailureCategory, dto.FailureReason)
	}

	// Exactly one retry, packages-only: the module level ran once for the full
	// suite and once for the retry — never a second full-suite run.
	if got := runner.RetryCalls(); len(got) != 1 || len(got[0]) != 1 || got[0][0] != "fixture" {
		t.Fatalf("retry calls = %v, want exactly one retry of [fixture]", got)
	}
	if n := runner.CallCount(testengine.LevelModule); n != 2 {
		t.Fatalf("module-level runs = %d, want 2 (full suite once + package retry once)", n)
	}
	if n := len(stageRecords(dto, "repair", "")); n != 0 {
		t.Fatalf("repair ran %d times for a flake; want 0", n)
	}

	ev := readVerifyEvidence(t, env, dto, "completed")
	if ev.FlakeRetry == nil {
		t.Fatal("verify evidence has no flake_retry record")
	}
	if len(ev.FlakeRetry.Packages) != 1 || ev.FlakeRetry.Packages[0] != "fixture" {
		t.Errorf("flake_retry.packages = %v, want [fixture]", ev.FlakeRetry.Packages)
	}
	if !ev.FlakeRetry.Passed {
		t.Error("flake_retry.passed = false, want true")
	}
	if len(ev.FlakeRetry.OriginalFailures) != 1 ||
		!strings.Contains(ev.FlakeRetry.OriginalFailures[0], "timing sensitive") {
		t.Errorf("flake_retry.original_failures = %v, want the original failure message", ev.FlakeRetry.OriginalFailures)
	}
}

// TestPipelineVerify_FlakeRetryAlsoFails: the module level fails and the
// single package re-run fails too — the stage fails as before, with both
// attempts in the evidence.
func TestPipelineVerify_FlakeRetryAlsoFails(t *testing.T) {
	retryFail := testengine.Result{
		Status: testengine.StatusFailed,
		Failed: 1,
		Failures: []testengine.TestFailure{{
			TestName: "TestFlaky",
			Package:  "fixture",
			Message:  "still failing on re-run",
		}},
	}
	runner := testengine.NewFakeRunner(testengine.FakeScript{
		PerLevel: map[testengine.VerificationLevel]testengine.Result{
			testengine.LevelModule: flakeModuleFailure(),
		},
		RetryResult: &retryFail,
	})
	env := newFaultEnv(t, faultDeps{runner: runner})

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dto, err := env.svc.RunPipeline(ctx, transport.PipelineRunRequest{
		ProjectID:   env.projID,
		Description: "flake retry also fails",
		Engine:      "fake",
		Model:       "fake/write-commit",
	})
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if dto.RunState == "completed" {
		t.Fatal("run completed although both module attempt and package retry failed")
	}
	if dto.FailureCategory != string(pipeline.FailureTest) {
		t.Errorf("failure_category = %q, want %q", dto.FailureCategory, pipeline.FailureTest)
	}
	// Every verify attempt (the initial one plus repair-loop re-verifications)
	// gets exactly one retry, always packages-only — never the whole suite.
	retries := runner.RetryCalls()
	if len(retries) == 0 {
		t.Fatal("no package retry happened")
	}
	for _, call := range retries {
		if len(call) != 1 || call[0] != "fixture" {
			t.Fatalf("retry call = %v, want exactly [fixture] (packages-only)", call)
		}
	}

	ev := readVerifyEvidence(t, env, dto, "failed")
	if ev.FlakeRetry == nil {
		t.Fatal("verify evidence has no flake_retry record")
	}
	if ev.FlakeRetry.Passed {
		t.Error("flake_retry.passed = true, want false")
	}
	// Both attempts must be in the evidence: the original module failure and
	// the failed package re-run.
	var moduleResults int
	for _, r := range ev.Results {
		if r.Level == testengine.LevelModule {
			moduleResults++
		}
	}
	if moduleResults != 2 {
		t.Errorf("module-level results in evidence = %d, want 2 (both attempts)", moduleResults)
	}
}

// TestPipelineVerify_NoRetryWithoutPackageAttribution: a module-level failure
// whose failures carry no package attribution (e.g. a compile error inside
// `go test`) is not a flake candidate — no re-run happens.
func TestPipelineVerify_NoRetryWithoutPackageAttribution(t *testing.T) {
	runner := testengine.NewFakeRunner(testengine.FakeScript{
		PerLevel: map[testengine.VerificationLevel]testengine.Result{
			testengine.LevelModule: {
				Level:    testengine.LevelModule,
				Status:   testengine.StatusFailed,
				Failed:   1,
				Failures: []testengine.TestFailure{{Message: "module verification failed"}},
			},
		},
	})
	env := newFaultEnv(t, faultDeps{runner: runner})

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dto, err := env.svc.RunPipeline(ctx, transport.PipelineRunRequest{
		ProjectID:   env.projID,
		Description: "module failure without package attribution",
		Engine:      "fake",
		Model:       "fake/write-commit",
	})
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if dto.RunState == "completed" {
		t.Fatal("run completed although the module level failed")
	}
	if got := runner.RetryCalls(); len(got) != 0 {
		t.Fatalf("retry calls = %v, want none (no package attribution)", got)
	}
	ev := readVerifyEvidence(t, env, dto, "failed")
	if ev.FlakeRetry != nil {
		t.Errorf("flake_retry = %+v, want nil (no retry without attribution)", ev.FlakeRetry)
	}
}

// TestFlakeRetryPackages pins the eligibility rule: retry only when every
// failure is attributable to a concrete test package.
func TestFlakeRetryPackages(t *testing.T) {
	cases := []struct {
		name string
		res  testengine.Result
		want []string
	}{
		{
			name: "no failures",
			res:  testengine.Result{},
			want: nil,
		},
		{
			name: "single package",
			res: testengine.Result{Failures: []testengine.TestFailure{
				{TestName: "TestA", Package: "example.com/mod/pkg"},
			}},
			want: []string{"example.com/mod/pkg"},
		},
		{
			name: "multiple packages deduped and sorted",
			res: testengine.Result{Failures: []testengine.TestFailure{
				{TestName: "TestA", Package: "example.com/mod/b"},
				{TestName: "TestB", Package: "example.com/mod/a"},
				{TestName: "TestC", Package: "example.com/mod/b"},
			}},
			want: []string{"example.com/mod/a", "example.com/mod/b"},
		},
		{
			name: "go vet failure is not a flake",
			res: testengine.Result{Failures: []testengine.TestFailure{
				{Package: "go vet", Message: "static analysis: printf mismatch"},
			}},
			want: nil,
		},
		{
			name: "unattributed failure blocks retry",
			res: testengine.Result{Failures: []testengine.TestFailure{
				{TestName: "TestA", Package: "example.com/mod/a"},
				{Message: "module verification failed"},
			}},
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := flakeRetryPackages(tc.res)
			if len(got) != len(tc.want) {
				t.Fatalf("flakeRetryPackages = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("flakeRetryPackages = %v, want %v", got, tc.want)
				}
			}
		})
	}
}
