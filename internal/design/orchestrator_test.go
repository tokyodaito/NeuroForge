package design_test

import (
	"context"
	"errors"
	"testing"

	"neuroforge/internal/adapter/imageprovider"
	"neuroforge/internal/adapter/imageprovider/fake"
	"neuroforge/internal/adapter/imageprovider/protocol"
	"neuroforge/internal/artifacts"
	"neuroforge/internal/design"
)

func mustStore(t *testing.T) *artifacts.Store {
	t.Helper()
	s, err := artifacts.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func newRegistry(t *testing.T, adapters ...imageprovider.Adapter) *imageprovider.Registry {
	t.Helper()
	r := imageprovider.NewRegistry()
	for i, a := range adapters {
		r.MustRegister(a, 100-i)
	}
	return r
}

// TestReferenceOnly_LocksAttached verifies §15.1 REFERENCE_ONLY: the attached
// image becomes the visual spec with no generation.
func TestReferenceOnly_LocksAttached(t *testing.T) {
	t.Parallel()
	store := mustStore(t)
	hash, _, _ := store.Write([]byte("attached"))
	r := newRegistry(t, fake.New(fake.AdapterOptions{Store: store, Installed: true}))
	o := design.New(design.OrchestratorOptions{Registry: r})

	brief := design.Brief{
		TaskID:    "T1",
		Mode:      design.ModeReferenceOnly,
		Reference: &protocol.Artifact{Hash: hash, Path: store.Path(hash), Format: protocol.FormatPNG, Width: 100, Height: 200},
		Viewport:  design.Viewport{Width: 100, Height: 200},
	}
	out, err := o.Run(context.Background(), brief, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Spec.IsLocked() {
		t.Fatal("spec not locked")
	}
	if out.Spec.Source != "attached" {
		t.Errorf("source = %q, want attached", out.Spec.Source)
	}
}

// TestGenerate_Always_ProducesVariants verifies §15.4: ALWAYS_GENERATE produces
// the requested number of variants and locks one (FIRST_VALID).
func TestGenerate_Always_ProducesVariants(t *testing.T) {
	t.Parallel()
	store := mustStore(t)
	r := newRegistry(t, fake.New(fake.AdapterOptions{Store: store, Installed: true}))
	o := design.New(design.OrchestratorOptions{
		Registry:  r,
		Selection: design.SelectionFirstValid,
	})

	brief := design.Brief{
		TaskID:      "T2",
		Mode:        design.ModeAlwaysGenerate,
		Description: "a login screen with a big primary button",
		Variants:    3,
		Tier:        protocol.TierDraft,
		Viewport:    design.Viewport{Width: 1080, Height: 2400},
	}
	out, err := o.Run(context.Background(), brief, []design.ProviderRoute{{Engine: "fake-image"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Variants) != 3 {
		t.Errorf("variants = %d, want 3", len(out.Variants))
	}
	if !out.Spec.IsLocked() {
		t.Fatal("spec not locked")
	}
	if out.Spec.Source != "generated" {
		t.Errorf("source = %q, want generated", out.Spec.Source)
	}
	if out.Spec.Viewport.Width != 1080 || out.Spec.Viewport.Height != 2400 {
		t.Errorf("viewport not locked: %+v", out.Spec.Viewport)
	}
}

// TestGenerate_HumanSelection_Waits verifies §15.4: HUMAN selection pauses the
// task in WAITING_DESIGN_SELECTION without locking a spec.
func TestGenerate_HumanSelection_Waits(t *testing.T) {
	t.Parallel()
	store := mustStore(t)
	r := newRegistry(t, fake.New(fake.AdapterOptions{Store: store, Installed: true}))
	o := design.New(design.OrchestratorOptions{Registry: r, Selection: design.SelectionHuman})

	brief := design.Brief{
		TaskID: "T3", Mode: design.ModeAlwaysGenerate,
		Description: "onboarding screen", Variants: 2, Tier: protocol.TierDraft,
		Viewport: design.Viewport{Width: 200, Height: 200},
	}
	out, err := o.Run(context.Background(), brief, []design.ProviderRoute{{Engine: "fake-image"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.WaitState != design.WaitDesignSelection {
		t.Errorf("wait = %q, want WAITING_DESIGN_SELECTION", out.WaitState)
	}
	if out.Spec.IsLocked() {
		t.Error("spec must NOT be locked while waiting for human selection")
	}
	// Variants are still produced (for the user to pick).
	if len(out.Variants) != 2 {
		t.Errorf("variants = %d, want 2", len(out.Variants))
	}
}

// TestImageQuotaFailover_FallbackToAttached verifies §15.5: when all providers
// hit quota failure, the attached image is used as fallback.
func TestImageQuotaFailover_FallbackToAttached(t *testing.T) {
	t.Parallel()
	store := mustStore(t)
	// Quota-failing fake provider.
	quotaAdapter := fake.New(fake.AdapterOptions{
		Scenario: fake.ScenarioQuota, Store: store, Installed: true,
	})
	r := newRegistry(t, quotaAdapter)
	o := design.New(design.OrchestratorOptions{Registry: r, Selection: design.SelectionAutomatic})

	hash, _, _ := store.Write([]byte("attached-ref"))
	brief := design.Brief{
		TaskID: "T4", Mode: design.ModeAlwaysGenerate,
		Description: "screen", Variants: 2, Tier: protocol.TierDraft,
		Viewport:  design.Viewport{Width: 100, Height: 100},
		Reference: &protocol.Artifact{Hash: hash, Path: store.Path(hash), Format: protocol.FormatPNG, Width: 100, Height: 100},
	}
	out, err := o.Run(context.Background(), brief, []design.ProviderRoute{{Engine: "fake-image"}})
	if err != nil {
		t.Fatal(err)
	}
	if !out.UsedFallback {
		t.Error("expected UsedFallback=true (§15.5)")
	}
	if !out.Spec.IsLocked() {
		t.Fatal("spec should lock to attached image on fallback")
	}
	if out.Spec.Source != "fallback" {
		t.Errorf("source = %q, want fallback", out.Spec.Source)
	}
	if out.Spec.ArtifactHash != hash {
		t.Errorf("artifact hash = %q, want %q", out.Spec.ArtifactHash, hash)
	}
	if out.Failovers != 1 {
		t.Errorf("failovers = %d, want 1", out.Failovers)
	}
}

// TestImageQuotaFailover_WaitQuota verifies §15.5: when all providers hit quota
// failure, no reference is attached, and generation is mandatory → WAITING_QUOTA.
func TestImageQuotaFailover_WaitQuota(t *testing.T) {
	t.Parallel()
	store := mustStore(t)
	quotaAdapter := fake.New(fake.AdapterOptions{Scenario: fake.ScenarioQuota, Store: store, Installed: true})
	r := newRegistry(t, quotaAdapter)
	o := design.New(design.OrchestratorOptions{
		Registry: r, Selection: design.SelectionAutomatic,
		GenerationRequired: true,
	})
	brief := design.Brief{
		TaskID: "T5", Mode: design.ModeAlwaysGenerate,
		Description: "screen", Variants: 1, Tier: protocol.TierDraft,
		Viewport: design.Viewport{Width: 100, Height: 100},
	}
	out, err := o.Run(context.Background(), brief, []design.ProviderRoute{{Engine: "fake-image"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.WaitState != design.WaitQuota {
		t.Errorf("wait = %q, want WAITING_QUOTA", out.WaitState)
	}
	if out.Spec.IsLocked() {
		t.Error("spec must NOT be locked when waiting for quota")
	}
}

// TestImageQuotaFailover_FailoverAcrossProviders verifies §15.5/§21.1: when the
// primary provider fails with quota, the orchestrator fails over to the next
// provider in the route chain and still produces a spec.
func TestImageQuotaFailover_FailoverAcrossProviders(t *testing.T) {
	t.Parallel()
	store := mustStore(t)
	primary := fake.New(fake.AdapterOptions{Engine: "primary", Scenario: fake.ScenarioQuota, Store: store, Installed: true})
	fallback := fake.New(fake.AdapterOptions{Engine: "fallback", Scenario: fake.ScenarioSuccess, Store: store, Installed: true})
	// Register primary with higher priority (would be picked first).
	r := imageprovider.NewRegistry()
	r.MustRegister(primary, 100)
	r.MustRegister(fallback, 50)
	o := design.New(design.OrchestratorOptions{Registry: r, Selection: design.SelectionAutomatic})

	brief := design.Brief{
		TaskID: "T6", Mode: design.ModeAlwaysGenerate,
		Description: "screen", Variants: 1, Tier: protocol.TierDraft,
		Viewport: design.Viewport{Width: 100, Height: 100},
	}
	// Route chain: primary first, then fallback.
	out, err := o.Run(context.Background(), brief, []design.ProviderRoute{
		{Engine: "primary"},
		{Engine: "fallback"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Spec.IsLocked() {
		t.Fatal("spec should lock via fallback provider")
	}
	if out.Spec.Artifact.Source != "generated" {
		t.Errorf("source = %q, want generated", out.Spec.Artifact.Source)
	}
	if out.Failovers != 1 {
		t.Errorf("failovers = %d, want 1 (primary failed)", out.Failovers)
	}
}

// TestGenerate_OptionalNoReferenceNoProvider verifies §15.5: optional design
// generation with no providers and no reference continues without a spec.
func TestGenerate_OptionalNoReferenceNoProvider(t *testing.T) {
	t.Parallel()
	store := mustStore(t)
	quotaAdapter := fake.New(fake.AdapterOptions{Scenario: fake.ScenarioQuota, Store: store, Installed: true})
	r := newRegistry(t, quotaAdapter)
	o := design.New(design.OrchestratorOptions{
		Registry: r, Selection: design.SelectionAutomatic,
		GenerationRequired: false, // optional
	})
	brief := design.Brief{
		TaskID: "T7", Mode: design.ModeAlwaysGenerate,
		Description: "screen", Variants: 1, Tier: protocol.TierDraft,
		Viewport: design.Viewport{Width: 100, Height: 100},
	}
	out, err := o.Run(context.Background(), brief, []design.ProviderRoute{{Engine: "fake-image"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.WaitState != design.WaitNone {
		t.Errorf("wait = %q, want none (optional)", out.WaitState)
	}
	if out.Spec.IsLocked() {
		t.Error("spec should not lock when no provider and optional")
	}
}

// TestGenerateIfMissing_ShortCircuitsOnReference verifies §15.1.
func TestGenerateIfMissing_ShortCircuitsOnReference(t *testing.T) {
	t.Parallel()
	store := mustStore(t)
	// Even a quota-failing provider should not be called.
	r := newRegistry(t, fake.New(fake.AdapterOptions{Scenario: fake.ScenarioQuota, Store: store, Installed: true}))
	o := design.New(design.OrchestratorOptions{Registry: r})
	hash, _, _ := store.Write([]byte("ref"))
	brief := design.Brief{
		TaskID: "T8", Mode: design.ModeGenerateIfMissing,
		Reference: &protocol.Artifact{Hash: hash, Path: store.Path(hash), Format: protocol.FormatPNG, Width: 50, Height: 50},
		Viewport:  design.Viewport{Width: 50, Height: 50},
	}
	out, err := o.Run(context.Background(), brief, []design.ProviderRoute{{Engine: "fake-image"}})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Spec.IsLocked() {
		t.Fatal("spec not locked")
	}
	if out.Spec.Source != "attached" {
		t.Errorf("source = %q", out.Spec.Source)
	}
}

// TestHumanResolveSelection verifies the human-selection resolver (§15.4).
func TestHumanResolveSelection(t *testing.T) {
	t.Parallel()
	store := mustStore(t)
	r := newRegistry(t, fake.New(fake.AdapterOptions{Store: store, Installed: true}))
	o := design.New(design.OrchestratorOptions{Registry: r})
	brief := design.Brief{TaskID: "T9", Mode: design.ModeAlwaysGenerate, Viewport: design.Viewport{Width: 10, Height: 10}}
	v := design.Variant{Index: 2, Engine: "fake-image", Artifact: protocol.Artifact{Hash: "abc", Source: "generated"}}
	spec := o.ResolveHumanSelection(brief, v)
	if spec.SelectedVariant != 2 {
		t.Errorf("variant = %d, want 2", spec.SelectedVariant)
	}
	if spec.ArtifactHash != "abc" {
		t.Errorf("hash = %q", spec.ArtifactHash)
	}
}

// TestMaxVariantsCap verifies §23 image.maximum_variants_per_task caps variant
// count.
func TestMaxVariantsCap(t *testing.T) {
	t.Parallel()
	store := mustStore(t)
	r := newRegistry(t, fake.New(fake.AdapterOptions{Store: store, Installed: true}))
	o := design.New(design.OrchestratorOptions{
		Registry: r, Selection: design.SelectionFirstValid, MaxVariants: 2,
	})
	brief := design.Brief{
		TaskID: "T10", Mode: design.ModeAlwaysGenerate,
		Description: "x", Variants: 10, Tier: protocol.TierDraft,
		Viewport: design.Viewport{Width: 10, Height: 10},
	}
	out, _ := o.Run(context.Background(), brief, []design.ProviderRoute{{Engine: "fake-image"}})
	if len(out.Variants) != 2 {
		t.Errorf("variants = %d, want 2 (capped)", len(out.Variants))
	}
}

// TestModesValid verifies the §15.1 and §15.4 enumerations.
func TestModesValid(t *testing.T) {
	for _, m := range []design.Mode{design.ModeOff, design.ModeReferenceOnly, design.ModeGenerateIfMissing, design.ModeAlwaysGenerate} {
		if !m.IsValid() {
			t.Errorf("mode %q invalid", m)
		}
	}
	if design.Mode("X").IsValid() {
		t.Error("unknown mode valid")
	}
	for _, m := range []design.SelectionMode{design.SelectionHuman, design.SelectionAutomatic, design.SelectionFirstValid} {
		if !m.IsValid() {
			t.Errorf("selection %q invalid", m)
		}
	}
}

// silence unused import in certain build configs.
var _ = errors.New
