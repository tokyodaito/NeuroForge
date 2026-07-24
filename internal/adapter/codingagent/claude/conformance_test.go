package claude

import (
	"context"
	"os"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/conformance"
	"neuroforge/internal/adapter/codingagent/fake"
)

// claudeConformanceFactory builds a Claude adapter backed by RECORDED Claude
// Code stream-json fixtures (rule §36.5: no real paid calls). Each fake.Scenario
// is mapped to a recorded byte stream that mimics the real CLI's documented
// output shape (see fixtureForScenario). The adapter genuinely translates those
// bytes — it is not stubbed at the event layer — so the conformance checks
// exercise the real translation/supervision/cancellation/classification paths.
//
// Honoured vs deferred (no faking):
//   - handshake, version_compatibility: honoured against stubbed detect/version
//     probes (metadata only; no run).
//   - event_ordering, malformed_output, cancellation, timeout, quota_failure,
//     resume, process_crash: honoured against recorded byte-stream fixtures via
//     the real supervision loop. The real `claude` binary path is additionally
//     covered by the opt-in claudesmoke test.
func claudeConformanceFactory(_ context.Context, scenario fake.Scenario) (codingagent.Adapter, func(), error) {
	a, err := New(Options{
		BinaryPath: "claude", // bypass real LookPath for deterministic metadata probes
		LookPath:   func(string) (string, error) { return "claude", nil },
		Probe: func(ctx context.Context, name string, args []string, env []string) ([]byte, []byte, int, error) {
			switch {
			case len(args) > 0 && args[0] == "--version":
				return []byte("2.1.205 (Claude Code)\n"), nil, 0, nil
			case len(args) >= 2 && args[0] == "auth" && args[1] == "status":
				return []byte(`{"loggedIn":true,"account":"ci"}`), nil, 0, nil
			}
			return nil, nil, 1, nil
		},
		Spawn:        replaySpawner(fixtureForScenario(scenario)),
		ArtifactsDir: os.TempDir(),
	})
	if err != nil {
		return nil, nil, err
	}
	return a, func() {}, nil
}

// TestConformanceClaudeAdapter runs the §13.3 conformance suite against the
// Claude adapter driven by recorded fixtures. All nine checks must pass.
func TestConformanceClaudeAdapter(t *testing.T) {
	if testing.Short() {
		t.Skip("conformance suite skipped in -short mode")
	}
	s := &conformance.Suite{Factory: claudeConformanceFactory, Timeout: 15 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	results := s.Run(ctx)

	passed, total := conformance.Summary(results)
	for _, r := range results {
		status := "PASS"
		if !r.Passed {
			status = "FAIL"
		}
		t.Logf("[%s] claude conformance :: %s — %s", status, r.Name, r.Detail)
	}
	if passed != total {
		t.Fatalf("claude conformance: %d/%d checks passed (expected all 9 against recorded fixtures)", passed, total)
	}
}

// TestConformanceNamesCoverMetadataChecks documents the minimum guaranteed
// deterministic surface even when no recorded stream is available.
func TestConformanceNamesCoverMetadataChecks(t *testing.T) {
	names := conformance.Names()
	hasHandshake := false
	hasVersion := false
	for _, n := range names {
		if n == "handshake" {
			hasHandshake = true
		}
		if n == "version_compatibility" {
			hasVersion = true
		}
	}
	if !hasHandshake || !hasVersion {
		t.Fatalf("conformance suite missing metadata checks: %v", names)
	}
}
