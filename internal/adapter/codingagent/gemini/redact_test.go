package gemini

import (
	"strings"
	"testing"
)

func TestRedactAPIKey(t *testing.T) {
	in := "error: API key AIzaSyAABCDEFGHIJKLMN0123456789xyz not valid"
	out := redact(in)
	if strings.Contains(out, "AIzaSy") {
		t.Errorf("api key not redacted: %s", out)
	}
	if !strings.Contains(out, "[redacted") {
		t.Errorf("expected redaction marker: %s", out)
	}
}

func TestRedactBearerToken(t *testing.T) {
	in := "Authorization: Bearer ya29.abcdEfghIjklMnopQrstUvwxYz1234567890"
	out := redact(in)
	if strings.Contains(out, "ya29.") {
		t.Errorf("bearer token not redacted: %s", out)
	}
}

func TestRedactDaemonToken(t *testing.T) {
	in := "FORGE_DAEMON_TOKEN=abc123def456ghi789jkl012mno345pqr"
	out := redact(in)
	if strings.Contains(out, "abc123def456") {
		t.Errorf("daemon token not redacted: %s", out)
	}
}

func TestRedactPreservesNonSecret(t *testing.T) {
	in := "model not found; please check the model id"
	out := redact(in)
	if out != in {
		t.Errorf("non-secret text altered: %q", out)
	}
}

func TestRedactEmpty(t *testing.T) {
	if redact("") != "" {
		t.Error("empty redact should be empty")
	}
}

func TestRedactBytes(t *testing.T) {
	in := []byte("key=abcdefghijklmnopqrstuvwxyz123456")
	out := redactBytes(in)
	if strings.Contains(string(out), "abcdefghijklmnopqrstuvwxyz") {
		t.Errorf("opaque token not redacted: %s", out)
	}
}

func TestContainsSecret(t *testing.T) {
	if !containsSecret("AIzaSyAaBBccDDeeFFggHHiiJJkkLLmmNNooPP") {
		t.Error("expected secret detected")
	}
	if containsSecret("totally benign message") {
		t.Error("benign message flagged as secret")
	}
}
