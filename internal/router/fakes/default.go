// Package fakes provides deterministic, network-free fixtures for routing,
// quota and budget tests (spec §33.1, rule §36.5: no real paid models in CI).
//
// It builds a small model catalog and account registry with fake (clearly
// non-real) model names so the router core never depends on real provider model
// identifiers (rule §36.8). It also offers a fake quota/budget harness that
// scripts deterministic snapshots and usage records.
package fakes

import (
	"neuroforge/internal/budget"
	"neuroforge/internal/quota"
	"neuroforge/internal/router"
)

// DefaultCatalog builds a small, deterministic catalog using obviously-fake
// model identifiers (no real provider names — rule §36.8). Two engines, three
// tiers, two accounts (one subscription-included, one paid API).
func DefaultCatalog() *router.Catalog {
	c := router.NewCatalog()
	entries := []router.CatalogEntry{
		// Engine "alpha" — subscription-included account "alpha-sub" + paid "alpha-api".
		{Tier: router.TierTiny, Engine: "alpha", Model: "alpha-mini", Account: "alpha-sub",
			CostPer1MInputUSD: 0, CostPer1MOutputUSD: 0, ContextWindow: 32_000, MaxOutput: 4_000,
			SubscriptionIncluded: true, Priority: 2},
		{Tier: router.TierSmall, Engine: "alpha", Model: "alpha-lite", Account: "alpha-sub",
			CostPer1MInputUSD: 0, CostPer1MOutputUSD: 0, ContextWindow: 64_000, MaxOutput: 8_000,
			SubscriptionIncluded: true, Priority: 2},
		{Tier: router.TierStandard, Engine: "alpha", Model: "alpha-pro", Account: "alpha-api",
			CostPer1MInputUSD: 3, CostPer1MOutputUSD: 12, ContextWindow: 128_000, MaxOutput: 16_000,
			Priority: 3},
		{Tier: router.TierHeavy, Engine: "alpha", Model: "alpha-max", Account: "alpha-api",
			CostPer1MInputUSD: 8, CostPer1MOutputUSD: 30, ContextWindow: 200_000, MaxOutput: 24_000,
			SupportsImages: true, Priority: 3},

		// Engine "beta" — paid API only, multimodal at the top.
		{Tier: router.TierTiny, Engine: "beta", Model: "beta-spark", Account: "beta-api",
			CostPer1MInputUSD: 0.5, CostPer1MOutputUSD: 2, ContextWindow: 16_000, MaxOutput: 2_000,
			Priority: 1},
		{Tier: router.TierStandard, Engine: "beta", Model: "beta-core", Account: "beta-api",
			CostPer1MInputUSD: 4, CostPer1MOutputUSD: 15, ContextWindow: 128_000, MaxOutput: 16_000,
			Priority: 2},
		{Tier: router.TierFrontier, Engine: "beta", Model: "beta-frontier", Account: "beta-api",
			CostPer1MInputUSD: 12, CostPer1MOutputUSD: 45, ContextWindow: 256_000, MaxOutput: 32_000,
			SupportsImages: true, Priority: 4},
	}
	for _, e := range entries {
		c.Add(e)
	}
	return c
}

// DefaultQuotaManager seeds a quota manager with sane "available" snapshots for
// every catalog account (exact confidence, plenty remaining).
func DefaultQuotaManager(catalog *router.Catalog) *quota.Manager {
	m := quota.New(quota.DefaultConfig())
	for _, acc := range catalog.Accounts() {
		rem := 1_000_000.0
		lim := 1_000_000.0
		m.Apply(quota.Snapshot{
			Account:    acc,
			Confidence: quota.ConfExact,
			State:      quota.StateAvailable,
			Remaining:  &rem,
			Limit:      &lim,
		})
	}
	return m
}

// DefaultBudgetController returns a budget controller with example limits
// (spec §23 example block, scaled for tests).
func DefaultBudgetController() *budget.Controller {
	return budget.New(budget.Limits{
		GlobalDailyUSD:   20,
		GlobalMonthlyUSD: 300,
		ProjectDailyUSD:  map[string]budget.USD{"work-app": 5},
		ImageDailyUSD:    3,
		ImageMaxVariants: 4,
		SoftFraction:     0.8,
	}, nil)
}

// DefaultRouter wires the default catalog, quota manager and budget controller
// into a ready-to-use Router. Used by CLI/TUI demos and tests.
func DefaultRouter() *router.Router {
	c := DefaultCatalog()
	return router.New(c, DefaultQuotaManager(c), DefaultBudgetController(), router.DefaultScoreConfig())
}
