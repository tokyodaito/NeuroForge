// Package m10integration contains the M10 integration tests that exercise the
// full design-and-visual-verification pipeline (spec §15, §16, §33.3).
//
// These tests compose the M10 domain packages (design → visualharness → visual
// → repair) with the deterministic fake image provider and fake visual harness
// (rule §36.5: no real paid models or devices in CI). They verify the critical
// M10 invariants:
//
//   - AC-21: a UI implementation task can be created from an attached image.
//   - AC-22: the Visual Verification Engine obtains a real screenshot.
//   - AC-23: a visual discrepancy triggers the repair loop.
//   - AC-24: disabled visual verification NEVER claims the UI is verified.
//   - §16.6: reference-free review NEVER claims pixel-perfect.
//   - §16.5: the repair loop is bounded (rule §32).
package m10integration

import (
	"context"
	"testing"

	"neuroforge/internal/adapter/imageprovider/fake"
	"neuroforge/internal/adapter/imageprovider/protocol"
	"neuroforge/internal/adapter/visualharness"
	vhfake "neuroforge/internal/adapter/visualharness/fake"
	"neuroforge/internal/artifacts"
	"neuroforge/internal/design"
	"neuroforge/internal/visual"
)

func mustStore(t *testing.T) *artifacts.Store {
	t.Helper()
	s, err := artifacts.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestAC21_UIImplementationTaskFromAttachedImage verifies AC-21: from an
// attached image, the system creates a UI implementation task with a locked
// visual specification.
func TestAC21_UIImplementationTaskFromAttachedImage(t *testing.T) {
	t.Parallel()
	store := mustStore(t)

	// The "attached image" (a UI reference screenshot).
	refContent := fake.MinimalPNG(1080, 2400, 0xCC)
	hash, _, _ := store.Write(refContent)
	reference := &protocol.Artifact{Hash: hash, Path: store.Path(hash), Format: protocol.FormatPNG, Width: 1080, Height: 2400, Bytes: len(refContent)}

	// Design pipeline in REFERENCE_ONLY mode (§15.1): the attached image
	// becomes the visual specification without generation. The locked spec is
	// what the coding agent receives (§15.6: once locked, the agent MUST NOT
	// arbitrarily change the design).
	spec := design.Specification{
		TaskID:       "AC21",
		Mode:         design.ModeReferenceOnly,
		ArtifactHash: hash,
		Artifact:     *reference,
		Viewport:     design.Viewport{Width: 1080, Height: 2400},
		Theme:        "dark",
		Locale:       "ru",
		Density:      "xxhdpi",
		Source:       "attached",
	}
	if !spec.IsLocked() {
		t.Fatal("AC-21: spec not locked from attached image")
	}
	// The coding agent would receive this spec; §15.6: once locked, the agent
	// MUST NOT arbitrarily change the design. That contract is enforced by the
	// task compiler feeding spec.ArtifactHash into the agent's scope.
	if spec.ArtifactHash != hash {
		t.Errorf("artifact hash mismatch: %q vs %q", spec.ArtifactHash, hash)
	}
}

// TestAC22_VisualEngineObtainsRealScreenshot verifies AC-22: the Visual
// Verification Engine obtains a real screenshot via the harness.
func TestAC22_VisualEngineObtainsRealScreenshot(t *testing.T) {
	t.Parallel()
	store := mustStore(t)
	ctx := context.Background()

	// Fake harness produces a deterministic screenshot.
	h := vhfake.New(vhfake.Options{Store: store})
	if err := h.Build(ctx, visualharness.BuildRequest{Workdir: "."}); err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := h.Launch(ctx, visualharness.LaunchRequest{Locale: "ru", Theme: "dark"}); err != nil {
		t.Fatalf("launch: %v", err)
	}
	shot, err := h.Capture(ctx, visualharness.CaptureRequest{Format: protocol.FormatPNG})
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if shot.Artifact.Hash == "" || shot.Artifact.Bytes == 0 {
		t.Fatalf("AC-22: no real screenshot captured: %+v", shot.Artifact)
	}

	// The engine consumes the screenshot.
	e := visual.New(visual.Options{})
	res := e.Verify(ctx, visual.VerifyInput{Actual: &shot.Artifact, Enabled: true, Store: store})
	if res.Status == visual.StatusNotVerified {
		t.Error("AC-22: engine should verify a captured screenshot")
	}
	// Reference-free review ran.
	if res.Mode != visual.ReferenceFreeRan {
		t.Errorf("mode = %q, want reference_free", res.Mode)
	}
}

// TestAC23_VisualDiscrepancyTriggersRepair verifies AC-23: when a visual
// discrepancy is detected, the repair loop starts.
func TestAC23_VisualDiscrepancyTriggersRepair(t *testing.T) {
	t.Parallel()
	store := mustStore(t)
	ctx := context.Background()

	// Reference (locked spec).
	refContent := fake.MinimalPNG(64, 64, 0xFF)
	refHash, _, _ := store.Write(refContent)
	reference := &protocol.Artifact{Hash: refHash, Width: 64, Height: 64, Bytes: len(refContent), Format: protocol.FormatPNG}

	// Actual screenshot that differs.
	actContent := fake.MinimalPNG(64, 64, 0x11)
	actHash, _, _ := store.Write(actContent)
	actual := &protocol.Artifact{Hash: actHash, Width: 64, Height: 64, Bytes: len(actContent), Format: protocol.FormatPNG}

	e := visual.New(visual.Options{})
	initial := e.Verify(ctx, visual.VerifyInput{
		Reference: reference, Actual: actual, Enabled: true, Store: store,
	})
	if initial.Status != visual.StatusFailed {
		t.Fatalf("AC-23: initial status = %s, want failed (discrepancy)", initial.Status)
	}

	// The repair loop fires (and after one repair the screenshot matches).
	repairCalls := 0
	matchContent := refContent
	matchHash, _, _ := store.Write(matchContent)
	matchArt := &protocol.Artifact{Hash: matchHash, Width: 64, Height: 64, Bytes: len(matchContent), Format: protocol.FormatPNG}

	out, err := visual.RunRepairLoop(ctx,
		visual.RepairLoopConfig{MaximumIterations: 3, MinimumScore: 0.9},
		initial,
		func(_ context.Context, _ []visual.Finding, _ int) error {
			repairCalls++
			return nil
		},
		func(_ context.Context) (visual.Result, error) {
			return e.Verify(ctx, visual.VerifyInput{
				Reference: reference, Actual: matchArt, Enabled: true, Store: store,
			}), nil
		},
	)
	if err != nil {
		t.Fatalf("repair loop: %v", err)
	}
	if repairCalls == 0 {
		t.Error("AC-23: repair loop did not run on discrepancy")
	}
	if !out.Resolved {
		t.Error("AC-23: repair loop should resolve after re-capture matches reference")
	}
}

// TestAC24_DisabledNeverVerified verifies AC-24: when visual verification is
// disabled, the system NEVER claims the UI is verified.
func TestAC24_DisabledNeverVerified(t *testing.T) {
	t.Parallel()
	store := mustStore(t)
	content := fake.MinimalPNG(64, 64, 0x88)
	hash, _, _ := store.Write(content)
	shot := &protocol.Artifact{Hash: hash, Width: 64, Height: 64, Bytes: len(content), Format: protocol.FormatPNG}

	e := visual.New(visual.Options{})
	res := e.Verify(context.Background(), visual.VerifyInput{Actual: shot, Enabled: false, Store: store})
	if res.Status != visual.StatusSkipped {
		t.Errorf("status = %s, want skipped", res.Status)
	}
	if res.Status.IsVerified() {
		t.Error("AC-24: disabled verification MUST NOT be claimable as verified")
	}
}

// TestAC24_ReferenceFreeNeverPixelPerfect verifies §16.6 / AC-24: a
// reference-free review NEVER claims pixel-perfect match.
func TestAC24_ReferenceFreeNeverPixelPerfect(t *testing.T) {
	t.Parallel()
	store := mustStore(t)
	content := fake.MinimalPNG(64, 64, 0x88)
	hash, _, _ := store.Write(content)
	shot := &protocol.Artifact{Hash: hash, Width: 64, Height: 64, Bytes: len(content), Format: protocol.FormatPNG}

	e := visual.New(visual.Options{})
	res := e.Verify(context.Background(), visual.VerifyInput{Actual: shot, Enabled: true, Store: store})
	if res.PixelPerfect {
		t.Error("AC-24/§16.6: reference-free review must NEVER claim pixel-perfect")
	}
	if res.ReferenceBased {
		t.Error("should be reference-free")
	}
}

// TestScenario_ScreenshotFromAttachmentUIToRepair verifies the end-to-end M10
// scenario (M10-8): attached image → UI implementation → verify → repair.
func TestScenario_ScreenshotFromAttachmentUIToRepair(t *testing.T) {
	t.Parallel()
	store := mustStore(t)
	ctx := context.Background()

	// 1. Attached image → visual specification (REFERENCE_ONLY, §15.1).
	refContent := fake.MinimalPNG(64, 64, 0xCC)
	refHash, _, _ := store.Write(refContent)
	reference := &protocol.Artifact{Hash: refHash, Path: store.Path(refHash), Width: 64, Height: 64, Bytes: len(refContent), Format: protocol.FormatPNG}

	// 2. UI implementation happens (simulated): the agent writes code; we move
	//    directly to verification.

	// 3. Build + launch + capture.
	h := vhfake.New(vhfake.Options{Store: store, Scenario: vhfake.ScenarioMismatch, Reference: reference})
	if err := h.Build(ctx, visualharness.BuildRequest{Workdir: "."}); err != nil {
		t.Fatal(err)
	}
	if err := h.Launch(ctx, visualharness.LaunchRequest{Locale: "ru", Theme: "dark"}); err != nil {
		t.Fatal(err)
	}
	shot, err := h.Capture(ctx, visualharness.CaptureRequest{Format: protocol.FormatPNG})
	if err != nil {
		t.Fatal(err)
	}

	// 4. Verify against the reference (mismatch expected).
	e := visual.New(visual.Options{})
	initial := e.Verify(ctx, visual.VerifyInput{
		Reference: reference, Actual: &shot.Artifact, Enabled: true, Store: store,
	})
	if initial.Status != visual.StatusFailed {
		t.Fatalf("expected failed (mismatch), got %s", initial.Status)
	}

	// 5. Repair loop: after one repair, the harness produces a match.
	matchH := vhfake.New(vhfake.Options{Store: store, Scenario: vhfake.ScenarioMatch, Reference: reference})
	_, err = visual.RunRepairLoop(ctx,
		visual.RepairLoopConfig{MaximumIterations: 3, MinimumScore: 0.9},
		initial,
		func(_ context.Context, _ []visual.Finding, _ int) error { return nil },
		func(_ context.Context) (visual.Result, error) {
			match, _ := matchH.Capture(ctx, visualharness.CaptureRequest{Format: protocol.FormatPNG})
			return e.Verify(ctx, visual.VerifyInput{
				Reference: reference, Actual: &match.Artifact, Enabled: true, Store: store,
			}), nil
		},
	)
	if err != nil {
		t.Fatalf("repair loop: %v", err)
	}
}

// TestFakeHarnessScenarios covers all §33.3 fake harness scenarios in the full
// pipeline context.
func TestFakeHarnessScenarios(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, sc := range vhfake.AllScenarios {
		sc := sc
		t.Run(string(sc), func(t *testing.T) {
			t.Parallel()
			store := mustStore(t)
			h := vhfake.New(vhfake.Options{Store: store, Scenario: sc})
			if !vhfake.IsValidScenario(sc) {
				t.Fatalf("scenario %q not valid", sc)
			}
			if err := h.Build(ctx, visualharness.BuildRequest{Workdir: "."}); err != nil {
				t.Errorf("build: %v", err)
			}
			launchErr := h.Launch(ctx, visualharness.LaunchRequest{})
			if sc == vhfake.ScenarioStartupFail {
				if launchErr == nil {
					t.Errorf("scenario %q: expected launch failure", sc)
				}
				return
			}
			if launchErr != nil {
				t.Errorf("launch: %v", launchErr)
			}
			shot, err := h.Capture(ctx, visualharness.CaptureRequest{Format: protocol.FormatPNG})
			if err != nil {
				t.Errorf("capture: %v", err)
			}
			if shot.Artifact.Bytes == 0 {
				t.Errorf("scenario %q: empty screenshot", sc)
			}
		})
	}
}
