package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"neuroforge/internal/pipeline"
	"neuroforge/internal/storage"
	"neuroforge/internal/workgraph"
)

// TestPipelineReady_ClaimStorageError_Fails is the N2 regression: a Claim
// error that is NOT a lease conflict (here: the scheduler's storage is
// closed, so GetPackage fails) must classify as database_failure — the driver
// fails the run — never as lease_lost, which would park it in blocked until
// the lease TTL.
func TestPipelineReady_ClaimStorageError_Fails(t *testing.T) {
	env := newFaultEnv(t, faultDeps{})
	ctx := context.Background()
	rc := setupReadyStageRun(t, env)

	// Swap the service's scheduler for one backed by a closed DB: every Claim
	// fails with a plain storage error (no ErrLeaseConflict / NotReady).
	brokenDB, err := storage.Open(ctx, filepath.Join(t.TempDir(), "broken.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := brokenDB.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	env.svc.sched = workgraph.NewScheduler(
		workgraph.NewWorkGraphStore(brokenDB, nil, nil),
		workgraph.NewLeaseManager(brokenDB),
	)
	if err := brokenDB.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = env.svc.handleReady(ctx, rc)
	var se *pipeline.StageError
	if !errors.As(err, &se) {
		t.Fatalf("handleReady error = %v, want *StageError", err)
	}
	if se.Category != pipeline.FailureDatabase {
		t.Errorf("category = %q, want database_failure for a non-conflict claim error", se.Category)
	}
}
