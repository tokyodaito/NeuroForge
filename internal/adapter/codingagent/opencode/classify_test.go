package opencode

import (
	"strings"
	"testing"

	"neuroforge/internal/adapter/codingagent/protocol"
)

func TestClassifyQuotaWithProviderProvenance(t *testing.T) {
	a := stubAdapter("", "", 0, false, "")
	fc := a.ClassifyFailure(2, nil, "anthropic: quota exhausted before edits\n")
	if fc.Class != protocol.FailureProviderQuota {
		t.Fatalf("class = %s, want PROVIDER_QUOTA", fc.Class)
	}
	if !fc.Failover {
		t.Error("quota should suggest failover")
	}
	if !strings.Contains(fc.Reason, "provider=anthropic") {
		t.Errorf("reason missing provider provenance: %q", fc.Reason)
	}
}

func TestClassifyRateLimitWithRetry(t *testing.T) {
	a := stubAdapter("", "", 0, false, "")
	fc := a.ClassifyFailure(2, nil, "openai: HTTP 429 too many requests\n")
	if fc.Class != protocol.FailureProviderRateLimit {
		t.Fatalf("class = %s, want PROVIDER_RATE_LIMIT", fc.Class)
	}
	if !fc.Retryable {
		t.Error("rate limit should be retryable")
	}
	if fc.MaxRetries <= 0 {
		t.Error("retryable class must be bounded (no infinite retry §32)")
	}
	if !strings.Contains(fc.Reason, "provider=openai") {
		t.Errorf("reason missing provider: %q", fc.Reason)
	}
}

func TestClassifyAuthBounded(t *testing.T) {
	a := stubAdapter("", "", 0, false, "")
	fc := a.ClassifyFailure(2, nil, "401 unauthorized: invalid api key\n")
	if fc.Class != protocol.FailureProviderAuth {
		t.Fatalf("class = %s, want PROVIDER_AUTH", fc.Class)
	}
}

func TestClassifyHonoursRunFailedEvent(t *testing.T) {
	a := stubAdapter("", "", 0, false, "")
	events := []protocol.NormalizedEvent{{
		Type:    protocol.EventRunFailed,
		Failure: &protocol.FailurePayload{Class: protocol.FailureModelNotAvailable, Reason: "model not available"},
	}}
	fc := a.ClassifyFailure(2, events, "")
	if fc.Class != protocol.FailureModelNotAvailable {
		t.Fatalf("class = %s, want MODEL_NOT_AVAILABLE", fc.Class)
	}
}

func TestClassifyCancelledFromEvent(t *testing.T) {
	a := stubAdapter("", "", 0, false, "")
	events := []protocol.NormalizedEvent{{Type: protocol.EventRunCancelled}}
	fc := a.ClassifyFailure(137, events, "")
	if fc.Class != protocol.FailureCancelled {
		t.Fatalf("class = %s, want CANCELLED", fc.Class)
	}
}

func TestClassifyCrashFromExitCode(t *testing.T) {
	a := stubAdapter("", "", 0, false, "")
	fc := a.ClassifyFailure(134, nil, "opencode panicked\n")
	if fc.Class != protocol.FailureEngineCrash {
		t.Fatalf("class = %s, want ENGINE_CRASH", fc.Class)
	}
}

func TestClassifyNeverInfiniteRetry(t *testing.T) {
	a := stubAdapter("", "", 0, false, "")
	for _, code := range []int{0, 1, 2, 124, 134, 137, 255} {
		fc := a.ClassifyFailure(code, nil, "some transient error\n")
		if fc.Retryable && fc.MaxRetries <= 0 {
			t.Errorf("exit %d: retryable with unbounded/zero MaxRetries", code)
		}
	}
}

func TestClassifyProviderProvenanceIdempotent(t *testing.T) {
	a := stubAdapter("", "", 0, false, "")
	fc := a.ClassifyFailure(2, nil, "gemini: quota exhausted\n")
	if strings.Count(fc.Reason, "provider=") > 1 {
		t.Errorf("provider marker duplicated: %q", fc.Reason)
	}
}
