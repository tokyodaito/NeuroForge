package codex

import (
	"testing"

	"neuroforge/internal/adapter/codingagent/protocol"
)

func TestClassifyQuota(t *testing.T) {
	fc := classifyFailure(2, nil, "error: quota exhausted before any edits\n")
	if fc.Class != protocol.FailureProviderQuota {
		t.Errorf("class = %s, want PROVIDER_QUOTA", fc.Class)
	}
	if !fc.Failover {
		t.Error("quota should suggest failover")
	}
	if fc.MaxRetries <= 0 {
		t.Error("MaxRetries must be bounded > 0")
	}
}

func TestClassifyRateLimit(t *testing.T) {
	fc := classifyFailure(2, nil, "HTTP 429 too many requests\n")
	if fc.Class != protocol.FailureProviderRateLimit {
		t.Errorf("class = %s, want PROVIDER_RATE_LIMIT", fc.Class)
	}
}

func TestClassifyAuthCodexLogin(t *testing.T) {
	// Codex-specific auth phrasing not covered by DefaultClassify.
	fc := classifyFailure(1, nil, "not logged in; run `codex login` to continue")
	if fc.Class != protocol.FailureProviderAuth {
		t.Errorf("class = %s, want PROVIDER_AUTH", fc.Class)
	}
}

func TestClassifyAuthGeneric(t *testing.T) {
	fc := classifyFailure(1, nil, "401 unauthorized: invalid api key")
	if fc.Class != protocol.FailureProviderAuth {
		t.Errorf("class = %s, want PROVIDER_AUTH", fc.Class)
	}
}

func TestClassifyCapacity(t *testing.T) {
	// Provider capacity is the Codex-specific refinement (DefaultClassify would
	// fall through to malformed).
	fc := classifyFailure(1, nil, "the server is overloaded / at capacity")
	if fc.Class != protocol.FailureProviderCapacity {
		t.Errorf("class = %s, want PROVIDER_CAPACITY", fc.Class)
	}
	if !fc.Retryable {
		t.Error("capacity should be retryable")
	}
}

func TestClassifyModelNotAvailable(t *testing.T) {
	fc := classifyFailure(1, nil, "model does not exist: no such model")
	if fc.Class != protocol.FailureModelNotAvailable {
		t.Errorf("class = %s, want MODEL_NOT_AVAILABLE", fc.Class)
	}
}

func TestClassifyEngineCrashBySignalExitCode(t *testing.T) {
	// Exit code >= 128 (signal) → engine crash.
	fc := classifyFailure(134, nil, "")
	if fc.Class != protocol.FailureEngineCrash {
		t.Errorf("class = %s, want ENGINE_CRASH", fc.Class)
	}
}

func TestClassifyHonoursTypedFailureEvent(t *testing.T) {
	// An explicit run.failed event with a valid class wins over stderr noise.
	events := []protocol.NormalizedEvent{
		{Type: protocol.EventRunFailed, Failure: &protocol.FailurePayload{Class: protocol.FailureProviderQuota, Reason: "explicit"}},
	}
	fc := classifyFailure(2, events, "some unrelated stderr")
	if fc.Class != protocol.FailureProviderQuota {
		t.Errorf("class = %s, want PROVIDER_QUOTA from typed event", fc.Class)
	}
	if fc.Reason != "explicit" {
		t.Errorf("reason = %q", fc.Reason)
	}
}

func TestClassifyTimeoutExitCodes(t *testing.T) {
	for _, code := range []int{124, 137} {
		fc := classifyFailure(code, nil, "")
		if fc.Class != protocol.FailureTimeout {
			t.Errorf("exit %d: class = %s, want TIMEOUT", code, fc.Class)
		}
	}
}

func TestClassifyNeverInfiniteRetry(t *testing.T) {
	// Rule §32: no class maps to an unbounded retry. Every retryable class has a
	// bounded MaxRetries.
	for _, stderr := range []string{
		"quota exhausted", "rate limit 429", "overloaded capacity", "unauthorized",
		"model does not exist", "", "timeout", "garbage",
	} {
		fc := classifyFailure(1, nil, stderr)
		if fc.Retryable && fc.MaxRetries <= 0 {
			t.Errorf("class %s is retryable but unbounded (stderr=%q)", fc.Class, stderr)
		}
	}
}

func TestClassifyDelegatesToDefaultForUnknown(t *testing.T) {
	// A non-zero exit with no recognizable signal defers to DefaultClassify
	// (malformed/protocol fallback).
	fc := classifyFailure(3, nil, "totally opaque error")
	if !fc.Class.IsValid() {
		t.Errorf("class %s not valid", fc.Class)
	}
}
