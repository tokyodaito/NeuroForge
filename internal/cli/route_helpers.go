package cli

import (
	"neuroforge/internal/quota"
	"neuroforge/internal/risk"
	"neuroforge/internal/router"
)

// These helpers wire the router/risk/complexity classifiers to the CLI without
// pulling them into the main route_cmd.go file. They stay in the cli package so
// the command surface remains thin.

func routerClassify(description string, files, turns int) router.Complexity {
	return router.ClassifyComplexity(router.ComplexitySignals{
		Description:        description,
		EstimatedFileCount: files,
		EstimatedTurns:     turns,
	}).Complexity
}

func routerRisk(description string) risk.Level {
	return risk.Classify(risk.Signals{Description: description}).Level
}

func routeRequest(c router.Complexity, r risk.Level, desc string, files, turns int, images, strong bool) router.Request {
	return router.Request{
		Complexity:   c,
		Risk:         r,
		Role:         desc,
		NeedsImages:  images,
		PreferStrong: strong,
	}
}

func parseComplexity(s string) (router.Complexity, bool) {
	for _, c := range router.Complexities() {
		if c.String() == s {
			return c, true
		}
	}
	return router.C2, false
}

func parseRisk(s string) (risk.Level, bool) {
	for _, r := range risk.Levels() {
		if r.String() == s {
			return r, true
		}
	}
	return risk.R0, false
}

// ---- JSON envelope ----

type routeExplainJSON struct {
	Complexity      string         `json:"complexity"`
	Risk            string         `json:"risk"`
	TargetTier      string         `json:"target_tier"`
	Selected        routeJSON      `json:"selected"`
	Alternatives    []routeJSON    `json:"alternatives"`
	Excluded        []excludedJSON `json:"excluded"`
	FallbackChain   []routeJSON    `json:"fallback_chain"`
	BudgetHardBlock bool           `json:"budget_hard_block"`
	BudgetSoft      bool           `json:"budget_soft_signal"`
	BudgetReason    string         `json:"budget_reason"`
	Quota           []quotaJSON    `json:"quota"`
	Derived         derivedJSON    `json:"derived"`
}

type derivedJSON struct {
	FromClassification string `json:"from_classification"`
	FromRisk           string `json:"from_risk"`
}

type routeJSON struct {
	Tier            string            `json:"tier"`
	Engine          string            `json:"engine"`
	Model           string            `json:"model"`
	Account         string            `json:"account"`
	EstimatedCost   float64           `json:"estimated_cost_usd"`
	QuotaConfidence string            `json:"quota_confidence"`
	Score           float64           `json:"score"`
	Included        bool              `json:"subscription_included"`
	ScoreBreakdown  []scoreFactorJSON `json:"score_breakdown,omitempty"`
	Reason          string            `json:"reason,omitempty"`
}

type scoreFactorJSON struct {
	Name   string  `json:"name"`
	Delta  float64 `json:"delta"`
	Detail string  `json:"detail"`
}

type excludedJSON struct {
	Tier    string `json:"tier"`
	Engine  string `json:"engine"`
	Model   string `json:"model"`
	Account string `json:"account"`
	Reason  string `json:"reason"`
}

type quotaJSON struct {
	Engine     string `json:"engine"`
	Account    string `json:"account"`
	State      string `json:"state"`
	Confidence string `json:"confidence"`
	Remaining  string `json:"remaining"`
	Limit      string `json:"limit"`
}

func explanationJSON(ex router.Explanation, cRes router.Complexity, rRes risk.Level, cOverridden, rOverridden bool) routeExplainJSON {
	complexityLabel := cRes.String()
	riskLabel := rRes.String()
	if cOverridden {
		complexityLabel = ex.Complexity.String() + " (overridden)"
	}
	if rOverridden {
		riskLabel = ex.Risk.String() + " (overridden)"
	}
	out := routeExplainJSON{
		Complexity:      ex.Complexity.String(),
		Risk:            ex.Risk.String(),
		TargetTier:      ex.TargetTier.String(),
		Selected:        toRouteJSON(ex.Selected, ""),
		BudgetHardBlock: ex.BudgetDecision.HardBlocked,
		BudgetSoft:      ex.BudgetDecision.SoftSignal,
		BudgetReason:    ex.BudgetDecision.Reason,
		Derived: derivedJSON{
			FromClassification: complexityLabel,
			FromRisk:           riskLabel,
		},
	}
	for _, a := range ex.Alternatives {
		out.Alternatives = append(out.Alternatives, toRouteJSON(a.Route, a.Reason))
	}
	for _, x := range ex.Excluded {
		out.Excluded = append(out.Excluded, excludedJSON{
			Tier: x.Entry.Tier.String(), Engine: x.Entry.Engine, Model: x.Entry.Model,
			Account: x.Entry.Account, Reason: x.Reason,
		})
	}
	for _, r := range ex.FallbackChain {
		out.FallbackChain = append(out.FallbackChain, toRouteJSON(r, ""))
	}
	for _, q := range ex.QuotaSummary {
		out.Quota = append(out.Quota, quotaJSON{
			Engine: q.Account.Engine, Account: q.Account.Account,
			State: string(q.State), Confidence: string(q.Confidence),
			Remaining: quota.FormatRemaining(q), Limit: quota.FormatLimit(q),
		})
	}
	return out
}

func toRouteJSON(r router.Route, reason string) routeJSON {
	j := routeJSON{
		Tier: r.Entry.Tier.String(), Engine: r.Entry.Engine, Model: r.Entry.Model,
		Account: r.Entry.Account, EstimatedCost: r.EstimatedCostUSD,
		QuotaConfidence: string(r.QuotaConfidence), Score: r.Score,
		Included: r.Entry.SubscriptionIncluded, Reason: reason,
	}
	for _, f := range r.ScoreBreakdown {
		j.ScoreBreakdown = append(j.ScoreBreakdown, scoreFactorJSON{Name: f.Name, Delta: f.Delta, Detail: f.Detail})
	}
	return j
}

// routerRender delegates to the router's explanation renderer.
func routerRender(ex router.Explanation) string {
	return router.RenderExplanation(ex)
}
