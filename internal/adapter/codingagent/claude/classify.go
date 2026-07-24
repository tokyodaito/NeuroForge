package claude

import (
	"strings"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/protocol"
)

// ClassifyFailure implements codingagent.Adapter. It honours an explicit
// terminal failure event emitted from the stream, then refines using Claude
// Code-specific stderr/error-category signals (the `result.errors[]` strings
// use a fixed vocabulary: rate_limit, billing_error, authentication_failed,
// model_not_found, overloaded, invalid_request, server_error, ...). Anything
// unclassified defers to [codingagent.DefaultClassify]. No class ever yields
// an unbounded retry (rule §32): every result comes from
// [protocol.DefaultPolicy], which bounds MaxRetries.
//
// Claude subscription quota is DISTINCT from API rate-limit (spec §20, rule
// §36.10): they map to PROVIDER_QUOTA and PROVIDER_RATE_LIMIT respectively.
func (a *Adapter) ClassifyFailure(exitCode int, events []protocol.NormalizedEvent, stderr string) protocol.FailureClassification {
	return classifyClaude(exitCode, events, redact(stderr))
}

// classifyInternal is the synthesis path used when the process exited without a
// terminal event. stderr is already redacted by supervise.
func (a *Adapter) classifyInternal(exitCode int, events []protocol.NormalizedEvent, stderr string) protocol.FailureClassification {
	return classifyClaude(exitCode, events, stderr)
}

func classifyClaude(exitCode int, events []protocol.NormalizedEvent, stderr string) protocol.FailureClassification {
	// 1. Honour an explicit terminal failure event; refine an ambiguous class
	//    (INTERNAL_ERROR from result error_during_execution) using stderr.
	for _, ev := range events {
		if ev.Type == protocol.EventRunFailed && ev.Failure != nil && ev.Failure.Class.IsValid() {
			cls := ev.Failure.Class
			if cls == protocol.FailureInternalError {
				if refined, ok := stderrSignal(stderr); ok {
					cls = refined
				}
			}
			fc := protocol.DefaultPolicy(cls)
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

	// 2. Inspect stderr for Claude-specific provider/engine signals.
	if cls, ok := stderrSignal(stderr); ok {
		fc := protocol.DefaultPolicy(cls)
		fc.ExitCode = exitCode
		return fc
	}

	// 3. Defer to the shared heuristics (exit-code and generic keyword based).
	return codingagent.DefaultClassify(exitCode, events, stderr)
}

// stderrSignal scans stderr for Claude Code error-category vocabulary and maps
// it to a §32 class. Returns ok=false when no specific signal is present.
func stderrSignal(stderr string) (protocol.FailureClass, bool) {
	low := strings.ToLower(stderr)
	switch {
	case strings.Contains(low, "rate_limit") || strings.Contains(low, "429") || strings.Contains(low, "too many requests"):
		return protocol.FailureProviderRateLimit, true
	case strings.Contains(low, "billing") || strings.Contains(low, "quota") || strings.Contains(low, "exhausted") || strings.Contains(low, "limit exceeded"):
		// Subscription quota (distinct from API rate-limit above).
		return protocol.FailureProviderQuota, true
	case strings.Contains(low, "authentication_failed") || strings.Contains(low, "unauthorized") || strings.Contains(low, "401") || strings.Contains(low, "invalid api key"):
		return protocol.FailureProviderAuth, true
	case strings.Contains(low, "model_not_found") || (strings.Contains(low, "model") && (strings.Contains(low, "not available") || strings.Contains(low, "not found"))):
		return protocol.FailureModelNotAvailable, true
	case strings.Contains(low, "overloaded") || strings.Contains(low, "529") || strings.Contains(low, "capacity"):
		return protocol.FailureProviderCapacity, true
	case strings.Contains(low, "invalid_request"):
		return protocol.FailureMalformedOutput, true
	}
	return "", false
}
