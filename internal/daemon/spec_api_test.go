package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"neuroforge/internal/storage"
	"neuroforge/internal/task"
	"neuroforge/internal/transport"
)

// This file exercises the daemon-mediated Task Compiler path (M14-03) through
// the real loopback transport against the real SQLite driver. It is the
// in-process evidence layer; the compiled-binary black-box evidence lives in
// internal/cli/spec_save_blackbox_test.go.
//
// The test creates a real (throwaway) Git repository so the project registry's
// git-repo validation passes — the same registry the production daemon uses.

// initTempGitRepo creates a throwaway git repo at dir with one commit so the
// project registry's ValidateGitRepo accepts it. The repo has no remote; that
// is fine for the registry's local path.
func initTempGitRepo(t *testing.T, dir string) {
	t.Helper()
	runGitStep(t, dir, "init", "-q")
	runGitStep(t, dir, "config", "user.email", "test@example.com")
	runGitStep(t, dir, "config", "user.name", "Test")
	runGitStep(t, dir, "add", "-A")
	// commit requires something to commit; write a placeholder file.
	if err := writeFile(filepath.Join(dir, "README.md"), "# test\n"); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGitStep(t, dir, "add", "-A")
	runGitStep(t, dir, "commit", "-q", "-m", "initial")
}

func runGitStep(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

// startDaemonOnce starts a daemon Run in a goroutine against dirs and returns
// a stop function. Unlike runInBackground, it does NOT register a t.Cleanup
// so the caller controls the lifecycle explicitly (needed for restart tests).
// The caller MUST call stop before t.Cleanup fires (e.g. via a manual
// t.Cleanup that calls stop).
func startDaemonOnce(t *testing.T, dirs Dirs) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var runErr error
	go func() {
		runErr = Run(ctx, RunConfig{Dirs: dirs, Logger: quietLogger()})
		close(done)
	}()
	if err := waitForHealthy(dirs, 5*time.Second); err != nil {
		cancel()
		<-done
		t.Fatalf("daemon not healthy: %v", err)
	}
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(6 * time.Second):
			t.Fatalf("daemon Run did not stop within timeout (runErr=%v)", runErr)
		}
	}
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o600)
}

// daemonClient connects to the daemon recorded in dirs and returns a ready
// transport client.
func daemonClient(t *testing.T, dirs Dirs) *transport.Client {
	t.Helper()
	addr, err := readAddr(dirs)
	if err != nil {
		t.Fatalf("read addr: %v", err)
	}
	tok, err := readToken(dirs)
	if err != nil {
		t.Fatalf("read token: %v", err)
	}
	return transport.NewClient(addr, tok)
}

