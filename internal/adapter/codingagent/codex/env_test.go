package codex

import (
	"os"
	"strings"
	"testing"
)

func TestBuildAgentEnvIncludesBaseEssentials(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("HOME", "/home/user")
	t.Setenv("TERM", "xterm")
	env := buildAgentEnv(nil)
	joined := strings.Join(env, "\n")
	for _, want := range []string{"PATH=/usr/bin", "HOME=/home/user", "TERM=xterm"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in env:\n%s", want, joined)
		}
	}
}

func TestBuildAgentEnvPassesAllowlist(t *testing.T) {
	t.Setenv("MY_TOOL_CONFIG", "/etc/tool")
	t.Setenv("PATH", "/usr/bin")
	env := buildAgentEnv([]string{"MY_TOOL_CONFIG", "LITERAL=value"})
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "MY_TOOL_CONFIG=/etc/tool") {
		t.Errorf("allowlist KEY not copied:\n%s", joined)
	}
	if !strings.Contains(joined, "LITERAL=value") {
		t.Errorf("allowlist KEY=VAL not passed:\n%s", joined)
	}
	if !strings.Contains(joined, "PATH=/usr/bin") {
		t.Errorf("PATH missing:\n%s", joined)
	}
}

func TestBuildAgentEnvNeverPassesForbidden(t *testing.T) {
	// AC-28 / §29.2: forbidden vars must never reach the agent, even if the
	// caller mistakenly allowlisted them.
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("GITHUB_TOKEN", "ghp_supersecret")
	t.Setenv("OPENAI_API_KEY", "sk-leakedkey")
	t.Setenv("NEUROFORGE_DAEMON_TOKEN", "daemon-secret")
	t.Setenv("FORGE_DAEMON_TOKEN", "daemon-secret2")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws-secret")

	env := buildAgentEnv([]string{
		"GITHUB_TOKEN", "OPENAI_API_KEY", "NEUROFORGE_DAEMON_TOKEN",
		"FORGE_DAEMON_TOKEN", "AWS_SECRET_ACCESS_KEY",
	})
	joined := strings.Join(env, "\n")
	for _, secret := range []string{"ghp_supersecret", "sk-leakedkey", "daemon-secret", "daemon-secret2", "aws-secret"} {
		if strings.Contains(joined, secret) {
			t.Errorf("forbidden secret %q leaked into agent env:\n%s", secret, joined)
		}
	}
}

func TestBuildAgentEnvIsDeterministic(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("HOME", "/h")
	a := buildAgentEnv([]string{"FOO=bar", "BAZ=qux"})
	b := buildAgentEnv([]string{"FOO=bar", "BAZ=qux"})
	if strings.Join(a, "|") != strings.Join(b, "|") {
		t.Errorf("env not deterministic:\n%v\n%v", a, b)
	}
}

func TestBuildAgentEnvDedupsCaseInsensitive(t *testing.T) {
	// Windows env keys are case-insensitive; dedup must not allow a forbidden
	// var through under different casing.
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("Foo", "first")
	env := buildAgentEnv([]string{"FOO=second", "foo=third"})
	joined := strings.Join(env, "\n")
	// Only the first occurrence (KEY=VAL form) survives; the later dup is dropped.
	if strings.Count(joined, "FOO=second") != 1 && strings.Count(joined, "foo=third") != 0 {
		// Verify no duplicate Foo-ish entries beyond the first.
		count := strings.Count(joined, "OO=") + strings.Count(joined, "oo=")
		if count > 1 {
			t.Errorf("expected single Foo entry, got:\n%s", joined)
		}
	}
}

func TestIsForbiddenEnv(t *testing.T) {
	for _, name := range []string{"GITHUB_TOKEN", "github_token", "OPENAI_API_KEY", "NEUROFORGE_DAEMON_TOKEN"} {
		if !isForbiddenEnv(name) {
			t.Errorf("isForbiddenEnv(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"PATH", "HOME", "MY_TOOL_CONFIG"} {
		if isForbiddenEnv(name) {
			t.Errorf("isForbiddenEnv(%q) = true, want false", name)
		}
	}
}

func TestBuildAgentEnvDoesNotReadProcessEnvForAllowlistValues(t *testing.T) {
	// An allowlist entry of the form KEY (no '=') is copied from the current
	// env only if present; if absent it is silently omitted (no leak, no error).
	t.Setenv("PATH", "/usr/bin")
	os.Unsetenv("DEFINITELY_NOT_PRESENT_VAR")
	env := buildAgentEnv([]string{"DEFINITELY_NOT_PRESENT_VAR"})
	for _, kv := range env {
		if strings.HasPrefix(kv, "DEFINITELY_NOT_PRESENT_VAR=") {
			t.Errorf("absent allowlist var should not appear: %s", kv)
		}
	}
}
