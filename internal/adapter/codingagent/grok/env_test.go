package grok

import (
	"strings"
	"testing"
)

func TestBuildEnvAllowlistNeverSecret(t *testing.T) {
	// AC-28 / §29.2: the daemon token and arbitrary secrets must never reach the
	// child. buildEnv only copies the allowlist + per-request entries + ExtraEnv.
	t.Setenv("FORGE_DAEMON_TOKEN", "super-secret")
	t.Setenv("GITHUB_TOKEN", "ghp_topsecret")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "shhh")

	env := buildEnv(nil, nil)
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "super-secret") || strings.Contains(joined, "ghp_topsecret") || strings.Contains(joined, "shhh") {
		t.Fatalf("secret leaked into child env:\n%s", joined)
	}
	if !strings.Contains(joined, "PATH=") {
		t.Errorf("PATH not propagated:\n%s", joined)
	}
}

func TestBuildEnvRequestAllowlist(t *testing.T) {
	t.Setenv("MY_TOOL_CONFIG", "/etc/tool")
	t.Setenv("FORGE_DAEMON_TOKEN", "nope")

	env := buildEnv([]string{"MY_TOOL_CONFIG", "EXPLICIT=ok"}, nil)
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "MY_TOOL_CONFIG=/etc/tool") {
		t.Errorf("allowlisted KEY not copied from current env: %s", joined)
	}
	if !strings.Contains(joined, "EXPLICIT=ok") {
		t.Errorf("allowlisted KEY=VAL not passed: %s", joined)
	}
	if strings.Contains(joined, "nope") {
		t.Errorf("daemon token should never be present: %s", joined)
	}
}

func TestBuildEnvEssentialKeys(t *testing.T) {
	// The essential keys must be in the allowlist so the CLI can run.
	want := []string{"TEMP", "TMP", "HOME", "USER", "LANG", "LC_ALL", "TERM", "PATH"}
	for _, k := range want {
		if !containsKey(allowlistKeys, k) {
			t.Errorf("allowlist missing required key %q", k)
		}
	}
	// Directly: set an allowlisted key and ensure propagation.
	t.Setenv("TEMP", "/tmp/grok-builder")
	env := buildEnv(nil, nil)
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "TEMP=/tmp/grok-builder") {
		t.Errorf("TEMP not propagated: %s", joined)
	}
}

func TestBuildEnvExtraEnvLast(t *testing.T) {
	env := buildEnv(nil, []string{"GROK_STUB_SCENARIO=success"})
	if !strings.Contains(strings.Join(env, "\n"), "GROK_STUB_SCENARIO=success") {
		t.Error("ExtraEnv not appended")
	}
}

func containsKey(keys []string, want string) bool {
	for _, k := range keys {
		if strings.EqualFold(k, want) {
			return true
		}
	}
	return false
}
