package router

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"neuroforge/internal/budget"
	"neuroforge/internal/quota"
	"neuroforge/internal/risk"
)

// Request is the input to route selection (spec §19.1 signals).
type Request struct {
	Complexity Complexity
	Risk       risk.Level
	Role       string
	Language   string

	// NeedsImages selects only image-capable models (§19.1 image input).
	NeedsImages bool
	// ContextTokens the model must hold (filters out too-small contexts).
	ContextTokens int

	ProjectID string
	TaskID    string

	// PreferStrong forces the upper tier when a band spans two (§19.3 C3/C4).
	PreferStrong bool

	// AllowIncludedOnly is set when a hard budget block permits only
	// subscription-included routes (§23). The router then restricts candidates
	// to SubscriptionIncluded entries.
	AllowIncludedOnly bool

	// DiversityPenalised lists (engine,account) pairs to deprioritise for
	// provider diversity (§19.1): e.g. the engines already used heavily.
	DiversityPenalised map[string]int // key: engine -> penalty
}

// Route is a chosen (engine, model, account, tier) plus the economic facts that
// explain the choice. Runtime is selected by the supervisor, not the router.
type Route struct {
	Entry CatalogEntry
	// EstimatedCostUSD is a deterministic estimate for the expected token
	// budget (input + output). The confidence of the underlying usage is
	// ESTIMATED by default (router estimates, provider not consulted) — never
	// shown as exact (rule §36.10).
	EstimatedCostUSD float64
	// QuotaConfidence echoes the account's quota confidence so the dashboard
	// never shows estimated usage as exact (AC-18).
	QuotaConfidence quota.Confidence
	Score           float64
	ScoreBreakdown  []ScoreFactor
}

// ScoreFactor is one contribution to a route's score (for §19.6 explanation).
type ScoreFactor struct {
	Name   string
	Delta  float64
	Detail string
}

// Alternative is a route that was considered but not selected.
type Alternative struct {
	Route  Route
	Reason string // why not chosen
}

// ExcludedRoute is a candidate removed before scoring.
type ExcludedRoute struct {
	Entry  CatalogEntry
	Reason string
}

// Explanation is the fully explainable route decision (spec §19.6). It carries
// the selected route, the target tier, ranked alternatives, excluded candidates
// with reasons, the quota summary and the budget decision that applied.
type Explanation struct {
	Selected       Route
	TargetTier     Tier
	Complexity     Complexity
	Risk           risk.Level
	Alternatives   []Alternative
	Excluded       []ExcludedRoute
	QuotaSummary   []quota.Snapshot
	BudgetDecision budget.Decision
	FallbackChain  []Route // ordered fallback routes (§21.1)
	Notes          []string
}

// Router selects routes deterministically. It holds no mutable runtime state of
// its own; it consults the catalog, quota manager and budget controller.
type Router struct {
	catalog *Catalog
	quota   *quota.Manager
	budget  *budget.Controller
	cfg     ScoreConfig
}

// ScoreConfig tunes the deterministic scorer. Zero values use DefaultScoreConfig.
type ScoreConfig struct {
	// ExpectedInputTokens / ExpectedOutputTokens drive the cost estimate.
	ExpectedInputTokens  int
	ExpectedOutputTokens int
	// CheaperIsBetter weight applied to (normalised) cost.
	CostWeight float64
	// IncludedBonus rewards subscription-included routes (no marginal cost).
	IncludedBonus float64
	// TierMatchBonus rewards the exact target tier; TierDistancePenalty per
	// step of distance (cheaper or stronger) away from target.
	TierMatchBonus      float64
	TierDistancePenalty float64
	// RiskFloorTier: for risk R3/R4, never pick below this tier even if
	// complexity is low (spec §26 risk influences model tier).
	RiskFloorTier Tier
	// DiversityPenalty applied per the request's DiversityPenalised map.
	DiversityPenalty float64
}

