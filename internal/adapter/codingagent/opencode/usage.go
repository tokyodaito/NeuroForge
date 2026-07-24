package opencode

import (
	"context"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// InspectQuota implements codingagent.Adapter. The OpenCode headless `run` does
// not expose a live remaining-quota API, so the adapter reports UNKNOWN (spec
// §20.1, rule §36.10: never report a quota as more precise than the provider
// warrants; default to the least precise level). Per-run token accounting still
// flows through usage.updated events when the engine reports them.
func (a *Adapter) InspectQuota(context.Context, protocol.Account) protocol.QuotaSnapshot {
	return protocol.QuotaSnapshot{
		Confidence: protocol.QuotaConfUnknown,
		State:      protocol.QuotaStateUnknown,
		Reason:     "opencode headless run exposes no live quota API; usage is reported via usage.updated events",
	}
}

// ListModels implements codingagent.Adapter. The OpenCode engine serves any
// model resolvable from its configured providers as "provider/model"; the core
// never hard-codes model names (rule §36.8). Rather than guess a catalogue, the
// adapter returns no descriptors and relies on the routed
// [protocol.AgentRunRequest.Model] (§12.1). A real model catalogue is supplied
// by the model-catalog milestone (M6-1); claiming one here would violate §36.25.
func (a *Adapter) ListModels(context.Context, protocol.Account) ([]protocol.ModelDescriptor, error) {
	return nil, nil
}

// mapUsage maps a parsed [protocol.UsagePayload] (from a usage.updated event in
// the stream) onto the canonical usage fields, coercing the confidence to the
// least-precise applicable level (spec §22, §22.8 prompt cache, rule §36.10).
// When the engine does not report tokens (all zero and no cost) the result
// carries QuotaConfUnknown so accounting never overstates precision.
func mapUsage(in protocol.UsagePayload) protocol.UsagePayload {
	out := protocol.UsagePayload{
		InputTokens:      in.InputTokens,
		OutputTokens:     in.OutputTokens,
		CacheReadTokens:  in.CacheReadTokens,
		CacheWriteTokens: in.CacheWriteTokens,
		Cost:             in.Cost,
		Currency:         in.Currency,
	}
	out.Confidence = normaliseUsageConfidence(in)
	return out
}

// normaliseUsageConfidence clamps the reported confidence to the
// least-precise applicable level and downgrades to UNKNOWN when no real figures
// are present (rule §36.10).
func normaliseUsageConfidence(in protocol.UsagePayload) protocol.QuotaConfidence {
	reported := in.Confidence
	if !isKnownUsageConfidence(reported) {
		reported = protocol.QuotaConfProviderReported
	}
	if !usageHasFigures(in) {
		return protocol.QuotaConfUnknown
	}
	// The OpenCode engine reports figures it observes from the backing provider,
	// not an authoritative remaining-quota figure, so usage is at most
	// PROVIDER_REPORTED precision (never EXACT).
	if reported == protocol.QuotaConfExact {
		return protocol.QuotaConfProviderReported
	}
	return reported
}

func isKnownUsageConfidence(c protocol.QuotaConfidence) bool {
	switch c {
	case protocol.QuotaConfExact, protocol.QuotaConfProviderReported,
		protocol.QuotaConfEstimated, protocol.QuotaConfInferred, protocol.QuotaConfUnknown:
		return true
	}
	return false
}

func usageHasFigures(in protocol.UsagePayload) bool {
	return in.InputTokens != 0 || in.OutputTokens != 0 ||
		in.CacheReadTokens != 0 || in.CacheWriteTokens != 0 || in.Cost != 0
}
