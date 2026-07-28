package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"neuroforge/internal/storage"
	"neuroforge/internal/task"
	"neuroforge/internal/transport"
	"neuroforge/internal/workgraph"
)

// This file is the in-process transport-level evidence layer for the M14-05
// Work Graph inspection API (mandatory AC: "graph show через daemon"). It
// drives the real loopback transport against the real SQLite driver and the
// real daemon Run; the compiled-binary black-box evidence lives in
// internal/cli/workgraph_show_blackbox_test.go.
//
// The dispatch path that calls WorkGraphStore.Save / Scheduler.Claim is a
// later milestone; here we seed the durable Work Graph + lease substrate
// through the same WorkGraphStore / LeaseManager the daemon uses (opening a
// second read/write handle to the same WAL DB) and verify the read path
// through the daemon's transport.

// openSecondHandle opens a second *storage.DB against dirs.StateDB so the test
// can seed rows through the same WorkGraphStore the daemon uses, without
// reaching through the (read-only) transport. SQLite WAL permits multiple
// connections; writes serialise on busy_timeout.
func openSecondHandle(t *testing.T, dirs Dirs) *storage.DB {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := storage.Open(ctx, dirs.StateDB, nil)
	if err != nil {
		t.Fatalf("open second handle: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestWorkGraphAdapter_ShowThroughTransport proves the daemon-mediated
// GET /tasks/{id}/workgraph path:
//   - persists a graph through the same WorkGraphStore the daemon uses;
//   - acquires a path lease so the readiness verdict can be observed;
//   - calls the transport endpoint and verifies the DTO + readiness verdicts.
func TestWorkGraphAdapter_ShowThroughTransport(t *testing.T) {
	dirs := WithRoot(t.TempDir())
	repoDir := t.TempDir()
	initTempGitRepo(t, repoDir)

	stop := startDaemonOnce(t, dirs)
	t.Cleanup(stop)
	ctx := context.Background()
	cli := daemonClient(t, dirs)

	// Register the project + task via transport (production path).
	proj, err := cli.AddProject(ctx, transport.AddProjectRequest{Path: repoDir, Name: "wg-test"})
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	createdTask, err := cli.AddTask(ctx, transport.AddTaskRequest{
		ProjectID:   proj.ID,
		Description: "Objective: Do something.\nAcceptance Criteria:\n- Foo.\n- Bar.",
		Priority:    "NORMAL",
	})
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	taskID := createdTask.ID

	// Compile a spec through the daemon (so a task.Specification is durable).
	if _, err := cli.CompileSpec(ctx, taskID, transport.CompileSpecRequest{}); err != nil {
		t.Fatalf("CompileSpec: %v", err)
	}
	// Load the spec via a second handle so we can call Decompose in-process
	// (the transport is the read path; decompose-and-save is the dispatch
	// layer's job, not a daemon endpoint yet). Decompose with an extra
	// AllowedScope partitioning: the M14-04 Decompose default copies the
	// spec's ProposedScope, which is empty when the compiler did not extract
	// one; here we partition the scope explicitly so the readiness calculator
	// has a path to block on. This is the same shape a future dispatch hook
	// will produce.
	db2 := openSecondHandle(t, dirs)
	specStore := task.NewSpecificationStore(db2, nil, nil)
	spec, err := specStore.GetLatest(ctx, taskID)
	if err != nil {
		t.Fatalf("GetLatest spec: %v", err)
	}
	v, err := workgraph.Decompose(spec)
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	// Partition AllowedScope: give each package a distinct path under src/.
	// This is the honest contract a future dispatcher will satisfy when it
	// routes a package to a workspace.
	{
		g := v.Graph()
		for i := range g.Packages {
			g.Packages[i].AllowedScope = []string{"src/pkg" + string(rune('0'+i)) + "/"}
		}
		v2, err := workgraph.ValidateWorkGraph(g)
		if err != nil {
			t.Fatalf("ValidateWorkGraph with AllowedScope: %v", err)
		}
		v = v2
	}
	graphStore := workgraph.NewWorkGraphStore(db2, nil, nil)
	if _, err := graphStore.Save(ctx, v); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 1. GET /tasks/{id}/workgraph returns every package with the chain's
	//    readiness map (first package ready; rest blocked by their
	//    predecessor).
	dto, err := cli.GetWorkGraph(ctx, taskID)
	if err != nil {
		t.Fatalf("GetWorkGraph: %v", err)
	}
	if dto.TaskID != taskID {
		t.Errorf("TaskID = %q want %q", dto.TaskID, taskID)
	}
	if len(dto.Packages) != len(spec.AcceptanceCriteria) {
		t.Errorf("packages = %d want %d (one per AC)", len(dto.Packages), len(spec.AcceptanceCriteria))
	}
	readyCount := 0
	for _, p := range dto.Packages {
		if p.Readiness == nil {
			t.Errorf("package %s has nil readiness verdict", p.ID)
			continue
		}
		if p.Readiness.Ready {
			readyCount++
		}
	}
	// Decompose chains packages sequentially; only the first is ready.
	if readyCount != 1 {
		t.Errorf("ready = %d want 1 (only the first package in the chain has no deps)", readyCount)
	}

	// 2. A path-lease conflict surfaces in the readiness verdict. Acquire
	//    the first package's allowed-scope path from a different workspace,
	//    then re-fetch; the verdict must now report "blocked" with the
	//    explainable cause.
	lm := workgraph.NewLeaseManager(db2)
	firstPkg := dto.Packages[0]
	if len(firstPkg.AllowedScope) == 0 {
		t.Fatalf("first package has empty AllowedScope; cannot test path-lease conflict")
	}
	if _, err := lm.AcquirePath(ctx, taskID, "other-ws", firstPkg.AllowedScope[0]); err != nil {
		t.Fatalf("AcquirePath: %v", err)
	}
	dto2, err := cli.GetWorkGraph(ctx, taskID)
	if err != nil {
		t.Fatalf("GetWorkGraph after lease: %v", err)
	}
	if dto2.Packages[0].Readiness == nil || dto2.Packages[0].Readiness.Ready {
		t.Errorf("first package should now be blocked by the lease: %+v", dto2.Packages[0].Readiness)
	}
	if dto2.Packages[0].Readiness == nil || !containsAny(dto2.Packages[0].Readiness.BlockedReasons, firstPkg.AllowedScope[0]) {
		t.Errorf("first package readiness should name the conflicting path %q: %+v",
			firstPkg.AllowedScope[0], dto2.Packages[0].Readiness)
	}
	// The active lease is also exposed on the DTO.
	if len(dto2.ActiveLeases) == 0 {
		t.Errorf("ActiveLeases empty; expected at least the one we just acquired")
	}

	// 3. Mark the first package succeeded through the same store; the next
	//    package in the chain should now report ready (deps satisfied).
	if err := graphStore.TransitionPackage(ctx, taskID, firstPkg.ID, workgraph.PackageSucceeded); err != nil {
		t.Fatalf("TransitionPackage: %v", err)
	}
	// Release the conflicting lease too so path conflicts clear.
	if _, err := lm.ReleaseAll(ctx, "other-ws"); err != nil {
		t.Fatalf("ReleaseAll: %v", err)
	}
	dto3, err := cli.GetWorkGraph(ctx, taskID)
	if err != nil {
		t.Fatalf("GetWorkGraph after transition: %v", err)
	}
	if len(dto3.Packages) < 2 {
		t.Fatalf("expected >=2 packages, got %d", len(dto3.Packages))
	}
	if dto3.Packages[1].Readiness == nil || !dto3.Packages[1].Readiness.Ready {
		t.Errorf("second package should be ready after first succeeded + lease released: %+v", dto3.Packages[1].Readiness)
	}
}

// TestWorkGraphAdapter_MissingGraphIs404 proves a missing task graph surfaces
// as a typed "not found" error rather than a 500 (so the CLI can render a
// helpful message rather than an internal-error blob).
func TestWorkGraphAdapter_MissingGraphIs404(t *testing.T) {
	dirs := WithRoot(t.TempDir())
	repoDir := t.TempDir()
	initTempGitRepo(t, repoDir)

	stop := startDaemonOnce(t, dirs)
	t.Cleanup(stop)
	ctx := context.Background()
	cli := daemonClient(t, dirs)

	proj, err := cli.AddProject(ctx, transport.AddProjectRequest{Path: repoDir})
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	createdTask, err := cli.AddTask(ctx, transport.AddTaskRequest{
		ProjectID:   proj.ID,
		Description: "Objective: Do something.\nAcceptance Criteria:\n- Foo.",
	})
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	_, err = cli.GetWorkGraph(ctx, createdTask.ID)
	if err == nil {
		t.Fatal("GetWorkGraph on a task with no graph should error")
	}
	// The GET path's error message includes the HTTP status (404) and the
	// server-side "work graph not found" cause (mirrors the spec_api GET
	// tests' assertion pattern).
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("err = %q, want it to mention HTTP 404", err.Error())
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %q, want it to contain 'not found'", err.Error())
	}
}

func containsAny(items []string, substr string) bool {
	for _, s := range items {
		if len(s) >= len(substr) {
			for i := 0; i+len(substr) <= len(s); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
		}
	}
	return false
}
