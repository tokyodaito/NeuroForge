package supervisor_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/fake"
	"neuroforge/internal/audit"
	"neuroforge/internal/storage"
	"neuroforge/internal/supervisor"
)

func TestEnvAllowlist_StripsForbidden(t *testing.T) {
	full := []string{
		"PATH=/usr/bin:/bin",
		"HOME=/home/user",
		"GITHUB_TOKEN=ghp_secret123",
		"GITLAB_TOKEN=glpat-secret",
		"NEUROFORGE_DAEMON_TOKEN=secret",
		"AWS_SECRET_ACCESS_KEY=secret",
		"TERM=xterm-256color",
		"DATABASE_URL=postgres://user:pass@host/db",
		"USER=bogdan",
		"LANG=en_US.UTF-8",
	}

	safe := supervisor.EnvAllowlist(full)

	// Must contain the allowed vars.
	if !contains(safe, "PATH=/usr/bin:/bin") {
		t.Error("PATH stripped")
	}
	if !contains(safe, "HOME=/home/user") {
		t.Error("HOME stripped")
	}
	if !contains(safe, "USER=bogdan") {
		t.Error("USER stripped")
	}

	// Must NOT contain any forbidden var.
	if err := supervisor.AssertEnvSafe(safe); err != nil {
		t.Errorf("AssertEnvSafe failed: %v", err)
	}
	for _, kv := range safe {
		name, _, _ := cut(kv, "=")
		switch name {
		case "GITHUB_TOKEN", "GITLAB_TOKEN", "NEUROFORGE_DAEMON_TOKEN",
			"AWS_SECRET_ACCESS_KEY", "DATABASE_URL":
			t.Errorf("forbidden env var %q leaked into allowlist", name)
		}
	}
}

func TestAssertEnvSafe_DetectsLeak(t *testing.T) {
	leaky := []string{"PATH=/usr/bin", "GITHUB_TOKEN=secret"}
	if err := supervisor.AssertEnvSafe(leaky); err == nil {
		t.Error("AssertEnvSafe should detect GITHUB_TOKEN leak")
	}
}

func TestSupervisor_RunFakeAgent(t *testing.T) {
	// Set up a workspace dir for the agent to write into.
	tmp := t.TempDir()
	wsPath := filepath.Join(tmp, "ws")
	if err := os.MkdirAll(wsPath, 0o755); err != nil {
		t.Fatal(err)
	}

	// Register the fake adapter.
	reg := codingagent.NewRegistry()
	reg.MustRegister(fake.New(fake.AdapterOptions{
		Installed: true,
		Scenario:  fake.ScenarioSuccess,
	}), 0)

	sup := supervisor.New(supervisor.Options{
		Adapters: reg,
		FullEnv:  []string{"PATH=/usr/bin", "HOME=" + tmp, "GITHUB_TOKEN=should-not-leak"},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := sup.Run(ctx, supervisor.RunRequest{
		WorkspaceID: "test-ws",
		Engine:      "fake",
	}, wsPath)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.Failed {
		t.Errorf("run failed: %+v", result.Outcome)
	}
	if result.Cancelled {
		t.Error("run was cancelled")
	}

	// The fake agent (success scenario) writes src/hello.txt.
	helloPath := filepath.Join(wsPath, "src", "hello.txt")
	if _, err := os.Stat(helloPath); err != nil {
		t.Errorf("fake agent did not write src/hello.txt: %v", err)
	}

	// Verify the run produced events.
	if len(result.Events) == 0 {
		t.Error("no events collected")
	}
}

func TestSupervisor_UnknownEngine(t *testing.T) {
	reg := codingagent.NewRegistry()
	sup := supervisor.New(supervisor.Options{Adapters: reg})

	_, err := sup.Run(context.Background(), supervisor.RunRequest{
		Engine: "nonexistent",
	}, "/tmp/fake")
	if err == nil {
		t.Error("expected error for unknown engine")
	}
}

func TestSupervisor_WithAudit(t *testing.T) {
	tmp := t.TempDir()
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

	wsPath := filepath.Join(tmp, "ws")
	os.MkdirAll(wsPath, 0o755)

	reg := codingagent.NewRegistry()
	reg.MustRegister(fake.New(fake.AdapterOptions{Installed: true, Scenario: fake.ScenarioSuccess}), 0)

	sup := supervisor.New(supervisor.Options{
		Adapters: reg,
		Audit:    rec,
		FullEnv:  []string{"PATH=/usr/bin", "HOME=" + tmp},
	})

	ctx := context.Background()
	_, err = sup.Run(ctx, supervisor.RunRequest{
		WorkspaceID: "ws-audit",
		Engine:      "fake",
	}, wsPath)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Verify audit events were recorded.
	events, err := rec.Filter(ctx, storage.AuditFilter{ScopeID: "ws-audit"})
	if err != nil {
		t.Fatalf("Filter audit: %v", err)
	}
	if len(events) < 2 {
		t.Errorf("expected >=2 audit events, got %d", len(events))
	}
}

// ---- helpers ----

func contains(slice []string, s string) bool {
	for _, x := range slice {
		if x == s {
			return true
		}
	}
	return false
}

func cut(s, sep string) (before, after string, found bool) {
	for i := 0; i+len(sep) <= len(s); i++ {
		if s[i:i+len(sep)] == sep {
			return s[:i], s[i+len(sep):], true
		}
	}
	return s, "", false
}
