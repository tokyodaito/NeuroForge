package grok

import (
	"testing"

	"neuroforge/internal/adapter/codingagent/protocol"
)

func TestMapUsageUnknownWhenEmpty(t *testing.T) {
	u := mapUsage(0, 0, 0, 0, 0, false, "")
	if u.Confidence != protocol.QuotaConfUnknown {
		t.Errorf("empty usage confidence = %s, want UNKNOWN", u.Confidence)
	}
	if u.Cost != 0 {
		t.Errorf("empty usage cost should be 0, got %v", u.Cost)
	}
	if u.Currency != "USD" {
		t.Errorf("default currency = %q, want USD", u.Currency)
	}
}

func TestMapUsageProviderReported(t *testing.T) {
	u := mapUsage(120, 80, 40, 10, 0.001, true, "USD")
	if u.Confidence != protocol.QuotaConfProviderReported {
		t.Errorf("confidence = %s, want PROVIDER_REPORTED", u.Confidence)
	}
	if u.InputTokens != 120 || u.OutputTokens != 80 || u.CacheReadTokens != 40 || u.CacheWriteTokens != 10 {
		t.Errorf("token fields not mapped: %+v", u)
	}
	if u.Cost != 0.001 {
		t.Errorf("cost = %v, want 0.001", u.Cost)
	}
}

func TestMapUsageTokensWithoutCostStillReported(t *testing.T) {
	// Tokens reported but no cost: still PROVIDER_REPORTED (not UNKNOWN), and
	// cost stays zero — we never fabricate a cost figure (rule §36.10).
	u := mapUsage(100, 50, 0, 0, 0, false, "")
	if u.Confidence != protocol.QuotaConfProviderReported {
		t.Errorf("confidence = %s, want PROVIDER_REPORTED", u.Confidence)
	}
	if u.Cost != 0 {
		t.Errorf("cost should be 0 when not reported, got %v", u.Cost)
	}
}

func TestMapUsageNeverOverstatesPrecision(t *testing.T) {
	// We never claim EXACT: Grok does not surface an authoritative remaining
	// quota, only usage tokens. EXACT is reserved for quota probes.
	u := mapUsage(10, 5, 0, 0, 0.1, true, "USD")
	if u.Confidence == protocol.QuotaConfExact {
		t.Error("usage must never claim EXACT confidence (rule §36.10)")
	}
}

func TestUsageAccumulator(t *testing.T) {
	var acc usageAccumulator
	acc.add(mapUsage(100, 50, 0, 0, 0.001, true, "USD"))
	acc.add(mapUsage(50, 30, 10, 5, 0.002, true, "USD"))
	acc.add(mapUsage(0, 0, 0, 0, 0, false, "")) // UNKNOWN item ignored for cost
	snap := acc.snapshot()
	if snap == nil {
		t.Fatal("snapshot nil")
	}
	if snap.InputTokens != 150 || snap.OutputTokens != 80 || snap.CacheReadTokens != 10 || snap.CacheWriteTokens != 5 {
		t.Errorf("accumulated tokens wrong: %+v", snap)
	}
	if snap.Cost != 0.003 {
		t.Errorf("accumulated cost = %v, want 0.003", snap.Cost)
	}
}

func TestUsageAccumulatorEmpty(t *testing.T) {
	var acc usageAccumulator
	if acc.snapshot() != nil {
		t.Error("empty accumulator should snapshot to nil")
	}
}
