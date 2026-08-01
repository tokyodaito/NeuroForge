package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"neuroforge/internal/pipeline"
	"neuroforge/internal/review"
	"neuroforge/internal/transport"
)

// Phase E, scenarios 6–9: provider failure matrix, verify→repair→success,
// repair exhaustion and review rejection paths.

// TestPipelineFault_ProviderFailureMatrix drives the deterministic fake
// engine's failure scenarios through the full pipeline and asserts the
// failure category lands on the run and the run reaches a failed terminal
// state — never completed, never a false success.
func TestPipelineFault_ProviderFailureMatrix(t *testing.T) {
	cases := []struct {
		name         string
		model        string
		timeoutSecs  int64
		wantState    string
		wantCategory string
		wantClass    string
		wantOutcome  string
	}{
		{
			name:         "AgentCrash",
			model:        "fake/crash",
			wantState:    "failed",
			wantCategory: string(pipeline.FailureAgentUnavailable),
			wantClass:    "AGENT_UNAVAILABLE",
			wantOutcome:  "failed",
		},
		{
			name:         "AuthFailure",
			model:        "fake/auth-failure",
			wantState:    "failed",
			wantCategory: string(pipeline.FailureAgentAuthUnavailable),
			wantClass:    "PROVIDER_AUTH",
			wantOutcome:  "failed",
		},
		{
			name:         "Timeout",
			model:        "fake/timeout",
			timeoutSecs:  1,
			wantState:    "failed",
			wantCategory: string(pipeline.FailureProviderTimeout),
			wantClass:    "TIMEOUT",
			wantOutcome:  "timed-out",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newFaultEnv(t, faultDeps{})
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()

			dto, err := env.svc.RunPipeline(ctx, transport.PipelineRunRequest{
				ProjectID:      env.projID,
				Description:    "provider failure matrix: " + tc.name,
				Engine:         "fake",
				Model:          tc.model,
				TimeoutSeconds: tc.timeoutSecs,
			})
			if err != nil {
				t.Fatalf("RunPipeline: %v", err)
			}
			if dto.RunState != tc.wantState {
				t.Errorf("run_state = %s, want %s (failure %s: %s)", dto.RunState, tc.wantState, dto.FailureCategory, dto.FailureReason)
			}
			if dto.RunState == "completed" {
				t.Fatal("failed provider scenario reported as completed (false success)")
			}
			if dto.FailureCategory != tc.wantCategory {
				t.Errorf("failure_category = %q, want %q", dto.FailureCategory, tc.wantCategory)
			}
			if dto.ErrorClass != tc.wantClass {
				t.Errorf("error_class = %q, want %q", dto.ErrorClass, tc.wantClass)
			}
			if dto.Outcome != tc.wantOutcome {
				t.Errorf("outcome = %q, want %q", dto.Outcome, tc.wantOutcome)
			}
			// The execute stage carries the failure; no finalize ever ran.
			failed := stageRecords(dto, "execute", "failed")
			if len(failed) != 1 || failed[0].FailureCategory != tc.wantCategory {
				t.Errorf("execute failed records = %+v, want one with category %s", failed, tc.wantCategory)
			}
			if n := len(stageRecords(dto, "finalize", "completed")); n != 0 {
				t.Errorf("finalize ran on a failed run (%d records)", n)
			}
			// No result ref may exist for a failed run.
			ref := "refs/heads/forge/result/" + dto.TaskID
			if out, gerr := faultGitCombined(env.repo, "rev-parse", "--verify", ref); gerr == nil && strings.TrimSpace(out) != "" {
				t.Errorf("result ref %s exists for a failed run", ref)
			}
			// The failure is durable and honest after a restart: still failed,
			// never resumed.
			env.restart(t, faultDeps{})
			env.reconcilePipeline(t)
			env.svc.ResumeActiveRuns(ctx)
			time.Sleep(200 * time.Millisecond)
			st := env.status(t, dto.TaskID)
			if st.RunState != tc.wantState {
				t.Errorf("run_state after restart = %s, want %s (terminal failures must not resume)", st.RunState, tc.wantState)
			}
		})
	}
}

// TestPipelineFault_MalformedReviewOutput_InvalidAgentOutput forces the
// reviewer agent to emit unparseable output and asserts the run fails with
// invalid_agent_output — unparseable review output must never be silently
// converted into an approval.
func TestPipelineFault_MalformedReviewOutput_InvalidAgentOutput(t *testing.T) {
	// A real AgentReviewer (the production review path) whose agent run
	// returns garbage: parseFindings yields ErrUnparseableReview.
	reviewer := review.NewAgentReviewer(func(_ context.Context, _ string) (string, error) {
		return "I cannot review this. No JSON here at all.", nil
	}, review.AgentReviewerOptions{})
	env := newFaultEnv(t, faultDeps{reviewer: reviewer})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	dto, err := env.svc.RunPipeline(ctx, transport.PipelineRunRequest{
		ProjectID:   env.projID,
		Description: "malformed review output",
		Engine:      "fake",
		Model:       "fake/write-commit",
	})
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if dto.RunState != "failed" {
		t.Fatalf("run_state = %s, want failed", dto.RunState)
	}
	if dto.FailureCategory != string(pipeline.FailureInvalidAgentOutput) {
		t.Errorf("failure_category = %q, want invalid_agent_output", dto.FailureCategory)
	}
	if dto.ErrorClass != "INVALID_AGENT_OUTPUT" {
		t.Errorf("error_class = %q, want INVALID_AGENT_OUTPUT", dto.ErrorClass)
	}
	if n := len(stageRecords(dto, "finalize", "completed")); n != 0 {
		t.Errorf("finalize ran despite unparseable review output")
	}
}

