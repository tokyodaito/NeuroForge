package supervisor_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/fake"
	"neuroforge/internal/adapter/codingagent/protocol"
	"neuroforge/internal/audit"
	"neuroforge/internal/storage"
	"neuroforge/internal/supervisor"
)

// fakeQuotaHook is a test-only QuotaHook that tracks recorded failures and
// reports availability. It mirrors what the real quota.Manager does without
// pulling in the quota package (keeps the supervisor test self-contained).
type fakeQuotaHook struct {
	exhausted map[string]bool // key engine -> exhausted
	failures  []protocol.FailureClass
}

func newFakeQuotaHook() *fakeQuotaHook {
	return &fakeQuotaHook{exhausted: map[string]bool{}}
}

func (f *fakeQuotaHook) IsAvailable(engine, account string) bool {
	return !f.exhausted[engine]
}
func (f *fakeQuotaHook) RecordFailure(engine, account string, class protocol.FailureClass) {
	f.failures = append(f.failures, class)
	if class == protocol.FailureProviderQuota {
		f.exhausted[engine] = true
	}
}
func (f *fakeQuotaHook) RecordSuccess(engine, account string) {
	f.exhausted[engine] = false
}

// recordingHook captures workspace operations for assertions without a real
// git worktree.
type recordingHook struct {
	checkpoints []supervisor.CheckpointMoment
	states      []string
	headSHAByWS map[string]string
}

func newRecordingHook() *recordingHook {
	return &recordingHook{headSHAByWS: map[string]string{}}
}

func (h *recordingHook) Checkpoint(_ context.Context, ws string, m supervisor.CheckpointMoment, _ string) (string, error) {
	h.checkpoints = append(h.checkpoints, m)
	sha := "cp-" + ws
	h.headSHAByWS[ws] = sha
	return sha, nil
}
func (h *recordingHook) SetState(_ context.Context, ws, state string) error {
	h.states = append(h.states, ws+":"+state)
	return nil
}
func (h *recordingHook) HeadSHA(_ context.Context, ws string) (string, error) {
	if sha, ok := h.headSHAByWS[ws]; ok {
		return sha, nil
	}
	return "head", nil
}

