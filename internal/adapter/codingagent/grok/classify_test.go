package grok

import (
	"testing"

	"neuroforge/internal/adapter/codingagent/protocol"
)

func TestClassifyDistinctClasses(t *testing.T) {
	cases := []struct {
		name    string
		exit    int
		events  []protocol.NormalizedEvent
		stderr  string
		wantCls protocol.FailureClass
	}{
		{"quota", 2, nil, "error: quota exhausted before any edits\n", protocol.FailureProviderQuota},
		{"rate-limit", 2, nil, "HTTP 429 too many requests\n", protocol.FailureProviderRateLimit},
		{"capacity", 2, nil, "provider capacity: overloaded (503)\n", protocol.FailureProviderCapacity},
		{"auth", 2, nil, "401 unauthorized: invalid api key\n", protocol.FailureProviderAuth},
		{"model-not-available", 2, nil, "model not available for this account\n", protocol.FailureModelNotAvailable},
		{"crash-signal", 134, nil, "panic: stack overflow\n", protocol.FailureEngineCrash},
		{"crash-exit-only", 134, nil, "", protocol.FailureEngineCrash},
		{"timeout-exit", 124, nil, "", protocol.FailureTimeout},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fc := grokClassify(c.exit, c.events, c.stderr)
			if fc.Class != c.wantCls {
				t.Errorf("class = %s, want %s (reason=%q)", fc.Class, c.wantCls, fc.Reason)
			}
			// No class may map to an infinite retry (rule §32).
			if fc.Retryable && fc.MaxRetries <= 0 {
				t.Errorf("class %s retryable with no bound", fc.Class)
			}
		})
	}
}

func TestClassifyCapacityDistinctFromQuotaAndRateLimit(t *testing.T) {
	// The three transient provider classes must NOT collapse into one.
	quota := grokClassify(2, nil, "quota exhausted\n")
	rl := grokClassify(2, nil, "429 too many requests\n")
	cap := grokClassify(2, nil, "service overloaded (503)\n")
	if quota.Class == cap.Class || rl.Class == cap.Class || quota.Class == rl.Class {
		t.Errorf("quota/rate-limit/capacity collapsed: %s/%s/%s", quota.Class, rl.Class, cap.Class)
	}
	if cap.Class != protocol.FailureProviderCapacity {
		t.Errorf("capacity = %s, want PROVIDER_CAPACITY", cap.Class)
	}
	if cap.Retryable == false && cap.Failover == false {
		t.Error("capacity should be retryable or failover")
	}
}

func TestClassifyHonoursExplicitEvent(t *testing.T) {
	// An explicit run.failed event in the stream wins (used by the synthesized
	// terminal path and by the engine's own result/failed item).
	evs := []protocol.NormalizedEvent{{
		Type:    protocol.EventRunFailed,
		Failure: &protocol.FailurePayload{Class: protocol.FailureProviderQuota, Reason: "engine said quota"},
	}}
	fc := grokClassify(2, evs, "")
	if fc.Class != protocol.FailureProviderQuota {
		t.Errorf("class = %s, want PROVIDER_QUOTA", fc.Class)
	}
	if fc.Failover == false {
		t.Error("quota should suggest failover")
	}
}

func TestClassifyCancelledEvent(t *testing.T) {
	evs := []protocol.NormalizedEvent{{Type: protocol.EventRunCancelled}}
	fc := grokClassify(137, evs, "")
	if fc.Class != protocol.FailureCancelled {
		t.Errorf("class = %s, want CANCELLED", fc.Class)
	}
	if fc.Policy != protocol.PolicyTerminal {
		t.Errorf("cancelled should be terminal, got %s", fc.Policy)
	}
}

func TestClassifyZeroExitIsTerminalNonFailure(t *testing.T) {
	fc := grokClassify(0, nil, "")
	// exit 0 with no failure signal: DefaultClassify returns terminal internal
	// error with Retryable=false (there is nothing to retry).
	if fc.Retryable {
		t.Error("exit-0 classify should not be retryable")
	}
}

func TestClassifyRedactsSecretsInReason(t *testing.T) {
	fc := grokClassify(2, nil, "401 unauthorized: invalid api key sk-abcd1234efgh5678\n")
	if fc.Reason == "" {
		t.Fatal("reason empty")
	}
	// The key body must be scrubbed.
	if containsStr(fc.Reason, "abcd1234efgh5678") {
		t.Errorf("secret not redacted in reason: %q", fc.Reason)
	}
}

func TestRedactSecrets(t *testing.T) {
	in := "bearer dQw4w9WgXcQAAAAA and token=abcdef123456 ghp_0123456789abcdef sk-1234567890abcdef"
	out := redactSecrets(in)
	if containsStr(out, "dQw4w9WgXcQAAAAA") || containsStr(out, "abcdef123456") || containsStr(out, "ghp_0123456789") || containsStr(out, "sk-1234567890") {
		t.Errorf("secrets not redacted: %q", out)
	}
	if out == "" {
		t.Error("redacted output empty")
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
