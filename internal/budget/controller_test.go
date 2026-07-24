package budget

import (
	"sync"
	"testing"
	"time"

	"neuroforge/internal/quota"
	"neuroforge/internal/risk"
)

func fixedNow() time.Time { return time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC) }

func TestRecordAndSummary_IncludedVsPaidSeparated(t *testing.T) {
	c := New(Limits{}, fixedNow)
	c.Record(UsageRecord{
		Engine: "fake", Model: "tiny-model", Tier: "TINY",
		InputTokens: 1000, OutputTokens: 100, CostUSD: 0,
		Included: true, Confidence: quota.ConfExact, At: fixedNow(),
	})
	c.Record(UsageRecord{
		Engine: "fake", Model: "std-model", Tier: "STANDARD",
		InputTokens: 5000, OutputTokens: 500, CostUSD: 2.5,
		Included: false, Confidence: quota.ConfExact, At: fixedNow(),
	})
	c.Record(UsageRecord{
		Engine: "fake", Model: "std-model", Tier: "STANDARD",
		InputTokens: 2000, OutputTokens: 200, CostUSD: 1.0,
		Included: false, Confidence: quota.ConfEstimated, At: fixedNow(),
	})

	s := c.Summary(time.Time{}, time.Time{})
	if s.IncludedCost != 0 {
		t.Errorf("included cost = %v, want 0 (subscription-included has no marginal cost)", s.IncludedCost)
	}
	if s.PaidCost != 3.5 {
		t.Errorf("paid cost = %v, want 3.5", s.PaidCost)
	}
	if s.InputTokens != 8000 {
		t.Errorf("input tokens = %d, want 8000", s.InputTokens)
	}
	if s.CoarsestConf != quota.ConfEstimated {
		t.Errorf("coarsest confidence = %v, want ESTIMATED (aggregate follows least precise)", s.CoarsestConf)
	}
	// Per-engine aggregation keeps the split.
	pe := s.PerEngine["fake"]
	if pe.PaidCost != 3.5 || pe.IncludedCost != 0 {
		t.Errorf("per-engine = %+v, want paid 3.5 / included 0", pe)
	}
}

func TestHardBudget_ForbidsPaidRun_PermitsIncluded(t *testing.T) {
	limits := Limits{GlobalDailyUSD: 10, SoftFraction: 0.8}
	c := New(limits, fixedNow)
	c.Record(UsageRecord{Engine: "fake", CostUSD: 9, Included: false, Confidence: quota.ConfExact, At: fixedNow()})

	// Another $2 paid run must be hard-blocked.
	d := c.CanAfford(Scope{ProviderType: ProviderCoding}, 2, true, true)
	if d.Allowed || !d.HardBlocked {
		t.Fatalf("expected hard block, got %+v", d)
	}
	if !d.IncludedPermitted {
		t.Error("hard block must still permit subscription-included routes (§23)")
	}

	// The same $2 as subscription-included usage must be allowed.
	d2 := c.CanAfford(Scope{ProviderType: ProviderCoding}, 2, false, true)
	if !d2.Allowed {
		t.Fatalf("included usage must not be blocked by paid hard limit, got %+v", d2)
	}
	if d2.HardBlocked {
		t.Error("included usage must never be hard-blocked")
	}
}

func TestSoftBudget_TriggersCheaperRoute(t *testing.T) {
	limits := Limits{GlobalDailyUSD: 10, SoftFraction: 0.8}
	c := New(limits, fixedNow)
	// $7 spent; $1 more crosses the 80% soft threshold ($8) but stays under hard ($10).
	c.Record(UsageRecord{Engine: "fake", CostUSD: 7, Included: false, Confidence: quota.ConfExact, At: fixedNow()})
	d := c.CanAfford(Scope{ProviderType: ProviderCoding}, 1, true, true)
	if !d.Allowed {
		t.Fatalf("should be allowed under hard limit, got %+v", d)
	}
	if !d.SoftSignal {
		t.Error("expected soft signal to prefer cheaper route")
	}
}

func TestNoLimit_AllowsAll(t *testing.T) {
	c := New(Limits{}, fixedNow)
	d := c.CanAfford(Scope{}, 1000, true, true)
	if !d.Allowed {
		t.Fatalf("no limits -> allowed, got %+v", d)
	}
}

