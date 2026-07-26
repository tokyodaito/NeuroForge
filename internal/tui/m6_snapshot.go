package tui

import (
	"context"
	"time"

	"neuroforge/internal/budget"
	"neuroforge/internal/quota"
	"neuroforge/internal/risk"
	"neuroforge/internal/router"
	"neuroforge/internal/router/fakes"
)

// m6Router returns the default in-process router (fake catalog/quotas/budget).
func m6Router() *router.Router { return fakes.DefaultRouter() }

// m6UsageSnapshot builds a deterministic demo usage summary so the Usage screen
// is demonstrable without a live run (rule §36.5). It records both
// subscription-included and paid API usage so the separation is visible.
//
// Timestamps are clamped into [startOfDayUTC(now), now] so the day window never
// goes empty near UTC midnight (fixed offsets of -1h/-2h would otherwise fall
// on the previous UTC day and drop CoarsestConf to UNKNOWN).
func m6UsageSnapshot() *budget.AggregatedSummary {
	bc := fakes.DefaultBudgetController()
	now := time.Now().UTC()
	day := startOfDayUTC(now)
	records := []budget.UsageRecord{
		{Engine: "alpha", Model: "alpha-pro", Tier: "STANDARD",
			Account:     quota.AccountID{Engine: "alpha", Account: "alpha-api"},
			InputTokens: 1_400_000, CachedTokens: 980_000, OutputTokens: 141_000,
			CostUSD: 6.81, Included: false, Confidence: quota.ConfEstimated,
			ProviderType: budget.ProviderCoding, At: clampToDayWindow(day, now, 2*time.Hour)},
		{Engine: "alpha", Model: "alpha-lite", Tier: "SMALL",
			Account:     quota.AccountID{Engine: "alpha", Account: "alpha-sub"},
			InputTokens: 320_000, OutputTokens: 22_000,
			CostUSD: 0, Included: true, Confidence: quota.ConfProviderReported,
			ProviderType: budget.ProviderCoding, At: clampToDayWindow(day, now, 1*time.Hour)},
		{Engine: "beta", Model: "beta-frontier", Tier: "FRONTIER",
			Account:     quota.AccountID{Engine: "beta", Account: "beta-api"},
			InputTokens: 90_000, OutputTokens: 8_000,
			CostUSD: 1.20, Included: false, Confidence: quota.ConfEstimated,
			ProviderType: budget.ProviderCoding, At: clampToDayWindow(day, now, 30*time.Minute)},
	}
	for _, r := range records {
		bc.Record(r)
	}
	s := bc.Summary(day, now)
	return &s
}

func clampToDayWindow(day, now time.Time, behind time.Duration) time.Time {
	t := now.Add(-behind)
	if t.Before(day) {
		return day
	}
	return t
}

// m6QuotaSnapshot returns the per-account quota rows for the dashboard.
func m6QuotaSnapshot() []quota.Snapshot {
	r := m6Router()
	mgr := fakes.DefaultQuotaManager(r.Catalog())
	var rows []quota.Snapshot
	for _, acc := range r.Catalog().Accounts() {
		rows = append(rows, mgr.Snapshot(acc))
	}
	return rows
}

// m6RouteSnapshot returns a route explanation for a representative task so the
// Route Decision screen is demonstrable (spec §19.6).
func m6RouteSnapshot() *router.Explanation {
	r := m6Router()
	ex, err := r.Route(context.Background(), router.Request{
		Complexity: router.ClassifyComplexity(router.ComplexitySignals{
			Description:        "add analytics dashboard and a public webhook integration",
			EstimatedFileCount: 9,
			EstimatedTurns:     18,
		}).Complexity,
		Risk: risk.Classify(risk.Signals{
			Description:     "add analytics dashboard and a public webhook integration",
			PublicAPIChange: true,
		}).Level,
	})
	if err != nil {
		return &router.Explanation{}
	}
	return &ex
}

func startOfDayUTC(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
