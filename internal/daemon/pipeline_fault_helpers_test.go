package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/fake"
	"neuroforge/internal/adapter/codingagent/protocol"
	"neuroforge/internal/audit"
	"neuroforge/internal/pipeline"
	"neuroforge/internal/project"
	"neuroforge/internal/review"
	"neuroforge/internal/storage"
	"neuroforge/internal/supervisor"
	"neuroforge/internal/task"
	"neuroforge/internal/transport"
	"neuroforge/internal/workgraph"
	"neuroforge/internal/workspace"
)

// Shared helpers for the deterministic pipeline fault-injection suite
// (Phase E). These tests prove the durability claims of the durable pipeline
// (internal/pipeline + daemon PipelineService) against REAL daemon services:
// restart recovery, idempotent re-drive, duplicate suppression, failure
// routing, repair loops, cancellation and the emergency stop.
//
// Fault injection is deterministic and hermetic: no network, no real coding
// agent. "Daemon kill" is simulated by abandoning the in-flight service
// (a blocked scripted adapter keeps the old goroutine parked without further
// durable writes, exactly like a process that died mid-stage) and building a
// FRESH PipelineService over the SAME NEUROFORGE_HOME (same SQLite DB, same
// dirs) — the shape the real startup reconciler + ResumeActiveRuns see after
// a SIGKILL.

// faultEnv bundles the real services a PipelineService needs, plus the
// fixture repo. Unlike pipelineTestEnv (pipeline_integration_test.go) it
// allows injecting a scripted adapter and a reviewer, and it supports
// simulated daemon restarts via restart().
type faultEnv struct {
	dirs     Dirs
	db       *storage.DB
	rec      *audit.Recorder
	tasks    *task.Backlog
	projects *project.Registry
	specs    *task.SpecificationStore
	graphs   *workgraph.WorkGraphStore
	leases   *workgraph.LeaseManager
	wm       *workspace.Manager
	svc      *PipelineService
	repo     string
	projID   string
	head     string // primary checkout HEAD at setup
}

// faultDeps selects the adapter (engine id must be "fake") and reviewer a
// service generation uses. Nil means the production default (deterministic
// fake engine / AgentReviewer over the supervisor).
type faultDeps struct {
	adapter  codingagent.Adapter
	reviewer review.Reviewer
}

func newFaultEnv(t *testing.T, deps faultDeps) *faultEnv {
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

	env := &faultEnv{
		dirs: dirs, db: db, rec: rec, tasks: tasks, projects: projects,
		specs: specs, graphs: graphs, leases: leases, wm: wm,
	}

	// Fixture repo: a buildable Go module (the verify stage runs gofmt/go
	// build/go vet/go test in the worktree). Mirrors pipelineTestEnv.
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
	env.repo = repo
	env.head = strings.TrimSpace(gitOutInDaemonTest(t, repo, "rev-parse", "HEAD"))

	p, err := projects.Add(ctx, project.AddRequest{Path: repo})
	if err != nil {
		t.Fatal(err)
	}
	env.projID = p.ID

	env.restart(t, deps)
	return env
}

// restart builds a FRESH PipelineService (new supervisor, new adapter
// registry, empty in-memory state) over the SAME durable home — the in-process
// equivalent of killing the daemon and starting a new one against the same
// NEUROFORGE_HOME. Durable state (SQLite, artifacts, params files) survives;
// in-memory state (driving map, cancels, lastFin) is gone.
func (env *faultEnv) restart(t *testing.T, deps faultDeps) {
	t.Helper()
	if deps.adapter == nil {
		deps.adapter = fake.New(fake.AdapterOptions{Installed: true})
	}
	reg := codingagent.NewRegistry()
	if err := reg.Register(deps.adapter, 0); err != nil {
		t.Fatal(err)
	}
	sup := supervisor.New(supervisor.Options{
		Adapters: reg,
		Audit:    env.rec,
		Logger:   quietLogger(),
		FullEnv:  os.Environ(),
	})
	svc, err := NewPipelineService(PipelineDeps{
		DB:       env.db,
		Recorder: env.rec,
		Logger:   quietLogger(),
		Dirs:     env.dirs,
		Tasks:    env.tasks,
		Projects: env.projects,
		Specs:    env.specs,
		Graphs:   env.graphs,
		Leases:   env.leases,
		WM:       env.wm,
		Sup:      sup,
		Reviewer: deps.reviewer,
	})
	if err != nil {
		t.Fatal(err)
	}
	env.svc = svc
}

// reconcilePipeline runs the daemon's startup pipeline reconciler (marks the
// in-flight stage of every active run interrupted), mirroring daemon boot.
func (env *faultEnv) reconcilePipeline(t *testing.T) []ReconcileDecision {
	t.Helper()
	r := &pipelineReconciler{store: env.svc.store}
	decisions, err := r.Reconcile(context.Background(), ReconcileTx{
		DB: env.db, Audit: env.rec, Dirs: env.dirs, Logger: quietLogger(),
	})
	if err != nil {
		t.Fatalf("pipeline reconciler: %v", err)
	}
	return decisions
}