// DefaultScoreConfig returns sane scoring weights.
func DefaultScoreConfig() ScoreConfig {
	return ScoreConfig{
		ExpectedInputTokens:  20_000,
		ExpectedOutputTokens: 4_000,
		CostWeight:           1.0,
		IncludedBonus:        0.5,
		TierMatchBonus:       1.0,
		TierDistancePenalty:  0.6,
		RiskFloorTier:        TierStandard,
		DiversityPenalty:     0.25,
	}
}

// New constructs a Router. nil quota/budget degrade to "always available / no
// limit" so the router stays usable in tests and offline demos.
func New(catalog *Catalog, qm *quota.Manager, bc *budget.Controller, cfg ScoreConfig) *Router {
	if catalog == nil {
		catalog = NewCatalog()
	}
	if (ScoreConfig{} == cfg) {
		cfg = DefaultScoreConfig()
	}
	if cfg.ExpectedInputTokens <= 0 {
		cfg.ExpectedInputTokens = 20_000
	}
	if cfg.ExpectedOutputTokens <= 0 {
		cfg.ExpectedOutputTokens = 4_000
	}
	return &Router{catalog: catalog, quota: qm, budget: bc, cfg: cfg}
}

// Catalog returns the router's catalog.
func (r *Router) Catalog() *Catalog { return r.catalog }

// EstimateCost computes a deterministic cost estimate for an entry at the
// configured expected token budget. Unknown prices (zero) yield 0.
func (r *Router) EstimateCost(e CatalogEntry) float64 {
	in := float64(r.cfg.ExpectedInputTokens) / 1_000_000 * e.CostPer1MInputUSD
	out := float64(r.cfg.ExpectedOutputTokens) / 1_000_000 * e.CostPer1MOutputUSD
	if e.CostPer1MInputUSD <= 0 && e.CostPer1MOutputUSD <= 0 {
		return 0
	}
	return in + out
}

// Route performs deterministic route selection (spec §19). It NEVER calls an
// LLM (rule §22.6) and returns a fully explainable decision (§19.6).
func (r *Router) Route(ctx context.Context, req Request) (Explanation, error) {
	if err := validateRequest(req); err != nil {
		return Explanation{}, err
	}

	target := BaseTier(req.Complexity, req.PreferStrong)
	// §26: risk influences model tier — high risk floors the tier.
	if req.Risk >= risk.R3 && r.cfg.RiskFloorTier > target {
		target = r.cfg.RiskFloorTier
	}

	// Collect candidate tiers: target plus neighbours (for fallback). We score
	// the target tier's entries first; if none survive, escalate then
	// de-escalate (§19.4/§19.5) to assemble a fallback chain.
	ex := Explanation{
		TargetTier: target,
		Complexity: req.Complexity,
		Risk:       req.Risk,
	}

	// Budget pre-check: is a paid run of the expected size allowed?
	estCost := r.estimateForTier(target)
	bc := r.budget
	var bdec budget.Decision
	if bc != nil {
		bdec = bc.CanAfford(budget.Scope{
			ProjectID: req.ProjectID, TaskID: req.TaskID, TaskRisk: req.Risk,
			ProviderType: budget.ProviderCoding,
		}, budget.USD(estCost), true, true)
	}
	ex.BudgetDecision = bdec

	// Hard block: restrict to subscription-included routes only (§23).
	includedOnly := req.AllowIncludedOnly
	if bdec.HardBlocked {
		includedOnly = true
		ex.Notes = append(ex.Notes, "hard budget exhausted — restricting to subscription-included routes (§23)")
	}

	// Assemble ordered tiers to try: target, then stronger (escalation), then
	// cheaper (de-escalation), per §19.4/§19.5.
	tryTiers := orderedFallbackTiers(target)

	// Score every eligible entry across all tiers once; this populates the
	// alternatives, the excluded list (with reasons) and the selected route.
	var excluded []ExcludedRoute
	alts := r.rankedAlternatives(req, target, includedOnly, &excluded)

	var selected *Route
	selectedIdx := -1
	// The selected route is the first alternative whose tier the router would
	// actually prefer (target tier first, then escalation, then de-escalation).
	for _, t := range tryTiers {
		for i := range alts {
			if alts[i].Route.Entry.Tier == t {
				s := alts[i].Route
				selected = &s
				selectedIdx = i
				break
			}
		}
		if selected != nil {
			break
		}
	}

	// Build the §21.1 fallback chain: per-tier best, in try-order.
	var chain []Route
	seenTier := map[Tier]bool{}
	for _, t := range tryTiers {
		for _, a := range alts {
			if a.Route.Entry.Tier == t && !seenTier[t] {
				chain = append(chain, a.Route)
				seenTier[t] = true
				break
			}
		}
	}

	ex.Alternatives = alts
	if selected != nil {
		ex.Selected = *selected
		// The selected route's "reason not chosen" is empty by definition.
		if selectedIdx >= 0 && selectedIdx < len(ex.Alternatives) {
			ex.Alternatives[selectedIdx].Reason = ""
		}
	} else {
		// No route survived. Produce a fully-explained failure.
		ex.Notes = append(ex.Notes, "no eligible route after quota/budget/capability filters")
	}
	ex.Excluded = excluded
	ex.FallbackChain = chain

	if r.quota != nil {
		ex.QuotaSummary = r.quota.All()
	}
	return ex, nil
}