func TestMostRestrictiveScopeWins(t *testing.T) {
	limits := Limits{
		GlobalDailyUSD: 100,
		ProjectDailyUSD: map[string]USD{
			"work": 5,
		},
		SoftFraction: 0.9,
	}
	c := New(limits, fixedNow)
	// project work spent $4 of $5; global is far from limit.
	c.Record(UsageRecord{Engine: "fake", CostUSD: 4, Included: false, ProjectID: "work", Confidence: quota.ConfExact, At: fixedNow()})
	// $2 more exceeds the project daily limit -> hard block even though global is fine.
	d := c.CanAfford(Scope{ProjectID: "work", ProviderType: ProviderCoding}, 2, true, true)
	if d.Allowed || !d.HardBlocked {
		t.Fatalf("project limit should hard-block, got %+v", d)
	}
	if d.LimitUSD != 5 {
		t.Errorf("effective limit = %v, want 5 (project)", d.LimitUSD)
	}
}

func TestImageBudgetSeparate(t *testing.T) {
	limits := Limits{ImageDailyUSD: 3, GlobalDailyUSD: 100, SoftFraction: 0.9}
	c := New(limits, fixedNow)
	// $3 of image spend exhausts the image budget (coding budget untouched).
	c.Record(UsageRecord{Engine: "img", CostUSD: 3, Included: false, ProviderType: ProviderImage, Confidence: quota.ConfExact, At: fixedNow()})
	d := c.CanAfford(Scope{ProviderType: ProviderImage}, 1, true, true)
	if d.Allowed {
		t.Fatalf("image budget should block image spend, got %+v", d)
	}
	if !contains(d.Reason, "image daily") {
		t.Errorf("reason = %q, want image daily", d.Reason)
	}
	// Coding spend under its own budget is fine.
	d2 := c.CanAfford(Scope{ProviderType: ProviderCoding}, 1, true, true)
	if !d2.Allowed {
		t.Fatalf("coding spend should be allowed separately, got %+v", d2)
	}
}

func TestTaskBudgetByRisk(t *testing.T) {
	limits := Limits{
		TaskDefaultUSD: map[risk.Level]USD{risk.R2: 4, risk.R4: 30},
		SoftFraction:   0.9,
	}
	c := New(limits, fixedNow)
	c.Record(UsageRecord{Engine: "fake", CostUSD: 4, Included: false, TaskID: "T-1", Confidence: quota.ConfExact, At: fixedNow()})
	// R2 task default is $4 -> hard block at the task scope.
	d := c.CanAfford(Scope{TaskID: "T-1", TaskRisk: risk.R2, ProviderType: ProviderCoding}, 0.5, true, true)
	if d.Allowed {
		t.Fatalf("task budget should hard-block, got %+v", d)
	}
	if d.LimitUSD != 4 {
		t.Errorf("task limit = %v, want 4", d.LimitUSD)
	}
}

func TestSpendExcludesIncluded(t *testing.T) {
	c := New(Limits{GlobalDailyUSD: 10}, fixedNow)
	c.Record(UsageRecord{Engine: "fake", CostUSD: 50, Included: true, Confidence: quota.ConfExact, At: fixedNow()})
	paidToday, _, _, _ := c.Spend(Scope{ProviderType: ProviderCoding})
	if paidToday != 0 {
		t.Errorf("included usage must not count as paid spend; got %v", paidToday)
	}
}

func TestAggregatedConfidence_NeverShowsEstimatedAsExact(t *testing.T) {
	c := New(Limits{}, fixedNow)
	c.Record(UsageRecord{Engine: "a", CostUSD: 1, Included: false, Confidence: quota.ConfExact, At: fixedNow()})
	c.Record(UsageRecord{Engine: "b", CostUSD: 1, Included: false, Confidence: quota.ConfEstimated, At: fixedNow()})
	s := c.Summary(time.Time{}, time.Time{})
	if s.CoarsestConf != quota.ConfEstimated {
		t.Fatalf("aggregate confidence = %v, want ESTIMATED", s.CoarsestConf)
	}
}

func TestController_ConcurrentRecordAndSummary(t *testing.T) {
	c := New(Limits{GlobalDailyUSD: 100000}, fixedNow)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				c.Record(UsageRecord{Engine: "fake", CostUSD: USD(n + 1), Included: j%2 == 0,
					Confidence: quota.ConfExact, At: fixedNow()})
				_ = c.Summary(time.Time{}, time.Time{})
				_ = c.CanAfford(Scope{ProviderType: ProviderCoding}, 1, true, false)
			}
		}(i)
	}
	wg.Wait()
	if c.Count() != 800 {
		t.Errorf("count = %d, want 800", c.Count())
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
