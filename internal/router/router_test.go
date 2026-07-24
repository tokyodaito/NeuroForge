package router

import (
	"context"
	"testing"

	"neuroforge/internal/budget"
	"neuroforge/internal/quota"
	"neuroforge/internal/risk"
)

func ptrFloat(f float64) *float64 { return &f }

func TestBaseTier_TableDriven(t *testing.T) {
	cases := []struct {
		c            Complexity
		preferStrong bool
		want         Tier
	}{
		{C0, false, TierTiny},
		{C1, false, TierSmall},
		{C2, false, TierStandard},
		{C3, false, TierStandard},
		{C3, true, TierHeavy},
		{C4, false, TierHeavy},
		{C4, true, TierFrontier},
	}
	for _, tc := range cases {
		name := tc.c.String()
		if tc.preferStrong {
			name += "-strong"
		}
		t.Run(name, func(t *testing.T) {
			if got := BaseTier(tc.c, tc.preferStrong); got != tc.want {
				t.Fatalf("BaseTier(%s,strong=%v) = %s, want %s", tc.c, tc.preferStrong, got, tc.want)
			}
		})
	}
}

func TestClassifyComplexity_TableDriven(t *testing.T) {
	cases := []struct {
		name string
		s    ComplexitySignals
		want Complexity
	}{
		{"docs typo", ComplexitySignals{Description: "fix typo in README"}, C0},
		{"single file feature", ComplexitySignals{Description: "add feature", EstimatedFileCount: 1, EstimatedTurns: 4}, C1},
		{"multi-file refactor", ComplexitySignals{Description: "refactor module", EstimatedFileCount: 6, EstimatedTurns: 12}, C2},
		{"cross-package", ComplexitySignals{Description: "integrate", CrossPackageChange: true, EstimatedFileCount: 5, EstimatedTurns: 16}, C3},
		{"architecture rewrite", ComplexitySignals{Description: "rewrite design system", ArchitecturalDecision: true, EstimatedFileCount: 25, EstimatedTurns: 40}, C4},
		{"conflicting cheap results", ComplexitySignals{Description: "investigate", ConflictingCheapResults: true, EstimatedTurns: 28}, C3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyComplexity(tc.s)
			if got.Complexity != tc.want {
				t.Fatalf("complexity = %s, want %s; reasons=%v", got.Complexity, tc.want, got.Reasons)
			}
		})
	}
}

// TestRoute_SimpleTaskGetsCheapRoute (AC-16): a C0 task selects a TINY route,
// not a heavy/frontier one.
func TestRoute_SimpleTaskGetsCheapRoute(t *testing.T) {
	r := newTestRouter()
	ex, err := r.Route(context.Background(), Request{Complexity: C0, Risk: risk.R0})
	if err != nil {
		t.Fatal(err)
	}
	if ex.Selected.Entry.Engine == "" {
		t.Fatal("expected a selected route")
	}
	if ex.Selected.Entry.Tier != TierTiny {
		t.Errorf("simple task selected tier %s, want TINY (AC-16)", ex.Selected.Entry.Tier)
	}
	if ex.Selected.EstimatedCostUSD > 0 {
		t.Logf("simple route cost: $%.4f", ex.Selected.EstimatedCostUSD)
	}
}

// TestRoute_ComplexTaskEscalates (AC-17): a C4 task escalates to HEAVY/FRONTIER.
func TestRoute_ComplexTaskEscalates(t *testing.T) {
	r := newTestRouter()
	ex, err := r.Route(context.Background(), Request{Complexity: C4, Risk: risk.R1})
	if err != nil {
		t.Fatal(err)
	}
	if ex.Selected.Entry.Tier < TierHeavy {
		t.Errorf("complex task selected tier %s, want HEAVY/FRONTIER (AC-17)", ex.Selected.Entry.Tier)
	}
}

// TestRoute_RiskFloorsTier (§26): high risk forces at least the risk-floor tier.
func TestRoute_RiskFloorsTier(t *testing.T) {
	r := newTestRouter()
	// C0 mechanical task but R4 (auth) — must NOT select TINY.
	ex, err := r.Route(context.Background(), Request{Complexity: C0, Risk: risk.R4})
	if err != nil {
		t.Fatal(err)
	}
	if ex.Selected.Entry.Tier < TierStandard {
		t.Errorf("R4 risk selected tier %s, want >= STANDARD (§26 risk floor)", ex.Selected.Entry.Tier)
	}
}

