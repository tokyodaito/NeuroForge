package kimi

import (
	"strings"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/protocol"
)

// usagePayload maps a Kimi usage object (plus any top-level usage fields on the
// carrying item) onto a protocol.UsagePayload with a correct QuotaConfidence
// (spec §22, §36.10). Confidence is PROVIDER_REPORTED when the engine supplied
// token counts, and UNKNOWN when nothing was reported — the adapter never
// fabricates numbers or overstates precision.
//
// Thinking/reasoning tokens: Kimi may report them, but protocol.UsagePayload
// has no reasoning field; rather than mislabel them, they are dropped and the
// confidence remains PROVIDER_REPORTED for the input/output axes that ARE
// reported. When reasoning tokens are the ONLY signal, confidence is UNKNOWN.
func usagePayload(u *kimiUsage, it kimiItem, fallbackCurrency string) *protocol.UsagePayload {
	p := &protocol.UsagePayload{Confidence: protocol.QuotaConfUnknown}

	if u == nil && it.Cost == nil {
		// Nothing reported at all: keep UNKNOWN with no fabricated values.
		return p
	}

	in, out, cr, cw, cost, currency, reported := readUsage(u, it, fallbackCurrency)
	p.InputTokens = in
	p.OutputTokens = out
	p.CacheReadTokens = cr
	p.CacheWriteTokens = cw
	p.Cost = cost
	if currency != "" {
		p.Currency = currency
	}
	if reported {
		p.Confidence = protocol.QuotaConfProviderReported
	}
	return p
}

// readUsage dereferences the optional usage fields into plain values (nil → 0)
// and reports whether any count/cost was actually supplied by the engine.
func readUsage(u *kimiUsage, it kimiItem, fallbackCurrency string) (in, out, cr, cw int64, cost float64, currency string, reported bool) {
	if u != nil {
		in = val(u.InputTokens)
		out = val(u.OutputTokens)
		cr = val(u.CacheReadTokens)
		cw = val(u.CacheWriteTokens)
		cost = fval(u.Cost)
		currency = u.Currency
		if u.InputTokens != nil || u.OutputTokens != nil || u.CacheReadTokens != nil || u.CacheWriteTokens != nil || u.Cost != nil {
			reported = true
		}
	}
	if it.Cost != nil {
		cost = *it.Cost
		reported = true
	}
	if currency == "" {
		currency = fallbackCurrency
	}
	return
}

func val(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func fval(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// classFromText infers a §32 failure class from human-readable error text. It is
// shared by the parser (for inline result/error items) and the failure
// classifier. Unknown text falls back to INTERNAL_ERROR.
func classFromText(text string) protocol.FailureClass {
	low := strings.ToLower(text)
	switch {
	case strings.Contains(low, "quota") || strings.Contains(low, "exhausted") || strings.Contains(low, "limit exceeded") || strings.Contains(low, "余额") || strings.Contains(low, "配额"):
		return protocol.FailureProviderQuota
	case strings.Contains(low, "rate limit") || strings.Contains(low, "429") || strings.Contains(low, "too many requests") || strings.Contains(low, "频率"):
		return protocol.FailureProviderRateLimit
	case strings.Contains(low, "overloaded") || strings.Contains(low, "capacity") || strings.Contains(low, "503") || strings.Contains(low, "529") || strings.Contains(low, "繁忙"):
		return protocol.FailureProviderCapacity
	case strings.Contains(low, "unauthorized") || strings.Contains(low, "401") || strings.Contains(low, "auth") || strings.Contains(low, "api key") || strings.Contains(low, "登录") || strings.Contains(low, "认证"):
		return protocol.FailureProviderAuth
	case strings.Contains(low, "model") && (strings.Contains(low, "not available") || strings.Contains(low, "not found") || strings.Contains(low, "不存在")):
		return protocol.FailureModelNotAvailable
	case strings.Contains(low, "timeout") || strings.Contains(low, "timed out") || strings.Contains(low, "超时"):
		return protocol.FailureTimeout
	}
	return protocol.FailureInternalError
}

// classifyFailure implements codingagent.Adapter.ClassifyFailure. It prefers an
// explicit terminal failure event from the run, then refines Kimi-specific
// signals (capacity, model-not-available) that the shared heuristic does not
// cover, and otherwise defers to [codingagent.DefaultClassify]. Every class maps
// to a bounded policy (rule §32: no infinite retry).
func (a *Adapter) classifyFailure(exitCode int, events []protocol.NormalizedEvent, stderr string) protocol.FailureClassification {
	stderr = redact(stderr)

	// 1. Honour an explicit terminal failure/cancel event emitted by the run.
	for _, ev := range events {
		if ev.Type == protocol.EventRunFailed && ev.Failure != nil && ev.Failure.Class.IsValid() {
			fc := protocol.DefaultPolicy(ev.Failure.Class)
			fc.ExitCode = exitCode
			if ev.Failure.Reason != "" {
				fc.Reason = ev.Failure.Reason
			}
			return refineClass(fc, ev.Failure.Reason)
		}
		if ev.Type == protocol.EventRunCancelled {
			fc := protocol.DefaultPolicy(protocol.FailureCancelled)
			fc.ExitCode = exitCode
			return fc
		}
	}

	// 2. Refine Kimi-specific stderr signals that DefaultClassify misses
	// (capacity, model-not-available, explicit timeout text).
	if c := classFromText(stderr); c != protocol.FailureInternalError {
		fc := protocol.DefaultPolicy(c)
		fc.ExitCode = exitCode
		return fc
	}

	// 3. Defer to the shared heuristic for the remaining taxonomy.
	return codingagent.DefaultClassify(exitCode, events, stderr)
}

// refineClass lets a failure event's own reason text upgrade the class when the
// engine emitted a generic class but the reason carries a more specific signal.
func refineClass(fc protocol.FailureClassification, reason string) protocol.FailureClassification {
	if reason == "" {
		return fc
	}
	if c := classFromText(reason); c != protocol.FailureInternalError && c != fc.Class {
		// Only override when the text is unambiguous AND more specific than a
		// generic INTERNAL_ERROR bucket.
		if fc.Class == protocol.FailureInternalError {
			upgraded := protocol.DefaultPolicy(c)
			upgraded.ExitCode = fc.ExitCode
			return upgraded
		}
	}
	return fc
}
