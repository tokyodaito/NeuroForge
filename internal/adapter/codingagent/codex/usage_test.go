package codex

import (
	"encoding/json"
	"testing"

	"neuroforge/internal/adapter/codingagent/protocol"
)

func objJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		t.Fatalf("bad fixture JSON: %v", err)
	}
	return obj
}

func TestMapUsageCodexShape(t *testing.T) {
	obj := objJSON(t, `{"type":"token_count","input_tokens":120,"output_tokens":80,"cached_input_tokens":40,"reasoning_tokens":12,"cost_usd":0.0021}`)
	u, ok := mapUsage(obj)
	if !ok {
		t.Fatal("expected usage mapping")
	}
	if u.InputTokens != 120 {
		t.Errorf("InputTokens = %d", u.InputTokens)
	}
	if u.OutputTokens != 80 {
		t.Errorf("OutputTokens = %d", u.OutputTokens)
	}
	if u.CacheReadTokens != 40 {
		t.Errorf("CacheReadTokens = %d", u.CacheReadTokens)
	}
	if u.Cost != 0.0021 {
		t.Errorf("Cost = %v", u.Cost)
	}
	if u.Currency != "USD" {
		t.Errorf("Currency = %q", u.Currency)
	}
	if u.Confidence != protocol.QuotaConfProviderReported {
		t.Errorf("Confidence = %q, want PROVIDER_REPORTED", u.Confidence)
	}
}

func TestMapUsageFieldAliases(t *testing.T) {
	// Alternative field names seen across providers/versions.
	obj := objJSON(t, `{"prompt_tokens":10,"completion_tokens":5,"cached_read_tokens":3}`)
	u, _ := mapUsage(obj)
	if u.InputTokens != 10 || u.OutputTokens != 5 || u.CacheReadTokens != 3 {
		t.Errorf("aliases not mapped: %+v", u)
	}
}

func TestMapUsageNoNumericFieldsIsUnknown(t *testing.T) {
	// A usage event with no numeric data must be UNKNOWN and zero — never
	// fabricated (rule §36.10).
	obj := objJSON(t, `{"type":"token_count"}`)
	u, ok := mapUsage(obj)
	if !ok {
		t.Fatal("expected usage mapping even with no fields")
	}
	if u.Confidence != protocol.QuotaConfUnknown {
		t.Errorf("Confidence = %q, want UNKNOWN", u.Confidence)
	}
	if u.InputTokens != 0 || u.OutputTokens != 0 || u.Cost != 0 {
		t.Errorf("expected all-zero usage, got %+v", u)
	}
}

func TestMapUsageZeroIsReportedNotFabricated(t *testing.T) {
	// A genuine reported zero is a real value, not a fabrication.
	obj := objJSON(t, `{"input_tokens":0,"output_tokens":0}`)
	u, _ := mapUsage(obj)
	// All zero but no cost: confidence UNKNOWN (nothing concrete reported).
	if u.Confidence != protocol.QuotaConfUnknown {
		t.Errorf("Confidence = %q, want UNKNOWN for all-zero", u.Confidence)
	}
}

func TestMapUsageCostWithoutCurrencyOnlyWhenPresent(t *testing.T) {
	obj := objJSON(t, `{"input_tokens":1,"output_tokens":1}`)
	u, _ := mapUsage(obj)
	if u.Currency != "" {
		t.Errorf("Currency should be empty when no cost reported, got %q", u.Currency)
	}
	if u.Cost != 0 {
		t.Errorf("Cost should be 0 when absent, got %v", u.Cost)
	}
}

func TestRedactStripsTokens(t *testing.T) {
	in := `error: unauthorized: bearer sk-1234567890abcdefghij ghp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa OPENAI_API_KEY=sk-leaked`
	out := redact(in)
	for _, secret := range []string{"sk-1234567890abcdefghij", "ghp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "sk-leaked"} {
		if contains(out, secret) {
			t.Errorf("redact leaked %q: %s", secret, out)
		}
	}
	if !contains(out, "[REDACTED]") {
		t.Errorf("redact did not insert [REDACTED]: %s", out)
	}
}
