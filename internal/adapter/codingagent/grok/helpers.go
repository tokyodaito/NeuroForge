package grok

import (
	"errors"
	"os/exec"
	"regexp"
	"strings"
)

// exitCodeOf extracts the process exit code from a cmd.Wait() error. A nil
// error is exit 0; an *exec.ExitError yields its code; anything else is 1.
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 1
}

// secretPatterns matches common credential shapes so they can be redacted from
// human-facing failure reasons (spec §29, AC-28: never store tokens/keys in
// events, captured stderr, or logs). The patterns are deliberately conservative
// (long opaque tokens, "Bearer ...", "key=...", "api key <value>"); ordinary
// diagnostic text is left intact. Over-redaction is preferred to leakage.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9\-\._~+\/=]{8,}`),
	// "key VALUE", "key=VALUE", "key: VALUE" forms (space, =, or colon separated).
	regexp.MustCompile(`(?i)(api[_\- ]?key|apikey|secret|token|password|passwd|authorization)\s*[=:]?\s+([A-Za-z0-9][A-Za-z0-9\-_.~+]{5,})`),
	regexp.MustCompile(`(?i)(api[_\-]?key|secret|token|password|passwd|authorization)\s*=\s*[^\s&]{6,}`),
	regexp.MustCompile(`(?i)xox[bpoas]-[A-Za-z0-9\-]{6,}`), // Slack-style tokens
	regexp.MustCompile(`(?i)gh[pousr]_[A-Za-z0-9]{8,}`),    // GitHub tokens
	regexp.MustCompile(`(?i)sk-[A-Za-z0-9\-_]{8,}`),        // OpenAI-style keys
	regexp.MustCompile(`[A-Fa-f0-9]{32,}`),                 // long hex (possible hash/secret)
	regexp.MustCompile(`[A-Za-z0-9+/]{40,}={0,2}`),         // long base64 blob
}

// redactSecrets replaces substrings that look like credentials with the
// placeholder "[REDACTED]". It never returns an empty string for non-empty
// input: if everything is redacted, the placeholder is returned so the caller
// still has a non-empty reason.
func redactSecrets(s string) string {
	if s == "" {
		return ""
	}
	out := s
	for _, re := range secretPatterns {
		out = re.ReplaceAllString(out, "[REDACTED]")
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "[REDACTED]"
	}
	return out
}
