package imageprovider_test

import (
	"context"
	"errors"
	"testing"

	"neuroforge/internal/adapter/imageprovider"
	"neuroforge/internal/adapter/imageprovider/fake"
	"neuroforge/internal/adapter/imageprovider/protocol"
)

// TestAdapter_Registry mirrors the coding-agent registry invariants.
func TestRegistry(t *testing.T) {
	t.Parallel()
	r := imageprovider.NewRegistry()
	if r.Len() != 0 {
		t.Fatalf("empty registry len = %d", r.Len())
	}
	a := fake.New(fake.AdapterOptions{Installed: true})
	if err := r.Register(a, 10); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, ok := r.Lookup(a.ID()); !ok {
		t.Fatalf("Lookup(%q) not found", a.ID())
	}
	if err := r.Register(a, 5); err == nil {
		t.Error("duplicate register must error")
	}
	if err := r.Register(nil, 1); err == nil {
		t.Error("nil register must error")
	}
	if ids := r.IDs(); len(ids) != 1 || ids[0] != a.ID() {
		t.Errorf("IDs() = %v", ids)
	}
}

// TestRegistry_PriorityOrdering verifies priority-desc, id-asc ordering.
func TestRegistry_PriorityOrdering(t *testing.T) {
	t.Parallel()
	r := imageprovider.NewRegistry()
	lo := fake.New(fake.AdapterOptions{Engine: "zlow"})
	hi := fake.New(fake.AdapterOptions{Engine: "ahigh"})
	mid := fake.New(fake.AdapterOptions{Engine: "mid"})
	r.MustRegister(lo, 1)
	r.MustRegister(hi, 100)
	r.MustRegister(mid, 50)
	got := r.IDs()
	want := []string{"ahigh", "mid", "zlow"}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("IDs()[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

// TestAdapter_FakeSuccess verifies the success scenario produces an artifact
// with a populated result.
func TestAdapter_FakeSuccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a := fake.New(fake.AdapterOptions{Installed: true})
	sink := &imageprovider.SliceSink{}
	res, err := a.Generate(ctx, protocol.ImageGenerationRequest{
		RunID: "r1", Engine: a.ID(), Model: "fake/standard",
		Tier: protocol.TierStandard, Prompt: "a login screen",
		Size: protocol.ImageSize{Width: 100, Height: 100}, Format: protocol.FormatPNG,
	}, sink)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(res.Artifacts) != 1 {
		t.Fatalf("want 1 artifact, got %d", len(res.Artifacts))
	}
	art := res.Artifacts[0]
	if art.Width != 100 || art.Height != 100 {
		t.Errorf("artifact dims = %dx%d", art.Width, art.Height)
	}
	if art.Bytes == 0 {
		t.Error("artifact bytes = 0")
	}
	if !res.Usage.Included {
		t.Error("fake provider usage should be subscription-included (no real cost)")
	}
	kinds := sink.Kinds()
	if len(kinds) < 3 || kinds[0] != protocol.EventStarted {
		t.Errorf("event kinds = %v", kinds)
	}
	last := kinds[len(kinds)-1]
	if last != protocol.EventCompleted {
		t.Errorf("last event = %v, want image.completed", last)
	}
}

// TestAdapter_FakeQuotaFailure verifies the quota scenario returns the right
// classified failure (critical for image quota failover §15.5).
func TestAdapter_FakeQuotaFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a := fake.New(fake.AdapterOptions{Scenario: fake.ScenarioQuota, Installed: true})
	_, err := a.Generate(ctx, protocol.ImageGenerationRequest{
		RunID: "r2", Engine: a.ID(), Model: "fake/standard",
	}, &imageprovider.SliceSink{})
	if !errors.Is(err, imageprovider.ErrQuotaExhausted) {
		t.Errorf("err = %v, want ErrQuotaExhausted", err)
	}
	fc := a.AnalyzeFailure(err)
	if fc.Class != protocol.FailureProviderQuota {
		t.Errorf("class = %s, want PROVIDER_QUOTA", fc.Class)
	}
	if !fc.Failover {
		t.Error("quota failure must request failover")
	}
}

// TestAdapter_FakeInvalidImage verifies invalid-image classification.
func TestAdapter_FakeInvalidImage(t *testing.T) {
	t.Parallel()
	a := fake.New(fake.AdapterOptions{Scenario: fake.ScenarioInvalidImage, Installed: true})
	_, err := a.Generate(context.Background(), protocol.ImageGenerationRequest{}, &imageprovider.SliceSink{})
	if !errors.Is(err, imageprovider.ErrInvalidImage) {
		t.Errorf("err = %v, want ErrInvalidImage", err)
	}
	fc := a.AnalyzeFailure(err)
	if fc.Class != protocol.FailureImageProvider {
		t.Errorf("class = %s, want IMAGE_PROVIDER_FAILURE", fc.Class)
	}
}

// TestAdapter_FakeFixtureDeterministic verifies the §33.2 deterministic fixture
// requirement: identical request → identical artifact bytes.
func TestAdapter_FakeFixtureDeterministic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	req := protocol.ImageGenerationRequest{
		Engine: "fake-image", Model: "fake/standard",
		Tier: protocol.TierStandard, Prompt: "foo", Theme: "dark",
		Size: protocol.ImageSize{Width: 32, Height: 32}, Format: protocol.FormatPNG,
	}
	a := fake.New(fake.AdapterOptions{Scenario: fake.ScenarioFixture, Installed: true})
	r1, err := a.Generate(ctx, req, &imageprovider.SliceSink{})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := a.Generate(ctx, req, &imageprovider.SliceSink{})
	if err != nil {
		t.Fatal(err)
	}
	if r1.Artifacts[0].Hash != r2.Artifacts[0].Hash {
		t.Errorf("fixture not deterministic: %s vs %s", r1.Artifacts[0].Hash, r2.Artifacts[0].Hash)
	}
}

// TestTier_Valid verifies the §14.3 tier set.
func TestTier_Valid(t *testing.T) {
	for _, tier := range protocol.Tiers() {
		if !tier.IsValid() {
			t.Errorf("tier %q not valid", tier)
		}
	}
	if protocol.ImageTier("IMAGE_FOO").IsValid() {
		t.Error("unknown tier reported valid")
	}
}

// TestModelsByTier verifies the registry tier filter (§14.3: router resolves a
// tier from the catalog, never a hard-coded model name).
func TestModelsByTier(t *testing.T) {
	t.Parallel()
	r := imageprovider.NewRegistry()
	r.MustRegister(fake.New(fake.AdapterOptions{Engine: "fake-image", Installed: true}), 10)
	ctx := context.Background()
	draft := r.ModelsByTier(ctx, protocol.TierDraft)
	if len(draft) != 1 || draft[0].Engine != "fake-image" {
		t.Errorf("draft models = %+v", draft)
	}
}
