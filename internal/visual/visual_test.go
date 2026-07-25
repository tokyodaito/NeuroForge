package visual_test

import (
	"context"
	"testing"

	"neuroforge/internal/adapter/imageprovider/fake"
	"neuroforge/internal/adapter/imageprovider/protocol"
	"neuroforge/internal/artifacts"
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

// TestVerify_DisabledNeverVerified verifies AC-24: when visual verification is
// disabled, the result MUST NOT be claimable as verified.
func TestVerify_DisabledNeverVerified(t *testing.T) {
	t.Parallel()
	e := visual.New(visual.Options{})
	res := e.Verify(context.Background(), visual.VerifyInput{Enabled: false})
	if res.Status != visual.StatusSkipped {
		t.Errorf("status = %s, want skipped", res.Status)
	}
	if res.Status.IsVerified() {
		t.Error("AC-24: disabled verification must NOT be claimable as verified")
	}
}

// TestVerify_NoScreenshotNeverVerified verifies AC-24: when no actual
// screenshot is provided, the result is not_verified.
func TestVerify_NoScreenshotNeverVerified(t *testing.T) {
	t.Parallel()
	e := visual.New(visual.Options{})
	res := e.Verify(context.Background(), visual.VerifyInput{Enabled: true})
	if res.Status != visual.StatusNotVerified {
		t.Errorf("status = %s, want not_verified", res.Status)
	}
	if res.Status.IsVerified() {
		t.Error("AC-24: missing screenshot must NOT be claimable as verified")
	}
}

// TestVerify_ReferenceBased_Passed verifies a reference-based happy path
// (actual byte-equal to reference).
func TestVerify_ReferenceBased_Passed(t *testing.T) {
	t.Parallel()
	store := mustStore(t)
	content := fake.MinimalPNG(32, 32, 0x88)
	hash, _, _ := store.Write(content)
	ref := &protocol.Artifact{Hash: hash, Width: 32, Height: 32, Bytes: len(content), Format: protocol.FormatPNG}
	act := &protocol.Artifact{Hash: hash, Width: 32, Height: 32, Bytes: len(content), Format: protocol.FormatPNG}

	e := visual.New(visual.Options{})
	res := e.Verify(context.Background(), visual.VerifyInput{
		Reference: ref, Actual: act, Enabled: true, Store: store,
	})
	if res.Status != visual.StatusPassed {
		t.Errorf("status = %s, want passed (findings=%+v)", res.Status, res.Findings)
	}
	if !res.ReferenceBased {
		t.Error("should be reference-based")
	}
	if !res.Status.IsVerified() {
		t.Error("passed status should be verified")
	}
}

// TestVerify_ReferenceBased_DifferentFails verifies a mismatch is flagged.
func TestVerify_ReferenceBased_DifferentFails(t *testing.T) {
	t.Parallel()
	store := mustStore(t)
	refContent := fake.MinimalPNG(32, 32, 0x88)
	actContent := fake.MinimalPNG(32, 32, 0x11) // very different
	refHash, _, _ := store.Write(refContent)
	actHash, _, _ := store.Write(actContent)
	ref := &protocol.Artifact{Hash: refHash, Width: 32, Height: 32, Bytes: len(refContent)}
	act := &protocol.Artifact{Hash: actHash, Width: 32, Height: 32, Bytes: len(actContent)}

	e := visual.New(visual.Options{})
	res := e.Verify(context.Background(), visual.VerifyInput{
		Reference: ref, Actual: act, Enabled: true, Store: store,
	})
	if res.Status != visual.StatusFailed {
		t.Errorf("status = %s, want failed", res.Status)
	}
	if res.Status.IsVerified() {
		t.Error("failed status must NOT be verified")
	}
}

// TestVerify_ReferenceFreeNeverPixelPerfect verifies §16.6/AC-24: a
// reference-free review NEVER claims pixel-perfect.
func TestVerify_ReferenceFreeNeverPixelPerfect(t *testing.T) {
	t.Parallel()
	store := mustStore(t)
	content := fake.MinimalPNG(32, 32, 0x88)
	hash, _, _ := store.Write(content)
	act := &protocol.Artifact{Hash: hash, Width: 32, Height: 32, Bytes: len(content)}

	e := visual.New(visual.Options{})
	res := e.Verify(context.Background(), visual.VerifyInput{
		Actual: act, Enabled: true, Store: store,
	})
	if res.PixelPerfect {
		t.Error("AC-24/§16.6: reference-free review must NOT claim pixel-perfect")
	}
	if res.ReferenceBased {
		t.Error("should be reference-free")
	}
	if res.Mode != visual.ReferenceFreeRan {
		t.Errorf("mode = %q, want reference_free", res.Mode)
	}
}

// TestVerify_BlankScreenBlocked verifies the §16.3 blank-screen check.
func TestVerify_BlankScreenBlocked(t *testing.T) {
	t.Parallel()
	store := mustStore(t)
	blank := make([]byte, 1024) // all zeros
	hash, _, _ := store.Write(blank)
	act := &protocol.Artifact{Hash: hash, Width: 32, Height: 32, Bytes: len(blank)}
	e := visual.New(visual.Options{})
	res := e.Verify(context.Background(), visual.VerifyInput{Actual: act, Enabled: true, Store: store})
	if res.Status != visual.StatusFailed {
		t.Errorf("status = %s, want failed (blank screen)", res.Status)
	}
	found := false
	for _, f := range res.Findings {
		if f.Code == "blank_screen" && f.Severity == visual.SeverityBlocker {
			found = true
		}
	}
	if !found {
		t.Error("expected blank_screen blocker finding")
	}
}

// TestRepairLoop_Resolves verifies §16.5: the loop resolves on re-verify.
func TestRepairLoop_Resolves(t *testing.T) {
	t.Parallel()
	store := mustStore(t)
	content := fake.MinimalPNG(32, 32, 0x88)
	hash, _, _ := store.Write(content)
	act := &protocol.Artifact{Hash: hash, Width: 32, Height: 32, Bytes: len(content)}

	e := visual.New(visual.Options{})
	initial := visual.Result{Status: visual.StatusFailed, Score: 0.5, Findings: []visual.Finding{
		{Severity: visual.SeverityMajor, Code: "visual_diff", Description: "off"},
	}}
	calls := 0
	out, err := visual.RunRepairLoop(context.Background(),
		visual.RepairLoopConfig{MaximumIterations: 3, MinimumScore: 0.9},
		initial,
		func(_ context.Context, _ []visual.Finding, _ int) error {
			calls++
			return nil
		},
		func(_ context.Context) (visual.Result, error) {
			// After one repair, verification passes.
			return e.Verify(context.Background(), visual.VerifyInput{Actual: act, Enabled: true, Store: store}), nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Resolved {
		t.Error("expected resolved")
	}
	if calls != 1 {
		t.Errorf("repair calls = %d, want 1", calls)
	}
}

// TestRepairLoop_Bounded verifies rule §32: the loop does not retry
// indefinitely.
func TestRepairLoop_Bounded(t *testing.T) {
	t.Parallel()
	initial := visual.Result{Status: visual.StatusFailed, Score: 0.1, Findings: []visual.Finding{
		{Severity: visual.SeverityMajor, Description: "x"},
	}}
	calls := 0
	out, _ := visual.RunRepairLoop(context.Background(),
		visual.RepairLoopConfig{MaximumIterations: 2, MinimumScore: 0.9},
		initial,
		func(_ context.Context, _ []visual.Finding, _ int) error { calls++; return nil },
		func(_ context.Context) (visual.Result, error) {
			return visual.Result{Status: visual.StatusFailed, Score: 0.2}, nil
		},
	)
	if out.Resolved {
		t.Error("should not resolve")
	}
	if calls != 2 {
		t.Errorf("repair calls = %d, want 2 (bounded)", calls)
	}
	if out.FinalResult.Status.IsVerified() {
		t.Error("AC-24: unresolved repair must not be verified")
	}
}

// TestRepairLoop_SkippedDoesNotRepair verifies AC-24: a skipped/not-verified
// result short-circuits the loop (no repair can help).
func TestRepairLoop_SkippedDoesNotRepair(t *testing.T) {
	t.Parallel()
	calls := 0
	out, _ := visual.RunRepairLoop(context.Background(),
		visual.DefaultRepairLoopConfig(),
		visual.Result{Status: visual.StatusSkipped},
		func(context.Context, []visual.Finding, int) error { calls++; return nil },
		func(context.Context) (visual.Result, error) { return visual.Result{Status: visual.StatusSkipped}, nil },
	)
	if out.Resolved {
		t.Error("skipped must not resolve")
	}
	if calls != 0 {
		t.Errorf("repair calls = %d, want 0 (no repair when skipped)", calls)
	}
}
