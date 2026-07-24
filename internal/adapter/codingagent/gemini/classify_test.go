package gemini

import (
	"context"
	"testing"

	"neuroforge/internal/adapter/codingagent/protocol"
)

func newClassifyAdapter() *Adapter { return New(Options{}) }

func TestClassifyQuota(t *testing.T) {
	a := newClassifyAdapter()
	fc := a.ClassifyFailure(2, nil, "error: RESOURCE_EXHAUSTED: quota exceeded\n")
	if fc.Class != protocol.FailureProviderQuota {
		t.Errorf("class = %s, want PROVIDER_QUOTA", fc.Class)
	}
	if !fc.Failover {
		t.Error("quota should suggest failover")
	}
	if fc.Retryable {
		t.Error("quota must not be auto-retryable (bounded failover only)")
	}
}

func TestClassifyRateLimit(t *testing.T) {
	a := newClassifyAdapter()
	fc := a.ClassifyFailure(2, nil, "HTTP 429 too many requests\n")
	if fc.Class != protocol.FailureProviderRateLimit {
		t.Errorf("class = %s, want PROVIDER_RATE_LIMIT", fc.Class)
	}
	if !fc.Retryable {
		t.Error("rate limit should be bounded-retryable")
	}
}

func TestClassifyAuth(t *testing.T) {
	a := newClassifyAdapter()
	fc := a.ClassifyFailure(1, nil, "API key not valid. Please pass a valid API key.\n")
	if fc.Class != protocol.FailureProviderAuth {
		t.Errorf("class = %s, want PROVIDER_AUTH", fc.Class)
	}
}

func TestClassifyCapacity(t *testing.T) {
	a := newClassifyAdapter()
	fc := a.ClassifyFailure(1, nil, "503 UNAVAILABLE: model is overloaded\n")
	if fc.Class != protocol.FailureProviderCapacity {
		t.Errorf("class = %s, want PROVIDER_CAPACITY", fc.Class)
	}
}

func TestClassifyModelNotAvailable(t *testing.T) {
	a := newClassifyAdapter()
	fc := a.ClassifyFailure(1, nil, "model some-model is not found\n")
	if fc.Class != protocol.FailureModelNotAvailable {
		t.Errorf("class = %s, want MODEL_NOT_AVAILABLE", fc.Class)
	}
	if !fc.Failover {
		t.Error("model not available should suggest failover")
	}
}

func TestClassifyEngineCrashViaExitCode(t *testing.T) {
	// No Gemini-specific stderr; falls through to DefaultClassify which maps a
	// signal-death exit code (>=128) to ENGINE_CRASH.
	a := newClassifyAdapter()
	fc := a.ClassifyFailure(134, nil, "some unrelated message\n")
	if fc.Class != protocol.FailureEngineCrash {
		t.Errorf("class = %s, want ENGINE_CRASH", fc.Class)
	}
}

func TestClassifyTimeoutExitCode(t *testing.T) {
	a := newClassifyAdapter()
	fc := a.ClassifyFailure(124, nil, "")
	if fc.Class != protocol.FailureTimeout {
		t.Errorf("class = %s, want TIMEOUT", fc.Class)
	}
}

func TestClassifyNoInfiniteRetry(t *testing.T) {
	// Every class must carry a bounded MaxRetries (rule §32). Sample across the
	// Gemini-specific classes.
	a := newClassifyAdapter()
	samples := []struct {
		exit   int
		stderr string
	}{
		{2, "quota exhausted"},
		{2, "429 rate limit"},
		{1, "API key not valid"},
		{1, "503 overloaded"},
		{1, "model not found"},
		{134, ""},
	}
	for _, s := range samples {
		fc := a.ClassifyFailure(s.exit, nil, s.stderr)
		// Retryable classes must have MaxRetries>0; terminal classes Retryable=false
		// with MaxRetries==0 also acceptable. The key invariant: never unbounded.
		if fc.Retryable && fc.MaxRetries <= 0 {
			t.Errorf("class %s retryable but MaxRetries=%d (unbounded)", fc.Class, fc.MaxRetries)
		}
	}
}

func TestClassifyFromEventsTerminalFailure(t *testing.T) {
	a := newClassifyAdapter()
	events := []protocol.NormalizedEvent{{
		Type:    protocol.EventRunFailed,
		Failure: &protocol.FailurePayload{Class: protocol.FailureProviderQuota, Reason: "mid-run quota"},
	}}
	fc := a.ClassifyFailure(2, events, "")
	if fc.Class != protocol.FailureProviderQuota {
		t.Errorf("class = %s, want PROVIDER_QUOTA from event", fc.Class)
	}
	if fc.Reason != "mid-run quota" {
		t.Errorf("reason = %s", fc.Reason)
	}
}

func TestCapabilitiesProfile(t *testing.T) {
	// The adapter must honestly report only what it wires. Verify the safe
	// defaults and the explicitly-not-implemented fields.
	h := &stubHost{
		probeFn: func(context.Context, []string, []string) (string, string, error) { return "0.23.0", "", nil },
	}
	a := newTestAdapter(h)
	caps := a.Capabilities(context.Background())
	if !caps.HeadlessMode {
		t.Error("HeadlessMode should be true")
	}
	if !caps.ModelSelection {
		t.Error("ModelSelection should be true (-m)")
	}
	if !caps.UsageReporting {
		t.Error("UsageReporting should be true")
	}
	if !caps.CachedUsageReporting {
		t.Error("CachedUsageReporting should be true for a known version")
	}
	// Explicitly not implemented (§36.25):
	for _, tt := range []struct {
		name string
		got  bool
	}{
		{"StreamingEvents", caps.StreamingEvents},
		{"SessionResume", caps.SessionResume},
		{"LiveUserMessages", caps.LiveUserMessages},
		{"ImageInput", caps.ImageInput},
		{"NativeSandbox", caps.NativeSandbox},
		{"MCP", caps.MCP},
		{"ACP", caps.ACP},
	} {
		if tt.got {
			t.Errorf("%s should be false (not implemented, §36.25)", tt.name)
		}
	}
}

func TestCapabilitiesNoVersionIsConservative(t *testing.T) {
	// Undetectable version → least-capable profile (no cached-usage claim).
	caps := capabilitiesFor(semver{})
	if caps.CachedUsageReporting {
		t.Error("should not claim cached usage for unknown version")
	}
	if !caps.HeadlessMode {
		t.Error("headless still supported regardless of version")
	}
}

func TestListModelsEmpty(t *testing.T) {
	a := newClassifyAdapter()
	ms, err := a.ListModels(context.Background(), protocol.Account{})
	if err != nil {
		t.Errorf("ListModels err: %v", err)
	}
	if len(ms) != 0 {
		t.Errorf("ListModels should be empty offline, got %d", len(ms))
	}
}

func TestInspectQuotaUnknown(t *testing.T) {
	a := newClassifyAdapter()
	q := a.InspectQuota(context.Background(), protocol.Account{})
	if q.Confidence != protocol.QuotaConfUnknown {
		t.Errorf("confidence = %s, want UNKNOWN", q.Confidence)
	}
}
