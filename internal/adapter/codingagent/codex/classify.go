package codex

import (
	"strings"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/protocol"
)

// classifyFailure maps a native Codex failure signal onto the §32 taxonomy. It
// prefers [codingagent.DefaultClassify] and refines a handful of Codex-specific
// signals DefaultClassify does not cover (provider capacity, Codex auth/login
// phrasings, model deprecation). Every class maps to a bounded policy via
// [protocol.DefaultPolicy] — no class produces an unbounded retry (rule §32).
//
// The classifier is deterministic (no LLM — rule §22.6).
func classifyFailure(exitCode int, events []protocol.NormalizedEvent, stderr string) protocol.FailureClassification {
	low := strings.ToLower(stderr)

	// Codex capacity / overload is not matched by DefaultClassify; refine it so
	// the supervisor can cooldown/failover instead of treating it as malformed.
	switch {
	case containsAny(low, "overloaded", "server is at capacity", "capacity", "503", "service unavailable"):
		return withExit(protocol.DefaultPolicy(protocol.FailureProviderCapacity), exitCode)
	case containsAny(low, "not logged in", "run `codex login`", "codex login", "no api key", "missing api key", "login required"):
		return withExit(protocol.DefaultPolicy(protocol.FailureProviderAuth), exitCode)
	case containsAny(low, "billing", "insufficient_quota", "usage limit reached", "plan limit"):
		return withExit(protocol.DefaultPolicy(protocol.FailureProviderQuota), exitCode)
	case containsAny(low, "deprecat", "model does not exist", "no such model"):
		return withExit(protocol.DefaultPolicy(protocol.FailureModelNotAvailable), exitCode)
	}

	// Honour a Codex-emitted terminal failure event first (DefaultClassify does
	// this too, but we call it explicitly so our refinements above take priority
	// only over the stderr heuristics, not over an explicit typed failure).
	for _, ev := range events {
		if ev.Type == protocol.EventRunFailed && ev.Failure != nil && ev.Failure.Class.IsValid() {
			fc := protocol.DefaultPolicy(ev.Failure.Class)
			fc.ExitCode = exitCode
			if ev.Failure.Reason != "" {
				fc.Reason = ev.Failure.Reason
			}
			return fc
		}
	}

	return codingagent.DefaultClassify(exitCode, events, stderr)
}

func containsAny(haystack string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}

func withExit(fc protocol.FailureClassification, exitCode int) protocol.FailureClassification {
	fc.ExitCode = exitCode
	return fc
}