func (env *faultEnv) status(t *testing.T, taskID string) transport.PipelineRunResultDTO {
	t.Helper()
	dto, err := env.svc.PipelineStatus(context.Background(), taskID)
	if err != nil {
		t.Fatalf("PipelineStatus(%s): %v", taskID, err)
	}
	return dto
}

func (env *faultEnv) waitRunState(t *testing.T, taskID, want string, timeout time.Duration) transport.PipelineRunResultDTO {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, err := env.svc.PipelineStatus(context.Background(), taskID)
		if err == nil && st.RunState == want {
			return st
		}
		time.Sleep(50 * time.Millisecond)
	}
	st := env.status(t, taskID)
	t.Fatalf("run_state never became %s (last: %s at stage %s, failure %s: %s)",
		want, st.RunState, st.CurrentStage, st.FailureCategory, st.FailureReason)
	return st
}

func (env *faultEnv) waitStage(t *testing.T, taskID, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, err := env.svc.PipelineStatus(context.Background(), taskID)
		if err == nil && st.CurrentStage == want && st.RunState == "active" {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	st := env.status(t, taskID)
	t.Fatalf("run never reached active stage %s (last: %s at %s)", want, st.RunState, st.CurrentStage)
}

func (env *faultEnv) workspaces(t *testing.T, taskID string) []workspace.Workspace {
	t.Helper()
	wss, err := env.wm.ListByTask(context.Background(), taskID)
	if err != nil {
		t.Fatalf("ListByTask(%s): %v", taskID, err)
	}
	return wss
}

// stageRecords is a small filter over the DTO stage history.
func stageRecords(dto transport.PipelineRunResultDTO, stage, status string) []transport.PipelineStageRecordDTO {
	var out []transport.PipelineStageRecordDTO
	for _, r := range dto.StageRecords {
		if r.Stage == stage && (status == "" || r.Status == status) {
			out = append(out, r)
		}
	}
	return out
}

func faultGitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v in %s: %v", args, dir, err)
	}
	return string(out)
}

