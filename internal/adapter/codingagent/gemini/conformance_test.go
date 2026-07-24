package gemini

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/conformance"
	"neuroforge/internal/adapter/codingagent/fake"
)

// TestConformanceSuite wires the Gemini adapter into the §13.3 conformance suite
// via a scenario-aware stub host. Every recorded/stub byte stream is offline and
// deterministic (rule §36.5 — no real paid API call). This validates that the
// adapter honours the protocol contract: handshake, version compatibility, event
// ordering, malformed-output resilience, cancellation, timeout, quota/crash
// classification.
//
// Honoured checks (deterministic, via recorded streams):
//
//   - handshake, version_compatibility: metadata probes against the stub CLI.
//   - event_ordering: a recorded Gemini `--output-format json` success document.
//   - malformed_output: a recorded malformed (non-JSON) byte stream.
//   - cancellation, timeout: a stub that hangs until the group is killed.
//   - quota_failure, process_crash: recorded non-zero exits + stderr.
//
// Deferred check (explicitly NOT implemented, §36.25):
//
//   - resume: the Gemini CLI exposes only index-based resume (`--resume latest|N`)
//     which cannot be mapped to NeuroForge's arbitrary-session-id continuation
//     contract without a paid call or fragile `--list-sessions` parsing. Resume
//     returns [ErrSessionResumeNotSupported] and [capabilitiesFor] reports
//     SessionResume=false. See docs/adapters/gemini.md.
func TestConformanceSuite(t *testing.T) {
	factory := func(_ context.Context, scenario fake.Scenario) (codingagent.Adapter, func(), error) {
		h := &stubHost{
			lookPathFn: func(string) (string, error) { return "/usr/local/bin/gemini", nil },
			probeFn:    func(context.Context, []string, []string) (string, string, error) { return "0.23.0", "", nil },
			launchFn:   launchFnForScenario(scenario),
		}
		a := newWithHost(Options{Binary: "gemini", ArtifactsDir: os.TempDir()}, h)
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

	// Metadata checks MUST pass — they are the protocol handshake floor.
	mustPass := map[string]bool{"handshake": true, "version_compatibility": true}
	// Honoured run-lifecycle checks (deterministic via recorded streams).
	honoured := map[string]bool{
		"event_ordering": true, "malformed_output": true,
		"cancellation": true, "timeout": true,
		"quota_failure": true, "process_crash": true,
	}
	// Deferred: resume is explicitly not implemented (§36.25).
	deferred := map[string]bool{"resume": true}

	for _, r := range results {
		if mustPass[r.Name] && !r.Passed {
			t.Errorf("required conformance check %q failed: %s", r.Name, r.Detail)
		}
		if honoured[r.Name] && !r.Passed {
			t.Errorf("honoured conformance check %q failed: %s", r.Name, r.Detail)
		}
		if deferred[r.Name] && r.Passed {
			t.Errorf("deferred check %q unexpectedly passed (resume is not implemented)", r.Name)
		}
	}
	// Exactly the deferred set should fail.
	if passed != total-len(deferred) {
		t.Errorf("conformance: %d/%d passed; expected %d (resume deferred), failures = %v",
			passed, total, total-len(deferred), failedNames(results, deferred))
	}
}

func failedNames(results []conformance.CheckResult, deferred map[string]bool) []string {
	var out []string
	for _, r := range results {
		if !r.Passed && !deferred[r.Name] {
			out = append(out, r.Name)
		}
	}
	return out
}

// launchFnForScenario returns a stub launch function that reproduces the
// requested fake scenario as an offline recorded byte stream / process
// behaviour. It does NOT fake protocol events: the Gemini adapter still parses
// the bytes through its real [parseStream] path and synthesises events itself.
func launchFnForScenario(scenario fake.Scenario) func([]string, string, []string, io.Reader) (launchedProcess, error) {
	successDoc := []byte(`{"response":{"text":"done"},"usage":{"metadata":{"promptTokenCount":5,"candidatesTokenCount":7,"totalTokenCount":12,"cachedContentTokenCount":1}},"session":{"id":"gemini-session-1"}}`)

	switch scenario {
	case fake.ScenarioSuccess, fake.ScenarioUsageEvents, fake.ScenarioResume:
		// Resume is exercised via the resume check, which calls Adapter.Resume;
		// this stub is only relevant if Start were used.
		return func([]string, string, []string, io.Reader) (launchedProcess, error) {
			return cannedProc(successDoc, 0, ""), nil
		}
	case fake.ScenarioMalformedJSON:
		return func([]string, string, []string, io.Reader) (launchedProcess, error) {
			return cannedProc([]byte("{not valid json"), 0, ""), nil
		}
	case fake.ScenarioQuotaBeforeEdits, fake.ScenarioQuotaAfterEdits:
		return func([]string, string, []string, io.Reader) (launchedProcess, error) {
			return cannedProc(nil, 2, "RESOURCE_EXHAUSTED: quota exhausted\n"), nil
		}
	case fake.ScenarioRateLimit:
		return func([]string, string, []string, io.Reader) (launchedProcess, error) {
			return cannedProc(nil, 2, "HTTP 429 too many requests\n"), nil
		}
	case fake.ScenarioAuthFailure:
		return func([]string, string, []string, io.Reader) (launchedProcess, error) {
			return cannedProc(nil, 1, "API key not valid. Please pass a valid API key.\n"), nil
		}
	case fake.ScenarioCrash:
		return func([]string, string, []string, io.Reader) (launchedProcess, error) {
			return cannedProc(nil, 134, "gemini agent panicked (simulated crash)\n"), nil
		}
	case fake.ScenarioTimeout, fake.ScenarioCancellation:
		return func([]string, string, []string, io.Reader) (launchedProcess, error) {
			return hangingProc(137, ""), nil
		}
	case fake.ScenarioPartialOutput:
		return func([]string, string, []string, io.Reader) (launchedProcess, error) {
			return cannedProc(nil, 1, ""), nil
		}
	case fake.ScenarioScopeViolation:
		return func([]string, string, []string, io.Reader) (launchedProcess, error) {
			return cannedProc(nil, 2, "scope violation: wrote outside allowed paths\n"), nil
		}
	default:
		return func([]string, string, []string, io.Reader) (launchedProcess, error) {
			return cannedProc(successDoc, 0, ""), nil
		}
	}
}

// Compile-time assertion that the adapter satisfies the full interface.
var _ codingagent.Adapter = (*Adapter)(nil)
