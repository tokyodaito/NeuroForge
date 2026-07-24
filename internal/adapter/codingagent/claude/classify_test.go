package claude

import (
	"testing"

	"neuroforge/internal/adapter/codingagent/protocol"
)

func TestClassifyHonoursExplicitFailureEvent(t *testing.T) {
	a, _ := New(Options{BinaryPath: "claude"})
	events := []protocol.NormalizedEvent{{
		Type:    protocol.EventRunFailed,
		Failure: &protocol.FailurePayload{Class: protocol.FailureProviderQuota, Reason: "sub quota"},
	}}
	fc := a.ClassifyFailure(2, events, "")
	if fc.Class != protocol.FailureProviderQuota {
		t.Errorf("class = %s", fc.Class)
	}
	if !fc.Failover {
		t.Errorf("quota should failover")
	}
	if fc.MaxRetries <= 0 {
		t.Errorf("MaxRetries should be bounded > 0")
	}
}

func TestClassifyCancelled(t *testing.T) {
	a, _ := New(Options{BinaryPath: "claude"})
	events := []protocol.NormalizedEvent{{Type: protocol.EventRunCancelled}}
	fc := a.ClassifyFailure(137, events, "")
	if fc.Class != protocol.FailureCancelled {
		t.Errorf("class = %s, want CANCELLED", fc.Class)
	}
	if fc.Policy != protocol.PolicyTerminal {
		t.Errorf("policy = %s, want terminal", fc.Policy)
	}
}

func TestClassifyStderrSignals(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
		want   protocol.FailureClass
	}{
		{"rate", "HTTP 429 too many requests", protocol.FailureProviderRateLimit},
		{"quota", "error: subscription quota exhausted", protocol.FailureProviderQuota},
		{"auth", "401 unauthorized: invalid api key", protocol.FailureProviderAuth},
		{"model", "model not found", protocol.FailureModelNotAvailable},
		{"capacity", "overloaded (529)", protocol.FailureProviderCapacity},
		{"malformed", "invalid_request: bad params", protocol.FailureMalformedOutput},
	}
	a, _ := New(Options{BinaryPath: "claude"})
	for _, c := range cases {
		fc := a.ClassifyFailure(2, nil, c.stderr)
		if fc.Class != c.want {
			t.Errorf("%s: class = %s, want %s", c.name, fc.Class, c.want)
		}
	}
}

func TestClassifyEngineCrashFromExitCode(t *testing.T) {
	a, _ := New(Options{BinaryPath: "claude"})
	// exit >= 128 → engine crash via DefaultClassify fallback.
	fc := a.ClassifyFailure(134, nil, "aborted")
	if fc.Class != protocol.FailureEngineCrash {
		t.Errorf("class = %s, want ENGINE_CRASH", fc.Class)
	}
	if !fc.Retryable || fc.MaxRetries <= 0 {
		t.Errorf("crash should be bounded-retry: %+v", fc)
	}
}

func TestClassifyTimeoutFromExitCode(t *testing.T) {
	a, _ := New(Options{BinaryPath: "claude"})
	fc := a.ClassifyFailure(124, nil, "")
	if fc.Class != protocol.FailureTimeout {
		t.Errorf("class = %s, want TIMEOUT", fc.Class)
	}
}

func TestClassifyRefinesAmbiguousInternal(t *testing.T) {
	// An explicit run.failed with INTERNAL_ERROR (from result error_during_execution)
	// whose stderr names a provider condition should be refined to that class.
	a, _ := New(Options{BinaryPath: "claude"})
	events := []protocol.NormalizedEvent{{
		Type:    protocol.EventRunFailed,
		Failure: &protocol.FailurePayload{Class: protocol.FailureInternalError, Reason: "exec error"},
	}}
	fc := a.ClassifyFailure(2, events, "billing_error: quota gone")
	if fc.Class != protocol.FailureProviderQuota {
		t.Errorf("class = %s, want PROVIDER_QUOTA after refinement", fc.Class)
	}
}

func TestClassifyNoUnboundedRetry(t *testing.T) {
	a, _ := New(Options{BinaryPath: "claude"})
	for _, code := range []int{0, 1, 2, 124, 134, 137} {
		fc := a.ClassifyFailure(code, nil, "rate limit")
		// Every retryable class must have a bounded MaxRetries (rule §32).
		if fc.Retryable && fc.MaxRetries <= 0 {
			t.Errorf("code %d: retryable but MaxRetries=%d (unbounded)", code, fc.MaxRetries)
		}
	}
}

func TestClassifyRedactsStderr(t *testing.T) {
	a, _ := New(Options{BinaryPath: "claude"})
	const secret = "sk-ant-api03-VERYSECRET0123456789"
	fc := a.ClassifyFailure(2, nil, "rate_limit "+secret)
	// The returned reason comes from DefaultPolicy (fixed string) — but ensure no
	// reason/output path echoes the raw secret.
	if containsSecret(fc.Reason, secret) {
		t.Errorf("secret leaked into reason: %q", fc.Reason)
	}
}

func containsSecret(s, secret string) bool {
	return len(s) > 0 && len(secret) > 0 && indexOf(s, secret) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