// faultGitCombined runs git and returns its combined output and error without
// failing the test (for refs that may legitimately not exist).
func faultGitCombined(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ---- scripted coding-agent adapter (test-only fault injection) ----

// scriptedReviewMarker mirrors the (unexported) fake.reviewPromptMarker:
// review prompts must be answered with a findings array; scripted behavior
// applies to coding (execute/repair) prompts only.
const scriptedReviewMarker = "Respond with ONLY a JSON array of findings"

// codingBehavior scripts one non-review agent run. emit publishes a
// normalized event to the supervisor's sink.
type codingBehavior func(ctx context.Context, call int, req protocol.AgentRunRequest, emit func(protocol.NormalizedEvent))

// scriptedCodingAdapter is a fake-compatible adapter whose coding-run
// behavior is scripted per test (review prompts are delegated to the embedded
// fake adapter, which answers "[]" deterministically). It is the fault
// injector: it can hang (daemon-kill / estop / cancel scenarios), write
// broken or fixed code (repair scenarios) and fail with any class.
type scriptedCodingAdapter struct {
	*fake.Adapter

	mu    sync.Mutex
	runs  map[string]context.CancelFunc
	calls int
	run   codingBehavior
}

func newScriptedCodingAdapter(b codingBehavior) *scriptedCodingAdapter {
	return &scriptedCodingAdapter{
		Adapter: fake.New(fake.AdapterOptions{Installed: true}),
		runs:    map[string]context.CancelFunc{},
		run:     b,
	}
}

// Start implements codingagent.Adapter. Review prompts go to the embedded
// fake (deterministic approve); coding prompts run the script.
func (a *scriptedCodingAdapter) Start(ctx context.Context, req protocol.AgentRunRequest, sink codingagent.EventSink) (protocol.RunHandle, error) {
	if strings.Contains(req.Prompt, scriptedReviewMarker) {
		return a.Adapter.Start(ctx, req, sink)
	}
	a.mu.Lock()
	a.calls++
	call := a.calls
	a.mu.Unlock()

	runCtx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	a.runs[req.RunID] = cancel
	a.mu.Unlock()

	handle := protocol.RunHandle{RunID: req.RunID, Engine: "fake", Model: req.Model, SessionID: "fake-scripted-session"}
	emit := func(ev protocol.NormalizedEvent) {
		ev.RunID = req.RunID
		ev.Engine = "fake"
		ev.Model = req.Model
		ev.Timestamp = time.Now().UTC()
		_ = sink.OnEvent(ctx, ev)
	}
	go func() {
		defer func() {
			a.mu.Lock()
			delete(a.runs, req.RunID)
			a.mu.Unlock()
			cancel()
		}()
		a.run(runCtx, call, req, emit)
	}()
	return handle, nil
}

// Cancel implements codingagent.Adapter.
func (a *scriptedCodingAdapter) Cancel(_ context.Context, handle protocol.RunHandle) error {
	a.mu.Lock()
	cancel, ok := a.runs[handle.RunID]
	a.mu.Unlock()
	if !ok {
		return fmt.Errorf("scripted fake: unknown run %q", handle.RunID)
	}
	cancel()
	return nil
}

// cancelAll terminates every in-flight scripted run (test cleanup for the
// abandoned "killed daemon" goroutines).
func (a *scriptedCodingAdapter) cancelAll() {
	a.mu.Lock()
	cs := make([]context.CancelFunc, 0, len(a.runs))
	for _, c := range a.runs {
		cs = append(cs, c)
	}
	a.mu.Unlock()
	for _, c := range cs {
		c()
	}
}

func (a *scriptedCodingAdapter) codingCalls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// emitFailure is a small helper for behaviors: run.failed with a class.
func emitFailure(emit func(protocol.NormalizedEvent), class protocol.FailureClass, reason string) {
	emit(protocol.NormalizedEvent{
		Type:    protocol.EventRunFailed,
		Failure: &protocol.FailurePayload{Class: class, Reason: reason},
	})
}

// writeCommitBehavior writes the given workspace-relative files, git-commits
// them (a no-op "nothing to commit" is tolerated, mirroring the fake
// write-commit scenario) and completes.
func writeCommitBehavior(files map[string]string) codingBehavior {
	return func(_ context.Context, _ int, req protocol.AgentRunRequest, emit func(protocol.NormalizedEvent)) {
		emit(protocol.NormalizedEvent{Type: protocol.EventRunStarted})
		for rel, content := range files {
			abs := filepath.Join(req.Workspace, rel)
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				emitFailure(emit, protocol.FailureInternalError, "mkdir: "+err.Error())
				return
			}
			if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
				emitFailure(emit, protocol.FailureInternalError, "write: "+err.Error())
				return
			}
		}
		if len(files) > 0 {
			if out, err := exec.Command("git", "-C", req.Workspace, "add", "-A").CombinedOutput(); err != nil {
				emitFailure(emit, protocol.FailureInternalError, "git add: "+err.Error()+": "+string(out))
				return
			}
			out, err := exec.Command("git", "-C", req.Workspace, "commit", "-m", "agent work",
				"--author=NeuroForge Fake <fake@neuroforge.local>").CombinedOutput()
			if err != nil && !strings.Contains(string(out), "nothing to commit") {
				emitFailure(emit, protocol.FailureInternalError, "git commit: "+err.Error()+": "+string(out))
				return
			}
		}
		emit(protocol.NormalizedEvent{Type: protocol.EventRunCompleted})
	}
}

// perCallBehavior runs first on coding call 1 and rest on every later call.
func perCallBehavior(first, rest codingBehavior) codingBehavior {
	return func(ctx context.Context, call int, req protocol.AgentRunRequest, emit func(protocol.NormalizedEvent)) {
		if call == 1 {
			first(ctx, call, req, emit)
			return
		}
		rest(ctx, call, req, emit)
	}
}

// blockUntilCancelledBehavior emits run.started and hangs until the run
// context is cancelled (API cancel / estop / test cleanup), then emits
// run.cancelled — the fake cancellation scenario's semantics.
func blockUntilCancelledBehavior() codingBehavior {
	return func(ctx context.Context, _ int, _ protocol.AgentRunRequest, emit func(protocol.NormalizedEvent)) {
		emit(protocol.NormalizedEvent{Type: protocol.EventRunStarted})
		<-ctx.Done()
		emit(protocol.NormalizedEvent{Type: protocol.EventRunCancelled})
	}
}

// unformattedGo is valid but gofmt-dirty Go source (spaces instead of a tab):
// the verify stage's syntax level (gofmt -l) fails on it deterministically.
const unformattedGo = "package main\n\nfunc Helper() {\n        println(\"broken\")\n}\n"

// formattedGo is the gofmt-clean counterpart the repair loop lands.
const formattedGo = "package main\n\nfunc Helper() {\n\tprintln(\"fixed\")\n}\n"

// ---- scripted reviewer ----

// flipReviewer rejects with one major finding per role for the first
// rejectFirst calls, then approves. Deterministic alternative to
// review.FakeReviewer.SetFindings polling: each review pass makes 3 role
// calls (correctness, architecture, security — all enabled by the default
// LOCAL_REVIEW profile policy), so rejectFirst=6 rejects the initial review
// pass AND the repair-stage re-derivation pass, then approves.
type flipReviewer struct {
	mu          sync.Mutex
	calls       int
	rejectFirst int
}

