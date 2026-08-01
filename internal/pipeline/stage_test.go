package pipeline_test

import (
	"testing"

	"neuroforge/internal/pipeline"
)

func TestCanTransitionStage_Legal(t *testing.T) {
	legal := []struct{ from, to pipeline.Stage }{
		{pipeline.StageCompile, pipeline.StagePlan},
		{pipeline.StagePlan, pipeline.StageReady},
		{pipeline.StageReady, pipeline.StageExecute},
		{pipeline.StageExecute, pipeline.StageVerify},
		{pipeline.StageVerify, pipeline.StageReview},
		{pipeline.StageVerify, pipeline.StageRepair},
		{pipeline.StageReview, pipeline.StageFinalize},
		{pipeline.StageReview, pipeline.StageRepair},
		{pipeline.StageRepair, pipeline.StageExecute},
	}
	for _, tr := range legal {
		if !pipeline.CanTransitionStage(pipeline.RunActive, tr.from, tr.to) {
			t.Errorf("CanTransitionStage(active, %s, %s) = false, want true", tr.from, tr.to)
		}
	}
}

func TestCanTransitionStage_Illegal(t *testing.T) {
	illegal := []struct {
		state    pipeline.RunState
		from, to pipeline.Stage
	}{
		{pipeline.RunActive, pipeline.StageCompile, pipeline.StageExecute},         // skips stages
		{pipeline.RunActive, pipeline.StagePlan, pipeline.StageFinalize},           // skips stages
		{pipeline.RunActive, pipeline.StageVerify, pipeline.StageFinalize},         // must pass review
		{pipeline.RunActive, pipeline.StageVerify, pipeline.StageExecute},          // no direct retry
		{pipeline.RunActive, pipeline.StageRepair, pipeline.StageVerify},           // must re-execute first
		{pipeline.RunActive, pipeline.StageExecute, pipeline.StageReady},           // no backwards move
		{pipeline.RunActive, pipeline.StageFinalize, pipeline.StageReady},          // finalize is the last stage
		{pipeline.RunActive, pipeline.StageCompile, pipeline.StageCompile},         // self is a store-level re-entry, not a transition
		{pipeline.RunCompleted, pipeline.StageFinalize, pipeline.StageReady},       // terminal
		{pipeline.RunFailed, pipeline.StageExecute, pipeline.StageVerify},          // terminal
		{pipeline.RunCancelled, pipeline.StageCompile, pipeline.StagePlan},         // terminal
		{pipeline.RunRepairExhausted, pipeline.StageRepair, pipeline.StageExecute}, // terminal
	}
	for _, tr := range illegal {
		if pipeline.CanTransitionStage(tr.state, tr.from, tr.to) {
			t.Errorf("CanTransitionStage(%s, %s, %s) = true, want false", tr.state, tr.from, tr.to)
		}
	}
}

func TestCanTransitionStage_WaitStates(t *testing.T) {
	for _, state := range []pipeline.RunState{pipeline.RunWaitingQuota, pipeline.RunBlocked} {
		if !pipeline.CanTransitionStage(state, pipeline.StageExecute, pipeline.StageReady) {
			t.Errorf("CanTransitionStage(%s, execute, ready) = false, want true (resume)", state)
		}
		if pipeline.CanTransitionStage(state, pipeline.StageExecute, pipeline.StageVerify) {
			t.Errorf("CanTransitionStage(%s, execute, verify) = true, want false", state)
		}
		if pipeline.CanTransitionStage(state, pipeline.StageCompile, pipeline.StagePlan) {
			t.Errorf("CanTransitionStage(%s, compile, plan) = true, want false", state)
		}
	}
}

