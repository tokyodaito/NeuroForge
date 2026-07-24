package claude

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Secret redaction (spec §29.2, AC-28). Captured stderr, error strings and
// logs must never persist provider tokens, bearer credentials or secret-valued
// environment entries. The redactor is deliberately targeted: it scrubs known
// secret shapes rather than every long string, so legitimate CLI output is not
// mangled.

var (
	// Anthropic API keys: sk-ant-api03-... / sk-ant-...
	reAnthropicKey = regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{6,}`)
	// Generic "Bearer <token>".
	reBearer = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._\-+]{6,}`)
	// "KEY=VALUE" where the key name looks secret-bearing.
	reSecretEnv = regexp.MustCompile(`(?i)\b([A-Z0-9_]*(?:TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIAL|API_KEY|APIKEY|AUTH)[A-Z0-9_]*)=([^\s"']+)`)
	// Long hex/base64 opaque blobs that look like OAuth tokens (≥40 chars,
	// only hex/base64 chars), as standalone tokens.
	reOpaqueToken = regexp.MustCompile(`\b[0-9A-Za-z_\-]{40,}\b`)
)

// redact returns a copy of s with recognised secret shapes replaced by "***".
func redact(s string) string {
	if s == "" {
		return s
	}
	s = reAnthropicKey.ReplaceAllString(s, "sk-ant-***")
	s = reBearer.ReplaceAllString(s, "Bearer ***")
	s = reSecretEnv.ReplaceAllString(s, "${1}=***")
	s = reOpaqueToken.ReplaceAllStringFunc(s, func(m string) string {
		// Only redact if it is not purely a path/hash we expect to see; keep it
		// conservative — redact runs of 40+ token-safe chars that are not
		// composed solely of digits (avoid swallowing numeric durations/ids).
		if isAllDigits(m) {
			return m
		}
		return "***"
	})
	return s
}

// redactBytes is the string-slot convenience used on error values.
func redactBytes(s string) string { return redact(s) }

func isAllDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

// ---- small path/time/filename helpers (kept here so run.go stays I/O-light) ----

// sanitizeRunID makes a run id safe for use as a filename component.
func sanitizeRunID(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '/', '\\', ':', ' ', '\t', '\n', '"', '*', '?', '<', '>', '|':
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "run"
	}
	return b.String()
}

// nowStamp renders a monotonic-ish filename suffix from a time source.
func nowStamp(now func() time.Time) string {
	return strconv.FormatInt(now().UnixNano(), 36)
}

// joinPath joins path elements volume-aware.
func joinPath(elem ...string) string { return filepath.Join(elem...) }

// osWriteFile is a thin wrapper to keep the "os" import out of run.go.
func osWriteFile(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
}
