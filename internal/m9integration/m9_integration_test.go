// Package m9integration contains the M9 integration tests that exercise the
// full image-provider + design pipeline composition (spec §14, §15, §33.2).
//
// These tests compose the M9 domain packages (artifacts → imageprovider →
// design → quota/budget) with the deterministic fake image provider (rule
// §36.5: no real paid models in CI). They verify the critical M9 invariants:
//
//   - AC-19: GPT Image and Nano Banana adapters are supported.
//   - AC-20: a visual specification can be generated from a text task.
//   - §15.5: image quota failure uses fallback.
//   - rule §36.9: coding agent and image provider are separate abstractions.
//   - rule §33: real image calls are opt-in; CI uses the fake provider only.
package m9integration

import (
	"context"
	"errors"
	"testing"

	"neuroforge/internal/adapter/imageprovider"
	"neuroforge/internal/adapter/imageprovider/fake"
	"neuroforge/internal/adapter/imageprovider/gptimage"
	"neuroforge/internal/adapter/imageprovider/nanobanana"
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

// TestAC19_GPTImageAndNanoBananaAdaptersSupported verifies AC-19: both required
// providers can be constructed, registered and respond to ListModels with all
// three §14.3 tiers. The adapters are real HTTP adapters but OPT-IN: only the
// fake provider is exercised with live calls in CI (rule §33).
func TestAC19_GPTImageAndNanoBananaAdaptersSupported(t *testing.T) {
	t.Parallel()
	store := mustStore(t)

	// GPT Image adapter — real HTTP adapter, but unconfigured here.
	gpt, err := gptimage.New(gptimage.Options{
		Credentials: gptimage.CredentialResolver(func(protocol.Account) (string, bool) { return "", false }),
		Store:       store,
	})
	if err != nil {
		t.Fatalf("gptimage.New: %v", err)
	}
	if gpt.ID() != gptimage.EngineID {
		t.Errorf("gpt id = %q", gpt.ID())
	}
	gptModels, err := gpt.ListModels(context.Background(), protocol.Account{})
	if err != nil {
		t.Fatalf("gpt ListModels: %v", err)
	}
	if !hasAllTiers(gptModels) {
		t.Errorf("gpt models missing tiers: %+v", gptModels)
	}

	// Nano Banana adapter — same shape.
	nano, err := nanobanana.New(nanobanana.Options{
		Credentials: nanobanana.CredentialResolver(func(protocol.Account) (string, bool) { return "", false }),
		Store:       store,
	})
	if err != nil {
		t.Fatalf("nanobanana.New: %v", err)
	}
	if nano.ID() != nanobanana.EngineID {
		t.Errorf("nano id = %q", nano.ID())
	}
	nanoModels, err := nano.ListModels(context.Background(), protocol.Account{})
	if err != nil {
		t.Fatalf("nano ListModels: %v", err)
	}
	if !hasAllTiers(nanoModels) {
		t.Errorf("nano models missing tiers: %+v", nanoModels)
	}

	// Both register cleanly into the same Registry (§14.2 surface).
	r := imageprovider.NewRegistry()
	r.MustRegister(gpt, 100)
	r.MustRegister(nano, 90)
	r.MustRegister(fake.New(fake.AdapterOptions{Store: store, Installed: true}), 0) // fake always present
	if r.Len() != 3 {
		t.Errorf("registry len = %d, want 3", r.Len())
	}

	// AnalyzeFailure is present on both real adapters (§14.2 surface).
	if gpt.AnalyzeFailure(imageprovider.ErrQuotaExhausted).Class != protocol.FailureProviderQuota {
		t.Error("gpt AnalyzeFailure misclassified quota")
	}
	if nano.AnalyzeFailure(imageprovider.ErrAuthFailed).Class != protocol.FailureProviderAuth {
		t.Error("nano AnalyzeFailure misclassified auth")
	}
}

// TestAC19_RealProvidersOptIn verifies rule §33: real image calls are opt-in.
// An unconfigured GPT Image adapter reports Health=unknown and refuses to
// generate (returns ErrAuthFailed), so it can never make a real HTTP call by
// accident.
func TestAC19_RealProvidersOptIn(t *testing.T) {
	t.Parallel()
	store := mustStore(t)
	gpt, _ := gptimage.New(gptimage.Options{
		Credentials: gptimage.CredentialResolver(func(protocol.Account) (string, bool) { return "", false }),
		Store:       store,
	})
	// Health reports unknown when unconfigured.
	if h := gpt.Health(context.Background(), protocol.Account{}); h.Status != protocol.HealthUnknown {
		t.Errorf("unconfigured health = %s, want unknown", h.Status)
	}
	// Generate refuses with ErrAuthFailed — no real HTTP call attempted.
	_, err := gpt.Generate(context.Background(), protocol.ImageGenerationRequest{
		RunID: "r", Engine: gptimage.EngineID,
	}, &imageprovider.SliceSink{})
	if !errors.Is(err, imageprovider.ErrAuthFailed) {
		t.Errorf("err = %v, want ErrAuthFailed (no real call)", err)
	}
}

// TestAC20_TextToVisualSpecification verifies AC-20: from a text task, the
// system can generate a visual specification.
func TestAC20_TextToVisualSpecification(t *testing.T) {
	t.Parallel()
	store := mustStore(t)
	r := imageprovider.NewRegistry()
	r.MustRegister(fake.New(fake.AdapterOptions{Store: store, Installed: true}), 0)
	o := design.New(design.OrchestratorOptions{
		Registry: r, Selection: design.SelectionAutomatic,
	})

	brief := design.Brief{
		TaskID:      "AC20",
		ProjectID:   "proj",
		Description: "Login screen with email/password fields and a primary 'Sign in' button",
		Mode:        design.ModeAlwaysGenerate,
		Variants:    3,
		Tier:        protocol.TierDraft,
		Viewport:    design.Viewport{Width: 1080, Height: 2400},
		Theme:       "dark",
		Locale:      "ru",
		Density:     "xxhdpi",
	}
	out, err := o.Run(context.Background(), brief, []design.ProviderRoute{{Engine: "fake-image"}})
	if err != nil {
		t.Fatalf("orchestrator: %v", err)
	}
	if !out.Spec.IsLocked() {
		t.Fatal("visual specification not locked")
	}
	// §15.6 metadata carried through.
	if out.Spec.Viewport.Width != 1080 || out.Spec.Viewport.Height != 2400 {
		t.Errorf("viewport not locked: %+v", out.Spec.Viewport)
	}
	if out.Spec.Theme != "dark" || out.Spec.Locale != "ru" || out.Spec.Density != "xxhdpi" {
		t.Errorf("theme/locale/density not locked: %+v", out.Spec)
	}
	if out.Spec.Source != "generated" {
		t.Errorf("source = %q, want generated", out.Spec.Source)
	}
	if out.Spec.ArtifactHash == "" {
		t.Error("artifact hash empty")
	}
}

// TestImageQuotaFailover_AttachedFallback verifies §15.5 / §33.4: on image
// quota failure, the orchestrator falls back to the attached image.
func TestImageQuotaFailover_AttachedFallback(t *testing.T) {
	t.Parallel()
	store := mustStore(t)
	r := imageprovider.NewRegistry()
	r.MustRegister(fake.New(fake.AdapterOptions{
		Scenario: fake.ScenarioQuota, Store: store, Installed: true,
	}), 0)
	o := design.New(design.OrchestratorOptions{Registry: r})

	hash, _, _ := store.Write([]byte("attached-image"))
	brief := design.Brief{
		TaskID:      "Q1",
		Description: "screen",
		Mode:        design.ModeAlwaysGenerate,
		Variants:    1,
		Tier:        protocol.TierDraft,
		Viewport:    design.Viewport{Width: 100, Height: 100},
		Reference:   &protocol.Artifact{Hash: hash, Path: store.Path(hash), Format: protocol.FormatPNG, Width: 100, Height: 100},
	}
	out, err := o.Run(context.Background(), brief, []design.ProviderRoute{{Engine: "fake-image"}})
	if err != nil {
		t.Fatal(err)
	}
	if !out.UsedFallback {
		t.Error("§15.5: expected fallback to attached image on quota failure")
	}
	if out.Spec.Source != "fallback" {
		t.Errorf("source = %q, want fallback", out.Spec.Source)
	}
}

// TestImageQuotaFailover_CrossProvider verifies §15.5 / §21.1: when the primary
// image provider hits quota, the orchestrator fails over to a second provider.
func TestImageQuotaFailover_CrossProvider(t *testing.T) {
	t.Parallel()
	store := mustStore(t)
	r := imageprovider.NewRegistry()
	r.MustRegister(fake.New(fake.AdapterOptions{
		Engine: "primary", Scenario: fake.ScenarioQuota, Store: store, Installed: true,
	}), 100)
	r.MustRegister(fake.New(fake.AdapterOptions{
		Engine: "secondary", Scenario: fake.ScenarioSuccess, Store: store, Installed: true,
	}), 50)
	o := design.New(design.OrchestratorOptions{Registry: r})

	brief := design.Brief{
		TaskID: "Q2", Description: "x", Mode: design.ModeAlwaysGenerate,
		Variants: 1, Tier: protocol.TierDraft, Viewport: design.Viewport{Width: 50, Height: 50},
	}
	out, err := o.Run(context.Background(), brief, []design.ProviderRoute{
		{Engine: "primary"}, {Engine: "secondary"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Spec.IsLocked() {
		t.Fatal("spec should lock via secondary provider")
	}
	if out.Failovers != 1 {
		t.Errorf("failovers = %d, want 1", out.Failovers)
	}
}

// TestRule36_9_SeparateAbstractions verifies rule §36.9: the coding agent and
// image provider are DIFFERENT adapter families with separate interfaces. This
// is a compile-time invariant enforced by the type system; this test asserts
// the registry boundaries are distinct.
func TestRule36_9_SeparateAbstractions(t *testing.T) {
	t.Parallel()
	store := mustStore(t)
	// The image-provider registry only accepts imageprovider.Adapter.
	ir := imageprovider.NewRegistry()
	ir.MustRegister(fake.New(fake.AdapterOptions{Store: store, Installed: true}), 0)
	// The coding-agent registry (package codingagent) is a separate type; an
	// imageprovider.Adapter cannot be registered there (compile-time enforced
	// by Go's interface types). This test exists to document the boundary.
	if ir.Len() != 1 {
		t.Errorf("image registry len = %d", ir.Len())
	}
}

// hasAllTiers reports whether the model list covers all three §14.3 tiers.
func hasAllTiers(models []protocol.ImageModel) bool {
	seen := map[protocol.ImageTier]bool{}
	for _, m := range models {
		seen[m.Tier] = true
	}
	return seen[protocol.TierDraft] && seen[protocol.TierStandard] && seen[protocol.TierHighQuality]
}