func TestCanTransitionRunState(t *testing.T) {
	cases := []struct {
		from, to pipeline.RunState
		want     bool
	}{
		{pipeline.RunActive, pipeline.RunWaitingQuota, true},
		{pipeline.RunActive, pipeline.RunBlocked, true},
		{pipeline.RunActive, pipeline.RunFailed, true},
		{pipeline.RunActive, pipeline.RunCancelled, true},
		{pipeline.RunActive, pipeline.RunRepairExhausted, true},
		{pipeline.RunActive, pipeline.RunCompleted, true},
		{pipeline.RunWaitingQuota, pipeline.RunActive, true},
		{pipeline.RunWaitingQuota, pipeline.RunCancelled, true},
		{pipeline.RunWaitingQuota, pipeline.RunFailed, true},
		{pipeline.RunWaitingQuota, pipeline.RunCompleted, false},
		{pipeline.RunWaitingQuota, pipeline.RunBlocked, false},
		{pipeline.RunBlocked, pipeline.RunActive, true},
		{pipeline.RunBlocked, pipeline.RunCancelled, true},
		{pipeline.RunBlocked, pipeline.RunFailed, true},
		{pipeline.RunBlocked, pipeline.RunWaitingQuota, false},
		{pipeline.RunActive, pipeline.RunActive, false},
		// Terminal states have no exits.
		{pipeline.RunCompleted, pipeline.RunActive, false},
		{pipeline.RunFailed, pipeline.RunActive, false},
		{pipeline.RunCancelled, pipeline.RunActive, false},
		{pipeline.RunRepairExhausted, pipeline.RunActive, false},
		{pipeline.RunRepairExhausted, pipeline.RunFailed, false},
	}
	for _, c := range cases {
		if got := pipeline.CanTransitionRunState(c.from, c.to); got != c.want {
			t.Errorf("CanTransitionRunState(%s, %s) = %v, want %v", c.from, c.to, got, c.want)
		}
	}
}

func TestIsValidFailureCategory(t *testing.T) {
	known := []pipeline.FailureCategory{
		pipeline.FailureCompile, pipeline.FailureTest, pipeline.FailureStaticAnalysis,
		pipeline.FailureReviewRejection, pipeline.FailureAgentUnavailable,
		pipeline.FailureAgentAuthUnavailable, pipeline.FailureProviderTimeout,
		pipeline.FailureQuotaExceeded, pipeline.FailureRateLimited,
		pipeline.FailureInvalidAgentOutput, pipeline.FailureNoCodeChanges,
		pipeline.FailureWorktree, pipeline.FailureGit, pipeline.FailureDatabase,
		pipeline.FailureLeaseLost, pipeline.FailureCancelled,
		pipeline.FailurePolicyRejection, pipeline.FailureInvariantViolation,
		pipeline.FailureInterrupted,
	}
	if len(known) != 19 {
		t.Fatalf("taxonomy has %d categories, want 19", len(known))
	}
	for _, c := range known {
		if !pipeline.IsValidFailureCategory(c) {
			t.Errorf("IsValidFailureCategory(%q) = false, want true", c)
		}
	}
	for _, c := range []pipeline.FailureCategory{"", "unknown", "compile-failure", "OOM"} {
		if pipeline.IsValidFailureCategory(c) {
			t.Errorf("IsValidFailureCategory(%q) = true, want false", c)
		}
	}
}

func TestRunStateClassification(t *testing.T) {
	for _, s := range []pipeline.RunState{
		pipeline.RunCompleted, pipeline.RunFailed, pipeline.RunCancelled, pipeline.RunRepairExhausted,
	} {
		if !pipeline.IsTerminalRunState(s) {
			t.Errorf("IsTerminalRunState(%s) = false, want true", s)
		}
		if pipeline.IsWaitState(s) {
			t.Errorf("IsWaitState(%s) = true, want false", s)
		}
	}
	for _, s := range []pipeline.RunState{pipeline.RunWaitingQuota, pipeline.RunBlocked} {
		if pipeline.IsTerminalRunState(s) {
			t.Errorf("IsTerminalRunState(%s) = true, want false", s)
		}
		if !pipeline.IsWaitState(s) {
			t.Errorf("IsWaitState(%s) = false, want true", s)
		}
	}
	if pipeline.IsTerminalRunState(pipeline.RunActive) || pipeline.IsWaitState(pipeline.RunActive) {
		t.Error("active must be neither terminal nor wait")
	}
}
