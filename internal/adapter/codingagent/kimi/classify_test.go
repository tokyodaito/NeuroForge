package kimi

import (
	"strings"
	"testing"

	"neuroforge/internal/adapter/codingagent/protocol"
)

func TestClassifyExplicitFailureEvent(t *testing.T) {
	a := New(Options{})
	evs := []protocol.NormalizedEvent{{
		Type:    protocol.EventRunFailed,
		Failure: &protocol.FailurePayload{Class: protocol.FailureProviderQuota, Reason: "quota exhausted"},
	}}
	fc := a.ClassifyFailure(2, evs, "")
	if fc.Class != protocol.FailureProviderQuota {
		t.Errorf("class = %s, want PROVIDER_QUOTA", fc.Class)
	}
	if !fc.Failover {
		t.Error("quota failure should suggest failover")
	}
	if fc.MaxRetries <= 0 || fc.MaxRetries > 5 {
		t.Errorf("MaxRetries = %d (must be bounded >0)", fc.MaxRetries)
	}
}

func TestClassifyCancelEvent(t *testing.T) {
	a := New(Options{})
	evs := []protocol.NormalizedEvent{{Type: protocol.EventRunCancelled}}
	fc := a.ClassifyFailure(137, evs, "")
	if fc.Class != protocol.FailureCancelled {
		t.Errorf("class = %s, want CANCELLED", fc.Class)
	}
	if fc.Policy != protocol.PolicyTerminal {
		t.Errorf("cancel should be terminal, got %s", fc.Policy)
	}
}

func TestClassifyStderrSignals(t *testing.T) {
	a := New(Options{})
	cases := []struct {
		stderr string
		exit   int
		want   protocol.FailureClass
	}{
		{"error: quota exhausted", 2, protocol.FailureProviderQuota},
		{"HTTP 429 too many requests", 2, protocol.FailureProviderRateLimit},
		{"server overloaded / capacity", 503, protocol.FailureProviderCapacity},
		{"401 unauthorized: invalid api key", 2, protocol.FailureProviderAuth},
		{"model kimi-x not available", 2, protocol.FailureModelNotAvailable},
		{"request timed out", 124, protocol.FailureTimeout},
	}
	for _, c := range cases {
		fc := a.ClassifyFailure(c.exit, nil, c.stderr)
		if fc.Class != c.want {
			t.Errorf("ClassifyFailure(%q, exit=%d) = %s, want %s", c.stderr, c.exit, fc.Class, c.want)
		}
		// Every class must have a bounded retry (rule §32: no infinite retry).
		if fc.Retryable && fc.MaxRetries <= 0 {
			t.Errorf("retryable class %s has unbounded MaxRetries=%d", fc.Class, fc.MaxRetries)
		}
	}
}

func TestClassifyEngineCrashFromExitCode(t *testing.T) {
	a := New(Options{})
	// 134 = 128+6 (SIGABRT); DefaultClassify maps >=128 to ENGINE_CRASH.
	fc := a.ClassifyFailure(134, nil, "kimi agent panicked")
	if fc.Class != protocol.FailureEngineCrash {
		t.Errorf("class = %s, want ENGINE_CRASH", fc.Class)
	}
	if !fc.Retryable || fc.MaxRetries <= 0 {
		t.Errorf("crash should be bounded-retryable: %+v", fc)
	}
}

func TestClassifyRedactsStderr(t *testing.T) {
	a := New(Options{})
	// Stderr containing a secret must not surface it in the classification reason.
	fc := a.ClassifyFailure(2, nil, "error: quota exhausted token=sk-supersecretvalue")
	if strings.Contains(fc.Reason, "supersecretvalue") {
		t.Errorf("secret leaked into reason: %q", fc.Reason)
	}
}

func TestClassifyModelNotAvailableFailover(t *testing.T) {
	a := New(Options{})
	fc := a.ClassifyFailure(2, nil, "model not available")
	if fc.Class != protocol.FailureModelNotAvailable {
		t.Fatalf("class = %s", fc.Class)
	}
	if !fc.Failover {
		t.Error("model-not-available should suggest failover (§21)")
	}
}

func TestNoInfiniteRetry(t *testing.T) {
	// Sweep every §32 class via DefaultPolicy and confirm none is unbounded.
	a := New(Options{})
	classes := []protocol.FailureClass{
		protocol.FailureProviderQuota, protocol.FailureProviderRateLimit,
		protocol.FailureProviderCapacity, protocol.FailureProviderAuth,
		protocol.FailureEngineCrash, protocol.FailureEngineProtocol,
		protocol.FailureModelNotAvailable, protocol.FailureTimeout,
		protocol.FailureCancelled, protocol.FailureMalformedOutput,
		protocol.FailureInternalError,
	}
	for _, c := range classes {
		fc := a.classifyFailure(1, []protocol.NormalizedEvent{{
			Type: protocol.EventRunFailed, Failure: &protocol.FailurePayload{Class: c},
		}}, "")
		if fc.Retryable && fc.MaxRetries <= 0 {
			t.Errorf("class %s: retryable with unbounded MaxRetries=%d", c, fc.MaxRetries)
		}
	}
}