// rankedAlternatives scores every eligible entry across fallback tiers and
// returns them best-first with a reason relative to the selected route.
func (r *Router) rankedAlternatives(req Request, target Tier, includedOnly bool, excluded *[]ExcludedRoute) []Alternative {
	type scored struct {
		rt   Route
		tier Tier
	}
	var all []scored
	for _, t := range Tiers() {
		for _, e := range r.catalog.ByTier(t) {
			if reason := r.excludeReason(e, req, includedOnly); reason != "" {
				*excluded = append(*excluded, ExcludedRoute{Entry: e, Reason: reason})
				continue
			}
			all = append(all, scored{rt: r.scoreEntry(e, t, target, req), tier: t})
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		return all[i].rt.Score > all[j].rt.Score
	})
	out := make([]Alternative, 0, len(all))
	for i, s := range all {
		// Always populate a reason; Route clears the selected route's reason.
		reason := altReason(s.rt, s.tier, target, all[0].rt)
		if reason == "" {
			reason = "not the highest-scoring candidate"
		}
		_ = i
		out = append(out, Alternative{Route: s.rt, Reason: reason})
	}
	return out
}

func altReason(rt Route, tier, target Tier, best Route) string {
	var parts []string
	if rt.Score < best.Score {
		parts = append(parts, fmt.Sprintf("lower score %.2f < %.2f", rt.Score, best.Score))
	}
	if tier != target {
		if tier < target {
			parts = append(parts, fmt.Sprintf("tier %s below target %s", tier, target))
		} else {
			parts = append(parts, fmt.Sprintf("tier %s above target %s (more expensive)", tier, target))
		}
	}
	if rt.Entry.SubscriptionIncluded {
		parts = append(parts, "subscription-included (no marginal cost)")
	}
	if len(parts) == 0 {
		return "not the highest-scoring candidate"
	}
	return strings.Join(parts, "; ")
}

// excludeReason returns "" when the entry is eligible, else a human-readable
// reason. Exhausted accounts are excluded here (§20.3).
func (r *Router) excludeReason(e CatalogEntry, req Request, includedOnly bool) string {
	if e.Disabled {
		return "entry disabled in catalog"
	}
	if req.NeedsImages && !e.SupportsImages {
		return "model lacks image input support"
	}
	if req.ContextTokens > 0 && e.ContextWindow > 0 && req.ContextTokens > e.ContextWindow {
		return fmt.Sprintf("context window %d < required %d", e.ContextWindow, req.ContextTokens)
	}
	if includedOnly && !e.SubscriptionIncluded {
		return "hard budget block — only subscription-included routes permitted (§23)"
	}
	if r.quota != nil && !r.quota.IsAvailable(e.AccountID()) {
		why := r.quota.WhyUnavailable(e.AccountID())
		if why == "" {
			why = "account unavailable"
		}
		return why
	}
	return ""
}

