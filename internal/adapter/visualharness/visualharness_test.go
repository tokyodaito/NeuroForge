package visualharness_test

import (
	"context"
	"testing"

	"neuroforge/internal/adapter/imageprovider/protocol"
	"neuroforge/internal/adapter/visualharness"
	"neuroforge/internal/adapter/visualharness/fake"
	"neuroforge/internal/artifacts"
)

func mustStore(t *testing.T) *artifacts.Store {
	t.Helper()
	s, err := artifacts.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestRegistry mirrors the image-provider registry invariants.
func TestRegistry(t *testing.T) {
	t.Parallel()
	r := visualharness.NewRegistry()
	h := fake.New(fake.Options{Store: mustStore(t)})
	if err := r.Register(h, 1); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(h, 1); err == nil {
		t.Error("duplicate register must error")
	}
	if _, ok := r.Lookup(h.ID()); !ok {
		t.Error("Lookup failed")
	}
	if _, ok := r.LookupPlatform(visualharness.PlatformGeneric); !ok {
		t.Error("LookupPlatform failed")
	}
}

// TestFake_LifecycleAndCapture verifies the fake harness lifecycle.
func TestFake_LifecycleAndCapture(t *testing.T) {
	t.Parallel()
	store := mustStore(t)
	h := fake.New(fake.Options{Store: store})
	ctx := context.Background()

	if _, err := h.Capture(ctx, visualharness.CaptureRequest{Format: protocol.FormatPNG}); err != nil {
		// Capture may be called without launch in tests; that is allowed.
		_ = err
	}
	if err := h.Build(ctx, visualharness.BuildRequest{Workdir: "."}); err != nil {
		t.Errorf("build: %v", err)
	}
	if err := h.Launch(ctx, visualharness.LaunchRequest{Locale: "ru", Theme: "dark"}); err != nil {
		t.Errorf("launch: %v", err)
	}
	shot, err := h.Capture(ctx, visualharness.CaptureRequest{Format: protocol.FormatPNG})
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if shot.Artifact.Hash == "" || shot.Artifact.Bytes == 0 {
		t.Errorf("bad screenshot: %+v", shot.Artifact)
	}
	if err := h.Shutdown(ctx); err != nil {
		t.Errorf("shutdown: %v", err)
	}
}

// TestFake_Scenarios verifies the §33.3 scenarios.
func TestFake_Scenarios(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// Match with reference.
	store := mustStore(t)
	refContent := []byte{0x89, 'P', 'N', 'G'}
	hash, _, _ := store.Write(refContent)
	ref := &protocol.Artifact{Hash: hash, Path: store.Path(hash)}
	matchH := fake.New(fake.Options{Store: store, Scenario: fake.ScenarioMatch, Reference: ref})
	shot, err := matchH.Capture(ctx, visualharness.CaptureRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if shot.Artifact.Hash != hash {
		t.Errorf("match scenario: hash %q, want reference %q", shot.Artifact.Hash, hash)
	}

	// Mismatch.
	mismatchH := fake.New(fake.Options{Store: store, Scenario: fake.ScenarioMismatch, Reference: ref})
	shot2, err := mismatchH.Capture(ctx, visualharness.CaptureRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if shot2.Artifact.Hash == hash {
		t.Error("mismatch scenario produced same hash as reference")
	}

	// Blank.
	blankH := fake.New(fake.Options{Store: store, Scenario: fake.ScenarioBlank})
	if _, err := blankH.Capture(ctx, visualharness.CaptureRequest{}); err != nil {
		t.Errorf("blank: %v", err)
	}

	// Clipped.
	clippedH := fake.New(fake.Options{Store: store, Scenario: fake.ScenarioClipped})
	if _, err := clippedH.Capture(ctx, visualharness.CaptureRequest{}); err != nil {
		t.Errorf("clipped: %v", err)
	}

	// Startup failure.
	failH := fake.New(fake.Options{Store: store, Scenario: fake.ScenarioStartupFail})
	err = failH.Launch(ctx, visualharness.LaunchRequest{})
	if err == nil {
		t.Error("startup-failure scenario should error on launch")
	}
	fc := failH.ClassifyFailure(err)
	if fc.Class != protocol.FailureVisualFailure {
		t.Errorf("startup-failure class = %s, want VISUAL_FAILURE", fc.Class)
	}
}
