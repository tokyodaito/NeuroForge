package runapp_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
	"neuroforge/internal/runapp"
	"neuroforge/internal/storage"
	"neuroforge/internal/task"
	"neuroforge/internal/workspace"
)

// fakeSupervisor implements runapp.SupervisorRunner in-process. It calls a
// script function to mutate the worktree (simulating the agent's edits) and
// then emits the configured terminal event.
type fakeSupervisor struct {
	mu       sync.Mutex
	prompt   string
	model    string
	engine   string
	script   func(ctx context.Context, wsPath string) // mutates the worktree
	terminal protocol.NormalizedEvent
	events   []protocol.NormalizedEvent
}

func (f *fakeSupervisor) Run(ctx context.Context, req runapp.SupervisorRequest, wsPath string) (runapp.SupervisorResult, error) {
	f.mu.Lock()
	f.prompt = req.Prompt
	f.model = req.Model
	f.engine = req.Engine
	script := f.script
	term := f.terminal
	events := append([]protocol.NormalizedEvent(nil), f.events...)
	f.mu.Unlock()

	if script != nil {
		script(ctx, wsPath)
	}
	// Append the terminal as the last event in the returned stream so the
	// finalize usage extractor can also see usage events.
	all := append([]protocol.NormalizedEvent(nil), events...)
	if term.Type != "" {
		all = append(all, term)
	}
	return runapp.SupervisorResult{
		Handle:    protocol.RunHandle{RunID: "test-run-1", Engine: req.Engine, Model: req.Model},
		Outcome:   term,
		Events:    all,
		Failed:    term.Type == protocol.EventRunFailed,
		Cancelled: term.Type == protocol.EventRunCancelled,
	}, nil
}

// newRunFixture wires a Service with the S7 fields populated: a real workspace
// manager, a fake supervisor, a real task backlog, an in-memory usage sink.
type runFixture struct {
	t        *testing.T
	home     string
	db       *storage.DB
	wm       *workspace.Manager
	bk       *task.Backlog
	sup      *fakeSupervisor
	usage    *memUsageSink
	svc      *runapp.Service
	repoPath string
}

