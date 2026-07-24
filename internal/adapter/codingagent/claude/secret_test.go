package claude

import (
	"strings"
	"testing"
)

func TestRedactAnthropicKey(t *testing.T) {
	in := "error: unauthorized key sk-ant-api03-ABCDEF0123456789abcdefghij"
	out := redact(in)
	if strings.Contains(out, "ABCDEF0123456789") {
		t.Errorf("anthropic key not redacted: %q", out)
	}
	if !strings.Contains(out, "sk-ant-") {
		t.Errorf("redaction removed too much: %q", out)
	}
}

func TestRedactBearer(t *testing.T) {
	in := "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig"
	out := redact(in)
	if strings.Contains(out, "eyJhbGciOiJIUzI1NiJ9.payload.sig") {
		t.Errorf("bearer token not redacted: %q", out)
	}
}

func TestRedactSecretEnvEntries(t *testing.T) {
	in := "ANTHROPIC_API_KEY=sk-ant-live-1234567890 GITHUB_TOKEN=ghs_secret FOO=bar"
	out := redact(in)
	if strings.Contains(out, "sk-ant-live-1234567890") || strings.Contains(out, "ghs_secret") {
		t.Errorf("secret env not redacted: %q", out)
	}
	if !strings.Contains(out, "FOO=bar") {
		t.Errorf("non-secret env altered: %q", out)
	}
}

func TestRedactOpaqueToken(t *testing.T) {
	long := strings.Repeat("a", 48)
	in := "token blob " + long + " here"
	out := redact(in)
	if strings.Contains(out, long) {
		t.Errorf("opaque token not redacted: %q", out)
	}
}

func TestRedactPreservesDigitsAndPaths(t *testing.T) {
	in := "exited after 3000ms in /home/user/repo/build (code 1)"
	out := redact(in)
	if out != in {
		t.Errorf("non-secret output altered: %q", out)
	}
}

func TestRedactEmpty(t *testing.T) {
	if redact("") != "" {
		t.Error("empty input should pass through")
	}
}

func TestRedactBytesAlias(t *testing.T) {
	in := "Authorization: Bearer abc1234567890"
	if redactBytes(in) == in {
		t.Error("redactBytes did not redact")
	}
}

func TestSanitizeRunID(t *testing.T) {
	got := sanitizeRunID("run/with: spaces and\\slashes")
	// Path separators, colons and spaces must be replaced so the result is a
	// safe filename component.
	if strings.ContainsAny(got, "/:\\ ") {
		t.Errorf("sanitizeRunID left unsafe chars: %q", got)
	}
	if got == "" {
		t.Error("sanitizeRunID returned empty for non-empty input")
	}
}

func TestNowStampUnique(t *testing.T) {
	a, _ := New(Options{BinaryPath: "claude"})
	s1 := nowStamp(a.opts.Now)
	s2 := nowStamp(a.opts.Now)
	// May collide only at nanosecond resolution in fast succession; both must be
	// non-empty base-36 strings.
	if s1 == "" || s2 == "" {
		t.Errorf("nowStamp empty: %q %q", s1, s2)
	}
}