// TestPipelineFault_VerifyFail_RepairFixes_Completes scripts the agent to
// write gofmt-dirty code first and the fix on the repair attempt. Asserts the
// verify failure is recorded, the repair stage runs once, verification
// re-runs and passes, and the run completes with a commit.
func TestPipelineFault_VerifyFail_RepairFixes_Completes(t *testing.T) {
	adapter := newScriptedCodingAdapter(perCallBehavior(
		writeCommitBehavior(map[string]string{"helper.go": unformattedGo}),
		writeCommitBehavior(map[string]string{"helper.go": formattedGo}),
	))
	env := newFaultEnv(t, faultDeps{adapter: adapter})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	dto, err := env.svc.RunPipeline(ctx, transport.PipelineRunRequest{
		ProjectID:         env.projID,
		Description:       "verify fails, repair fixes",
		Engine:            "fake",
		Model:             "fake/standard",
		MaxRepairAttempts: 2,
	})
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if dto.RunState != "completed" {
		t.Fatalf("run_state = %s (failure %s: %s), want completed", dto.RunState, dto.FailureCategory, dto.FailureReason)
	}
	if dto.Outcome != "completed-with-commit" {
		t.Errorf("outcome = %q, want completed-with-commit", dto.Outcome)
	}

	// The first verify failed as static analysis (gofmt); exactly one repair
	// attempt ran; verification re-ran at the bumped attempt and passed.
	verifyFailed := stageRecords(dto, "verify", "failed")
	if len(verifyFailed) != 1 || verifyFailed[0].FailureCategory != string(pipeline.FailureStaticAnalysis) {
		t.Errorf("verify failed records = %+v, want one static_analysis_failure", verifyFailed)
	}
	if n := len(stageRecords(dto, "repair", "completed")); n != 1 {
		t.Errorf("repair completed records = %d, want 1", n)
	}
	verifyDone := stageRecords(dto, "verify", "completed")
	if len(verifyDone) != 1 || verifyDone[0].Attempt != 2 {
		t.Errorf("verify completed records = %+v, want one at attempt 2", verifyDone)
	}
	// The agent ran exactly two coding runs: the broken execute + the fixing
	// repair (review prompts are not coding runs).
	if n := adapter.codingCalls(); n != 2 {
		t.Errorf("coding agent calls = %d, want 2 (execute + repair)", n)
	}
	ref := "refs/heads/forge/result/" + dto.TaskID
	if sha := strings.TrimSpace(faultGitOut(t, env.repo, "rev-parse", "--verify", ref)); sha == "" {
		t.Errorf("result ref %s missing", ref)
	}
}

// TestPipelineFault_RepairExhaustion scripts the agent to ALWAYS write
// gofmt-dirty code. With max_repair_attempts=2 the run must end
// repair_exhausted with the verify failure category — never failed-as-success
// and never silently completed.
func TestPipelineFault_RepairExhaustion(t *testing.T) {
	adapter := newScriptedCodingAdapter(
		writeCommitBehavior(map[string]string{"helper.go": unformattedGo}))
	env := newFaultEnv(t, faultDeps{adapter: adapter})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	dto, err := env.svc.RunPipeline(ctx, transport.PipelineRunRequest{
		ProjectID:         env.projID,
		Description:       "repair never converges",
		Engine:            "fake",
		Model:             "fake/standard",
		MaxRepairAttempts: 2,
	})
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if dto.RunState != "repair_exhausted" {
		t.Fatalf("run_state = %s, want repair_exhausted", dto.RunState)
	}
	if dto.RunState == "completed" || dto.Outcome == "completed-with-commit" {
		t.Fatal("unfixable run reported as success")
	}
	if dto.FailureCategory != string(pipeline.FailureStaticAnalysis) {
		t.Errorf("failure_category = %q, want static_analysis_failure", dto.FailureCategory)
	}
	if dto.FailureReason == "" {
		t.Errorf("failure_reason empty: repair_exhausted must carry evidence")
	}
	// Exactly two repair attempts were consumed; verify failed once per
	// attempt plus the initial one (attempts 1..3); no review/finalize ran.
	if n := len(stageRecords(dto, "repair", "completed")); n != 2 {
		t.Errorf("repair completed records = %d, want 2", n)
	}
	if n := len(stageRecords(dto, "verify", "failed")); n != 3 {
		t.Errorf("verify failed records = %d, want 3 (initial + 2 repairs)", n)
	}
	if n := len(stageRecords(dto, "review", "completed")); n != 0 {
		t.Errorf("review ran on an unfixable run")
	}
	if n := len(stageRecords(dto, "finalize", "completed")); n != 0 {
		t.Errorf("finalize ran on an unfixable run")
	}
	// No result ref for a run that never passed verification.
	ref := "refs/heads/forge/result/" + dto.TaskID
	if out, gerr := faultGitCombined(env.repo, "rev-parse", "--verify", ref); gerr == nil && strings.TrimSpace(out) != "" {
		t.Errorf("result ref %s exists for a repair_exhausted run", ref)
	}
	// Terminal: restart recovery must not resume it.
	env.restart(t, faultDeps{adapter: adapter})
	env.reconcilePipeline(t)
	env.svc.ResumeActiveRuns(ctx)
	time.Sleep(200 * time.Millisecond)
	if st := env.status(t, dto.TaskID); st.RunState != "repair_exhausted" {
		t.Errorf("run_state after restart = %s, want repair_exhausted", st.RunState)
	}
}

