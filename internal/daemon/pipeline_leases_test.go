package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
	"neuroforge/internal/pipeline"
	"neuroforge/internal/transport"
)

// scopedDescription compiles to a specification whose ProposedScope leases
// src/main.go, so the ready stage acquires a real path lease.
const scopedDescription = "Implement the requested change.\n\nScope:\n- src/main.go\n"

// TestPipelineLease_ReleasedAfterCompletedRun proves (H4) that the lease the
// ready stage claims is bounded and released when the run reaches a terminal
// state: a completed run holds no active leases.
func TestPipelineLease_ReleasedAfterCompletedRun(t *testing.T) {
	env := newFaultEnv(t, faultDeps{})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	dto, err := env.svc.RunPipeline(ctx, transport.PipelineRunRequest{
		ProjectID:   env.projID,
		Description: scopedDescription,
		Engine:      "fake",
		Model:       "fake/write-commit",
	})
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if dto.RunState != "completed" {
		t.Fatalf("run_state = %s (failure %s: %s), want completed", dto.RunState, dto.FailureCategory, dto.FailureReason)
	}
	// Sanity: the ready stage actually claimed a lease.
	claimed := false
	for _, r := range stageRecords(dto, "ready", "completed") {
		if strings.Contains(r.EvidenceRef, "claimed:1") {
			claimed = true
		}
	}
	if !claimed {
		t.Fatalf("ready stage did not claim a lease (records: %+v)", dto.StageRecords)
	}
	// The completed run must hold no active leases.
	active, err := env.leases.ListActiveByProject(ctx, env.projID)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Errorf("active leases after completed run: %+v, want none (release on terminal)", active)
	}
}

// TestPipelineLease_ReleasedAfterFailedRun proves the lease release also
// happens on a failed terminal run (agent failure), not just on completion.
func TestPipelineLease_ReleasedAfterFailedRun(t *testing.T) {
	adapter := newScriptedCodingAdapter(func(_ context.Context, _ int, _ protocol.AgentRunRequest, emit func(protocol.NormalizedEvent)) {
		emit(protocol.NormalizedEvent{Type: protocol.EventRunStarted})
		emitFailure(emit, protocol.FailureInternalError, "scripted agent failure")
	})
	env := newFaultEnv(t, faultDeps{adapter: adapter})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	dto, err := env.svc.RunPipeline(ctx, transport.PipelineRunRequest{
		ProjectID:   env.projID,
		Description: scopedDescription,
		Engine:      "fake",
		Model:       "fake/standard",
	})
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if dto.RunState != "failed" {
		t.Fatalf("run_state = %s, want failed", dto.RunState)
	}
	active, err := env.leases.ListActiveByProject(ctx, env.projID)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Errorf("active leases after failed run: %+v, want none (release on terminal)", active)
	}
}

// TestPipelineLease_ConflictBlocksThenResumes proves (H4) that a lease
// conflict parks the run in blocked WITHOUT executing the agent, and that the
// blocked run resumes to completion once the conflicting lease expires.
func TestPipelineLease_ConflictBlocksThenResumes(t *testing.T) {
	adapter := newScriptedCodingAdapter(writeCommitBehavior(map[string]string{"RESULT.md": "after unblock\n"}))
	env := newFaultEnv(t, faultDeps{adapter: adapter})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// A competing workspace holds the scoped path with a short TTL.
	if _, err := env.leases.AcquirePathTTL(ctx, env.projID, "ws-competitor", "src/main.go", 400*time.Millisecond); err != nil {
		t.Fatalf("acquire competing lease: %v", err)
	}

	dto, err := env.svc.RunPipeline(ctx, transport.PipelineRunRequest{
		ProjectID:   env.projID,
		Description: scopedDescription,
		Engine:      "fake",
		Model:       "fake/standard",
	})
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if dto.RunState != string(pipeline.RunBlocked) {
		t.Fatalf("run_state = %s (failure %s: %s), want blocked", dto.RunState, dto.FailureCategory, dto.FailureReason)
	}
	if dto.FailureCategory != string(pipeline.FailureLeaseLost) {
		t.Errorf("failure_category = %s, want %s", dto.FailureCategory, pipeline.FailureLeaseLost)
	}
	if n := adapter.codingCalls(); n != 0 {
		t.Errorf("coding agent calls = %d, want 0 (a blocked run must not execute)", n)
	}

	// After the conflicting lease expires, restart recovery re-drives the
	// blocked run to completion.
	time.Sleep(600 * time.Millisecond)
	env.svc.ResumeActiveRuns(ctx)
	env.waitRunState(t, dto.TaskID, "completed", 90*time.Second)
	if n := adapter.codingCalls(); n != 1 {
		t.Errorf("coding agent calls after unblock = %d, want 1", n)
	}
}
