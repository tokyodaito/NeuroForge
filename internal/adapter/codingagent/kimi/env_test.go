package kimi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildRunEnvNoCredentialsLeak(t *testing.T) {
	// AC-28 / §29.2: the agent env must never carry merge tokens, the daemon
	// auth token or unrelated API keys, even when they are in the host env.
	t.Setenv("FORGE_DAEMON_TOKEN", "super-secret-daemon")
	t.Setenv("GITHUB_MERGE_TOKEN", "merge-secret")
	t.Setenv("OPENAI_API_KEY", "sk-leaked")
	t.Setenv("MY_RANDOM_SECRET", "hush")

	env := buildRunEnv("KIMI_HOME", "/tmp/home", nil, nil)
	joined := strings.Join(env, "\n")
	for _, bad := range []string{"super-secret-daemon", "merge-secret", "sk-leaked", "hush"} {
		if strings.Contains(joined, bad) {
			t.Errorf("credential leaked into agent env: %s\n%s", bad, joined)
		}
	}
}

func TestBuildRunEnvBaseKeysAndAllowlist(t *testing.T) {
	t.Setenv("MY_ALLOWED", "allowed-value")
	t.Setenv("TERM", "xterm")

	env := buildRunEnv("KIMI_HOME", "/home/k", []string{"MY_ALLOWED", "EXTRA=literal"}, []string{"OPT=1"})

	have := map[string]string{}
	for _, kv := range env {
		k, v, ok := splitEnvEntry(kv)
		if !ok {
			continue
		}
		have[k] = v
	}
	if have["TERM"] != "xterm" {
		t.Errorf("base key TERM not passed: %v", have)
	}
	if have["MY_ALLOWED"] != "allowed-value" {
		t.Errorf("bare allowlist key not copied from host: %v", have)
	}
	if have["EXTRA"] != "literal" {
		t.Errorf("literal allowlist KEY=VAL not passed: %v", have)
	}
	if have["OPT"] != "1" {
		t.Errorf("ExtraEnv not passed: %v", have)
	}
	if have["KIMI_HOME"] != "/home/k" {
		t.Errorf("isolated home not set: %v", have)
	}
	if have["NO_COLOR"] != "1" {
		t.Errorf("NO_COLOR not forced: %v", have)
	}
}

func TestBuildRunEnvHomeWinsOverAllowlist(t *testing.T) {
	// An allowlist entry must not shadow the isolated home (first wins).
	t.Setenv("KIMI_HOME", "/user/global")
	env := buildRunEnv("KIMI_HOME", "/run/home", []string{"KIMI_HOME"}, nil)
	for _, kv := range env {
		if strings.HasPrefix(kv, "KIMI_HOME=") && !strings.HasSuffix(kv, "/run/home") {
			t.Errorf("allowlist shadowed isolated home: %s", kv)
		}
	}
}

func TestBuildRunEnvCaseInsensitiveDedup(t *testing.T) {
	// The dedup is case-insensitive: Path and PATH are the same key, so the
	// base PATH is not duplicated.
	t.Setenv("PATH", "/base")
	env := buildRunEnv("KIMI_HOME", "/h", []string{"PATH=/overridden"}, nil)
	count := 0
	for _, kv := range env {
		if strings.EqualFold(kv[:len("PATH")], "PATH") && strings.Contains(kv, "=") {
			count++
		}
	}
	// At most one PATH-like entry (the allowlist override, since base PATH is
	// added first then dedup drops the override OR vice-versa — either way one).
	if count > 1 {
		t.Errorf("PATH duplicated case-insensitively: %d entries in %v", count, env)
	}
}

func TestIsolatedHomeDirRootedInWorkspace(t *testing.T) {
	ws := t.TempDir()
	dir, err := isolatedHomeDir(ws)
	if err != nil {
		t.Fatalf("isolatedHomeDir: %v", err)
	}
	if !strings.HasPrefix(dir, ws) {
		t.Errorf("home %q not rooted in workspace %q", dir, ws)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Errorf("home dir not created: %v", err)
	}
	// Namespaced to avoid colliding with the engine's own state.
	if filepath.Base(dir) != ".neuroforge-kimi" {
		t.Errorf("home dir base = %q, want .neuroforge-kimi", filepath.Base(dir))
	}
}

func TestIsolatedHomeDirFallsBackToTemp(t *testing.T) {
	dir, err := isolatedHomeDir("")
	if err != nil {
		t.Fatalf("isolatedHomeDir(empty): %v", err)
	}
	if !strings.HasPrefix(dir, os.TempDir()) {
		t.Errorf("home %q not rooted in temp %q", dir, os.TempDir())
	}
}

func TestRedactMasksCredentials(t *testing.T) {
	// Redaction is best-effort and case-insensitive; we assert each secret value
	// is gone from the output rather than asserting exact formatting.
	cases := []struct {
		in     string
		secret string // must NOT appear in the redacted output
	}{
		{"token is sk-abcdefghijklmnopqrstuvwxyz", "abcdefghijklmnopqrstuvwxyz"},
		{"Bearer abc.def-ghi_jkl", "abc.def-ghi_jkl"},
		{"Authorization: Bearer secrettoken", "secrettoken"},
		{"api_key=sk-live-1234567890", "sk-live-1234567890"},
		{"error: invalid api_key sk-deadbeefdeadbeef", "sk-deadbeefdeadbeef"},
		{"token=hunter2-value-here", "hunter2-value-here"},
	}
	for _, c := range cases {
		out := redact(c.in)
		if strings.Contains(out, c.secret) {
			t.Errorf("redact leaked %q in %q -> %q", c.secret, c.in, out)
		}
	}
}

func TestRedactPreservesNonSecrets(t *testing.T) {
	out := redact("quota exhausted before any edits")
	if out != "quota exhausted before any edits" {
		t.Errorf("redact altered a benign string: %q", out)
	}
}