func newRunFixture(t *testing.T) *runFixture {
	t.Helper()
	home := t.TempDir()
	dbPath := filepath.Join(home, "state.db")
	ctx := context.Background()
	db, err := storage.Open(ctx, dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := db.CreateProject(ctx, storage.Project{
		ID: "proj", Name: "Test", Path: "/tmp/test", State: "IDLE",
		Profile: "LOCAL_REVIEW", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(ctx, storage.Task{
		ID: "task-1", ProjectID: "proj", Description: "test", State: "NEW",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "test@test.local")
	runGit(t, repo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# T\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "init")

	wm := workspace.NewManager(db, nil, home, nil)
	bk := task.NewBacklog(db, nil, filepath.Join(home, "artifacts"), nil)
	sup := &fakeSupervisor{}
	usage := &memUsageSink{}
	svc := runapp.NewServiceWithRunner(runapp.RunOptions{
		Workspaces: wm,
		Supervisor: sup,
		Tasks:      bk,
		DB:         db,
		Usage:      usage,
	})
	return &runFixture{t: t, home: home, db: db, wm: wm, bk: bk, sup: sup, usage: usage, svc: svc, repoPath: repo}
}

type memUsageSink struct {
	mu     sync.Mutex
	events []runapp.UsageEvent
}

func (m *memUsageSink) RecordUsage(_ context.Context, e runapp.UsageEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, e)
	return nil
}

func (m *memUsageSink) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.events)
}

// TestRun_PromptReachesAdapter verifies FR-6: the prompt/description is
// forwarded to the adapter verbatim. The fake supervisor records the prompt
// it received; we assert byte-for-byte equality.
func TestRun_PromptReachesAdapter(t *testing.T) {
	f := newRunFixture(t)
	wantPrompt := "Add a RESULT.md file and commit it"
	f.sup.script = func(_ context.Context, wsPath string) {}
	f.sup.terminal = protocol.NormalizedEvent{Type: protocol.EventRunFailed, Failure: &protocol.FailurePayload{Class: protocol.FailureInternalError}}

	res, err := f.svc.Run(context.Background(), runapp.RunRequest{
		ProjectID:   "proj",
		ProjectPath: f.repoPath,
		TaskID:      "task-1",
		Description: wantPrompt,
		Engine:      "opencode",
		Model:       "zai-coding-plan/glm-5.2",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if f.sup.prompt != wantPrompt {
		t.Errorf("prompt = %q, want %q", f.sup.prompt, wantPrompt)
	}
	if f.sup.model != "zai-coding-plan/glm-5.2" {
		t.Errorf("model = %q", f.sup.model)
	}
	if f.sup.engine != "opencode" {
		t.Errorf("engine = %q", f.sup.engine)
	}
	if res.Outcome != runapp.OutcomeFailed {
		t.Errorf("outcome = %q, want failed", res.Outcome)
	}
}

// TestRun_ModelReachesAdapter verifies FR-7: the model id is forwarded to
// the adapter. Indirectly covered by the prompt test; here we explicitly
// assert.
func TestRun_ModelReachesAdapter(t *testing.T) {
	f := newRunFixture(t)
	f.sup.script = func(_ context.Context, wsPath string) {}
	f.sup.terminal = protocol.NormalizedEvent{Type: protocol.EventRunFailed, Failure: &protocol.FailurePayload{Class: protocol.FailureInternalError}}
	if _, err := f.svc.Run(context.Background(), runapp.RunRequest{
		ProjectID:   "proj",
		ProjectPath: f.repoPath,
		TaskID:      "task-1",
		Description: "x",
		Engine:      "opencode",
		Model:       "zai-coding-plan/glm-5.2",
	}); err != nil {
		t.Fatal(err)
	}
	if f.sup.model != "zai-coding-plan/glm-5.2" {
		t.Errorf("model = %q, want zai-coding-plan/glm-5.2", f.sup.model)
	}
}

// TestRun_CompletedWithCommit exercises the happy path end-to-end: fake agent
// creates a file and commits; outcome is completed-with-commit; workspace is
// terminal completed; task is COMPLETED; result ref exists.
func TestRun_CompletedWithCommit(t *testing.T) {
	f := newRunFixture(t)
	f.sup.script = func(_ context.Context, wsPath string) {
		full := filepath.Join(wsPath, "RESULT.md")
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return
		}
		_ = os.WriteFile(full, []byte("hello\n"), 0o644)
		runGit(f.t, wsPath, "add", "-A")
		runGit(f.t, wsPath, "commit", "-m", "agent work")
	}
	f.sup.terminal = protocol.NormalizedEvent{Type: protocol.EventRunCompleted}
	primaryBefore := readHeadSHA(t, f.repoPath)

	res, err := f.svc.Run(context.Background(), runapp.RunRequest{
		ProjectID:   "proj",
		ProjectPath: f.repoPath,
		TaskID:      "task-1",
		Description: "Add RESULT.md",
		Engine:      "opencode",
		Model:       "zai-coding-plan/glm-5.2",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != runapp.OutcomeCompletedWithCommit {
		t.Errorf("outcome = %q, want completed-with-commit", res.Outcome)
	}
	if res.CommitSHA == "" {
		t.Errorf("commit_sha empty")
	}
	if res.ActualHEADSHA == res.BaseSHA {
		t.Errorf("actual_head_sha == base_sha; FR-10 violated")
	}
	if res.ActualHEADSHA != res.CommitSHA {
		t.Errorf("actual_head_sha %q != commit_sha %q", res.ActualHEADSHA, res.CommitSHA)
	}
	if res.ResultBranch != "refs/heads/forge/result/task-1" {
		t.Errorf("result_branch = %q, want refs/heads/forge/result/task-1", res.ResultBranch)
	}
	// workspace terminal
	ws, _ := f.wm.Get(context.Background(), res.WorkspaceID)
	if ws.State != workspace.StateCompleted {
		t.Errorf("workspace state = %q, want completed", ws.State)
	}
	// task terminal
	tk, _ := f.bk.Get(context.Background(), "task-1")
	if tk.State != task.StateCompleted {
		t.Errorf("task state = %q, want COMPLETED", tk.State)
	}
	// primary checkout untouched (I.12)
	if got := readHeadSHA(t, f.repoPath); got != primaryBefore {
		t.Errorf("primary HEAD changed: %s -> %s", primaryBefore, got)
	}
}

// TestRun_NoChanges verifies KF-01/I.4: a process that completes without
// producing changes is classified completed-no-changes; workspace failed;
// task FAILED; exit non-zero.
func TestRun_NoChanges(t *testing.T) {
	f := newRunFixture(t)
	f.sup.script = func(_ context.Context, wsPath string) {}
	f.sup.terminal = protocol.NormalizedEvent{Type: protocol.EventRunCompleted}

	res, err := f.svc.Run(context.Background(), runapp.RunRequest{
		ProjectID:   "proj",
		ProjectPath: f.repoPath,
		TaskID:      "task-1",
		Description: "do nothing",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != runapp.OutcomeCompletedNoChanges {
		t.Errorf("outcome = %q, want completed-no-changes", res.Outcome)
	}
	ws, _ := f.wm.Get(context.Background(), res.WorkspaceID)
	if ws.State != workspace.StateFailed {
		t.Errorf("workspace state = %q, want failed (no-change run)", ws.State)
	}
	tk, _ := f.bk.Get(context.Background(), "task-1")
	if tk.State != task.StateFailed {
		t.Errorf("task state = %q, want FAILED", tk.State)
	}
}

// TestRun_UsageRecorded verifies KF-10: usage events emitted by the adapter
// are persisted via the UsageSink.
func TestRun_UsageRecorded(t *testing.T) {
	f := newRunFixture(t)
	f.sup.script = func(_ context.Context, wsPath string) {
		full := filepath.Join(wsPath, "x.txt")
		_ = os.WriteFile(full, []byte("x\n"), 0o644)
		runGit(f.t, wsPath, "add", "-A")
		runGit(f.t, wsPath, "commit", "-m", "x")
	}
	f.sup.terminal = protocol.NormalizedEvent{Type: protocol.EventRunCompleted}
	f.sup.events = []protocol.NormalizedEvent{
		{
			Type: protocol.EventUsageUpdated,
			Usage: &protocol.UsagePayload{
				InputTokens: 100, OutputTokens: 50, Cost: 0.001,
			},
		},
	}
	if _, err := f.svc.Run(context.Background(), runapp.RunRequest{
		ProjectID:   "proj",
		ProjectPath: f.repoPath,
		TaskID:      "task-1",
		Description: "x",
	}); err != nil {
		t.Fatal(err)
	}
	if n := f.usage.Count(); n != 1 {
		t.Errorf("usage events recorded = %d, want 1 (KF-10)", n)
	}
}

// TestRun_FailedAdapter verifies the failed adapter path end-to-end.
func TestRun_FailedAdapter(t *testing.T) {
	f := newRunFixture(t)
	f.sup.script = func(_ context.Context, wsPath string) {}
	f.sup.terminal = protocol.NormalizedEvent{
		Type:    protocol.EventRunFailed,
		Failure: &protocol.FailurePayload{Class: protocol.FailureEngineCrash, Reason: "boom"},
	}
	res, err := f.svc.Run(context.Background(), runapp.RunRequest{
		ProjectID:   "proj",
		ProjectPath: f.repoPath,
		TaskID:      "task-1",
		Description: "x",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != runapp.OutcomeFailed {
		t.Errorf("outcome = %q, want failed", res.Outcome)
	}
	ws, _ := f.wm.Get(context.Background(), res.WorkspaceID)
	if ws.State != workspace.StateFailed {
		t.Errorf("state = %q, want failed", ws.State)
	}
}

// TestRun_TimedOut verifies the timeout path end-to-end (S6 integration).
func TestRun_TimedOut(t *testing.T) {
	f := newRunFixture(t)
	f.sup.script = func(_ context.Context, wsPath string) {}
	f.sup.terminal = protocol.NormalizedEvent{
		Type:    protocol.EventRunFailed,
		Failure: &protocol.FailurePayload{Class: protocol.FailureTimeout, Reason: "deadline"},
	}
	res, err := f.svc.Run(context.Background(), runapp.RunRequest{
		ProjectID:   "proj",
		ProjectPath: f.repoPath,
		TaskID:      "task-1",
		Description: "x",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != runapp.OutcomeTimedOut {
		t.Errorf("outcome = %q, want timed-out", res.Outcome)
	}
}

// TestRun_Cancelled verifies the cancelled path end-to-end.
func TestRun_Cancelled(t *testing.T) {
	f := newRunFixture(t)
	f.sup.script = func(_ context.Context, wsPath string) {}
	f.sup.terminal = protocol.NormalizedEvent{Type: protocol.EventRunCancelled}
	res, err := f.svc.Run(context.Background(), runapp.RunRequest{
		ProjectID:   "proj",
		ProjectPath: f.repoPath,
		TaskID:      "task-1",
		Description: "x",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != runapp.OutcomeCancelled {
		t.Errorf("outcome = %q, want cancelled", res.Outcome)
	}
	ws, _ := f.wm.Get(context.Background(), res.WorkspaceID)
	if ws.State != workspace.StateCancelled {
		t.Errorf("state = %q, want cancelled", ws.State)
	}
}

// TestRun_PrimaryCheckoutUntouched verifies FR-4 / I.12: the primary
// checkout's HEAD and file set are unchanged after a run.
func TestRun_PrimaryCheckoutUntouched(t *testing.T) {
	f := newRunFixture(t)
	primaryHeadBefore := readHeadSHA(t, f.repoPath)
	primaryFilesBefore := gitOutput(t, "git", "-C", f.repoPath, "ls-files")
	f.sup.script = func(_ context.Context, wsPath string) {
		_ = os.WriteFile(filepath.Join(wsPath, "new.txt"), []byte("x\n"), 0o644)
		runGit(f.t, wsPath, "add", "-A")
		runGit(f.t, wsPath, "commit", "-m", "agent")
	}
	f.sup.terminal = protocol.NormalizedEvent{Type: protocol.EventRunCompleted}
	_, err := f.svc.Run(context.Background(), runapp.RunRequest{
		ProjectID:   "proj",
		ProjectPath: f.repoPath,
		TaskID:      "task-1",
		Description: "x",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := readHeadSHA(t, f.repoPath); got != primaryHeadBefore {
		t.Errorf("primary HEAD changed: %s -> %s", primaryHeadBefore, got)
	}
	got := gitOutput(t, "git", "-C", f.repoPath, "ls-files")
	if got != primaryFilesBefore {
		t.Errorf("primary file set changed:\nwas:\n%sgot:\n%s", primaryFilesBefore, got)
	}
}

// _ keeps the package's errors import alive for future assertion helpers.
var _ = errors.New