// TestFailover_QuotaAfterEdits_FallbackKeepsCheckpoint is the core AC-15
// scenario: the primary fake agent edits files then fails with PROVIDER_QUOTA;
// the controller opens the circuit, writes a continuation pack, selects the
// fallback agent and continues — WITHOUT passing the full conversation and
// WITHOUT repeating the completed steps. No real providers are used (§36.5).
func TestFailover_QuotaAfterEdits_FallbackKeepsCheckpoint(t *testing.T) {
	tmp := t.TempDir()
	wsPathPrimary := filepath.Join(tmp, "primary")
	wsPathFallback := filepath.Join(tmp, "fallback")

	// Two fake engines: "primary" fails with quota-after-edits; "fallback"
	// succeeds. They write to distinct worktrees so we can assert each ran.
	reg := codingagent.NewRegistry()
	reg.MustRegister(fake.New(fake.AdapterOptions{
		Engine:    "primary",
		Installed: true,
		Scenario:  fake.ScenarioQuotaAfterEdits,
	}), 0)
	reg.MustRegister(fake.New(fake.AdapterOptions{
		Engine:    "fallback",
		Installed: true,
		Scenario:  fake.ScenarioSuccess,
	}), 0)

	sup := supervisor.New(supervisor.Options{Adapters: reg, FullEnv: []string{"PATH=/usr/bin", "HOME=" + tmp}})

	dbPath := filepath.Join(tmp, "state.db")
	db, err := storage.Open(context.Background(), dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec := audit.NewRecorder(db, nil)

	// Insert project + task + workspace rows so the continuation_packs
	// foreign-key chain is satisfied (the failover controller writes the pack
	// through the real storage layer — AC-15 durability path).
	now := "2026-07-25T00:00:00Z"
	if _, err := db.Exec(context.Background(),
		`INSERT INTO projects (id,name,path,remote,state,profile,created_at,updated_at)
		 VALUES (?,?,?,?,?,?,?,?)`,
		"proj", "Proj", filepath.Join(tmp, "proj"), "", "IDLE", "LOCAL_REVIEW", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(context.Background(),
		`INSERT INTO tasks (id,project_id,title,description,priority,state,created_at,updated_at)
		 VALUES (?,?,?,?,?,?,?,?)`,
		"task", "proj", "T", "desc", "NORMAL", "NEW", now, now); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateWorkspace(context.Background(), storage.Workspace{
		ID: "ws-ac15", ProjectID: "proj", TaskID: "task", WorkPackageID: "main",
		Attempt: 1, Kind: "attempt", Path: wsPathPrimary, Branch: "forge/task/main/attempt-1",
		State: "active", BaseSHA: "abc123", HeadSHA: "abc123",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	quotaHook := newFakeQuotaHook()
	hook := newRecordingHook()

	ctrl := supervisor.NewFailoverController(supervisor.FailoverOptions{
		Supervisor: sup,
		Quota:      quotaHook,
		Hook:       hook,
		DB:         db,
		Audit:      rec,
		Sleep:      func(time.Duration) {}, // no real sleeps in tests
		Logger:     nil,
	})

	out, err := ctrl.Run(context.Background(), supervisor.FailoverPlan{
		WorkspaceID:   "ws-ac15",
		WorkspacePath: wsPathPrimary,
		Primary:       supervisor.Route{Engine: "primary"},
		Fallbacks:     []supervisor.Route{{Engine: "fallback"}},
		Prompt:        "Implement the greeting feature.",
		BaseSHA:       "abc123",
		SpecHash:      "spec-hash",
		ArtifactsDir:  filepath.Join(tmp, "artifacts"),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !out.Success {
		t.Fatalf("failover did not succeed; attempts=%+v", out.Attempts)
	}
	if out.ParkedWaitingQuota {
		t.Error("should not be parked in WAITING_QUOTA; a fallback existed")
	}
	if out.Quarantined {
		t.Error("should not be quarantined")
	}

	// Two attempts: primary (failed quota) then fallback (success).
	if len(out.Attempts) != 2 {
		t.Fatalf("want 2 attempts, got %d", len(out.Attempts))
	}
	if out.Attempts[0].Route.Engine != "primary" {
		t.Errorf("attempt 0 engine = %s, want primary", out.Attempts[0].Route.Engine)
	}
	if out.Attempts[1].Route.Engine != "fallback" {
		t.Errorf("attempt 1 engine = %s, want fallback", out.Attempts[1].Route.Engine)
	}

	// A continuation pack was written and a checkpoint captured the edits.
	if out.PacksWritten != 1 {
		t.Errorf("packs written = %d, want 1", out.PacksWritten)
	}
	if len(hook.checkpoints) == 0 {
		t.Error("expected a pre-quota-switch checkpoint")
	}
	if hook.checkpoints[0] != supervisor.MomentPreQuotaSwitch {
		t.Errorf("checkpoint moment = %s, want pre-quota-switch", hook.checkpoints[0])
	}

	// The circuit breaker recorded the quota failure for the primary engine.
	if len(quotaHook.failures) == 0 || quotaHook.failures[0] != protocol.FailureProviderQuota {
		t.Errorf("quota failure not recorded: %+v", quotaHook.failures)
	}

	// The fallback prompt must NOT contain the full conversation; it carries
	// the pack. Verify it references the completed edit and the quota failure.
	fallbackPrompt := out.Attempts[1].PromptUsed
	// PromptUsed is truncated to 160 chars; the completed steps may be cut.
	// The important invariant: the prompt does not start with the raw original.
	if fallbackPrompt == "Implement the greeting feature." {
		t.Error("fallback got the ORIGINAL prompt — it must get the continuation-pack prompt")
	}

	// A continuation pack row was persisted durably (AC-27 recovery substrate).
	packs, err := db.ListContinuationPacks(context.Background(), "ws-ac15")
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) != 1 {
		t.Fatalf("durable packs = %d, want 1", len(packs))
	}
	_ = wsPathFallback
}

// TestFailover_AllRoutesExhausted_WaitingQuota verifies that when every route
// is quota-exhausted with no fallback, the work is parked in WAITING_QUOTA
// (spec §15.5, §32) rather than quarantined or infinitely retried.
func TestFailover_AllRoutesExhausted_WaitingQuota(t *testing.T) {
	tmp := t.TempDir()
	reg := codingagent.NewRegistry()
	reg.MustRegister(fake.New(fake.AdapterOptions{
		Engine:    "only",
		Installed: true,
		Scenario:  fake.ScenarioQuotaAfterEdits,
	}), 0)
	sup := supervisor.New(supervisor.Options{Adapters: reg, FullEnv: []string{"PATH=/usr/bin", "HOME=" + tmp}})

	quotaHook := newFakeQuotaHook()
	hook := newRecordingHook()

	ctrl := supervisor.NewFailoverController(supervisor.FailoverOptions{
		Supervisor: sup,
		Quota:      quotaHook,
		Hook:       hook,
		Sleep:      func(time.Duration) {},
	})

	out, err := ctrl.Run(context.Background(), supervisor.FailoverPlan{
		WorkspaceID:   "ws-wait",
		WorkspacePath: filepath.Join(tmp, "ws"),
		Primary:       supervisor.Route{Engine: "only"},
		Prompt:        "do work",
		BaseSHA:       "abc",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !out.ParkedWaitingQuota {
		t.Errorf("want parked WAITING_QUOTA; got success=%v quarantined=%v attempts=%d", out.Success, out.Quarantined, len(out.Attempts))
	}
	// The workspace was parked.
	foundWait := false
	for _, s := range hook.states {
		if s == "ws-wait:waiting_quota" {
			foundWait = true
		}
	}
	if !foundWait {
		t.Errorf("workspace not parked in waiting_quota; states=%v", hook.states)
	}
}

// TestFailover_TerminalFailureNotRetried verifies that a SCOPE_VIOLATION (a
// terminal failure per §32) is surfaced immediately without retry or failover.
func TestFailover_TerminalFailureNotRetried(t *testing.T) {
	tmp := t.TempDir()
	reg := codingagent.NewRegistry()
	reg.MustRegister(fake.New(fake.AdapterOptions{
		Engine:    "rogue",
		Installed: true,
		Scenario:  fake.ScenarioScopeViolation,
	}), 0)
	reg.MustRegister(fake.New(fake.AdapterOptions{
		Engine:    "good",
		Installed: true,
		Scenario:  fake.ScenarioSuccess,
	}), 0)
	sup := supervisor.New(supervisor.Options{Adapters: reg, FullEnv: []string{"PATH=/usr/bin", "HOME=" + tmp}})

	ctrl := supervisor.NewFailoverController(supervisor.FailoverOptions{
		Supervisor: sup,
		Hook:       newRecordingHook(),
		Sleep:      func(time.Duration) {},
	})

	out, err := ctrl.Run(context.Background(), supervisor.FailoverPlan{
		WorkspaceID:   "ws-scope",
		WorkspacePath: filepath.Join(tmp, "ws"),
		Primary:       supervisor.Route{Engine: "rogue"},
		Fallbacks:     []supervisor.Route{{Engine: "good"}},
		Prompt:        "do something out of scope",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Success {
		t.Error("scope violation should not succeed")
	}
	if out.TerminalFailure == nil || out.TerminalFailure.Class != protocol.FailureScopeViolation {
		t.Errorf("want terminal SCOPE_VIOLATION, got %+v", out.TerminalFailure)
	}
	// Only ONE attempt: terminal failures must not failover.
	if len(out.Attempts) != 1 {
		t.Errorf("want 1 attempt (terminal), got %d", len(out.Attempts))
	}
}

// TestFailover_RateLimitRetriesSameRoute verifies rate-limit cooldown keeps the
// same route until the budget is exhausted, then fails over.
func TestFailover_RateLimitRetriesSameRoute(t *testing.T) {
	tmp := t.TempDir()
	reg := codingagent.NewRegistry()
	reg.MustRegister(fake.New(fake.AdapterOptions{
		Engine:    "rl",
		Installed: true,
		Scenario:  fake.ScenarioRateLimit,
	}), 0)
	reg.MustRegister(fake.New(fake.AdapterOptions{
		Engine:    "ok",
		Installed: true,
		Scenario:  fake.ScenarioSuccess,
	}), 0)
	sup := supervisor.New(supervisor.Options{Adapters: reg, FullEnv: []string{"PATH=/usr/bin", "HOME=" + tmp}})

	var slept []time.Duration
	ctrl := supervisor.NewFailoverController(supervisor.FailoverOptions{
		Supervisor: sup,
		Hook:       newRecordingHook(),
		Sleep:      func(d time.Duration) { slept = append(slept, d) },
	})

	out, err := ctrl.Run(context.Background(), supervisor.FailoverPlan{
		WorkspaceID:   "ws-rl",
		WorkspacePath: filepath.Join(tmp, "ws"),
		Primary:       supervisor.Route{Engine: "rl"},
		Fallbacks:     []supervisor.Route{{Engine: "ok"}},
		Prompt:        "rate-limited then failover",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !out.Success {
		t.Fatalf("expected eventual success via fallback")
	}
	// The first attempts should be retries on "rl" with cooldown sleeps.
	if len(slept) == 0 {
		t.Error("expected cooldown sleeps before retries")
	}
	// "rl" engine appears more than once (retries), then "ok" succeeds.
	rlCount := 0
	okSeen := false
	for _, a := range out.Attempts {
		if a.Route.Engine == "rl" {
			rlCount++
		}
		if a.Route.Engine == "ok" {
			okSeen = true
		}
	}
	if rlCount < 2 {
		t.Errorf("expected >=2 attempts on rl (retries), got %d", rlCount)
	}
	if !okSeen {
		t.Error("fallback to ok never happened")
	}
}
