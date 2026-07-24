package opencode

import (
	"regexp"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// secretValueRe matches common "key=value" credential assignments where the
// value is long enough to be a real secret (>= 12 chars), regardless of the
// exact provider. It deliberately preserves the KEY and the signal words around
// it (e.g. "401 unauthorized", "quota exhausted") so failure classification
// still works; only the secret VALUE is replaced with ***.
var secretValueRe = regexp.MustCompile(`(?i)(sk-|Bearer\s+|api[_-]?key\s*[=:]|token\s*[=:]|secret\s*[=:]|password\s*[=:]|passwd\s*[=:]|Authorization\s*:\s*Bearer\s+)([A-Za-z0-9_\-./+=]{12,})`)

// longTokenRe matches opaque long tokens that are unlikely to be meaningful
// words (>= 20 chars of token alphabet) and redacts them. Conservative length
// to avoid clobbering stack traces or sentences.
var longTokenRe = regexp.MustCompile(`\b[A-Za-z0-9_\-]{20,}\b`)

// redactSecrets returns s with credential values replaced by "***", preserving
// surrounding signal text for classification (spec §29, rule: never store
// tokens/keys in events, captured stderr or logs). It is applied to captured
// stderr before classification/storage and to raw bytes before persisting
// malformed-output artifacts.
func redactSecrets(s string) string {
	if s == "" {
		return s
	}
	s = secretValueRe.ReplaceAllStringFunc(s, func(match string) string {
		loc := secretValueRe.FindStringSubmatchIndex(match)
		if loc == nil || len(loc) < 6 {
			return match
		}
		prefix := match[loc[2]:loc[3]]
		return prefix + "***"
	})
	// Redact bare opaque tokens that survived the key=value pass.
	s = longTokenRe.ReplaceAllString(s, "***")
	return s
}

// redactBytes returns a copy of b with secrets redacted.
func redactBytes(b []byte) []byte {
	return []byte(redactSecrets(string(b)))
}

// containsSecret reports whether s contains anything resembling a secret value,
// used by tests to assert no leakage.
func containsSecret(s string) bool {
	return redactSecrets(s) != s
}

// noSecrets asserts that s contains no redactable secret and is used only in
// tests (kept here next to the redactor for locality).
func noSecrets(s string) bool { return !containsSecret(s) }

// redactEvent scrubs credential values from the leak-prone fields of a forwarded
// event without mangling structured payloads (spec §29: never store tokens/keys
// in events). It redacts:
//   - Raw (unparsed bytes from malformed/unknown lines — the main leak vector),
//   - Warning.Message and Failure.Reason (engine-authored error text that may
//     echo a header/env).
//
// Structured message deltas, tool/command payloads and file changes are left
// intact: the adapter never forwards secrets to the engine (allowlisted env), so
// the engine cannot legitimately echo them, and redacting structured output
// would harm functionality.
func redactEvent(ev protocol.NormalizedEvent) protocol.NormalizedEvent {
	if len(ev.Raw) > 0 {
		ev.Raw = redactBytes(ev.Raw)
	}
	if ev.Warning != nil {
		ev.Warning.Message = redactSecrets(ev.Warning.Message)
	}
	if ev.Failure != nil {
		ev.Failure.Reason = redactSecrets(ev.Failure.Reason)
	}
	return ev
}
