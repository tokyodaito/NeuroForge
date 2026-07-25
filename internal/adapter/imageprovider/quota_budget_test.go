package imageprovider_test

import (
	"context"
	"testing"
	"time"

	"neuroforge/internal/adapter/imageprovider"
	"neuroforge/internal/adapter/imageprovider/fake"
	"neuroforge/internal/adapter/imageprovider/protocol"
	"neuroforge/internal/artifacts"
	"neuroforge/internal/budget"
	"neuroforge/internal/quota"
)

// TestImageQuota_TrackedSeparatelyFromCoding verifies §14.4: image quota is
// tracked on a SEPARATE budget/account from coding quota.
func TestImageQuota_TrackedSeparatelyFromCoding(t *testing.T) {
	t.Parallel()
	store, _ := artifacts.New(t.TempDir())
	a := fake.New(fake.AdapterOptions{Store: store, Installed: true})

	// One image account, one coding account.
	const imageAccount = "gpt-image/work"
	const codingAccount = "codex/work"
	qm := quota.New(quota.Config{})

	// Simulate the image provider reporting quota exhaustion on the IMAGE
	// account only.
	qm.RecordFailure(quota.AccountID{Engine: "gpt-image", Account: "work"}, protocol.FailureProviderQuota)

	// Image account is not available (quota exhausted).
	if qm.IsAvailable(quota.AccountID{Engine: "gpt-image", Account: "work"}) {
		t.Error("image account should be unavailable after quota exhaustion")
	}
	// Coding account is unaffected (§14.4 separate tracking).
	if !qm.IsAvailable(quota.AccountID{Engine: "codex", Account: "work"}) {
		t.Error("coding account should remain available (image quota is separate, §14.4)")
	}
	// Use the var so the linter is happy.
	_ = imageAccount
	_ = codingAccount
	_ = a
}

// TestImageBudget_TrackedSeparatelyFromCoding verifies §23/§14.4: image
// spending is bounded by the image daily budget, separate from coding.
func TestImageBudget_TrackedSeparatelyFromCoding(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	limits := budget.Limits{
		GlobalDailyUSD: 100,
		ImageDailyUSD:  5, // image budget separate and tight
	}
	ctrl := budget.New(limits, func() time.Time { return now })

	// Spend $4 on image generation (within image budget).
	ctrl.Record(budget.UsageRecord{
		Account: quota.AccountID{Engine: "gpt-image", Account: "work"},
		Engine:  "gpt-image", ProviderType: budget.ProviderImage,
		ImageGens: 10, CostUSD: 4, Confidence: quota.ConfEstimated,
		At: now,
	})
	// Spend $50 on coding (within global, irrelevant to image budget).
	ctrl.Record(budget.UsageRecord{
		Account: quota.AccountID{Engine: "codex", Account: "work"},
		Engine:  "codex", ProviderType: budget.ProviderCoding,
		InputTokens: 1_000_000, CostUSD: 50, Confidence: quota.ConfExact,
		At: now,
	})

	// Another $4 image generation: total image spend $8 > $5 image budget.
	dec := ctrl.CanAfford(budget.Scope{ProviderType: budget.ProviderImage}, 4, true, true)
	if dec.Allowed {
		t.Error("image spend should be blocked by the separate image budget (§23)")
	}
	if !dec.HardBlocked {
		t.Error("expected HardBlocked on image budget")
	}

	// Coding spend $4 should still be allowed (separate budget, §14.4).
	decCoding := ctrl.CanAfford(budget.Scope{ProviderType: budget.ProviderCoding}, 4, true, true)
	if !decCoding.Allowed {
		t.Errorf("coding spend should be allowed (separate budget): %+v", decCoding)
	}
}

// TestImageUsage_RecordedFromProvider verifies a fake generation produces a
// usage record that aggregates correctly into the image bucket (§14.4).
func TestImageUsage_RecordedFromProvider(t *testing.T) {
	t.Parallel()
	store, _ := artifacts.New(t.TempDir())
	a := fake.New(fake.AdapterOptions{Store: store, Installed: true})

	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	ctrl := budget.New(budget.Limits{}, func() time.Time { return now })

	res, err := a.Generate(context.Background(), protocol.ImageGenerationRequest{
		RunID: "r", Engine: "fake-image", Model: "fake/standard",
		Tier: protocol.TierStandard, Prompt: "x",
		Size: protocol.ImageSize{Width: 16, Height: 16}, Format: protocol.FormatPNG,
	}, &imageprovider.SliceSink{})
	if err != nil {
		t.Fatal(err)
	}
	ctrl.Record(budget.UsageRecord{
		Account:      quota.AccountID{Engine: res.Engine, Account: "work"},
		Engine:       res.Engine,
		Model:        res.Model,
		ProviderType: budget.ProviderImage,
		ImageGens:    res.Usage.ImageGens,
		CostUSD:      budget.USD(res.Usage.CostUSD),
		Confidence:   res.Usage.Confidence,
		Included:     res.Usage.Included,
		At:           now,
	})

	summary := ctrl.Summary(now.Add(-time.Hour), now.Add(time.Hour))
	if summary.ImageGens != 1 {
		t.Errorf("ImageGens = %d, want 1", summary.ImageGens)
	}
	imgCost, ok := summary.PerProvider[string(budget.ProviderImage)]
	if !ok {
		t.Fatal("no image bucket in summary")
	}
	// Fake provider reports Included=true and CostUSD=0 (no real cost, rule
	// §36.5); the meaningful signal is ImageGens in the image bucket.
	if imgCost.ImageGens != 1 {
		t.Errorf("image bucket ImageGens = %d, want 1", imgCost.ImageGens)
	}
}