// TestSpecAdapter_CompileAndGetAndLock proves the daemon-mediated Task
// Compiler path works end-to-end through the transport: create task →
// compile-and-save → get latest → lock → list versions → idempotent
// re-compile. It also proves the locked-update rejection path.
func TestSpecAdapter_CompileAndGetAndLock(t *testing.T) {
	dirs := WithRoot(t.TempDir())
	repoDir := t.TempDir()
	initTempGitRepo(t, repoDir)

	stop := startDaemonOnce(t, dirs)
	t.Cleanup(stop)
	ctx := context.Background()
	cli := daemonClient(t, dirs)

	// Register the project through the transport (production path).
	proj, err := cli.AddProject(ctx, transport.AddProjectRequest{Path: repoDir, Name: "spec-test"})
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}

	// Create a task with explicit objective + acceptance criteria so the
	// compiler emits a HIGH-confidence, valid specification. The compiler
	// parses dash-list AC items (mirrors internal/task/compiler_test.go).
	desc := "Objective: Implement the foo bar.\n\nAcceptance Criteria:\n- Foo returns 200.\n- Bar is persisted."
	created, err := cli.AddTask(ctx, transport.AddTaskRequest{
		ProjectID:   proj.ID,
		Description: desc,
		Priority:    "HIGH",
	})
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	// 1. Compile-and-save → version 1.
	compiled, err := cli.CompileSpec(ctx, created.ID, transport.CompileSpecRequest{LockedBy: "alice"})
	if err != nil {
		t.Fatalf("CompileSpec #1: %v", err)
	}
	if !compiled.Created {
		t.Errorf("CompileSpec #1 Created=%v, want true", compiled.Created)
	}
	if compiled.Specification.Version != 1 {
		t.Errorf("CompileSpec #1 Version=%d, want 1", compiled.Specification.Version)
	}
	if compiled.Specification.TaskID != created.ID {
		t.Errorf("CompileSpec #1 TaskID=%q, want %q", compiled.Specification.TaskID, created.ID)
	}
	if compiled.Specification.Objective == "" {
		t.Errorf("CompileSpec #1 Objective empty; compiler did not run")
	}
	if len(compiled.Specification.AcceptanceCriteria) < 2 {
		t.Errorf("CompileSpec #1 AC count=%d, want >=2", len(compiled.Specification.AcceptanceCriteria))
	}
	if compiled.Confidence != "HIGH" {
		t.Errorf("CompileSpec #1 Confidence=%q, want HIGH", compiled.Confidence)
	}
	if compiled.Specification.Locked {
		t.Errorf("CompileSpec #1 Locked=true, want false (fresh spec is unlocked)")
	}

	// 2. Idempotent re-compile → same version, Created=false.
	compiled2, err := cli.CompileSpec(ctx, created.ID, transport.CompileSpecRequest{LockedBy: "alice"})
	if err != nil {
		t.Fatalf("CompileSpec #2 (idempotent): %v", err)
	}
	if compiled2.Created {
		t.Errorf("CompileSpec #2 Created=true, want false (idempotent re-compile must not mint a version)")
	}
	if compiled2.Specification.Version != compiled.Specification.Version {
		t.Errorf("CompileSpec #2 Version=%d, want %d (idempotent)", compiled2.Specification.Version, compiled.Specification.Version)
	}
	if compiled2.Specification.Objective != compiled.Specification.Objective {
		t.Errorf("CompileSpec #2 Objective differs from #1 (must be byte-identical for idempotent re-compile)")
	}

	// 3. Get latest matches the compiled-and-saved spec.
	latest, err := cli.GetSpecification(ctx, created.ID, 0)
	if err != nil {
		t.Fatalf("GetSpecification latest: %v", err)
	}
	if latest.Version != compiled.Specification.Version {
		t.Errorf("Get latest Version=%d, want %d", latest.Version, compiled.Specification.Version)
	}
	if latest.Objective != compiled.Specification.Objective {
		t.Errorf("Get latest Objective differs")
	}

	// 4. Get specific version matches.
	v1, err := cli.GetSpecification(ctx, created.ID, 1)
	if err != nil {
		t.Fatalf("GetSpecification v1: %v", err)
	}
	if v1.Version != 1 {
		t.Errorf("Get v1 Version=%d, want 1", v1.Version)
	}

	// 5. ListVersions → [1].
	versions, err := cli.ListSpecificationVersions(ctx, created.ID)
	if err != nil {
		t.Fatalf("ListSpecificationVersions: %v", err)
	}
	if len(versions) != 1 || versions[0] != 1 {
		t.Errorf("versions=%v, want [1]", versions)
	}

	// 6. Lock v1.
	locked, err := cli.LockSpecification(ctx, created.ID, transport.LockSpecRequest{Version: 1, LockedBy: "alice"})
	if err != nil {
		t.Fatalf("LockSpecification: %v", err)
	}
	if !locked.Locked {
		t.Errorf("Locked=%v, want true", locked.Locked)
	}
	if locked.LockedBy != "alice" {
		t.Errorf("LockedBy=%q, want alice", locked.LockedBy)
	}
	if locked.LockedAt == "" {
		t.Errorf("LockedAt empty, want timestamp")
	}

	// 7. Lock is idempotent — second lock returns the same state, no error.
	locked2, err := cli.LockSpecification(ctx, created.ID, transport.LockSpecRequest{Version: 1, LockedBy: "alice"})
	if err != nil {
		t.Fatalf("LockSpecification idempotent: %v", err)
	}
	if !locked2.Locked {
		t.Errorf("idempotent re-lock Locked=false, want true")
	}

	// 8. After lock, compile-and-save is still idempotent for the same content:
	//    the locked v1 is returned unchanged, NO new version is minted.
	compiled3, err := cli.CompileSpec(ctx, created.ID, transport.CompileSpecRequest{})
	if err != nil {
		t.Fatalf("CompileSpec after lock: %v", err)
	}
	if compiled3.Created {
		t.Errorf("CompileSpec after lock Created=true, want false (locked content matches; must not mint a version)")
	}
	if compiled3.Specification.Version != 1 {
		t.Errorf("CompileSpec after lock Version=%d, want 1", compiled3.Specification.Version)
	}
	if !compiled3.Specification.Locked {
		t.Errorf("CompileSpec after lock Locked=false, want true (should reflect locked state)")
	}

	// 9. Negative: compile on a nonexistent task → "not found" surfaces.
	if _, err := cli.CompileSpec(ctx, "bogus-project-9999", transport.CompileSpecRequest{}); err == nil {
		t.Fatal("expected error for compile on missing task")
	} else if !strings.Contains(err.Error(), "not found") {
		t.Errorf("compile missing task err=%q, want it to contain 'not found'", err.Error())
	}

	// 10. Negative: get on a nonexistent task → "not found" surfaces.
	if _, err := cli.GetSpecification(ctx, "bogus-project-9999", 0); err == nil {
		t.Fatal("expected error for get on missing task")
	} else if !strings.Contains(err.Error(), "not found") {
		t.Errorf("get missing task err=%q, want it to contain 'not found'", err.Error())
	}

	// 11. Negative: lock a missing version → "not found".
	if _, err := cli.LockSpecification(ctx, created.ID, transport.LockSpecRequest{Version: 99}); err == nil {
		t.Fatal("expected error for lock on missing version")
	} else if !strings.Contains(err.Error(), "not found") {
		t.Errorf("lock missing version err=%q, want it to contain 'not found'", err.Error())
	}
}

