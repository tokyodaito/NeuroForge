package grok

import (
	"neuroforge/internal/adapter/codingagent/protocol"
)

// mapUsage converts a Grok usage item into a [protocol.UsagePayload] honouring
// spec §22 / §14.4 and the confidence contract (rule §36.10): never overstate
// precision. When the item carries no token figures the confidence is UNKNOWN
// and all fields stay zero; cached-token fields are populated only when the
// engine reports them.
func mapUsage(in, out, cacheRead, cacheWrite int64, cost float64, hasCost bool, currency string) *protocol.UsagePayload {
	u := &protocol.UsagePayload{
		InputTokens:      in,
		OutputTokens:     out,
		CacheReadTokens:  cacheRead,
		CacheWriteTokens: cacheWrite,
	}

	switch {
	case in <= 0 && out <= 0 && cacheRead <= 0 && cacheWrite <= 0:
		// No token data at all.
		u.Confidence = protocol.QuotaConfUnknown
	case cost > 0 || hasCost:
		// Provider surfaced an authoritative cost figure alongside tokens.
		u.Confidence = protocol.QuotaConfProviderReported
	default:
		// Tokens reported but no cost: provider-reported usage precision only.
		u.Confidence = protocol.QuotaConfProviderReported
	}

	if hasCost {
		u.Cost = cost
	}
	if currency != "" {
		u.Currency = currency
	} else {
		u.Currency = "USD"
	}
	return u
}

// mergeUsageAccumulators is a running tally used to surface the final usage of a
// run when the engine emits multiple usage items. It sums tokens and cost.
type usageAccumulator struct {
	in, out, cacheRead, cacheWrite int64
	cost                           float64
	hasCost                        bool
}

func (a *usageAccumulator) add(u *protocol.UsagePayload) {
	if u == nil {
		return
	}
	a.in += u.InputTokens
	a.out += u.OutputTokens
	a.cacheRead += u.CacheReadTokens
	a.cacheWrite += u.CacheWriteTokens
	if u.Confidence != protocol.QuotaConfUnknown {
		a.cost += u.Cost
		if u.Cost > 0 {
			a.hasCost = true
		}
	}
}

// snapshot returns the accumulated usage as a payload, or nil if nothing was
// reported.
func (a *usageAccumulator) snapshot() *protocol.UsagePayload {
	if a.in == 0 && a.out == 0 && a.cacheRead == 0 && a.cacheWrite == 0 && !a.hasCost {
		return nil
	}
	return mapUsage(a.in, a.out, a.cacheRead, a.cacheWrite, a.cost, a.hasCost, "USD")
}
