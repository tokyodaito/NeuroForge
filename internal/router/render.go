package router

import (
	"fmt"
	"strings"

	"neuroforge/internal/quota"
)

// RenderExplanation produces a human-readable, fully-explainable route decision
// (spec §19.6): selected route, target tier, alternatives, estimated cost,
// quota, and the reasons other routes were excluded.
func RenderExplanation(ex Explanation) string {
	var b strings.Builder
	b.WriteString("ROUTE DECISION\n")
	b.WriteString(fmt.Sprintf("  complexity:  %s   risk: %s   target tier: %s\n",
		ex.Complexity, ex.Risk, ex.TargetTier))
	if ex.Selected.Entry.Engine != "" {
		e := ex.Selected.Entry
		b.WriteString(fmt.Sprintf("  selected:    %s / %s / %s  [%s]\n",
			e.Engine, e.Model, e.Account, e.Tier))
		b.WriteString(fmt.Sprintf("  est. cost:   $%.4f  (%s)\n",
			ex.Selected.EstimatedCostUSD, quota.ConfidenceTag(ex.Selected.QuotaConfidence)))
		b.WriteString("  score:       " + renderBreakdown(ex.Selected.ScoreBreakdown) + "\n")
	} else {
		b.WriteString("  selected:    (none)\n")
	}
	for _, n := range ex.Notes {
		b.WriteString("  note:        " + n + "\n")
	}
	if ex.BudgetDecision.Reason != "" {
		tag := ""
		if ex.BudgetDecision.HardBlocked {
			tag = " [HARD BLOCK]"
		} else if ex.BudgetDecision.SoftSignal {
			tag = " [SOFT]"
		}
		b.WriteString(fmt.Sprintf("  budget:      %s%s\n", ex.BudgetDecision.Reason, tag))
	}

	if len(ex.FallbackChain) > 1 {
		b.WriteString("\nFALLBACK CHAIN (§21.1)\n")
		for i, r := range ex.FallbackChain {
			label := "primary"
			if i > 0 {
				label = fmt.Sprintf("fallback #%d", i)
			}
			e := r.Entry
			b.WriteString(fmt.Sprintf("  %-12s %s / %s / %s  [%s]  $%.4f\n",
				label, e.Engine, e.Model, e.Account, e.Tier, r.EstimatedCostUSD))
		}
	}

	if len(ex.Alternatives) > 0 {
		b.WriteString("\nALTERNATIVES (ranked)\n")
		for _, a := range ex.Alternatives {
			e := a.Route.Entry
			marker := "  "
			if e.Engine == ex.Selected.Entry.Engine && e.Model == ex.Selected.Entry.Model && e.Account == ex.Selected.Entry.Account {
				marker = "> "
			}
			reason := a.Reason
			if reason == "" {
				reason = "selected"
			}
			b.WriteString(fmt.Sprintf("%s%-10s %s / %s / %s  score %.2f  $%.4f  — %s\n",
				marker, e.Tier, e.Engine, e.Model, e.Account, a.Route.Score, a.Route.EstimatedCostUSD, reason))
		}
	}

	if len(ex.Excluded) > 0 {
		b.WriteString("\nEXCLUDED\n")
		for _, x := range ex.Excluded {
			e := x.Entry
			b.WriteString(fmt.Sprintf("  %-10s %s / %s / %s  — %s\n",
				e.Tier, e.Engine, e.Model, e.Account, x.Reason))
		}
	}

	if len(ex.QuotaSummary) > 0 {
		b.WriteString("\nQUOTA\n")
		for _, s := range ex.QuotaSummary {
			b.WriteString(fmt.Sprintf("  %-10s %s  %s  remaining %s / %s\n",
				s.Account, s.State, quota.ConfidenceTag(s.Confidence),
				quota.FormatRemaining(s), quota.FormatLimit(s)))
		}
	}
	return b.String()
}

func renderBreakdown(factors []ScoreFactor) string {
	var parts []string
	for _, f := range factors {
		parts = append(parts, fmt.Sprintf("%s=%+.2f", f.Name, f.Delta))
	}
	if len(parts) == 0 {
		return "0.00"
	}
	total := 0.0
	for _, f := range factors {
		total += f.Delta
	}
	return fmt.Sprintf("%.2f (%s)", total, strings.Join(parts, " "))
}