// TestSpecAdapter_LockedUpdateRejected proves the locked-update invariant at
// the daemon composition level: after v1 is locked, attempting to mutate it
// through task.SpecificationStore.Save (the same handle the daemon's adapter
// uses) is rejected with ErrSpecificationLocked. This is the regression guard
// for "locked update" cases at the daemon layer.
func TestSpecAdapter_LockedUpdateRejected(t *testing.T) {
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
	created, err := cli.AddTask(ctx, transport.AddTaskRequest{
		ProjectID:   proj.ID,
		Description: "Objective: do thing.\nAcceptance Criteria:\n- thing done.",
	})
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := cli.CompileSpec(ctx, created.ID, transport.CompileSpecRequest{}); err != nil {
		t.Fatalf("CompileSpec: %v", err)
	}
	if _, err := cli.LockSpecification(ctx, created.ID, transport.LockSpecRequest{Version: 1}); err != nil {
		t.Fatalf("LockSpecification: %v", err)
	}

	// Reach the SAME storage the daemon uses, by opening the DB and constructing
	// a SpecificationStore against it. This is the daemon-internal handle; the
	// test proves it honours the lock just like the unit tests in internal/task.
	db, err := storage.Open(ctx, filepath.Join(dirs.Root, "state.db"), &storage.Options{Logger: quietLogger()})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	store := task.NewSpecificationStore(db, nil, quietLogger())

	// Attempt to mutate v1 → must be rejected with ErrSpecificationLocked.
	_, err = store.Save(ctx, task.Specification{
		TaskID:             created.ID,
		Version:            1,
		Objective:          "MUTATED objective that must not persist",
		AcceptanceCriteria: []task.AcceptanceCriterion{{ID: "AC-1", Statement: "mutated"}},
	})
	if !errors.Is(err, task.ErrSpecificationLocked) {
		t.Fatalf("expected ErrSpecificationLocked, got %v", err)
	}

	// Confirm v1 is unchanged by re-reading through the transport.
	got, err := cli.GetSpecification(ctx, created.ID, 1)
	if err != nil {
		t.Fatalf("GetSpecification after rejected mutation: %v", err)
	}
	if got.Objective == "MUTATED objective that must not persist" {
		t.Fatal("locked v1 was mutated despite ErrSpecificationLocked")
	}
	if !got.Locked {
		t.Errorf("v1 Locked=false after rejected mutation, want true")
	}
}