// TestPipelineFault_ReviewRejection_RepairFixes_Completes forces the reviewer
// to reject the first review pass (and the repair-stage re-derivation), then
// approve. Asserts review_rejection routes to repair and the run completes
// once the rejection clears.
func TestPipelineFault_ReviewRejection_RepairFixes_Completes(t *testing.T) {
	// 3 roles per pass: pass 1 (calls 1–3) rejects, the repair re-derivation
	// (calls 4–6) still sees the findings, pass 2 (calls 7–9) approves.
	reviewer := &flipReviewer{rejectFirst: 6}
	env := newFaultEnv(t, faultDeps{reviewer: reviewer})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	dto, err := env.svc.RunPipeline(ctx, transport.PipelineRunRequest{
		ProjectID:         env.projID,
		Description:       "review rejects then approves",
		Engine:            "fake",
		Model:             "fake/write-commit",
		MaxRepairAttempts: 2,
	})
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if dto.RunState != "completed" {
		t.Fatalf("run_state = %s (failure %s: %s), want completed", dto.RunState, dto.FailureCategory, dto.FailureReason)
	}
	reviewFailed := stageRecords(dto, "review", "failed")
	if len(reviewFailed) != 1 || reviewFailed[0].FailureCategory != string(pipeline.FailureReviewRejection) {
		t.Errorf("review failed records = %+v, want one review_rejection", reviewFailed)
	}
	if n := len(stageRecords(dto, "repair", "completed")); n != 1 {
		t.Errorf("repair completed records = %d, want 1", n)
	}
	if n := len(stageRecords(dto, "review", "completed")); n != 1 {
		t.Errorf("review completed records = %d, want 1 (second pass approved)", n)
	}
	if n := reviewer.reviewCalls(); n < 9 {
		t.Errorf("reviewer calls = %d, want >= 9 (3 roles × reject + re-derive + approve)", n)
	}
}

// TestPipelineFault_ReviewRejection_ExhaustsRepair forces the reviewer to
// reject forever. With max_repair_attempts=1 the run must end
// repair_exhausted with category review_rejection.
func TestPipelineFault_ReviewRejection_ExhaustsRepair(t *testing.T) {
	reviewer := &flipReviewer{rejectFirst: 1 << 30}
	env := newFaultEnv(t, faultDeps{reviewer: reviewer})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	dto, err := env.svc.RunPipeline(ctx, transport.PipelineRunRequest{
		ProjectID:         env.projID,
		Description:       "review always rejects",
		Engine:            "fake",
		Model:             "fake/write-commit",
		MaxRepairAttempts: 1,
	})
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if dto.RunState != "repair_exhausted" {
		t.Fatalf("run_state = %s, want repair_exhausted", dto.RunState)
	}
	if dto.FailureCategory != string(pipeline.FailureReviewRejection) {
		t.Errorf("failure_category = %q, want review_rejection", dto.FailureCategory)
	}
	if n := len(stageRecords(dto, "review", "failed")); n != 2 {
		t.Errorf("review failed records = %d, want 2 (initial + after repair)", n)
	}
	if n := len(stageRecords(dto, "repair", "completed")); n != 1 {
		t.Errorf("repair completed records = %d, want 1", n)
	}
	if n := len(stageRecords(dto, "finalize", "completed")); n != 0 {
		t.Errorf("finalize ran on a rejected run")
	}
	ref := "refs/heads/forge/result/" + dto.TaskID
	if out, gerr := faultGitCombined(env.repo, "rev-parse", "--verify", ref); gerr == nil && strings.TrimSpace(out) != "" {
		t.Errorf("result ref %s exists for a repair_exhausted run", ref)
	}
}
