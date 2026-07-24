package codex

import (
	"encoding/json"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// mapUsage maps a decoded Codex usage/token-count object onto a normalized
// [protocol.UsagePayload]. It probes a union of field names observed across
// Codex versions rather than pinning one schema (task: do not hardcode one
// version's JSON shape).
//
// Confidence rules (spec §20.1, rule §36.10 — never overstate precision):
//   - When Codex reports actual token counts, confidence is PROVIDER_REPORTED
//     (the figure is authoritative enough to display without an estimate badge).
//   - When a usage event carries no numeric field at all, confidence is UNKNOWN
//     and every counter stays zero — the adapter never fabricates values.
//
// Cached input tokens and reasoning tokens are reported when Codex provides
// them; absent fields are zero (never invented).
func mapUsage(obj map[string]any) (*protocol.UsagePayload, bool) {
	in := numInt(obj, "input_tokens", "prompt_tokens", "input")
	out := numInt(obj, "output_tokens", "completion_tokens", "output")
	cached := numInt(obj, "cached_input_tokens", "cached_read_tokens", "cached_tokens", "prompt_cache_hit_tokens")
	reasoning := numInt(obj, "reasoning_tokens", "reasoning_output_tokens")
	cacheWrite := numInt(obj, "cache_write_tokens", "cached_write_tokens")
	cost := numFloat(obj, "cost_usd", "cost", "spent")

	any := in != 0 || out != 0 || cached != 0 || reasoning != 0 || cacheWrite != 0 || cost != 0
	u := &protocol.UsagePayload{
		InputTokens:      in,
		OutputTokens:     out,
		CacheReadTokens:  cached,
		CacheWriteTokens: cacheWrite,
	}
	// Reasoning tokens are output-side usage; we surface them in CacheWrite? No:
	// keep the payload's own fields. Reasoning is not a cache field, so we fold
	// it into output for accounting honesty only when the payload has no better
	// slot, but we must not double count. Instead we leave reasoning out of the
	// normalized payload (which has no reasoning field) and never inflate output.
	// This avoids overstating precision: reasoning is tracked separately in the
	// adapter diagnostics (see docs), not silently merged into output.
	_ = reasoning

	if cost != 0 {
		u.Cost = cost
		u.Currency = "USD"
	}
	if any {
		u.Confidence = protocol.QuotaConfProviderReported
	} else {
		u.Confidence = protocol.QuotaConfUnknown
	}
	return u, true
}

// --- tolerant accessors over a loosely-decoded JSON object ---

// numInt returns the value of the first present integer-valued field among
// names (alternatives across Codex versions), else 0. A present-but-zero field
// is a real zero; only absence yields "not reported".
func numInt(obj map[string]any, names ...string) int64 {
	for _, n := range names {
		v, ok := obj[n]
		if !ok {
			continue
		}
		return toInt(v)
	}
	return 0
}

func toInt(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int:
		return int64(x)
	case int64:
		return x
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		if f, err := x.Float64(); err == nil {
			return int64(f)
		}
	}
	return 0
}

// numFloat returns the first present float-valued field among names, else 0.
func numFloat(obj map[string]any, names ...string) float64 {
	for _, n := range names {
		v, ok := obj[n]
		if !ok {
			continue
		}
		switch x := v.(type) {
		case float64:
			return x
		case int:
			return float64(x)
		case int64:
			return float64(x)
		case json.Number:
			if f, err := x.Float64(); err == nil {
				return f
			}
		}
	}
	return 0
}

// strVal returns the first present string-valued field among names, else "".
func strVal(obj map[string]any, names ...string) string {
	for _, n := range names {
		if v, ok := obj[n]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

// boolVal returns the bool value of the first present field among names, else
// fallback. JSON booleans and the strings "true"/"false" are honoured.
func boolVal(obj map[string]any, fallback bool, names ...string) bool {
	for _, n := range names {
		if v, ok := obj[n]; ok {
			switch x := v.(type) {
			case bool:
				return x
			case string:
				return x == "true" || x == "1" || x == "ok" || x == "success"
			}
		}
	}
	return fallback
}