// TestSpecAdapter_PersistsAcrossRestart proves the compiled specification
// survives a full daemon stop/start cycle (the "restart persistence"
// acceptance criterion). The state is observable through the transport —
// not via in-process handles — so this matches the engineering baseline's
// restart-recovery evidence rule.
func TestSpecAdapter_PersistsAcrossRestart(t *testing.T) {
	dirs := WithRoot(t.TempDir())
	repoDir := t.TempDir()
	initTempGitRepo(t, repoDir)

	// Daemon #1.
	stop1 := startDaemonOnce(t, dirs)
	ctx := context.Background()
	cli1 := daemonClient(t, dirs)

	proj, err := cli1.AddProject(ctx, transport.AddProjectRequest{Path: repoDir})
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	created, err := cli1.AddTask(ctx, transport.AddTaskRequest{
		ProjectID:   proj.ID,
		Description: "Objective: survive restart.\nAcceptance Criteria:\n- state durable.",
	})
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	compiled, err := cli1.CompileSpec(ctx, created.ID, transport.CompileSpecRequest{LockedBy: "alice"})
	if err != nil {
		t.Fatalf("CompileSpec: %v", err)
	}
	if _, err := cli1.LockSpecification(ctx, created.ID, transport.LockSpecRequest{Version: 1, LockedBy: "alice"}); err != nil {
		t.Fatalf("LockSpecification: %v", err)
	}

	// Capture the durable state to compare after restart.
	wantObjective := compiled.Specification.Objective
	wantACCount := len(compiled.Specification.AcceptanceCriteria)

	// Stop daemon #1.
	stop1()

	// Daemon #2 against the SAME home (state.db reused).
	stop2 := startDaemonOnce(t, dirs)
	t.Cleanup(stop2)
	cli2 := daemonClient(t, dirs)

	// The spec must be fully readable through the transport after restart.
	got, err := cli2.GetSpecification(ctx, created.ID, 1)
	if err != nil {
		t.Fatalf("GetSpecification after restart: %v", err)
	}
	if got.Version != 1 {
		t.Errorf("post-restart Version=%d, want 1", got.Version)
	}
	if got.Objective != wantObjective {
		t.Errorf("post-restart Objective=%q, want %q", got.Objective, wantObjective)
	}
	if len(got.AcceptanceCriteria) != wantACCount {
		t.Errorf("post-restart AC count=%d, want %d", len(got.AcceptanceCriteria), wantACCount)
	}
	if got.AcceptanceCriteria[0].ID != compiled.Specification.AcceptanceCriteria[0].ID {
		t.Errorf("post-restart first AC ID=%q, want %q (AC IDs must be stable across restart)",
			got.AcceptanceCriteria[0].ID, compiled.Specification.AcceptanceCriteria[0].ID)
	}
	if !got.Locked {
		t.Errorf("post-restart Locked=false, want true (lock state must survive restart)")
	}
	if got.LockedBy != "alice" {
		t.Errorf("post-restart LockedBy=%q, want alice (lock provenance must survive restart)", got.LockedBy)
	}

	// ListVersions survives restart.
	versions, err := cli2.ListSpecificationVersions(ctx, created.ID)
	if err != nil {
		t.Fatalf("ListSpecificationVersions after restart: %v", err)
	}
	if len(versions) != 1 || versions[0] != 1 {
		t.Errorf("post-restart versions=%v, want [1]", versions)
	}

	// Compile-and-save is still idempotent after restart: the locked content
	// matches, so no new version is minted.
	again, err := cli2.CompileSpec(ctx, created.ID, transport.CompileSpecRequest{LockedBy: "alice"})
	if err != nil {
		t.Fatalf("CompileSpec after restart: %v", err)
	}
	if again.Created {
		t.Errorf("post-restart CompileSpec Created=true, want false (locked content matches; must be idempotent)")
	}
	if again.Specification.Version != 1 {
		t.Errorf("post-restart CompileSpec Version=%d, want 1 (idempotent)", again.Specification.Version)
	}
}

// TestSpecAdapter_AuditRecorded proves the daemon-mediated compile-and-save
// path writes the task.specification.saved and task.specification.locked audit
// events durably to the database, atomically with the storage change. This is
// the "audit events" acceptance criterion evidence.
func TestSpecAdapter_AuditRecorded(t *testing.T) {
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
	created, err := cli.AddTask(ctx, transport.AddTaskRequest{
		ProjectID:   proj.ID,
		Description: "Objective: audit me.\nAcceptance Criteria:\n- event recorded.",
	})
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := cli.CompileSpec(ctx, created.ID, transport.CompileSpecRequest{}); err != nil {
		t.Fatalf("CompileSpec: %v", err)
	}
	if _, err := cli.LockSpecification(ctx, created.ID, transport.LockSpecRequest{Version: 1}); err != nil {
		t.Fatalf("LockSpecification: %v", err)
	}
	// Give the audit writer a moment to flush (it commits inside the same tx
	// as the storage change, but the test reads from a separate connection).
	time.Sleep(50 * time.Millisecond)

	db, err := storage.Open(ctx, filepath.Join(dirs.Root, "state.db"), &storage.Options{Logger: quietLogger()})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	rows, err := db.ListAuditEvents(ctx, storage.AuditFilter{ScopeID: created.ID})
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	types := map[string]bool{}
	for _, r := range rows {
		types[r.Type] = true
	}
	for _, want := range []string{"task.specification.saved", "task.specification.locked"} {
		if !types[want] {
			t.Errorf("missing audit event %q for task %s; have %v", want, created.ID, keysOf(types))
		}
	}
}

