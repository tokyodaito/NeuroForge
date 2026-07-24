package codex

import "regexp"

// redactionPatterns match common credential shapes that must never be persisted
// in events, captured stderr or logs (spec §29.2, AC-28). Redaction is
// defence-in-depth: the allowlisted environment already keeps secrets out of the
// agent process, but Codex stderr/JSONL could echo a token if a provider error
// ever quoted one back. Patterns are intentionally broad — over-redacting a
// diagnostic line is always safer than persisting a secret.
var redactionPatterns = []*regexp.Regexp{
	// OpenAI-style keys (sk-...).
	regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`),
	// GitHub tokens (ghp_/gho_/ghu_/ghs_/ghr_ ...).
	regexp.MustCompile(`gh[opusr]_[A-Za-z0-9]{36,}`),
	// "bearer <token>" (space-separated, the common header form).
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{8,}`),
	// Any KEY/TOKEN/SECRET/PASSWORD/AUTH-named assignment: covers env-var-style
	// leaks like OPENAI_API_KEY=..., AUTHORIZATION=..., x-token=... regardless of
	// the prefix (no \b so it matches inside compound names).
	regexp.MustCompile(`(?i)[a-z0-9_]*(api[_-]?key|token|secret|password|auth)[a-z0-9_]*\s*[:=]\s*[^\s]{6,}`),
}

// redact returns a copy of s with anything matching a redaction pattern replaced
// by "[REDACTED]". It is applied to captured stderr before classification and to
// any raw bytes persisted as an artifact, so secrets never reach durable state.
func redact(s string) string {
	for _, re := range redactionPatterns {
		s = re.ReplaceAllString(s, "[REDACTED]")
	}
	return s
}
