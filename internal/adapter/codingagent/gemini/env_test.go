package gemini

import (
	"os"
	"strings"
	"testing"
)

func TestBuildEnvAllowlistOnly(t *testing.T) {
	// AC-28: never forward the whole env; only the positive allowlist + request
	// entries. Known secrets must never appear.
	t.Setenv("FORGE_DAEMON_TOKEN", "super-secret-daemon-token")
	t.Setenv("GITHUB_TOKEN", "ghp_secrettoken")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws_secret_value")
	t.Setenv("PATH", "/usr/bin")

	env := buildEnv([]string{"MY_TOOL_OPT=allowed-value"})
	joined := strings.Join(env, "\n")

	for _, secret := range []string{"super-secret-daemon-token", "ghp_secrettoken", "aws_secret_value"} {
		if strings.Contains(joined, secret) {
			t.Errorf("secret leaked into agent env: %s\n%s", secret, joined)
		}
	}
	if !strings.Contains(joined, "PATH=/usr/bin") {
		t.Errorf("PATH not forwarded: %s", joined)
	}
	if !strings.Contains(joined, "MY_TOOL_OPT=allowed-value") {
		t.Errorf("allowlist KEY=VAL not forwarded: %s", joined)
	}
	// Ensure only allowlisted keys are present (no incidental leakage).
	for _, kv := range env {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		if !isAllowedKey(key) {
			t.Errorf("non-allowlisted key %q in env", key)
		}
	}
}

func TestBuildEnvCopiesExistingKeyByName(t *testing.T) {
	t.Setenv("MY_EXISTING", "present")
	env := buildEnv([]string{"MY_EXISTING"})
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "MY_EXISTING=present") {
		t.Errorf("named allowlist key not copied: %s", joined)
	}
}

func TestBuildEnvMissingKeyByNameOmitted(t *testing.T) {
	// A KEY-only allowlist entry that is unset in the current env is omitted
	// (never invented).
	env := buildEnv([]string{"DEFINITELY_UNSET_VAR_X9Y2"})
	for _, kv := range env {
		if strings.HasPrefix(kv, "DEFINITELY_UNSET_VAR_X9Y2") {
			t.Errorf("unset named key should be omitted: %s", kv)
		}
	}
}

func TestBuildEnvWindowsEssentialsForwarded(t *testing.T) {
	// When present, the Windows-essential keys must be forwarded so Node.js
	// (the Gemini CLI runtime) can start on Windows.
	t.Setenv("SystemRoot", "C:\\Windows")
	t.Setenv("USERPROFILE", "C:\\Users\\test")
	t.Setenv("TEMP", "C:\\Temp")
	env := buildEnv(nil)
	joined := strings.Join(env, "\n")
	for _, want := range []string{"SystemRoot=", "USERPROFILE=", "TEMP="} {
		if !strings.Contains(joined, want) {
			t.Errorf("Windows essential %q not forwarded: %s", want, joined)
		}
	}
}

func isAllowedKey(key string) bool {
	for _, k := range baseEnvKeys {
		if k == key {
			return true
		}
	}
	// request allowlist entries are also allowed.
	switch key {
	case "MY_TOOL_OPT", "MY_EXISTING":
		return true
	}
	return false
}

// TestBuildEnvNoDaubFromUnrelatedEnv ensures an unrelated env var never leaks.
func TestBuildEnvNoDaubFromUnrelatedEnv(t *testing.T) {
	t.Setenv("UNRELATED_SECRET", "leak-me-not")
	if os.Getenv("UNRELATED_SECRET") == "" {
		t.Skip("could not set env")
	}
	env := buildEnv(nil)
	for _, kv := range env {
		if strings.Contains(kv, "leak-me-not") {
			t.Errorf("unrelated secret leaked: %s", kv)
		}
	}
}