func (r *flipReviewer) Review(_ context.Context, role review.Role, _ review.ReviewRequest) ([]review.Finding, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.calls <= r.rejectFirst {
		return []review.Finding{{
			Role:        role,
			Severity:    review.SeverityMajor,
			Title:       "scripted major finding",
			Description: "flipReviewer scripted rejection",
			Remediation: "fix the scripted finding",
		}}, nil
	}
	return nil, nil
}

func (r *flipReviewer) reviewCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// ---- killed-run fixture (restart between stages) ----

// faultStageChain is the happy-path stage order the driver walks.
var faultStageChain = []pipeline.Stage{
	pipeline.StageCompile, pipeline.StagePlan, pipeline.StageReady,
	pipeline.StageExecute, pipeline.StageVerify, pipeline.StageReview,
	pipeline.StageFinalize,
}

// setupKilledRun creates a run and advances its DURABLE state to look exactly
// like a run whose daemon died while stage `at` was in flight: every prior
// stage completed, `at` entered and open. The workspace (for stages at/after
// ready) and a committed agent change (for stages at/after verify) are
// created exactly as the real handlers would have left them.
//
// This is the service-level simulation the task brief allows when a stage
// boundary cannot be paused through the agent: the durable rows are
// indistinguishable from a real kill between Drive stage transitions.
func (env *faultEnv) setupKilledRun(t *testing.T, at pipeline.Stage, description string) (taskID string, ws workspace.Workspace) {
	t.Helper()
	ctx := context.Background()

	tk, err := env.tasks.Add(ctx, task.AddRequest{ProjectID: env.projID, Description: description})
	if err != nil {
		t.Fatal(err)
	}
	params := pipelineParams{
		ProjectID:   env.projID,
		ProjectPath: env.repo,
		Description: description,
		Engine:      "fake",
		Model:       "fake/write-commit",
	}
	if err := env.svc.saveParams(tk.ID, params); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.store.CreateRun(ctx, tk.ID, env.projID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := env.tasks.Transition(ctx, tk.ID, task.ActionDispatch); err != nil {
		t.Fatal(err)
	}

	// The workspace exists once the ready stage ran; the agent's committed
	// change exists once the execute stage ran.
	needWorkspace := at != pipeline.StageCompile && at != pipeline.StagePlan
	needCommit := at != pipeline.StageCompile && at != pipeline.StagePlan &&
		at != pipeline.StageReady && at != pipeline.StageExecute
	if needWorkspace {
		created, err := env.wm.Create(ctx, workspace.CreateRequest{
			ProjectID:   env.projID,
			ProjectPath: env.repo,
			TaskID:      tk.ID,
		})
		if err != nil {
			t.Fatal(err)
		}
		ws = created
	}
	if needCommit {
		mustWriteDaemonTest(t, filepath.Join(ws.Path, "RESULT.md"), "hello\n")
		runGitInDaemonTest(t, ws.Path, "add", "-A")
		runGitInDaemonTest(t, ws.Path, "commit", "-m", "agent work",
			"--author=NeuroForge Fake <fake@neuroforge.local>")
	}

	// Walk the chain, completing every stage before `at`.
	for i, st := range faultStageChain {
		if st == at {
			return tk.ID, ws
		}
		if at == pipeline.StageRepair && st == pipeline.StageVerify {
			// The repair stage is entered from a verify failure, not from the
			// happy chain: record the failed verify, consume one repair
			// attempt, then transition into repair.
			if err := env.svc.store.FailStage(ctx, tk.ID, pipeline.StageVerify,
				pipeline.FailureStaticAnalysis, "injected verification failure", "test-evidence"); err != nil {
				t.Fatalf("fail verify: %v", err)
			}
			if ok, _, err := env.svc.store.IncrementRepairAttempt(ctx, tk.ID); err != nil || !ok {
				t.Fatalf("increment repair attempt: ok=%v err=%v", ok, err)
			}
			if _, err := env.svc.store.Transition(ctx, tk.ID, pipeline.StageRepair, "verification failed", ""); err != nil {
				t.Fatalf("transition to repair: %v", err)
			}
			return tk.ID, ws
		}
		if err := env.svc.store.CompleteStage(ctx, tk.ID, st, "test-evidence"); err != nil {
			t.Fatalf("complete %s: %v", st, err)
		}
		next := faultStageChain[i+1]
		if _, err := env.svc.store.Transition(ctx, tk.ID, next, "", ""); err != nil {
			t.Fatalf("transition %s -> %s: %v", st, next, err)
		}
	}
	t.Fatalf("stage %s is not on the happy-path chain", at)
	return "", ws
}
