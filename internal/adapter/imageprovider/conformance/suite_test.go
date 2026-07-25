package conformance_test

import (
	"context"
	"testing"

	"neuroforge/internal/adapter/imageprovider"
	"neuroforge/internal/adapter/imageprovider/conformance"
	"neuroforge/internal/adapter/imageprovider/fake"
	"neuroforge/internal/artifacts"
)

// TestSuite_FakePassesAll verifies the conformance suite passes against the
// fake image provider (the CI path — no real providers, rule §33).
func TestSuite_FakePassesAll(t *testing.T) {
	t.Parallel()
	store, err := artifacts.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	suite := &conformance.Suite{
		Factory: func(context.Context) (imageprovider.Adapter, func(), error) {
			return fake.New(fake.AdapterOptions{Store: store, Installed: true}), func() {}, nil
		},
	}
	results := suite.Run(context.Background())
	passed, total := conformance.Summary(results)
	for _, r := range results {
		status := "PASS"
		if !r.Passed {
			status = "FAIL"
		}
		t.Logf("  [%s] %-30s %s", status, r.Name, r.Detail)
	}
	if passed != total {
		t.Errorf("%d/%d checks passed", passed, total)
	}
}
