package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"neuroforge/internal/pipeline"
)

// slowGoTest is a gofmt-clean test that sleeps far longer than the test is
// willing to wait: cancellation must interrupt it via process-group kill.
const slowGoTest = `package main

import (
	"testing"
	"time"
)

func TestSlow(t *testing.T) {
	time.Sleep(60 * time.Second)
}
`

// TestPipelineVerify_SIGINTClassifiedCancelled is the M6 regression test: a
// caller cancellation (SIGINT on `forge run`) arriving while verification
// runs must classify the run as CANCELLED — never as invariant_violation or a
// bogus verification failure driving a repair loop.
func TestPipelineVerify_SIGINTClassifiedCancelled(t *testing.T) {
	env := newFaultEnv(t, faultDeps{})

	// A run parked at the verify stage with a committed change, plus a slow
	// test so verification is still in flight when the cancel lands.
	taskID, ws := env.setupKilledRun(t, pipeline.StageVerify, "sigint during verify")
	if err := os.WriteFile(filepath.Join(ws.Path, "slow_test.go"), []byte(slowGoTest), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitInDaemonTest(t, ws.Path, "add", "-A")
	runGitInDaemonTest(t, ws.Path, "commit", "-m", "add slow test",
		"--author=NeuroForge Fake <fake@neuroforge.local>")

	ctx, cancel := context.WithCancel(context.Background())
	driveDone := make(chan error, 1)
	go func() { driveDone <- env.svc.driver.Drive(ctx, taskID) }()

	// Let the verify handler start (it bounds itself to the slow test), then
	// cancel — the SIGINT shape.
	time.Sleep(750 * time.Millisecond)
	cancel()

	select {
	case err := <-driveDone:
		if err != nil {
			t.Fatalf("Drive: %v", err)
		}
	case <-time.After(90 * time.Second):
		t.Fatal("Drive did not return after cancel (process-group kill broken?)")
	}

	run, err := env.svc.store.CurrentRun(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != pipeline.RunCancelled {
		t.Errorf("run state = %s, want cancelled (M6 misclassification)", run.State)
	}
	if run.FailureCategory != pipeline.FailureCancelled {
		t.Errorf("failure category = %s, want %s (SIGINT was misclassified)",
			run.FailureCategory, pipeline.FailureCancelled)
	}
}
