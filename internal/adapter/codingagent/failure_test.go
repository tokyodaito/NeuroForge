package codingagent

import (
	"testing"

	"neuroforge/internal/adapter/codingagent/protocol"
)

func TestDefaultClassifyExplicitFailureEvent(t *testing.T) {
	events := []protocol.NormalizedEvent{
		{Type: protocol.EventRunFailed, Failure: &protocol.FailurePayload{Class: protocol.FailureProviderQuota, Reason: "no quota"}},
	}
	fc := DefaultClassify(2, events, "")
	if fc.Class != protocol.FailureProviderQuota {
		t.Errorf("class = %s, want PROVIDER_QUOTA", fc.Class)
	}
	if !fc.Failover {
		t.Errorf("quota should suggest failover")
	}
	if fc.Reason != "no quota" {
		t.Errorf("reason = %q", fc.Reason)
	}
}

func TestDefaultClassifyCancellationEvent(t *testing.T) {
	events := []protocol.NormalizedEvent{{Type: protocol.EventRunCancelled}}
	fc := DefaultClassify(137, events, "")
	if fc.Class != protocol.FailureCancelled {
		t.Errorf("class = %s, want CANCELLED", fc.Class)
	}
	if fc.Retryable {
		t.Errorf("cancelled should not be retryable")
	}
}

func TestDefaultClassifyStderrSignals(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
		want   protocol.FailureClass
	}{
		{"quota", "error: monthly quota exhausted", protocol.FailureProviderQuota},
		{"rate limit", "HTTP 429 too many requests", protocol.FailureProviderRateLimit},
		{"auth", "401 unauthorized: invalid api key", protocol.FailureProviderAuth},
		{"not installed", "sh: codex: command not found", protocol.FailureEngineNotInstalled},
		{"model missing", "model gpt-x not available", protocol.FailureModelNotAvailable},
		{"scope", "ERROR: scope violation detected", protocol.FailureScopeViolation},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fc := DefaultClassify(1, nil, c.stderr)
			if fc.Class != c.want {
				t.Errorf("class = %s, want %s (stderr=%q)", fc.Class, c.want, c.stderr)
			}
		})
	}
}

func TestDefaultClassifyExitCode(t *testing.T) {
	cases := []struct {
		code int
		want protocol.FailureClass
	}{
		{124, protocol.FailureTimeout},
		{137, protocol.FailureTimeout}, // SIGKILL treated as timeout/kill
		{134, protocol.FailureEngineCrash},
		{139, protocol.FailureEngineCrash}, // SIGSEGV
		{3, protocol.FailureMalformedOutput},
	}
	for _, c := range cases {
		fc := DefaultClassify(c.code, nil, "")
		if fc.Class != c.want {
			t.Errorf("code %d: class = %s, want %s", c.code, fc.Class, c.want)
		}
	}
}

func TestDefaultClassifyExitZeroTerminal(t *testing.T) {
	// Classifying a non-failure must be terminal and non-retryable.
	fc := DefaultClassify(0, nil, "")
	if fc.Retryable {
		t.Error("exit 0 should not be retryable")
	}
	if fc.Policy != protocol.PolicyTerminal {
		t.Errorf("policy = %s, want terminal", fc.Policy)
	}
}
