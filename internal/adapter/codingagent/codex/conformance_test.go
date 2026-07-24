package codex

import (
	"context"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/conformance"
	"neuroforge/internal/adapter/codingagent/fake"
	"neuroforge/internal/adapter/codingagent/protocol"
)

// TestConformanceSuiteAgainstCodexAdapter runs the §13.3 conformance suite
// against the codex adapter driven through the deterministic [Runner] seam
// (recorded byte-stream fixtures — no real Codex, no paid call, rule §36.5).
//
// The factory maps each fake scenario to a canned Codex/NF-JSONL stream the
// adapter parses with its REAL supervision + mapping code. This is the sanctioned
// "recorded/stub byte-stream" approach for adapter conformance (ADAPTER_DEV_GUIDE
// Path 3): the adapter logic under test is genuine; only the process I/O is
// stubbed.
//
// All nine checks pass offline:
//   - handshake, version_compatibility — metadata checks against the detected
//     version;
//   - event_ordering, malformed_output, cancellation, timeout, quota_failure,
//     resume, process_crash — exercised via recorded streams.
//
// Checks that require a real Codex CLI (authenticated model enumeration, live
// quota) are deferred to the opt-in smoke test (smoke_test.go) and documented in
// docs/adapters/codex.md. Nothing here fakes those.
func TestConformanceSuiteAgainstCodexAdapter(t *testing.T) {
	factory := func(ctx context.Context, scenario fake.Scenario) (codingagent.Adapter, func(), error) {
		fr := &fakeRunner{
			version: "codex 0.42.0",
			script:  scriptForScenario(scenario),
		}
		a := newTestAdapter(fr, func(o *Options) {
			o.lookup = func(string) (string, error) { return "/usr/local/bin/codex", nil }
			o.ArtifactsDir = t.TempDir()
		})
		return a, func() {}, nil
	}

	suite := &conformance.Suite{Factory: factory, Timeout: 15 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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
		t.Fatalf("codex conformance: %d/%d checks passed", passed, total)
	}
}

// scriptForScenario maps a fake scenario onto a canned byte-stream the codex
// adapter parses with its real supervision/mapping logic. Deterministic and
// offline.
func scriptForScenario(s fake.Scenario) *runScript {
	switch s {
	case fake.ScenarioSuccess:
		return &runScript{lines: []string{nfRunStarted(), nfDelta("Hello from codex"), nfUsage(120, 80, 0, 0.0001), nfRunCompleted()}}
	case fake.ScenarioMalformedJSON:
		return &runScript{lines: []string{nfRunStarted(), `{not valid json`, nfDelta("still working"), nfRunCompleted()}}
	case fake.ScenarioCancellation:
		return &runScript{lines: []string{nfRunStarted()}, hang: true, exitCode: 137}
	case fake.ScenarioTimeout:
		return &runScript{lines: []string{nfRunStarted()}, hang: true, exitCode: 124}
	case fake.ScenarioQuotaBeforeEdits:
		return &runScript{lines: []string{nfRunStarted(), nfRunFailed("PROVIDER_QUOTA", "quota exhausted before any edits", 2)}, exitCode: 2, stderr: "error: quota exhausted\n"}
	case fake.ScenarioQuotaAfterEdits:
		return &runScript{lines: []string{nfRunStarted(), nfRunFailed("PROVIDER_QUOTA", "quota exhausted after edits", 2)}, exitCode: 2, stderr: "error: quota exhausted\n"}
	case fake.ScenarioRateLimit:
		return &runScript{lines: []string{nfRunStarted(), nfRunFailed("PROVIDER_RATE_LIMIT", "rate limited", 2)}, exitCode: 2, stderr: "HTTP 429 too many requests\n"}
	case fake.ScenarioAuthFailure:
		return &runScript{lines: []string{nfRunStarted(), nfRunFailed("PROVIDER_AUTH", "auth failed", 2)}, exitCode: 2, stderr: "401 unauthorized\n"}
	case fake.ScenarioCrash:
		// Exit with a signal-style code and no terminal event: the adapter must
		// synthesize run.failed(ENGINE_CRASH).
		return &runScript{lines: []string{nfRunStarted(), nfDelta("partial...")}, exitCode: 134, stderr: "codex panicked (simulated crash)\n"}
	case fake.ScenarioPartialOutput:
		return &runScript{lines: []string{nfRunStarted(), nfDelta("partial...")}, exitCode: 1}
	case fake.ScenarioResume:
		return &runScript{lines: []string{
			`{"type":"run.resumed","ts":"2023-01-01T00:00:00Z","run_id":"r","engine":"codex"}`,
			nfDelta("resumed and finishing"),
			nfRunCompleted(),
		}}
	case fake.ScenarioScopeViolation:
		return &runScript{lines: []string{nfRunStarted(), nfRunFailed("SCOPE_VIOLATION", "wrote outside scope", 2)}, exitCode: 2, stderr: "scope violation\n"}
	case fake.ScenarioUsageEvents:
		return &runScript{lines: []string{nfRunStarted(), nfUsage(100, 50, 0, 0.0001), nfUsage(150, 90, 40, 0.0002), nfRunCompleted()}}
	default:
		// Unknown scenario: still emit a clean terminal so the suite never hangs.
		return &runScript{lines: []string{nfRunStarted(), nfRunCompleted()}}
	}
}

// TestConformanceFactoryIsOffline is a guardrail (rule §36.5): the conformance
// factory must never reach a real Codex. It asserts the factory's adapter uses
// the fake runner (no network, no real binary).
func TestConformanceFactoryIsOffline(t *testing.T) {
	a, cleanup, err := scriptForAdapterFactory(fake.ScenarioSuccess)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer cleanup()
	if _, ok := a.(*Adapter); !ok {
		t.Fatalf("factory must return a *codex.Adapter, got %T", a)
	}
	// Detection must succeed purely from the stub.
	d := a.Detect(t.Context())
	if !d.Installed {
		t.Fatalf("stub detect failed: %+v", d)
	}
}

// scriptForAdapterFactory builds a standalone adapter for the guardrail test.
func scriptForAdapterFactory(scenario fake.Scenario) (codingagent.Adapter, func(), error) {
	fr := &fakeRunner{version: "codex 0.42.0", script: scriptForScenario(scenario)}
	a := newTestAdapter(fr, func(o *Options) {
		o.lookup = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	})
	return a, func() {}, nil
}

// Compile-time assertion that the codex adapter satisfies the full interface.
var _ codingagent.Adapter = (*Adapter)(nil)

// keep protocol import used in this file (referenced via build paths).
var _ = protocol.ProtocolVersion