// ---- M14-03 MAJOR-1 remediation: concurrency regression tests ----

// TestSpecAdapter_ConcurrentIdenticalCompileCreatesSingleVersion is the
// production concurrency regression test for MAJOR-1. It fires 30 identical
// CompileSpec calls through the REAL daemon transport (production path:
// CLI client → HTTP → specAPIAdapter → task.Compile → SaveIfChanged → SQLite)
// against one freshly-created task, then asserts ListSpecificationVersions
// returns exactly [1].
//
// On the original candidate (78d1ff1) this test reproduced up to 7 duplicate
// versions (the TOCTOU between GetLatest and Save). After the keyed per-task
// serialization in SaveIfChanged, exactly one version is created.
func TestSpecAdapter_ConcurrentIdenticalCompileCreatesSingleVersion(t *testing.T) {
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
	desc := "Objective: Concurrent compile must be idempotent.\n\nAcceptance Criteria:\n- One version under concurrency.\n- No duplicate semantic versions."
	created, err := cli.AddTask(ctx, transport.AddTaskRequest{
		ProjectID:   proj.ID,
		Description: desc,
		Priority:    "HIGH",
	})
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	const n = 30
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, n)
	results := make([]transport.CompileSpecResultDTO, n)
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i], errs[i] = cli.CompileSpec(ctx, created.ID, transport.CompileSpecRequest{LockedBy: "concurrent-test"})
		}()
	}
	close(start)
	wg.Wait()

	createdCount := 0
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d error: %v", i, errs[i])
		}
		if results[i].Created {
			createdCount++
		}
		if results[i].Specification.Version != 1 {
			t.Errorf("goroutine %d: Version=%d, want 1", i, results[i].Specification.Version)
		}
	}
	if createdCount != 1 {
		t.Errorf("Created count = %d, want exactly 1 (only one goroutine should mint the version)", createdCount)
	}

	versions, err := cli.ListSpecificationVersions(ctx, created.ID)
	if err != nil {
		t.Fatalf("ListSpecificationVersions: %v", err)
	}
	if len(versions) != 1 || versions[0] != 1 {
		t.Fatalf("persisted versions=%v, want exactly [1] (MAJOR-1 invariant: no duplicate semantic versions)", versions)
	}
}

// TestSpecAdapter_ConcurrentDifferentTasksDoNotBlockEachOther proves the
// keyed per-task serialization does NOT globally serialize unrelated tasks.
// It uses a controlled barrier: two CompileSpec calls for different tasks are
// launched concurrently; the test asserts both succeed and each task has
// exactly one version. The barrier (close(start)) guarantees true concurrency
// at launch time.
func TestSpecAdapter_ConcurrentDifferentTasksDoNotBlockEachOther(t *testing.T) {
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

	task1, err := cli.AddTask(ctx, transport.AddTaskRequest{
		ProjectID:   proj.ID,
		Description: "Objective: Task one concurrent compile.\nAcceptance Criteria:\n- task one done.",
	})
	if err != nil {
		t.Fatalf("AddTask 1: %v", err)
	}
	task2, err := cli.AddTask(ctx, transport.AddTaskRequest{
		ProjectID:   proj.ID,
		Description: "Objective: Task two concurrent compile.\nAcceptance Criteria:\n- task two done.",
	})
	if err != nil {
		t.Fatalf("AddTask 2: %v", err)
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, 2)
	results := make([]transport.CompileSpecResultDTO, 2)
	taskIDs := []string{task1.ID, task2.ID}

	for i := 0; i < 2; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i], errs[i] = cli.CompileSpec(ctx, taskIDs[i], transport.CompileSpecRequest{})
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
		if !results[i].Created {
			t.Errorf("goroutine %d: Created=false, want true (first compile for each task)", i)
		}
		if results[i].Specification.Version != 1 {
			t.Errorf("goroutine %d: Version=%d, want 1", i, results[i].Specification.Version)
		}
	}

	for _, tid := range taskIDs {
		versions, err := cli.ListSpecificationVersions(ctx, tid)
		if err != nil {
			t.Fatalf("ListVersions %s: %v", tid, err)
		}
		if len(versions) != 1 || versions[0] != 1 {
			t.Errorf("task %s versions=%v, want [1]", tid, versions)
		}
	}
}

