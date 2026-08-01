package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"neuroforge/internal/pipeline"
	"neuroforge/internal/review"
	"neuroforge/internal/transport"
)

// mutatingReviewer approves cleanly but writes a file into the run's worktree
// during the review pass — the exact abuse security review H2 covers: the
// review agent has full write tools after verification, so its edits would be
// finalized unverified without the worktree-fingerprint guard.
type mutatingReviewer struct {
	env *faultEnv
}

func (r *mutatingReviewer) Review(ctx context.Context, _ review.Role, _ review.ReviewRequest) ([]review.Finding, error) {
	wss, err := r.env.wm.ListAll(ctx)
	if err != nil || len(wss) == 0 {
		return nil, nil
	}
	ws := wss[len(wss)-1]
	if err := os.WriteFile(filepath.Join(ws.Path, "TAMPERED-BY-REVIEWER.md"), []byte("unverified edit\n"), 0o644); err != nil {
		return nil, err
	}
	return nil, nil // "clean" review — the mutation must still be caught
}

// TestPipelineReview_ReviewerMutationRejected proves the review stage detects
// a reviewer that modified the worktree and fails the run honestly
// (policy_rejection) instead of finalizing unverified edits (H2).
func TestPipelineReview_ReviewerMutationRejected(t *testing.T) {
	env := newFaultEnv(t, faultDeps{})
	env.restart(t, faultDeps{reviewer: &mutatingReviewer{env: env}})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	dto, err := env.svc.RunPipeline(ctx, transport.PipelineRunRequest{
		ProjectID:   env.projID,
		Description: "reviewer mutation guard",
		Engine:      "fake",
		Model:       "fake/write-commit",
	})
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if dto.RunState != "failed" {
		t.Fatalf("run_state = %s, want failed (reviewer tampered with the worktree)", dto.RunState)
	}
	if dto.FailureCategory != string(pipeline.FailurePolicyRejection) {
		t.Errorf("failure_category = %s, want %s", dto.FailureCategory, pipeline.FailurePolicyRejection)
	}
	if !strings.Contains(dto.FailureReason, "reviewer modified the worktree") {
		t.Errorf("failure_reason = %q, want it to name the reviewer mutation", dto.FailureReason)
	}
	// The run must NEVER have been finalized: no completed finalize record.
	for _, r := range stageRecords(dto, "finalize", "completed") {
		t.Errorf("finalize completed despite reviewer mutation: %+v", r)
	}
}

// TestPipelineReview_EvidenceCarriesReviewMetadata proves the persisted review
// evidence distinguishes a genuinely-reviewed pass from a policy-skipped one
// (review finding M5): it carries the engine label and the roles that ran or
// were skipped, not just the findings.
func TestPipelineReview_EvidenceCarriesReviewMetadata(t *testing.T) {
	env := newFaultEnv(t, faultDeps{})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	dto, err := env.svc.RunPipeline(ctx, transport.PipelineRunRequest{
		ProjectID:   env.projID,
		Description: "review evidence metadata",
		Engine:      "fake",
		Model:       "fake/write-commit",
	})
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if dto.RunState != "completed" {
		t.Fatalf("run_state = %s, want completed", dto.RunState)
	}
	recs := stageRecords(dto, "review", "completed")
	if len(recs) != 1 {
		t.Fatalf("completed review records = %d, want 1", len(recs))
	}
	ref := strings.TrimPrefix(recs[0].EvidenceRef, "artifact:")
	raw, err := env.svc.artifacts.Read(ref)
	if err != nil {
		t.Fatalf("read review evidence artifact: %v", err)
	}
	var ev reviewEvidence
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatalf("parse review evidence: %v", err)
	}
	if ev.Label != "REVIEWED" {
		t.Errorf("evidence label = %q, want REVIEWED (a skipped review must read NOT AI-REVIEWED)", ev.Label)
	}
	if len(ev.RolesRun) == 0 {
		t.Error("evidence roles_run is empty; the reviewed roles must be recorded")
	}
	if len(ev.RolesSkipped) != 0 {
		t.Errorf("evidence roles_skipped = %v, want none for the default profile", ev.RolesSkipped)
	}
}
