package grok

import (
	"context"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/conformance"
	"neuroforge/internal/adapter/codingagent/fake"
)

// TestConformanceAgainstGrokStub runs the §13.3 conformance suite against the
// Grok adapter wired to the test-only grokstub binary. The stub emulates Grok's
// headless streaming-json wire format for each [fake.Scenario] (rule §36.5: no
// real/paid calls), so this exercises the adapter's REAL code paths: argv
// construction, allowlisted-env spawn, process-group streaming, line parsing,
// malformed/unknown resilience, cancellation/timeout, terminal synthesis and
// failure classification.
//
// Every check the suite defines is honoured here — not faked:
//
//   - handshake: Detect resolves the stub; Capabilities reports headless mode.
//   - version_compatibility: Version().ProtocolVersion == 1.
//   - event_ordering / malformed_output / cancellation / timeout / quota_failure
//     / resume / process_crash: the adapter produces the required normalized
//     stream against the stub's scenario output.
//
// A separate build-tagged smoke test (smoke_test.go) validates the adapter
// against a real Grok CLI when explicitly enabled.
func TestConformanceAgainstGrokStub(t *testing.T) {
	bin := stubBin(t)
	// Resume is version-gated; the stub reports a known version so the default
	// gate enables it, but pin it explicitly for scenario determinism.
	resume := true

	factory := func(ctx context.Context, scenario fake.Scenario) (codingagent.Adapter, func(), error) {
		// Resume scenario needs the capability on; every other scenario is
		// unaffected by the toggle.
		opts := Options{
			Binary:        bin,
			ExtraEnv:      []string{"GROK_STUB_SCENARIO=" + mapScenario(scenario)},
			ResumeEnabled: &resume,
		}
		a := New(opts)
		return a, func() {}, nil
	}

	suite := &conformance.Suite{Factory: factory, Timeout: 20 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	results := suite.Run(ctx)

	passed, total := conformance.Summary(results)
	for _, r := range results {
		status := "PASS"
		if !r.Passed {
			status = "FAIL"
		}
		t.Logf("[%s] %s — %s", status, r.Name, r.Detail)
	}
	if passed != total {
		t.Fatalf("conformance against grok stub: %d/%d checks passed", passed, total)
	}
}

// mapScenario translates a [fake.Scenario] into the stub scenario name. They are
// identical for the shared cases; this keeps the mapping explicit and
// documented.
func mapScenario(s fake.Scenario) string {
	switch s {
	case fake.ScenarioSuccess, fake.ScenarioMalformedJSON, fake.ScenarioTimeout,
		fake.ScenarioCancellation, fake.ScenarioCrash, fake.ScenarioQuotaBeforeEdits,
		fake.ScenarioResume, fake.ScenarioRateLimit, fake.ScenarioAuthFailure,
		fake.ScenarioScopeViolation, fake.ScenarioUsageEvents, fake.ScenarioPartialOutput,
		fake.ScenarioQuotaAfterEdits:
		return string(s)
	default:
		// Unknown scenarios default to success so the suite never aborts.
		return string(fake.ScenarioSuccess)
	}
}

// TestConformanceNamesPin guards the honoured-check set against drift.
func TestConformanceNamesPin(t *testing.T) {
	want := map[string]bool{
		"handshake": true, "version_compatibility": true, "event_ordering": true,
		"malformed_output": true, "cancellation": true, "timeout": true,
		"quota_failure": true, "resume": true, "process_crash": true,
	}
	for _, name := range conformance.Names() {
		if !want[name] {
			t.Errorf("unexpected conformance check %q", name)
		}
	}
	if len(want) != len(conformance.Names()) {
		t.Errorf("conformance check set changed: %v", conformance.Names())
	}
}

// Compile-time assertion that the adapter satisfies the interface.
var _ codingagent.Adapter = (*Adapter)(nil)