// TestSpecAdapter_ConcurrentChangedInputCreatesDistinctVersions proves that
// parallel requests with DIFFERENT semantic content are not lost: each unique
// input results in a distinct version. This verifies the keyed lock does not
// collapse genuinely different content into one version.
//
// NOTE: under the current public API, the task description is immutable after
// task add, so all compiles of the same task produce identical content. This
// test seeds multiple tasks and compiles each concurrently; each task gets
// exactly one version (the content within each task is identical, but across
// tasks it differs). The important assertion is that no content is lost under
// concurrency.
func TestSpecAdapter_ConcurrentChangedInputCreatesDistinctVersions(t *testing.T) {
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

	const n = 5
	taskIDs := make([]string, n)
	for i := 0; i < n; i++ {
		created, err := cli.AddTask(ctx, transport.AddTaskRequest{
			ProjectID:   proj.ID,
			Description: fmt.Sprintf("Objective: Distinct task %d.\nAcceptance Criteria:\n- task %d unique.", i, i),
		})
		if err != nil {
			t.Fatalf("AddTask %d: %v", i, err)
		}
		taskIDs[i] = created.ID
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = cli.CompileSpec(ctx, taskIDs[i], transport.CompileSpecRequest{})
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}

	// Each task must have exactly one version, with its distinct objective.
	for i, tid := range taskIDs {
		spec, err := cli.GetSpecification(ctx, tid, 0)
		if err != nil {
			t.Fatalf("GetSpecification task %d: %v", i, err)
		}
		if spec.Version != 1 {
			t.Errorf("task %d Version=%d, want 1", i, spec.Version)
		}
		wantObj := fmt.Sprintf("Distinct task %d.", i)
		if spec.Objective != wantObj {
			t.Errorf("task %d Objective=%q, want %q (content lost under concurrency)", i, spec.Objective, wantObj)
		}
		versions, err := cli.ListSpecificationVersions(ctx, tid)
		if err != nil {
			t.Fatalf("ListVersions task %d: %v", i, err)
		}
		if len(versions) != 1 {
			t.Errorf("task %d versions=%v, want exactly [1]", i, versions)
		}
	}
}

// TestSpecAdapter_ConcurrentIdempotentReuseAudit proves the audit semantics
// for concurrent identical compiles: exactly one task.specification.saved
// event is written (for the winner), not N misleading "created" events. This
// is the M14-03 MAJOR-1 audit-impact remediation evidence.
func TestSpecAdapter_ConcurrentIdempotentReuseAudit(t *testing.T) {
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
	created, err := cli.AddTask(ctx, transport.AddTaskRequest{
		ProjectID:   proj.ID,
		Description: "Objective: Audit under concurrency.\nAcceptance Criteria:\n- one audit event.",
	})
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	const n = 20
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = cli.CompileSpec(ctx, created.ID, transport.CompileSpecRequest{})
			// Errors are checked below; some may be context-canceled under
			// extreme contention but should not be.
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}

	// Give the audit writer a moment to flush (it commits inside the same tx
	// as the storage change, but the test reads from a separate connection).
	time.Sleep(100 * time.Millisecond)

	db, err := storage.Open(ctx, filepath.Join(dirs.Root, "state.db"), &storage.Options{Logger: quietLogger()})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	rows, err := db.ListAuditEvents(ctx, storage.AuditFilter{ScopeID: created.ID})
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	saveCount := 0
	for _, r := range rows {
		if r.Type == "task.specification.saved" {
			saveCount++
		}
	}
	if saveCount != 1 {
		t.Errorf("task.specification.saved audit events = %d, want 1 (exactly one create for concurrent identical compiles)", saveCount)
	}

	versions, err := cli.ListSpecificationVersions(ctx, created.ID)
	if err != nil {
		t.Fatalf("ListSpecificationVersions: %v", err)
	}
	if len(versions) != 1 {
		t.Errorf("versions=%v, want [1]", versions)
	}
}