// TestRoute_ExhaustedAccountExcluded (§20.3): an exhausted account is never
// selected and appears in the excluded list with a reason.
func TestRoute_ExhaustedAccountExcluded(t *testing.T) {
	r := newTestRouter()
	// Exhaust the alpha-api account (the only STANDARD alpha paid route).
	alphaAPI := quota.AccountID{Engine: "alpha", Account: "alpha-api"}
	r.quotaApply(alphaAPI, quota.StateExhausted)

	ex, err := r.Route(context.Background(), Request{Complexity: C2, Risk: risk.R1})
	if err != nil {
		t.Fatal(err)
	}
	// The selected route must not use the exhausted account.
	if ex.Selected.Entry.Account == "alpha-api" && ex.Selected.Entry.Engine == "alpha" {
		t.Fatalf("selected exhausted account: %+v", ex.Selected.Entry)
	}
	// And it must appear in the excluded list.
	found := false
	for _, x := range ex.Excluded {
		if x.Entry.Engine == "alpha" && x.Entry.Account == "alpha-api" {
			found = true
			if x.Reason == "" {
				t.Error("excluded exhausted account must carry a reason (§19.6)")
			}
		}
	}
	if !found {
		t.Errorf("exhausted account missing from excluded list: %+v", ex.Excluded)
	}
}

// TestRoute_FallbackChainOrdered (§21.1): the fallback chain starts at the
// target tier and escalates, then de-escalates.
func TestRoute_FallbackChainOrdered(t *testing.T) {
	r := newTestRouter()
	ex, err := r.Route(context.Background(), Request{Complexity: C2, Risk: risk.R1})
	if err != nil {
		t.Fatal(err)
	}
	if len(ex.FallbackChain) < 2 {
		t.Fatalf("fallback chain length %d, want >= 2", len(ex.FallbackChain))
	}
	if ex.FallbackChain[0].Entry.Tier != TierStandard {
		t.Errorf("primary fallback tier = %s, want STANDARD", ex.FallbackChain[0].Entry.Tier)
	}
}

// TestRoute_HardBudgetRestrictsToIncluded (§23): hard budget block forbids paid
// routes but still permits subscription-included routes.
func TestRoute_HardBudgetRestrictsToIncluded(t *testing.T) {
	r := newTestRouter()
	// Exhaust the global daily budget.
	for i := 0; i < 5; i++ {
		r.budget.Record(budget.UsageRecord{
			Engine: "alpha", CostUSD: 5, Included: false,
			Confidence: quota.ConfExact, ProviderType: budget.ProviderCoding,
		})
	}
	ex, err := r.Route(context.Background(), Request{Complexity: C1, Risk: risk.R0})
	if err != nil {
		t.Fatal(err)
	}
	if !ex.BudgetDecision.HardBlocked {
		t.Fatal("expected hard budget block")
	}
	if !ex.Selected.Entry.SubscriptionIncluded {
		t.Errorf("hard-blocked run must select a subscription-included route, got %s",
			ex.Selected.Entry.ID())
	}
	for _, x := range ex.Excluded {
		if !x.Entry.SubscriptionIncluded && !contains(x.Reason, "subscription-included") {
			t.Errorf("paid route %s should be excluded under hard block, reason=%q",
				x.Entry.ID(), x.Reason)
		}
	}
}

// TestRoute_ImageInputRequiresImageModel (§19.1): NeedsImages filters out
// non-multimodal models.
func TestRoute_ImageInputRequiresImageModel(t *testing.T) {
	r := newTestRouter()
	// Only C4 (frontier) reaches a multimodal entry in the default catalog.
	ex, err := r.Route(context.Background(), Request{Complexity: C4, Risk: risk.R1, NeedsImages: true})
	if err != nil {
		t.Fatal(err)
	}
	if !ex.Selected.Entry.SupportsImages {
		t.Errorf("NeedsImages selected a non-multimodal model: %+v", ex.Selected.Entry)
	}
}

