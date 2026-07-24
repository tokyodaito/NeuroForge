package protocol

import (
	"encoding/json"
	"testing"
)

func TestFailureClassValidity(t *testing.T) {
	classes := []FailureClass{
		FailureProviderQuota, FailureProviderRateLimit, FailureProviderCapacity,
		FailureProviderAuth, FailureEngineNotInstalled, FailureEngineCrash,
		FailureEngineProtocol, FailureModelNotAvailable, FailureImageProvider,
		FailureTimeout, FailureCancelled, FailureBuildFailure, FailureTestFailure,
		FailureVisualFailure, FailureScopeViolation, FailurePolicyViolation,
		FailureMalformedOutput, FailureMergeConflict, FailureBudgetExceeded,
		FailureInternalError,
	}
	if len(classes) != 20 {
		t.Fatalf("expected 20 §32 classes, got %d", len(classes))
	}
	for _, c := range classes {
		if !c.IsValid() {
			t.Errorf("class %q not valid", c)
		}
	}
	if FailureClass("BOGUS").IsValid() {
		t.Error("bogus class reported valid")
	}
}

func TestDefaultPolicyCoverage(t *testing.T) {
	// Every §32 class must have a defined policy (no infinite retry).
	classes := []FailureClass{
		FailureProviderQuota, FailureProviderRateLimit, FailureProviderCapacity,
		FailureProviderAuth, FailureEngineNotInstalled, FailureEngineCrash,
		FailureEngineProtocol, FailureModelNotAvailable, FailureImageProvider,
		FailureTimeout, FailureCancelled, FailureBuildFailure, FailureTestFailure,
		FailureVisualFailure, FailureScopeViolation, FailurePolicyViolation,
		FailureMalformedOutput, FailureMergeConflict, FailureBudgetExceeded,
		FailureInternalError,
	}
	for _, c := range classes {
		fc := DefaultPolicy(c)
		if fc.Class != c {
			t.Errorf("class %q: DefaultPolicy returned class %q", c, fc.Class)
		}
		if fc.Policy == "" {
			t.Errorf("class %q: empty policy", c)
		}
		if fc.Reason == "" {
			t.Errorf("class %q: empty reason", c)
		}
		// Retryable classes must carry a bounded MaxRetries (>0) so the
		// supervisor never retries infinitely (rule §32).
		if fc.Retryable && fc.MaxRetries <= 0 {
			t.Errorf("class %q: retryable but MaxRetries=%d", c, fc.MaxRetries)
		}
	}
}

func TestDefaultPolicyTerminalClassesNotRetryable(t *testing.T) {
	// Cancellation, scope/policy violations, build/test/visual failures and
	// merge conflicts must not be auto-retried.
	for _, c := range []FailureClass{
		FailureCancelled, FailureScopeViolation, FailurePolicyViolation,
		FailureBuildFailure, FailureTestFailure, FailureVisualFailure,
		FailureMergeConflict,
	} {
		fc := DefaultPolicy(c)
		if fc.Retryable {
			t.Errorf("class %q should not be retryable", c)
		}
		if fc.Policy != PolicyTerminal && fc.Policy != PolicyPause && fc.Policy != PolicyQuarantine {
			t.Errorf("class %q: policy=%s, want terminal/pause/quarantine", c, fc.Policy)
		}
	}
}

func TestDefaultPolicyFailoverHints(t *testing.T) {
	// Quota, auth, capacity and model-availability should suggest failover.
	for _, c := range []FailureClass{
		FailureProviderQuota, FailureProviderAuth, FailureModelNotAvailable,
	} {
		fc := DefaultPolicy(c)
		if !fc.Failover {
			t.Errorf("class %q should suggest failover", c)
		}
	}
}

func TestCapabilitiesMerge(t *testing.T) {
	a := AgentCapabilities{HeadlessMode: true, ModelSelection: true}
	b := AgentCapabilities{StreamingEvents: true, ModelSelection: true}
	m := a.Merge(b)
	if !m.HeadlessMode || !m.StreamingEvents || !m.ModelSelection {
		t.Errorf("merge lost a capability: %+v", m)
	}
	if m.InteractiveMode {
		t.Errorf("merge set an unset capability")
	}
}

func TestRequestIDRoundTrip(t *testing.T) {
	cases := []RequestID{NewNumberID(42), NewStringID("abc")}
	for _, id := range cases {
		m := JSONRPCMessage{JSONRPC: "2.0", ID: &id, Method: MethodAgentDetect}
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var back JSONRPCMessage
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if back.ID == nil {
			t.Fatalf("id lost on round-trip")
		}
		// Re-marshal both ids and compare bytes.
		out1, _ := json.Marshal(id)
		out2, _ := json.Marshal(*back.ID)
		if string(out1) != string(out2) {
			t.Errorf("id mismatch: %s vs %s", out1, out2)
		}
	}
}

func TestHandshakeTypes(t *testing.T) {
	hr := HandshakeRequest{ProtocolMin: 1, ProtocolMax: 1, Client: "forge", ClientVersion: "0.1"}
	resp := HandshakeResponse{ProtocolVersion: ProtocolVersion, Server: "plugin", AgentID: "x", ProtocolMin: 1, ProtocolMax: 1}
	if hr.ProtocolMax < hr.ProtocolMin {
		t.Fatal("bad range")
	}
	if resp.ProtocolVersion != ProtocolVersion {
		t.Fatal("plugin must speak the current protocol version")
	}
}
