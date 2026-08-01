package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/fake"
	"neuroforge/internal/audit"
	"neuroforge/internal/pipeline"
	"neuroforge/internal/project"
	"neuroforge/internal/storage"
	"neuroforge/internal/supervisor"
	"neuroforge/internal/task"
	"neuroforge/internal/transport"
	"neuroforge/internal/workgraph"
	"neuroforge/internal/workspace"
)

// End-to-end integration of the durable pipeline (M14-06): real daemon
// services (storage, workspace manager, supervisor, spec/graph stores) with
// the deterministic fake engine, driving a full run through the
// PipelineService exactly as the daemon wires it.

// pipelineTestEnv bundles the real services a PipelineService needs.
type pipelineTestEnv struct {
	dirs   Dirs
	db     *storage.DB
	tasks  *task.Backlog
	svc    *PipelineService
	repo   string
	projID string
	head   string // primary checkout HEAD at setup
}

func newPipelineTestEnv(t *testing.T) *pipelineTestEnv {
	t.Helper()
	ctx := context.Background()
	home := t.TempDir()
	dirs := WithRoot(home)
	if err := dirs.Ensure(); err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(ctx, dirs.StateDB, &storage.Options{Logger: quietLogger()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	rec := audit.NewRecorder(db, quietLogger())
	wm := workspace.NewManager(db, rec, dirs.WorkspacesDir, quietLogger())
	projects := project.NewRegistry(db, rec, quietLogger())
	tasks := task.NewBacklog(db, rec, dirs.ArtifactsDir, quietLogger())
	specs := task.NewSpecificationStore(db, rec, quietLogger())
	graphs := workgraph.NewWorkGraphStore(db, rec, quietLogger())
	leases := workgraph.NewLeaseManager(db)

	reg := codingagent.NewRegistry()
	if err := reg.Register(fake.New(fake.AdapterOptions{Installed: true}), 0); err != nil {
		t.Fatal(err)
	}
	sup := supervisor.New(supervisor.Options{
		Adapters: reg,
		Audit:    rec,
		Logger:   quietLogger(),
		FullEnv:  os.Environ(),
	})

	svc, err := NewPipelineService(PipelineDeps{
		DB:       db,
		Recorder: rec,
		Logger:   quietLogger(),
		Dirs:     dirs,
		Tasks:    tasks,
		Projects: projects,
		Specs:    specs,
		Graphs:   graphs,
		Leases:   leases,
		WM:       wm,
		Sup:      sup,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Fixture repo: a buildable Go module (the verify stage runs gofmt/go
	// build/go vet/go test in the worktree). go 1.22 keeps the directive
	// satisfiable by the ambient toolchain without a download.
	repo := t.TempDir()
	runGitInDaemonTest(t, repo, "init", "-b", "main")
	runGitInDaemonTest(t, repo, "config", "user.email", "test@test.local")
	runGitInDaemonTest(t, repo, "config", "user.name", "Test")
	runGitInDaemonTest(t, repo, "config", "core.autocrlf", "false")
	mustWriteDaemonTest(t, filepath.Join(repo, "go.mod"), "module fixture\n\ngo 1.22\n")
	mustWriteDaemonTest(t, filepath.Join(repo, "main.go"), "package main\n\nfunc main() {}\n")
	mustWriteDaemonTest(t, filepath.Join(repo, "README.md"), "# T\n")
	runGitInDaemonTest(t, repo, "add", "-A")
	runGitInDaemonTest(t, repo, "commit", "-m", "init")
	head := strings.TrimSpace(gitOutInDaemonTest(t, repo, "rev-parse", "HEAD"))

	p, err := projects.Add(ctx, project.AddRequest{Path: repo})
	if err != nil {
		t.Fatal(err)
	}
	return &pipelineTestEnv{
		dirs: dirs, db: db, tasks: tasks, svc: svc,
		repo: repo, projID: p.ID, head: head,
	}
}

func gitOutInDaemonTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v in %s: %v", args, dir, err)
	}
	return string(out)
}

func TestPipelineService_EndToEnd_FakeEngine(t *testing.T) {
	env := newPipelineTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	dto, err := env.svc.RunPipeline(ctx, transport.PipelineRunRequest{
		ProjectID:   env.projID,
		Description: "add a hello file",
		Engine:      "fake",
		Model:       "fake/write-commit",
	})
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if dto.RunState != "completed" {
		t.Fatalf("run_state = %s (failure %s: %s), want completed", dto.RunState, dto.FailureCategory, dto.FailureReason)
	}
	if dto.Outcome != "completed-with-commit" {
		t.Errorf("outcome = %s, want completed-with-commit", dto.Outcome)
	}

	// Every pipeline stage has a completed record.
	wantStages := []string{"compile", "plan", "ready", "execute", "verify", "review", "finalize"}
	seen := map[string]bool{}
	for _, r := range dto.StageRecords {
		if r.Status == "completed" {
			seen[r.Stage] = true
		}
	}
	for _, st := range wantStages {
		if !seen[st] {
			t.Errorf("stage %s has no completed record (records: %+v)", st, dto.StageRecords)
		}
	}

	// The result ref exists in the fixture repo.
	ref := "refs/heads/forge/result/" + dto.TaskID
	if sha := strings.TrimSpace(gitOutInDaemonTest(t, env.repo, "rev-parse", "--verify", ref)); sha == "" {
		t.Errorf("result ref %s missing", ref)
	}
	if dto.ResultRef != ref {
		t.Errorf("result_ref = %q, want %q", dto.ResultRef, ref)
	}

	// Clean worktree at finalize (the fake committed its own work): the
	// finalize stage must NOT add an extra auto-commit — the result ref is
	// the agent's commit, exactly one commit beyond the base.
	if subj := strings.TrimSpace(gitOutInDaemonTest(t, env.repo, "log", "-1", "--format=%s", ref)); subj != "agent work" {
		t.Errorf("result ref subject = %q, want the agent's commit %q (no finalize auto-commit on a clean tree)", subj, "agent work")
	}
	if n := strings.TrimSpace(gitOutInDaemonTest(t, env.repo, "rev-list", "--count", env.head+".."+ref)); n != "1" {
		t.Errorf("commits beyond base = %s, want exactly 1", n)
	}

	// The task reached its terminal COMPLETED state.
	tk, err := env.tasks.Get(ctx, dto.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if tk.State != task.StateCompleted {
		t.Errorf("task state = %s, want COMPLETED", tk.State)
	}

	// The primary checkout is untouched: same HEAD, clean tree.
	if got := strings.TrimSpace(gitOutInDaemonTest(t, env.repo, "rev-parse", "HEAD")); got != env.head {
		t.Errorf("primary HEAD changed: %s → %s", env.head, got)
	}
	if status := strings.TrimSpace(gitOutInDaemonTest(t, env.repo, "status", "--porcelain")); status != "" {
		t.Errorf("primary checkout dirty: %s", status)
	}
}

func TestPipelineService_EstopBlocksAndResume(t *testing.T) {
	env := newPipelineTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Engage the stop: a new run must be refused (ErrEmergencyStopped) and the
	// run must stay active (parked), not failed.
	if _, err := env.svc.SetEmergencyStop(ctx, true, "test stop"); err != nil {
		t.Fatal(err)
	}
	dto, err := env.svc.RunPipeline(ctx, transport.PipelineRunRequest{
		ProjectID: env.projID, Description: "parked run", Engine: "fake",
	})
	if err != nil {
		t.Fatalf("RunPipeline under estop: %v", err)
	}
	if dto.RunState != "active" {
		t.Fatalf("run_state = %s, want active (parked by estop)", dto.RunState)
	}
	if dto.ErrorClass != "EMERGENCY_STOP" {
		t.Errorf("error_class = %s, want EMERGENCY_STOP", dto.ErrorClass)
	}

	// Clear the stop and let restart recovery re-drive the parked run.
	if _, err := env.svc.SetEmergencyStop(ctx, false, ""); err != nil {
		t.Fatal(err)
	}
	env.svc.ResumeActiveRuns(ctx)
	waitForRunState(t, env, dto.TaskID, "completed", 60*time.Second)
}

func TestPipelineService_CancelIsDurableAndNotResumed(t *testing.T) {
	env := newPipelineTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Start a hanging run in the background (fake/cancellation blocks until
	// cancelled).
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = env.svc.RunPipeline(ctx, transport.PipelineRunRequest{
			ProjectID: env.projID, Description: "hang until cancelled",
			Engine: "fake", Model: "fake/cancellation",
		})
	}()

	// Discover the task and wait for the execute stage (agent in flight).
	var taskID string
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		tasks, err := env.tasks.ListByProject(ctx, env.projID)
		if err == nil && len(tasks) > 0 {
			taskID = tasks[len(tasks)-1].ID
			st, err := env.svc.PipelineStatus(ctx, taskID)
			if err == nil && st.CurrentStage == "execute" {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if taskID == "" {
		t.Fatal("run did not start")
	}
	if st, err := env.svc.PipelineStatus(ctx, taskID); err != nil || st.CurrentStage != "execute" {
		t.Fatalf("run never reached execute stage: %+v err=%v", st, err)
	}

	dto, err := env.svc.CancelPipeline(ctx, taskID)
	if err != nil {
		t.Fatalf("CancelPipeline: %v", err)
	}
	if dto.RunState != "cancelled" {
		t.Fatalf("run_state = %s, want cancelled", dto.RunState)
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("RunPipeline did not return after cancel")
	}

	// Restart recovery must NOT resume a cancelled run (durable cancel).
	env.svc.ResumeActiveRuns(ctx)
	time.Sleep(300 * time.Millisecond)
	st, err := env.svc.PipelineStatus(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if st.RunState != "cancelled" {
		t.Errorf("run_state = %s after recovery, want cancelled", st.RunState)
	}

	// Cancel is idempotent.
	if _, err := env.svc.CancelPipeline(ctx, taskID); err != nil {
		t.Errorf("second cancel: %v", err)
	}
}

// TestPipelineService_FinalizeAutoCommit_UncommittedAgentChanges drives the
// full pipeline with the write-no-commit fake (the agent writes a file but
// never commits). The finalize stage must deterministically produce the
// result commit itself: the factory owns the managed worktree, so the outcome
// is completed-with-commit and the result ref points at a NEW commit that
// contains the agent's files — never completed-with-uncommitted-changes with
// the ref stuck at the base SHA.
func TestPipelineService_FinalizeAutoCommit_UncommittedAgentChanges(t *testing.T) {
	env := newPipelineTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	dto, err := env.svc.RunPipeline(ctx, transport.PipelineRunRequest{
		ProjectID:   env.projID,
		Description: "add a file without committing",
		Engine:      "fake",
		Model:       "fake/write-no-commit",
	})
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if dto.RunState != "completed" {
		t.Fatalf("run_state = %s (failure %s: %s), want completed", dto.RunState, dto.FailureCategory, dto.FailureReason)
	}
	if dto.Outcome != "completed-with-commit" {
		t.Errorf("outcome = %s, want completed-with-commit (finalize auto-commit)", dto.Outcome)
	}

	ref := "refs/heads/forge/result/" + dto.TaskID
	sha := strings.TrimSpace(gitOutInDaemonTest(t, env.repo, "rev-parse", "--verify", ref))
	if sha == "" {
		t.Fatalf("result ref %s missing", ref)
	}
	if sha == env.head {
		t.Errorf("result ref still points at the base SHA %s; want a new result commit", sha)
	}
	if dto.CommitSHA != sha {
		t.Errorf("commit_sha = %q, want result ref SHA %q", dto.CommitSHA, sha)
	}

	// The result commit is the factory's auto-commit and contains the file
	// the agent left uncommitted.
	wantSubj := "forge: result for task " + dto.TaskID
	if subj := strings.TrimSpace(gitOutInDaemonTest(t, env.repo, "log", "-1", "--format=%s", ref)); subj != wantSubj {
		t.Errorf("result commit subject = %q, want %q", subj, wantSubj)
	}
	if names := strings.TrimSpace(gitOutInDaemonTest(t, env.repo, "show", "--name-only", "--format=", ref)); !strings.Contains(names, "uncommitted.txt") {
		t.Errorf("result commit does not contain the agent's file uncommitted.txt: %q", names)
	}

	// The auto-commit was audited.
	if !env.hasAuditEvent(t, dto.TaskID, "pipeline.finalize_autocommit") {
		t.Error("no pipeline.finalize_autocommit audit event recorded")
	}

	// The primary checkout is untouched: same HEAD, clean tree.
	if got := strings.TrimSpace(gitOutInDaemonTest(t, env.repo, "rev-parse", "HEAD")); got != env.head {
		t.Errorf("primary HEAD changed: %s → %s", env.head, got)
	}
	if status := strings.TrimSpace(gitOutInDaemonTest(t, env.repo, "status", "--porcelain")); status != "" {
		t.Errorf("primary checkout dirty: %s", status)
	}

	// Re-drive the finalize handler (restart recovery path): the worktree is
	// now clean, so no duplicate commit may be created and the ref must not
	// move.
	if _, err := env.svc.handleFinalize(ctx, &pipeline.RunContext{
		TaskID: dto.TaskID, ProjectID: env.projID, Stage: pipeline.StageFinalize, Attempt: 2,
	}); err != nil {
		t.Fatalf("re-driven finalize: %v", err)
	}
	if got := strings.TrimSpace(gitOutInDaemonTest(t, env.repo, "rev-parse", "--verify", ref)); got != sha {
		t.Errorf("result ref moved on re-drive: %s → %s (duplicate commit?)", sha, got)
	}
	if n := strings.TrimSpace(gitOutInDaemonTest(t, env.repo, "rev-list", "--count", env.head+".."+ref)); n != "1" {
		t.Errorf("commits beyond base = %s after re-drive, want exactly 1", n)
	}
}

// hasAuditEvent reports whether an audit event of the given type exists for
// the task.
func (env *pipelineTestEnv) hasAuditEvent(t *testing.T, taskID, eventType string) bool {
	t.Helper()
	events, err := env.svc.rec.Filter(context.Background(), storage.AuditFilter{ScopeID: taskID})
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	for _, ev := range events {
		if ev.Type == eventType {
			return true
		}
	}
	return false
}

func waitForRunState(t *testing.T, env *pipelineTestEnv, taskID, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, err := env.svc.PipelineStatus(context.Background(), taskID)
		if err == nil && st.RunState == want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	st, _ := env.svc.PipelineStatus(context.Background(), taskID)
	t.Fatalf("run_state never became %s (last: %s at stage %s, failure %s: %s)",
		want, st.RunState, st.CurrentStage, st.FailureCategory, st.FailureReason)
}
