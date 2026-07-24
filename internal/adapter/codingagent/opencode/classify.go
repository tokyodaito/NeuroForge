package opencode

import (
	"strings"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/protocol"
)

// ClassifyFailure implements codingagent.Adapter. It defers to the shared
// [codingagent.DefaultClassify] for the canonical §32 mapping, then refines a
// few cases where OpenCode surfaces a richer signal: it recovers the underlying
// backing provider from the error text and records it in the classification
// reason/metadata so routing/failover (M6/M7) can act on it (spec §32).
//
// The classifier is deterministic (no LLM — rule §22.6) and never returns an
// unbounded retry: every class carries a bounded MaxRetries via
// [protocol.DefaultPolicy] (rule §32).
func (a *Adapter) ClassifyFailure(exitCode int, events []protocol.NormalizedEvent, stderr string) protocol.FailureClassification {
	fc := codingagent.DefaultClassify(exitCode, events, stderr)
	fc.Reason = annotateProvider(fc.Reason, stderr)
	return fc
}

// providerSignal pairs a stderr fragment with the provider it implies. Used to
// surface the backing provider in the §32 reason so failover can target it.
var providerSignal = []struct {
	frag     string
	provider string
}{
	{"anthropic", "anthropic"},
	{"claude", "anthropic"},
	{"openai", "openai"},
	{"google", "google"},
	{"gemini", "google"},
	{"azure", "azure"},
	{"bedrock", "aws"},
	{"aws", "aws"},
	{"vertex", "google"},
	{"groq", "groq"},
	{"mistral", "mistral"},
	{"deepseek", "deepseek"},
	{"xai", "xai"},
	{"grok", "xai"},
}

// detectProvider scans stderr (and any failure reason already produced) for a
// backing-provider hint. Returns "" when none is found.
func detectProvider(stderr, reason string) string {
	hay := strings.ToLower(stderr + " " + reason)
	for _, s := range providerSignal {
		if strings.Contains(hay, s.frag) {
			return s.provider
		}
	}
	return ""
}

// annotateProvider appends a "provider=<p>" provenance marker to the reason when
// a backing provider can be inferred, so routing/failover (M6/M7) can act on it
// (spec §32). It is idempotent and never overwrites an existing marker.
func annotateProvider(reason, stderr string) string {
	p := detectProvider(stderr, reason)
	if p == "" {
		return reason
	}
	if strings.Contains(reason, "provider=") {
		return reason
	}
	if reason == "" {
		return "provider=" + p
	}
	return reason + " (provider=" + p + ")"
}
