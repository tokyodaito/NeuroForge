package opencode

import (
	"context"
	"testing"

	"neuroforge/internal/adapter/codingagent/protocol"
)

func TestMapUsageClampsExactToProviderReported(t *testing.T) {
	in := protocol.UsagePayload{InputTokens: 10, OutputTokens: 5, Cost: 0.1, Confidence: protocol.QuotaConfExact}
	out := mapUsage(in)
	if out.Confidence != protocol.QuotaConfProviderReported {
		t.Errorf("EXACT must clamp to PROVIDER_REPORTED, got %s", out.Confidence)
	}
}

func TestMapUsageUnknownWhenNoFigures(t *testing.T) {
	in := protocol.UsagePayload{Confidence: protocol.QuotaConfProviderReported} // all zero
	out := mapUsage(in)
	if out.Confidence != protocol.QuotaConfUnknown {
		t.Errorf("no figures => UNKNOWN, got %s", out.Confidence)
	}
}

func TestMapUsagePreservesProviderReported(t *testing.T) {
	in := protocol.UsagePayload{InputTokens: 1, Confidence: protocol.QuotaConfProviderReported}
	out := mapUsage(in)
	if out.Confidence != protocol.QuotaConfProviderReported {
		t.Errorf("got %s", out.Confidence)
	}
}

func TestMapUsageCarriesCachedTokens(t *testing.T) {
	in := protocol.UsagePayload{InputTokens: 100, OutputTokens: 50, CacheReadTokens: 40, CacheWriteTokens: 10, Cost: 0.001, Currency: "USD", Confidence: protocol.QuotaConfProviderReported}
	out := mapUsage(in)
	if out.CacheReadTokens != 40 || out.CacheWriteTokens != 10 {
		t.Errorf("cached tokens lost: %+v", out)
	}
	if out.Currency != "USD" {
		t.Errorf("currency lost: %+v", out)
	}
}

func TestInspectQuotaUnknown(t *testing.T) {
	a := stubAdapter("", "", 0, false, "")
	q := a.InspectQuota(context.Background(), protocol.Account{})
	if q.Confidence != protocol.QuotaConfUnknown {
		t.Errorf("confidence = %s, want UNKNOWN", q.Confidence)
	}
	if q.State != protocol.QuotaStateUnknown {
		t.Errorf("state = %s, want UNKNOWN", q.State)
	}
}

func TestListModelsReturnsNothing(t *testing.T) {
	a := stubAdapter("", "", 0, false, "")
	ms, err := a.ListModels(context.Background(), protocol.Account{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(ms) != 0 {
		t.Errorf("expected no descriptors (no hardcoded models §36.8/§36.25), got %d", len(ms))
	}
}