// scoreEntry computes the deterministic score for one eligible entry.
func (r *Router) scoreEntry(e CatalogEntry, tier, target Tier, req Request) Route {
	rt := Route{Entry: e, EstimatedCostUSD: r.EstimateCost(e), QuotaConfidence: quota.ConfEstimated}
	if r.quota != nil {
		rt.QuotaConfidence = r.quota.Snapshot(e.AccountID()).Confidence
	}
	var factors []ScoreFactor

	// Tier match: prefer the exact target tier; penalise distance either way.
	dist := tier - target
	if dist < 0 {
		dist = -dist
	}
	tierBonus := r.cfg.TierMatchBonus - float64(dist)*r.cfg.TierDistancePenalty
	factors = append(factors, ScoreFactor{
		Name: "tier-match", Delta: tierBonus,
		Detail: fmt.Sprintf("tier %s vs target %s (distance %d)", tier, target, dist),
	})

	// Cost: cheaper is better (normalised against a soft cap). Unknown cost is
	// treated neutrally so free/fake models are not unfairly penalised.
	costDelta := 0.0
	if rt.EstimatedCostUSD > 0 {
		cap := 10.0 // $10 expected -> full penalty
		norm := rt.EstimatedCostUSD / cap
		if norm > 1 {
			norm = 1
		}
		costDelta = -r.cfg.CostWeight * norm
		factors = append(factors, ScoreFactor{Name: "cost", Delta: costDelta,
			Detail: fmt.Sprintf("estimated $%.4f", rt.EstimatedCostUSD)})
	}

	// Subscription-included routes get a bonus (no marginal cost, §23).
	if e.SubscriptionIncluded {
		factors = append(factors, ScoreFactor{Name: "included", Delta: r.cfg.IncludedBonus,
			Detail: "subscription-included route"})
	}

	// Priority bias from the catalog.
	if e.Priority != 0 {
		factors = append(factors, ScoreFactor{Name: "priority", Delta: float64(e.Priority) * 0.1,
			Detail: fmt.Sprintf("catalog priority %d", e.Priority)})
	}

	// Provider diversity penalty (§19.1).
	if pen, ok := req.DiversityPenalised[e.Engine]; ok && pen > 0 {
		d := -r.cfg.DiversityPenalty * float64(pen)
		factors = append(factors, ScoreFactor{Name: "diversity", Delta: d,
			Detail: fmt.Sprintf("engine %s over-used (penalty %d)", e.Engine, pen)})
	}

	total := 0.0
	for _, f := range factors {
		total += f.Delta
	}
	rt.Score = total
	rt.ScoreBreakdown = factors
	return rt
}

func (r *Router) estimateForTier(t Tier) float64 {
	var best float64
	for _, e := range r.catalog.ByTier(t) {
		if c := r.EstimateCost(e); c > best {
			best = c
		}
	}
	return best
}

// orderedFallbackTiers returns the target tier first, then stronger tiers
// (escalation, §19.4), then cheaper tiers (de-escalation, §19.5). Used to build
// the §21.1 fallback chain.
func orderedFallbackTiers(target Tier) []Tier {
	out := []Tier{target}
	for t := target + 1; t <= maxTier; t++ {
		out = append(out, t)
	}
	for t := target - 1; t >= TierTiny; t-- {
		out = append(out, t)
	}
	return out
}

func validateRequest(req Request) error {
	if !req.Complexity.IsValid() {
		return fmt.Errorf("router: complexity %d is not a valid band", req.Complexity)
	}
	if !req.Risk.IsValid() {
		return fmt.Errorf("router: risk %d is not a valid band", req.Risk)
	}
	return nil
}
