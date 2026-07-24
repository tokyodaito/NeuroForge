package codingagent

import (
	"strings"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// DefaultClassify maps a native failure signal — process exit code, the run's
// normalized events and captured stderr — onto the §32 taxonomy using shared
// heuristics (spec §12.2 ClassifyFailure, §32). Adapters without an
// engine-specific signal should call this.
//
// The classifier is deterministic (no LLM — rule §22.6) and never produces an
// infinite-retry classification (rule §32): it builds a
// [protocol.FailureClassification] via [protocol.DefaultPolicy].
func DefaultClassify(exitCode int, events []protocol.NormalizedEvent, stderr string) protocol.FailureClassification {
	// 1. Honour an explicit terminal failure event if the adapter emitted one.
	for _, ev := range events {
		if ev.Type == protocol.EventRunFailed && ev.Failure != nil && ev.Failure.Class.IsValid() {
			fc := protocol.DefaultPolicy(ev.Failure.Class)
			fc.ExitCode = exitCode
			if ev.Failure.Reason != "" {
				fc.Reason = ev.Failure.Reason
			}
			return fc
		}
		if ev.Type == protocol.EventRunCancelled {
			fc := protocol.DefaultPolicy(protocol.FailureCancelled)
			fc.ExitCode = exitCode
			return fc
		}
	}

	// 2. Inspect stderr for well-known provider/engine signals.
	low := strings.ToLower(stderr)
	switch {
	case strings.Contains(low, "quota") || strings.Contains(low, "exhausted") || strings.Contains(low, "limit exceeded"):
		return withExit(protocol.DefaultPolicy(protocol.FailureProviderQuota), exitCode)
	case strings.Contains(low, "rate limit") || strings.Contains(low, "429") || strings.Contains(low, "too many requests"):
		return withExit(protocol.DefaultPolicy(protocol.FailureProviderRateLimit), exitCode)
	case strings.Contains(low, "unauthorized") || strings.Contains(low, "401") || strings.Contains(low, "invalid api key") || strings.Contains(low, "auth"):
		return withExit(protocol.DefaultPolicy(protocol.FailureProviderAuth), exitCode)
	case strings.Contains(low, "not found") && strings.Contains(low, "command"):
		return withExit(protocol.DefaultPolicy(protocol.FailureEngineNotInstalled), exitCode)
	case strings.Contains(low, "model") && (strings.Contains(low, "not available") || strings.Contains(low, "not found")):
		return withExit(protocol.DefaultPolicy(protocol.FailureModelNotAvailable), exitCode)
	case strings.Contains(low, "scope") && strings.Contains(low, "violat"):
		return withExit(protocol.DefaultPolicy(protocol.FailureScopeViolation), exitCode)
	}

	// 3. Infer from exit code when there is no explicit signal.
	switch {
	case exitCode == 0:
		// No real failure; classify as terminal success-ish internal error only
		// if the caller insisted on classifying a non-failure.
		fc := protocol.DefaultPolicy(protocol.FailureInternalError)
		fc.ExitCode = exitCode
		fc.Reason = "ClassifyFailure called with exit code 0 (no failure)"
		fc.Retryable = false
		fc.Policy = protocol.PolicyTerminal
		return fc
	case exitCode == 124 || exitCode == 137:
		// 124: common `timeout` convention; 137: SIGKILL (often a kill/timeout).
		return withExit(protocol.DefaultPolicy(protocol.FailureTimeout), exitCode)
	case exitCode >= 128:
		// Death by signal (128+n) → engine crash.
		return withExit(protocol.DefaultPolicy(protocol.FailureEngineCrash), exitCode)
	}

	// 4. Fallback: malformed/protocol error for non-zero exits without signal.
	return withExit(protocol.DefaultPolicy(protocol.FailureMalformedOutput), exitCode)
}

func withExit(fc protocol.FailureClassification, exitCode int) protocol.FailureClassification {
	fc.ExitCode = exitCode
	return fc
}
