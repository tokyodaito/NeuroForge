package gemini

import (
	"strings"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/protocol"
)

// ClassifyFailure implements [codingagent.Adapter]. It layers Gemini-specific
// stderr signals on top of [codingagent.DefaultClassify], then defers to the
// shared heuristics. No class maps to an unbounded retry (rule §32): every
// classification is built via [protocol.DefaultPolicy].
//
// Gemini-specific signals handled before the default:
//
//   - PROVIDER_QUOTA: RESOURCE_EXHAUSTED / "quota" / "billing".
//   - PROVIDER_RATE_LIMIT: HTTP 429 / "rate limit" / "RESOURCE_EXHAUSTED" with
//     a rate hint (mapped to quota when ambiguous — see classifyGemini).
//   - PROVIDER_AUTH: "API key not valid" / PERMISSION_DENIED / 401 / "login
//     required".
//   - PROVIDER_CAPACITY: HTTP 503 / 503 / UNAVAILABLE / "overloaded".
//   - MODEL_NOT_AVAILABLE: "model" + "not found"/"not supported".
//
// A signal absent from both the events and stderr falls through to
// DefaultClassify, which inspects the exit code (signal death → ENGINE_CRASH,
// 124/137 → TIMEOUT).
func (a *Adapter) ClassifyFailure(exitCode int, events []protocol.NormalizedEvent, stderr string) protocol.FailureClassification {
	if fc, ok := classifyFromEvents(events, exitCode); ok {
		return fc
	}
	if fc, ok := classifyGemini(stderr, exitCode); ok {
		return fc
	}
	return codingagent.DefaultClassify(exitCode, events, stderr)
}

// classifyFromEvents honours an explicit terminal failure/cancel event emitted
// during the run, mirroring DefaultClassify's first pass but kept here so the
// adapter's classification stays self-consistent.
func classifyFromEvents(events []protocol.NormalizedEvent, exitCode int) (protocol.FailureClassification, bool) {
	for _, ev := range events {
		if ev.Type == protocol.EventRunFailed && ev.Failure != nil && ev.Failure.Class.IsValid() {
			fc := protocol.DefaultPolicy(ev.Failure.Class)
			fc.ExitCode = exitCode
			if ev.Failure.Reason != "" {
				fc.Reason = ev.Failure.Reason
			}
			return fc, true
		}
		if ev.Type == protocol.EventRunCancelled {
			fc := protocol.DefaultPolicy(protocol.FailureCancelled)
			fc.ExitCode = exitCode
			return fc, true
		}
	}
	return protocol.FailureClassification{}, false
}

// classifyGemini inspects stderr for Gemini/Google-API-specific signals. It is
// deliberately conservative: ambiguous quota-vs-rate-limit text (e.g.
// RESOURCE_EXHAUSTED) maps to PROVIDER_QUOTA (failover, no auto-retry), since a
// wrong rate-limit retry could burn quota. Returns ok=false when no
// Gemini-specific signal is found.
func classifyGemini(stderr string, exitCode int) (protocol.FailureClassification, bool) {
	if stderr == "" {
		return protocol.FailureClassification{}, false
	}
	low := strings.ToLower(stderr)

	switch {
	case containsAny(low, "resource_exhausted", "quota", "billing", "limit exceeded", "exhausted"):
		fc := withExitCode(protocol.DefaultPolicy(protocol.FailureProviderQuota), exitCode)
		fc.Reason = "gemini provider quota exhausted"
		return fc, true
	case containsAny(low, "429", "rate limit", "too many requests", "rate_limit"):
		fc := withExitCode(protocol.DefaultPolicy(protocol.FailureProviderRateLimit), exitCode)
		fc.Reason = "gemini provider rate limited"
		return fc, true
	case containsAny(low, "api key not valid", "permission_denied", "unauthorized", "401", "login required", "invalid api key", "auth"):
		fc := withExitCode(protocol.DefaultPolicy(protocol.FailureProviderAuth), exitCode)
		fc.Reason = "gemini authentication failed"
		return fc, true
	case containsAny(low, "503", "unavailable", "overloaded", "try again later", "capacity"):
		fc := withExitCode(protocol.DefaultPolicy(protocol.FailureProviderCapacity), exitCode)
		fc.Reason = "gemini provider capacity/availability"
		return fc, true
	case modelUnavailable(low):
		fc := withExitCode(protocol.DefaultPolicy(protocol.FailureModelNotAvailable), exitCode)
		fc.Reason = "gemini model not available"
		return fc, true
	}
	return protocol.FailureClassification{}, false
}

// modelUnavailable detects "model X not found/supported" without hard-coding any
// model name (rule §36.8): it matches the words "model" near a negation, never
// a specific identifier.
func modelUnavailable(low string) bool {
	if !strings.Contains(low, "model") {
		return false
	}
	return strings.Contains(low, "not found") ||
		strings.Contains(low, "not supported") ||
		strings.Contains(low, "does not exist") ||
		strings.Contains(low, "unsupported")
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

func withExitCode(fc protocol.FailureClassification, exitCode int) protocol.FailureClassification {
	fc.ExitCode = exitCode
	return fc
}
