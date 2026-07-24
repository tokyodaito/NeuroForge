package daemon

import (
	"context"
	"path/filepath"
	"testing"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/fake"
	"neuroforge/internal/adapter/codingagent/protocol"
	"neuroforge/internal/audit"
	"neuroforge/internal/quota"
	"neuroforge/internal/storage"
	"neuroforge/internal/supervisor"
	"neuroforge/internal/workspace"
)

func TestWorkspaceHook_CheckpointAndState(t *testing.T) {
	wm, db, rec, ws, _ := newAttemptFixture(t)
	hook := &workspaceHook{wm: wm}

	// Checkpoint via the hook translates the supervisor moment to the workspace
	// moment.
	sha, err := hook.Checkpoint(context.Background(), ws.ID, supervisor.MomentPreQuotaSwitch, "msg")
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if sha == "" {
		t.Error("checkpoint returned empty SHA")
	}
	// SetState parks the workspace.
	if err := hook.SetState(context.Background(), ws.ID, "waiting_quota"); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	updated, _ := wm.Get(context.Background(), ws.ID)
	if updated.State != workspace.StateWaitingQuota {
		t.Errorf("state = %s, want waiting_quota", updated.State)
	}
	// HeadSHA reads back the head.
	head, err := hook.HeadSHA(context.Background(), ws.ID)
	if err != nil {
		t.Fatalf("HeadSHA: %v", err)
	}
	if head == "" {
		t.Error("HeadSHA empty")
	}
	_ = db
	_ = rec
}

func TestQuotaHook_Feedback(t *testing.T) {
	qm := quota.New(quota.Config{})
	hook := &quotaHook{qm: qm}

	if !hook.IsAvailable("codex", "main") {
		t.Error("freshly-seen account should be available")
	}
	hook.RecordFailure("codex", "main", protocol.FailureProviderQuota)
	// Quota exhaustion opens the breaker / marks exhausted.
	if hook.IsAvailable("codex", "main") {
		t.Error("quota-exhausted account should not be available")
	}
	hook.RecordSuccess("codex", "main")
}

// TestFailoverController_WithRealHooks is a focused integration of the
// failover controller using the real workspace + quota hooks (no network, no
// paid providers). It exercises the end-to-end hook wiring the daemon uses.
func TestFailoverController_WithRealHooks(t *testing.T) {
	wm, db, rec, ws, root := newAttemptFixture(t)
	wsPath := ws.Path

	reg := codingagent.NewRegistry()
	reg.MustRegister(fake.New(fake.AdapterOptions{
		Engine: "primary", Installed: true, Scenario: fake.ScenarioQuotaAfterEdits,
	}), 0)
	reg.MustRegister(fake.New(fake.AdapterOptions{
		Engine: "fallback", Installed: true, Scenario: fake.ScenarioSuccess,
	}), 0)

	sup := supervisor.New(supervisor.Options{
		Adapters: reg, Audit: rec, FullEnv: []string{"PATH=/usr/bin", "HOME=" + root},
	})
	qm := quota.New(quota.Config{})

	ctrl := supervisor.NewFailoverController(supervisor.FailoverOptions{
		Supervisor: sup,
		Quota:      &quotaHook{qm: qm},
		Hook:       &workspaceHook{wm: wm},
		DB:         db,
		Audit:      rec,
	})

	out, err := ctrl.Run(context.Background(), supervisor.FailoverPlan{
		WorkspaceID:   ws.ID,
		WorkspacePath: wsPath,
		Primary:       supervisor.Route{Engine: "primary"},
		Fallbacks:     []supervisor.Route{{Engine: "fallback"}},
		Prompt:        "do the thing",
		BaseSHA:       ws.BaseSHA,
		ArtifactsDir:  filepath.Join(root, "artifacts"),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !out.Success {
		t.Fatalf("expected success; attempts=%+v", out.Attempts)
	}
	if out.PacksWritten != 1 {
		t.Errorf("packs written = %d, want 1", out.PacksWritten)
	}

	// The primary engine's account is now exhausted in the quota manager.
	if qm.IsAvailable(quota.AccountID{Engine: "primary"}) {
		t.Error("primary should be quota-exhausted after the failure")
	}
	// A checkpoint was created in the real workspace.
	cps, err := wm.ListCheckpoints(context.Background(), ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cps) == 0 {
		t.Error("expected at least one checkpoint")
	}
}

// keep imports referenced
var (
	_ = storage.ContinuationPack{}
	_ = audit.ScopeTask
)
