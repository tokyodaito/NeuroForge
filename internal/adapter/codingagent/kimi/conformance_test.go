package kimi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/conformance"
	"neuroforge/internal/adapter/codingagent/fake"
)

// This test wires the REAL kimi adapter into the §13.3 conformance suite. The
// adapter code under test is genuine production code; only the CLI binary is
// swapped for the deterministic, offline kimistub (rule §36.5: no paid calls).
// The stub faithfully emits Kimi-format stream-json for each fake scenario, so
// every conformance check exercises real detection, command-building, process
// spawning, stream parsing, cancellation, failure classification and resume.

// stubScenarioFor maps a conformance (fake) scenario onto the kimistub scenario
// name. They are identical for the scenarios the suite uses; the map makes the
// coupling explicit and centralizes any future divergence.
func stubScenarioFor(s fake.Scenario) string {
	switch s {
	case fake.ScenarioSuccess, fake.ScenarioQuotaBeforeEdits, fake.ScenarioQuotaAfterEdits,
		fake.ScenarioMalformedJSON, fake.ScenarioTimeout, fake.ScenarioCrash,
		fake.ScenarioPartialOutput, fake.ScenarioResume, fake.ScenarioCancellation,
		fake.ScenarioRateLimit, fake.ScenarioAuthFailure, fake.ScenarioScopeViolation,
		fake.ScenarioUsageEvents:
		return string(s)
	}
	return "success"
}

var (
	confStubOnce sync.Once
	confStubBin  string
)

func confStubBinary(t *testing.T) string {
	t.Helper()
	confStubOnce.Do(func() {
		dir, err := os.MkdirTemp("", "kimistub-conf-*")
		if err != nil {
			t.Fatalf("mktemp: %v", err)
		}
		bin := filepath.Join(dir, "kimistub")
		root := moduleRoot(t)
		cmd := exec.Command("go", "build", "-o", bin, "./internal/adapter/codingagent/kimi/internal/kimistub")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build kimistub: %v\n%s", err, out)
		}
		confStubBin = bin
	})
	if confStubBin == "" {
		t.Fatal("kimistub not built")
	}
	return confStubBin
}

func confFactoryFor(t *testing.T) conformance.AdapterFactory {
	stub := confStubBinary(t)
	return func(ctx context.Context, scenario fake.Scenario) (codingagent.Adapter, func(), error) {
		opts := Options{
			BinaryOverride: stub,
			ArtifactsDir:   t.TempDir(),
			ExtraEnv:       []string{"KIMI_STUB_SCENARIO=" + stubScenarioFor(scenario)},
		}
		a := New(opts)
		return a, func() {}, nil
	}
}

func TestKimiAdapterConformance(t *testing.T) {
	// Honour the suite's metadata checks deterministically (handshake,
	// version_compatibility) AND the run-based checks via the stub. All nine
	// checks are expected to pass because the adapter is real and the stub is a
	// faithful offline stand-in; this is documented (not faked) per §36.25.
	suite := &conformance.Suite{
		Factory: confFactoryFor(t),
		Timeout: 20 * time.Second,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	results := suite.Run(ctx)

	passed, total := conformance.Summary(results)
	for _, r := range results {
		status := "PASS"
		if !r.Passed {
			status = "FAIL"
		}
		t.Logf("[%s] kimi %s — %s", status, r.Name, r.Detail)
	}
	if passed != total {
		t.Fatalf("kimi conformance: %d/%d checks passed (see log above)", passed, total)
	}
}
