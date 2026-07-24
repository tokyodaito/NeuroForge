package gemini

import (
	"regexp"
)

// redactPatterns match common credential shapes that must never be persisted in
// captured stderr, events or logs (spec §29, AC-28). Matching is best-effort
// and conservative: redacting too aggressively (e.g. an opaque token) is safe;
// leaking a secret is not. Patterns run in order; the first match wins per span.
var redactPatterns = []*redaction{
	// Google / Gemini API keys (AIza…).
	newRedaction(`AIza[0-9A-Za-z_\-]{20,}`, "[redacted:apikey]"),
	// Google OAuth access tokens (ya29.…).
	newRedaction(`ya29\.[A-Za-z0-9_\-]+`, "[redacted:oauthtoken]"),
	// Generic "key=val" / "key:val" credential assignments.
	newRedaction(`(?i)(token|api[_-]?key|apikey|secret|password|passwd|access[_-]?token|authorization)\s*[:=]\s*[A-Za-z0-9_\-\.=]{8,}`, "[redacted:credential]"),
	// The NeuroForge daemon auth token variable, if it ever appears inline.
	newRedaction(`(?i)FORGE_DAEMON_TOKEN=[^\s]+`, "FORGE_DAEMON_TOKEN=[redacted]"),
	// Long opaque blobs (>=32 chars) that look like secrets (base64/hex/JWT-ish).
	newRedaction(`[A-Za-z0-9][A-Za-z0-9_=\-\.]{31,}`, "[redacted:opaque]"),
}

type redaction struct {
	re   *regexp.Regexp
	repl string
}

func newRedaction(pattern, repl string) *redaction {
	return &redaction{re: regexp.MustCompile(pattern), repl: repl}
}

// redact masks credential-like substrings in s. It is applied to captured
// stderr before the value is stored in an event or artifact. Unknown secrets
// that do not match a pattern may still pass through; the allowlisted
// environment (§29.2) is the primary defence, so secrets should not reach the
// agent process in the first place.
func redact(s string) string {
	if s == "" {
		return s
	}
	for _, r := range redactPatterns {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}

// redactBytes is the byte-oriented equivalent of [redact].
func redactBytes(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	return []byte(redact(string(b)))
}

// containsSecret reports whether s contains a substring resembling a credential.
// Used by tests; not relied upon for enforcement (redact is the guard).
func containsSecret(s string) bool {
	for _, r := range redactPatterns {
		if r.re.MatchString(s) {
			return true
		}
	}
	return false
}
