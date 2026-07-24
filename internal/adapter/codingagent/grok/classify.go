package grok

import (
	"strings"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/protocol"
)

// ClassifyFailure implements codingagent.Adapter. It defers to the shared
// [codingagent.DefaultClassify] for the common taxonomy, then refines two
// Grok-specific signals that the shared classifier does not distinguish (spec
// §32): PROVIDER_CAPACITY and PROVIDER_RATE_LIMIT must be distinct classes, and
// capacity must never be lumped into quota. No returned class maps to an
// unbounded retry (rule §32) — all routing goes through [protocol.DefaultPolicy]
// which bounds MaxRetries.
func (a *Adapter) ClassifyFailure(exitCode int, events []protocol.NormalizedEvent, stderr string) protocol.FailureClassification {
	return grokClassify(exitCode, events, stderr)
}

// grokClassify is the pure, testable classifier.
func grokClassify(exitCode int, events []protocol.NormalizedEvent, stderr string) protocol.FailureClassification {
	// 1. Honour an explicit terminal failure/cancel event the adapter (or the
	//    engine) already emitted. This mirrors DefaultClassify's first step and
	//    keeps classification consistent with the recorded event stream.
	for _, ev := range events {
		if ev.Type == protocol.EventRunFailed && ev.Failure != nil && ev.Failure.Class.IsValid() {
			fc := protocol.DefaultPolicy(ev.Failure.Class)
			fc.ExitCode = exitCode
			if ev.Failure.Reason != "" {
				fc.Reason = ev.Failure.Reason
			}
			return refineGrok(fc, stderr)
		}
		if ev.Type == protocol.EventRunCancelled {
			fc := protocol.DefaultPolicy(protocol.FailureCancelled)
			fc.ExitCode = exitCode
			return fc
		}
	}

	// 2. Grok-specific stderr signals, checked before the generic fallback so
	//    capacity is never mis-bucketed as quota or rate-limit.
	low := strings.ToLower(stderr)
	switch {
	case containsAny(low, "overloaded", "capacity", "service unavailable", "temporarily unavailable", "503", "502", "can't handle"):
		return refineGrok(withExit(protocol.DefaultPolicy(protocol.FailureProviderCapacity), exitCode), stderr)
	case containsAny(low, "rate limit", "rate_limit", "429", "too many requests", "throttl"):
		return refineGrok(withExit(protocol.DefaultPolicy(protocol.FailureProviderRateLimit), exitCode), stderr)
	case containsAny(low, "quota", "exhausted", "limit exceeded", "billing", "credit"):
		return refineGrok(withExit(protocol.DefaultPolicy(protocol.FailureProviderQuota), exitCode), stderr)
	case containsAny(low, "unauthorized", "401", "invalid api key", "invalid_api_key", "auth", "login required"):
		return refineGrok(withExit(protocol.DefaultPolicy(protocol.FailureProviderAuth), exitCode), stderr)
	case containsAny(low, "model") && containsAny(low, "not available", "not found", "deprecated"):
		return refineGrok(withExit(protocol.DefaultPolicy(protocol.FailureModelNotAvailable), exitCode), stderr)
	}

	// 3. Defer to the shared classifier (exit-code inference, signal death,
	//    malformed fallback). Reason is redacted to avoid leaking secrets.
	fc := codingagent.DefaultClassify(exitCode, events, stderr)
	fc.Reason = redactSecrets(fc.Reason)
	return fc
}

// refineGrok applies Grok-specific post-processing to a chosen classification:
// it keeps a short, redacted hint from the engine's own stderr message so
// tokens/keys captured there never flow into events or logs (spec §29, AC-28).
// The exit code and policy are left intact.
func refineGrok(fc protocol.FailureClassification, stderr string) protocol.FailureClassification {
	if stderr != "" {
		fc.Reason = redactSecrets(firstLine(stderr))
	} else {
		fc.Reason = redactSecrets(fc.Reason)
	}
	return fc
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

func withExit(fc protocol.FailureClassification, exitCode int) protocol.FailureClassification {
	fc.ExitCode = exitCode
	return fc
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