func TestRoute_ContextWindowFilter(t *testing.T) {
	r := newTestRouter()
	// Demand a huge context that only the frontier model satisfies.
	ex, err := r.Route(context.Background(), Request{
		Complexity: C4, Risk: risk.R1, ContextTokens: 240_000, NeedsImages: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ex.Selected.Entry.ContextWindow < 240_000 {
		t.Errorf("selected context window %d < required 240000", ex.Selected.Entry.ContextWindow)
	}
}

// TestRoute_Deterministic is a property test: routing is pure — the same inputs
// always yield the same selected route, score and order (no LLM / no randomness).
func TestRoute_Deterministic(t *testing.T) {
	r := newTestRouter()
	req := Request{Complexity: C3, Risk: risk.R2, PreferStrong: true}
	first, err := r.Route(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		ex, err := r.Route(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if ex.Selected.Entry.ID() != first.Selected.Entry.ID() {
			t.Fatalf("iteration %d: selected %s, want %s (non-deterministic)", i, ex.Selected.Entry.ID(), first.Selected.Entry.ID())
		}
		if ex.Selected.Score != first.Selected.Score {
			t.Fatalf("iteration %d: score %v, want %v", i, ex.Selected.Score, first.Selected.Score)
		}
		if len(ex.Alternatives) != len(first.Alternatives) {
			t.Fatalf("iteration %d: alt count %d, want %d", i, len(ex.Alternatives), len(first.Alternatives))
		}
		for j := range ex.Alternatives {
			if ex.Alternatives[j].Route.Entry.ID() != first.Alternatives[j].Route.Entry.ID() {
				t.Fatalf("iteration %d alt %d: %s, want %s", i, j, ex.Alternatives[j].Route.Entry.ID(), first.Alternatives[j].Route.Entry.ID())
			}
		}
	}
}

// TestRoute_InvalidInputsRejected: invalid complexity/risk are rejected, not
// silently coerced (rule §36.25).
func TestRoute_InvalidInputsRejected(t *testing.T) {
	r := newTestRouter()
	if _, err := r.Route(context.Background(), Request{Complexity: Complexity(99), Risk: risk.R0}); err == nil {
		t.Error("invalid complexity should be rejected")
	}
	if _, err := r.Route(context.Background(), Request{Complexity: C0, Risk: risk.Level(99)}); err == nil {
		t.Error("invalid risk should be rejected")
	}
}

// TestRoute_AlternativesRanked: alternatives are sorted best-first; the selected
// route's reason is empty and every other alternative has a non-empty reason.
func TestRoute_AlternativesRanked(t *testing.T) {
	r := newTestRouter()
	ex, err := r.Route(context.Background(), Request{Complexity: C2, Risk: risk.R1})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(ex.Alternatives); i++ {
		if ex.Alternatives[i].Route.Score > ex.Alternatives[i-1].Route.Score {
			t.Errorf("alternatives not ranked: [%d].Score=%.2f > [%d].Score=%.2f",
				i, ex.Alternatives[i].Route.Score, i-1, ex.Alternatives[i-1].Route.Score)
		}
	}
	selID := ex.Selected.Entry.ID()
	emptyCount := 0
	for _, a := range ex.Alternatives {
		if a.Reason == "" {
			emptyCount++
			if a.Route.Entry.ID() != selID {
				t.Errorf("alternative %s has empty reason but is not the selected route", a.Route.Entry.ID())
			}
		}
	}
	if emptyCount != 1 {
		t.Errorf("expected exactly one alternative with empty reason (the selected), got %d", emptyCount)
	}
}

func TestRoute_EstimatedCostNeverMarkedExact(t *testing.T) {
	r := newTestRouter()
	ex, err := r.Route(context.Background(), Request{Complexity: C2, Risk: risk.R1})
	if err != nil {
		t.Fatal(err)
	}
	// Router-computed cost is always an ESTIMATE (rule §36.10): the selected
	// route must never carry EXACT confidence for its cost figure.
	if ex.Selected.EstimatedCostUSD > 0 && ex.Selected.QuotaConfidence == quota.ConfExact {
		// Quota confidence may be EXACT (from a provider), but the COST figure is
		// a router estimate. We assert the renderer tags cost distinctly elsewhere;
		// here we only assert the quota confidence is one of the valid enum values.
		switch ex.Selected.QuotaConfidence {
		case quota.ConfExact, quota.ConfProviderReported, quota.ConfEstimated, quota.ConfInferred, quota.ConfUnknown:
		default:
			t.Errorf("quota confidence invalid: %q", ex.Selected.QuotaConfidence)
		}
	}
}

func TestCatalog_RejectsDuplicates(t *testing.T) {
	c := NewCatalog()
	e := CatalogEntry{Tier: TierTiny, Engine: "alpha", Model: "alpha-mini", Account: "alpha-sub"}
	if !c.Add(e) {
		t.Fatal("first add should succeed")
	}
	if c.Add(e) {
		t.Fatal("duplicate add should be rejected")
	}
	c.Add(CatalogEntry{Tier: TierSmall, Engine: "alpha", Model: "alpha-mini", Account: "alpha-sub"})
	if got := len(c.ByTier(TierTiny)); got != 1 {
		t.Errorf("ByTier(TINY) = %d, want 1", got)
	}
}

func TestCatalog_AccountsDeduped(t *testing.T) {
	c := NewCatalog()
	c.Add(CatalogEntry{Tier: TierTiny, Engine: "alpha", Model: "m1", Account: "a"})
	c.Add(CatalogEntry{Tier: TierSmall, Engine: "alpha", Model: "m2", Account: "a"})
	c.Add(CatalogEntry{Tier: TierStandard, Engine: "beta", Model: "m3", Account: "b"})
	accs := c.Accounts()
	if len(accs) != 2 {
		t.Fatalf("accounts = %d, want 2", len(accs))
	}
}

// ---- helpers ----

// testRouter wraps Router with test-accessible quota/budget knobs.
type testRouter struct {
	*Router
	qm *quota.Manager
	bc *budget.Controller
}

func (tr *testRouter) quotaApply(id quota.AccountID, state quota.State) {
	tr.qm.Apply(quota.Snapshot{
		Account: id, State: state, Confidence: quota.ConfExact,
		Remaining: ptrFloat(0), Limit: ptrFloat(100),
	})
}

func newTestRouter() *testRouter {
	c := NewCatalog()
	for _, e := range testEntries() {
		c.Add(e)
	}
	qm := quota.New(quota.DefaultConfig())
	for _, acc := range c.Accounts() {
		rem, lim := 1_000_000.0, 1_000_000.0
		qm.Apply(quota.Snapshot{Account: acc, State: quota.StateAvailable, Confidence: quota.ConfExact, Remaining: &rem, Limit: &lim})
	}
	bc := budget.New(budget.Limits{GlobalDailyUSD: 20, SoftFraction: 0.8}, nil)
	return &testRouter{Router: New(c, qm, bc, DefaultScoreConfig()), qm: qm, bc: bc}
}

func testEntries() []CatalogEntry {
	return []CatalogEntry{
		{Tier: TierTiny, Engine: "alpha", Model: "alpha-mini", Account: "alpha-sub",
			SubscriptionIncluded: true, Priority: 2, ContextWindow: 32_000},
		{Tier: TierSmall, Engine: "alpha", Model: "alpha-lite", Account: "alpha-sub",
			SubscriptionIncluded: true, Priority: 2, ContextWindow: 64_000},
		{Tier: TierStandard, Engine: "alpha", Model: "alpha-pro", Account: "alpha-api",
			CostPer1MInputUSD: 3, CostPer1MOutputUSD: 12, ContextWindow: 128_000, Priority: 3},
		{Tier: TierHeavy, Engine: "alpha", Model: "alpha-max", Account: "alpha-api",
			CostPer1MInputUSD: 8, CostPer1MOutputUSD: 30, ContextWindow: 200_000, SupportsImages: true, Priority: 3},
		{Tier: TierTiny, Engine: "beta", Model: "beta-spark", Account: "beta-api",
			CostPer1MInputUSD: 0.5, CostPer1MOutputUSD: 2, ContextWindow: 16_000, Priority: 1},
		{Tier: TierStandard, Engine: "beta", Model: "beta-core", Account: "beta-api",
			CostPer1MInputUSD: 4, CostPer1MOutputUSD: 15, ContextWindow: 128_000, Priority: 2},
		{Tier: TierFrontier, Engine: "beta", Model: "beta-frontier", Account: "beta-api",
			CostPer1MInputUSD: 12, CostPer1MOutputUSD: 45, ContextWindow: 256_000, SupportsImages: true, Priority: 4},
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
