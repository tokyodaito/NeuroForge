package tui

import (
	"fmt"
	"strings"

	"neuroforge/internal/budget"
	"neuroforge/internal/quota"
)

// usageView renders the Usage screen (spec §6.1, §14.4, AC-18). Subscription-
// included usage and paid API cost are shown on separate lines; totals carry a
// confidence tag so estimated values are never displayed as exact.
func usageView(m Model) string {
	var b strings.Builder
	b.WriteString(bold("USAGE") + "\n\n")
	if m.UsageSummary == nil {
		b.WriteString(dim("  (no usage data)") + "\n")
		return b.String()
	}
	s := m.UsageSummary
	b.WriteString(fmt.Sprintf("Coding input       %s\n", humanTok(s.InputTokens)))
	b.WriteString(fmt.Sprintf("Cached input       %s\n", humanTok(s.CachedTokens)))
	b.WriteString(fmt.Sprintf("Coding output      %s\n", humanTok(s.OutputTokens)))
	b.WriteString(fmt.Sprintf("Image generations  %d\n", s.ImageGens))
	b.WriteString(fmt.Sprintf("Included cost      %s  %s\n", moneyUSD(s.IncludedCost),
		dim("(subscription-covered, no marginal cost)")))
	b.WriteString(fmt.Sprintf("Paid API cost      %s\n", moneyUSD(s.PaidCost)))
	b.WriteString(fmt.Sprintf("Estimated total    %s  %s\n", moneyUSD(s.TotalCost),
		quota.ConfidenceTag(s.CoarsestConf)))
	if len(s.PerEngine) > 0 {
		b.WriteString("\n" + bold("BY ENGINE") + "\n")
		for eng, pc := range s.PerEngine {
			b.WriteString(fmt.Sprintf("  %-12s included %s  paid %s  in %s  out %s\n",
				eng, moneyUSD(pc.IncludedCost), moneyUSD(pc.PaidCost),
				humanTok(pc.InputTokens), humanTok(pc.OutputTokens)))
		}
	}
	b.WriteString("\n" + dim("Estimated/inferred totals are tagged; never shown as exact (AC-18).") + "\n")
	return b.String()
}

// quotasView renders the Quotas screen (spec §6.1, §20). Each account's state,
// remaining quota and confidence are shown; rate-limit and exhaustion are
// rendered as distinct states.
func quotasView(m Model) string {
	var b strings.Builder
	b.WriteString(bold("PROVIDER QUOTAS") + "\n\n")
	if len(m.QuotaRows) == 0 {
		b.WriteString(dim("  No provider accounts configured.") + "\n")
		return b.String()
	}
	b.WriteString(fmt.Sprintf("%-14s %-14s %-14s %-14s %-12s\n", "ENGINE", "ACCOUNT", "STATE", "REMAINING", "CONFIDENCE"))
	b.WriteString(dim(strings.Repeat("-", 74)) + "\n")
	for _, s := range m.QuotaRows {
		b.WriteString(fmt.Sprintf("%-14s %-14s %s%-14s%s %-14s %-12s\n",
			s.Account.Engine, s.Account.Account, quotaStateColor(string(s.State)),
			s.State, "\x1b[0m",
			quota.FormatRemaining(s), quota.ConfidenceTag(s.Confidence)))
	}
	b.WriteString("\n" + dim("Remaining values: EXACT/PROVIDER_REPORTED shown plainly; ESTIMATED/INFERRED prefixed with '~'.") + "\n")
	return b.String()
}

// routeDecisionView renders the Route Decision screen (spec §19.6): selected
// route, alternatives, fallback chain, quota and exclusion reasons.
func routeDecisionView(m Model) string {
	var b strings.Builder
	b.WriteString(bold("ROUTE DECISION") + "\n\n")
	if m.RouteDecision == nil {
		b.WriteString(dim("  (no route decision available)") + "\n")
		return b.String()
	}
	ex := m.RouteDecision
	b.WriteString(fmt.Sprintf("complexity: %s   risk: %s   target tier: %s\n",
		ex.Complexity, ex.Risk, ex.TargetTier))
	if ex.Selected.Entry.Engine != "" {
		e := ex.Selected.Entry
		b.WriteString(fmt.Sprintf("selected:   %s / %s / %s  [%s]\n", e.Engine, e.Model, e.Account, e.Tier))
		b.WriteString(fmt.Sprintf("est. cost:  $%.4f  %s\n", ex.Selected.EstimatedCostUSD,
			quota.ConfidenceTag(ex.Selected.QuotaConfidence)))
	}
	if ex.BudgetDecision.Reason != "" {
		tag := ""
		if ex.BudgetDecision.HardBlocked {
			tag = warn(" [HARD BLOCK]")
		} else if ex.BudgetDecision.SoftSignal {
			tag = warn(" [SOFT]")
		}
		b.WriteString("budget:     " + ex.BudgetDecision.Reason + tag + "\n")
	}
	if len(ex.FallbackChain) > 1 {
		b.WriteString("\n" + bold("FALLBACK CHAIN") + "\n")
		for i, r := range ex.FallbackChain {
			label := "primary"
			if i > 0 {
				label = fmt.Sprintf("fallback #%d", i)
			}
			e := r.Entry
			b.WriteString(fmt.Sprintf("  %-12s %s / %s / %s  [%s]\n", label, e.Engine, e.Model, e.Account, e.Tier))
		}
	}
	if len(ex.Alternatives) > 0 {
		b.WriteString("\n" + bold("ALTERNATIVES") + "\n")
		for _, a := range ex.Alternatives {
			e := a.Route.Entry
			marker := "  "
			if e.ID() == ex.Selected.Entry.ID() {
				marker = ok("> ")
			}
			reason := a.Reason
			if reason == "" {
				reason = dim("selected")
			}
			b.WriteString(fmt.Sprintf("%s%-10s %s / %s / %s  score %.2f  — %s\n",
				marker, e.Tier, e.Engine, e.Model, e.Account, a.Route.Score, reason))
		}
	}
	return b.String()
}

// ---- formatting helpers shared by the M6 views ----

func humanTok(n int) string {
	switch {
	case n >= 1_000_000:
		return trimNum(fmt.Sprintf("%.2f", float64(n)/1_000_000)) + "M"
	case n >= 1_000:
		return trimNum(fmt.Sprintf("%.2f", float64(n)/1_000)) + "k"
	}
	return fmt.Sprintf("%d", n)
}

func moneyUSD(v budget.USD) string { return fmt.Sprintf("$%.2f", float64(v)) }

// quotaStateColor maps a quota state to an ANSI colour for the dashboard.
func quotaStateColor(s string) string {
	switch s {
	case "AVAILABLE":
		return "\x1b[32m" // green
	case "LOW", "DEGRADED":
		return "\x1b[33m" // yellow
	case "RATE_LIMITED":
		return "\x1b[35m" // magenta
	case "EXHAUSTED", "AUTH_REQUIRED":
		return "\x1b[31m" // red
	}
	return "\x1b[2m" // dim
}

func trimNum(s string) string {
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
	}
	return s
}
