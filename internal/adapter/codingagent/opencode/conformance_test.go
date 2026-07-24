package opencode

import (
	"context"
	"os"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/conformance"
	"neuroforge/internal/adapter/codingagent/fake"
)

// opencodeConfFactory builds an OpenCode adapter whose Detect always succeeds
// with a known engine version and whose spawn replays a RECORDED byte-stream
// fixture for the requested scenario. This drives the adapter's REAL run pipeline
// (buildArgv → allowlisted env → spawn → JSONL parse → supervise → malformed
// save → cancel/timeout → ClassifyFailure) entirely offline, so no real OpenCode
// binary and no paid provider call is ever made (rule §36.5).
//
// What this honours vs. defers (no faking — §36.25):
//
//   - HONOURED (all 9 conformance checks): handshake, version_compatibility,
//     event_ordering, malformed_output, cancellation, timeout, quota_failure,
//     resume, process_crash. Each is exercised through genuine adapter code;
//     only the process transport is a recorded stream.
//   - DEFERRED to TestSmoke (build-tagged / -short-skipped): behaviour against a
//     REAL opencode binary and a real backing provider. That is explicitly
//     opt-in and never runs in normal/CI builds.
func opencodeConfFactory(ctx context.Context, scenario fake.Scenario) (codingagent.Adapter, func(), error) {
	artDir, err := os.MkdirTemp("", "opencode-conf-*")
	if err != nil {
		return nil, nil, err
	}
	a := New(Options{ArtifactsDir: artDir, Binary: "/fake/opencode"})
	a.lookPath = func(string) (string, error) { return "/fake/opencode", nil }
	a.runProbe = func(context.Context, string) (string, string, error) { return "opencode 0.1.48", "", nil }
	stream, stderr, code, hang := scenarioStream(scenario)
	a.spawn = func([]string, string, []string) (runProcess, error) {
		return newStubRun(stream, stderr, code, hang), nil
	}
	_ = ctx
	return a, func() { _ = os.RemoveAll(artDir) }, nil
}

// TestConformanceSuite runs the §13.3 conformance suite against the OpenCode
// adapter (via recorded byte-stream fixtures) and asserts every check passes.
func TestConformanceSuite(t *testing.T) {
	if testing.Short() {
		t.Skip("conformance suite is skipped in -short mode")
	}
	s := &conformance.Suite{Factory: opencodeConfFactory, Timeout: 15 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	results := s.Run(ctx)
	passed, total := conformance.Summary(results)
	for _, r := range results {
		status := "PASS"
		if !r.Passed {
			status = "FAIL"
		}
		t.Logf("[%s] %s — %s", status, r.Name, r.Detail)
	}
	if passed != total {
		t.Fatalf("opencode conformance: %d/%d checks passed", passed, total)
	}
}
