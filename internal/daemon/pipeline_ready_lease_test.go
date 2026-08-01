package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"

	"neuroforge/internal/pipeline"
	"neuroforge/internal/task"
	"neuroforge/internal/workgraph"
)

// Ready-stage lease classification tests (review follow-up N1): the
// handler-level proof that a run whose decomposed packages share an
// AllowedScope does not self-block on its own workspace's lease, while a
// foreign workspace's lease still parks the run in blocked.

// setupReadyStageRun creates a task + run + persisted work graph in the exact
// durable shape handleReady expects, returning the run context for the ready
// stage. The graph mirrors workgraph.Decompose's output for a spec with two
// ACs and a non-empty ProposedScope: a chain of two packages sharing one
// AllowedScope.
func setupReadyStageRun(t *testing.T, env *faultEnv) *pipeline.RunContext {
	t.Helper()
	ctx := context.Background()

	tk, err := env.tasks.Add(ctx, task.AddRequest{ProjectID: env.projID, Description: "shared scope ready"})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.svc.saveParams(tk.ID, pipelineParams{
		ProjectID: env.projID, ProjectPath: env.repo, Description: "shared scope ready",
		Engine: "fake", Model: "fake/write-commit",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.store.CreateRun(ctx, tk.ID, env.projID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := env.tasks.Transition(ctx, tk.ID, task.ActionDispatch); err != nil {
		t.Fatal(err)
	}

	mk := func(id string, deps ...string) workgraph.WorkPackage {
		return workgraph.WorkPackage{
			ID: tk.ID + "-" + id, TaskID: tk.ID, Stage: workgraph.StageImplementation,
			Title: "Implement " + id, Objective: "obj",
			AcceptedACIDs: []string{id}, AllowedScope: []string{"main.go"},
			Dependencies: deps,
			State:        workgraph.PackagePending,
		}
	}
	vg, err := workgraph.ValidateWorkGraph(workgraph.WorkGraph{
		TaskID:   tk.ID,
		Packages: []workgraph.WorkPackage{mk("AC-1"), mk("AC-2", tk.ID+"-AC-1")},
	})
	if err != nil {
		t.Fatalf("ValidateWorkGraph: %v", err)
	}
	if _, err := env.graphs.Save(ctx, vg); err != nil {
		t.Fatalf("Save graph: %v", err)
	}
	return &pipeline.RunContext{TaskID: tk.ID, ProjectID: env.projID, Stage: pipeline.StageReady}
}

// TestPipelineReady_SharedScopePackages_NotSelfBlocked is the N1 daemon-level
// regression: the run's first package claims the shared scope; the second
// (chained) package must be skipped for its unmet dependency ONLY — never
// classified as a lease conflict against the run's own workspace. Before the
// fix, handleReady returned lease_lost here and the driver parked the run in
// blocked until the 4h lease TTL.
func TestPipelineReady_SharedScopePackages_NotSelfBlocked(t *testing.T) {
	env := newFaultEnv(t, faultDeps{})
	ctx := context.Background()
	rc := setupReadyStageRun(t, env)

	evidence, err := env.svc.handleReady(ctx, rc)
	if err != nil {
		t.Fatalf("handleReady self-blocked on its own lease: %v", err)
	}
	if !strings.Contains(evidence, "claimed:1") {
		t.Errorf("evidence = %q, want exactly one package claimed (the chain head)", evidence)
	}

	// The chain head succeeds; on the next ready pass the second package must
	// claim cleanly under the SAME workspace (its own lease re-acquired
	// idempotently) — project-scoped isolation excludes only foreign holders.
	if err := env.graphs.TransitionPackage(ctx, rc.TaskID, rc.TaskID+"-AC-1", workgraph.PackageSucceeded); err != nil {
		t.Fatalf("succeed AC-1: %v", err)
	}
	evidence, err = env.svc.handleReady(ctx, rc)
	if err != nil {
		t.Fatalf("handleReady second pass: %v", err)
	}
	if !strings.Contains(evidence, "claimed:1") {
		t.Errorf("evidence = %q, want the dependent package claimed", evidence)
	}
	p, err := env.graphs.GetPackage(ctx, rc.TaskID, rc.TaskID+"-AC-2")
	if err != nil {
		t.Fatal(err)
	}
	if p.State != workgraph.PackageRunning {
		t.Errorf("AC-2 state = %q, want running", p.State)
	}
}

// TestPipelineReady_ForeignLeaseConflict_StillBlocks is the N1 isolation
// guard at the handler level: a lease held by a FOREIGN workspace in the same
// project still classifies as lease_lost (the driver parks the run in
// blocked, resumable after the lease clears).
func TestPipelineReady_ForeignLeaseConflict_StillBlocks(t *testing.T) {
	env := newFaultEnv(t, faultDeps{})
	ctx := context.Background()
	rc := setupReadyStageRun(t, env)

	if _, err := env.leases.AcquirePath(ctx, env.projID, "foreign-ws", "main.go"); err != nil {
		t.Fatalf("foreign acquire: %v", err)
	}
	_, err := env.svc.handleReady(ctx, rc)
	var se *pipeline.StageError
	if !errors.As(err, &se) {
		t.Fatalf("handleReady error = %v, want *StageError", err)
	}
	if se.Category != pipeline.FailureLeaseLost {
		t.Errorf("category = %q, want lease_lost for a foreign conflict", se.Category)
	}
	if !strings.Contains(se.Reason, "foreign-ws") {
		t.Errorf("reason should name the holding workspace: %q", se.Reason)
	}
}
