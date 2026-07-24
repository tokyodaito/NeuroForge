package router

import (
	"context"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
	"neuroforge/internal/budget"
	"neuroforge/internal/quota"
	"neuroforge/internal/risk"
)

func pFloat(f float64) *float64 { return &f }

func zeroTime() time.Time                              { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
func zeroTimePlus() time.Time                          { return time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC) }
func quotaFailureProviderQuota() protocol.FailureClass { return protocol.FailureProviderQuota }

// TestM6Scenario_AC16_AC17_AC18 is the M6-10 demonstrable scenario. It exercises
// the full deterministic decision flow (risk → complexity → router → quota →
// budget) and asserts the three M6 acceptance criteria:
//
//   - AC-16: a simple task receives a cheap route (TINY/SMALL).
//   - AC-17: a complex task escalates to a strong model (HEAVY/FRONTIER).
//   - AC-18: usage figures carry distinct confidence so estimated values are
//     never shown as exact.
//
// No real paid model is invoked (rule §36.5): the catalog uses fakes.
func TestM6Scenario_AC16_AC17_AC18(t *testing.T) {
	c := NewCatalog()
	for _, e := range testEntries() {
		c.Add(e)
	}
	fixedNow := zeroTimePlus()
	qm := quota.New(quota.Config{
		FailureThreshold: 3, OpenDuration: time.Second, LowRemainingFraction: 0.2,
		Now: func() time.Time { return fixedNow },
	})
	for _, acc := range c.Accounts() {
		rem, lim := 1_000_000.0, 1_000_000.0
		qm.Apply(quota.Snapshot{Account: acc, State: quota.StateAvailable, Confidence: quota.ConfExact, Remaining: &rem, Limit: &lim})
	}
	bc := budget.New(budget.Limits{GlobalDailyUSD: 20, SoftFraction: 0.8}, nil)
	r := New(c, qm, bc, DefaultScoreConfig())

	// AC-16: simple docs task.
	simple := ClassifyComplexity(ComplexitySignals{Description: "fix a typo in the README", EstimatedFileCount: 1})
	simpleRisk := risk.Classify(risk.Signals{Description: "fix a typo in the README"})
	if simple.Complexity > C1 {
		t.Fatalf("simple task complexity = %s, want <= C1", simple.Complexity)
	}
	simpleEx, err := r.Route(context.Background(), Request{Complexity: simple.Complexity, Risk: simpleRisk.Level})
	if err != nil {
		t.Fatal(err)
	}
	if simpleEx.Selected.Entry.Tier > TierSmall {
		t.Errorf("AC-16: simple task got tier %s, want <= SMALL (cheap route)", simpleEx.Selected.Entry.Tier)
	}
	t.Logf("AC-16: simple task -> %s [%s] $%.4f", simpleEx.Selected.Entry.ID(), simpleEx.Selected.Entry.Tier, simpleEx.Selected.EstimatedCostUSD)

	// AC-17: complex architectural task escalates.
	complexSig := ClassifyComplexity(ComplexitySignals{
		Description:           "rewrite the design system across packages",
		EstimatedFileCount:    30,
		EstimatedTurns:        40,
		ArchitecturalDecision: true,
		CrossPackageChange:    true,
	})
	complexRisk := risk.Classify(risk.Signals{Description: "rewrite the design system across packages"})
	complexEx, err := r.Route(context.Background(), Request{Complexity: complexSig.Complexity, Risk: complexRisk.Level})
	if err != nil {
		t.Fatal(err)
	}
	if complexEx.Selected.Entry.Tier < TierHeavy {
		t.Errorf("AC-17: complex task got tier %s, want >= HEAVY (escalation)", complexEx.Selected.Entry.Tier)
	}
	if len(complexEx.FallbackChain) < 2 {
		t.Errorf("complex task must have a fallback chain (§21.1), got %d", len(complexEx.FallbackChain))
	}
	t.Logf("AC-17: complex task -> %s [%s] $%.4f", complexEx.Selected.Entry.ID(), complexEx.Selected.Entry.Tier, complexEx.Selected.EstimatedCostUSD)

	// AC-18: usage aggregation keeps confidence distinct. Record a mix of
	// exact + estimated usage and confirm the aggregate is ESTIMATED (never
	// shown as exact).
	bc.Record(budget.UsageRecord{Engine: "alpha", CostUSD: 2, Included: false, Confidence: quota.ConfExact, ProviderType: budget.ProviderCoding})
	bc.Record(budget.UsageRecord{Engine: "alpha", CostUSD: 1, Included: false, Confidence: quota.ConfEstimated, ProviderType: budget.ProviderCoding})
	agg := bc.Summary(time.Time{}, time.Time{})
	if agg.CoarsestConf != quota.ConfEstimated {
		t.Errorf("AC-18: aggregate confidence = %v, want ESTIMATED (least precise wins)", agg.CoarsestConf)
	}
	if quota.FormatRemaining(quota.Snapshot{Confidence: quota.ConfEstimated, Remaining: pFloat(125000)}) != "~125k" {
		t.Error("AC-18: estimated figures must render with a '~' prefix")
	}

	// §20.3 invariant: exhausting an account removes it from routing while a
	// rate-limit on another account is a distinct, recoverable state.
	alphaAPI := quota.AccountID{Engine: "alpha", Account: "alpha-api"}
	betaAPI := quota.AccountID{Engine: "beta", Account: "beta-api"}
	qm.RecordFailure(alphaAPI, quotaFailureProviderQuota())
	qm.Apply(quota.Snapshot{Account: betaAPI, State: quota.StateRateLimited, Confidence: quota.ConfProviderReported,
		Remaining: pFloat(80), Limit: pFloat(100), RetryAfter: 60 * time.Second, ObservedAt: fixedNow})
	if qm.Snapshot(alphaAPI).State != quota.StateExhausted {
		t.Error("quota exhaustion must be EXHAUSTED")
	}
	if qm.Snapshot(betaAPI).State != quota.StateRateLimited {
		t.Error("rate-limit must be RATE_LIMITED, distinct from exhaustion")
	}
	if qm.IsAvailable(alphaAPI) {
		t.Error("exhausted account must be excluded from new routes")
	}
}
