package budget

import (
	"sort"
	"sync"
	"time"

	"neuroforge/internal/quota"
	"neuroforge/internal/risk"
)

// USD is a monetary amount in US dollars. Floats are adequate for the
// deterministic arithmetic here (no rounding beyond cents is performed; rule
// §22.6 forbids LLM-based arithmetic, not floats).
type USD float64

// Limits describes the spending limits across scopes (spec §23). Fields with a
// zero value are unset (no limit for that scope). Hard limits forbid new paid
// runs; soft limits trigger a cheaper route / fewer variants.
type Limits struct {
	GlobalDailyUSD   USD
	GlobalMonthlyUSD USD
	ProjectDailyUSD  map[string]USD
	TaskDefaultUSD   map[risk.Level]USD // per-task default by risk band
	TaskUSD          map[string]USD     // explicit per-task override
	ImageDailyUSD    USD
	ImageMaxVariants int

	// SoftFraction: when spend reaches this fraction of a hard limit, a soft
	// signal fires (cheaper route / fewer variants) even though the hard
	// limit has not been reached. Range (0,1).
	SoftFraction float64
}

// LookupTaskLimit returns the per-task limit for a task, honouring an explicit
// override then the risk-band default. Zero means no per-task limit.
func (l Limits) LookupTaskLimit(taskID string, r risk.Level) USD {
	if v, ok := l.TaskUSD[taskID]; ok {
		return v
	}
	return l.TaskDefaultUSD[r]
}

// UsageRecord is one observed usage event from a coding/image run (spec §14.4).
// Included=true means the usage was covered by a subscription quota and has no
// marginal paid cost (§23). Confidence carries through to rendering so
// estimated usage is never shown as exact (rule §36.10, AC-18).
type UsageRecord struct {
	Account      quota.AccountID
	Engine       string
	Model        string
	Tier         string
	InputTokens  int
	OutputTokens int
	CachedTokens int
	ImageGens    int
	CostUSD      USD
	Confidence   quota.Confidence
	Included     bool // subscription-included vs paid API cost
	ProviderType ProviderType
	ProjectID    string
	TaskID       string
	At           time.Time
}

// ProviderType distinguishes coding providers from image providers (spec §14.4).
type ProviderType string

const (
	ProviderCoding ProviderType = "coding"
	ProviderImage  ProviderType = "image"
)

// Scope selects which limit scopes to evaluate for a prospective spend.
type Scope struct {
	ProjectID    string
	TaskID       string
	TaskRisk     risk.Level
	ProviderType ProviderType
}

// Decision is the outcome of a budget check.
type Decision struct {
	Allowed      bool   // the spend may proceed
	HardBlocked  bool   // a hard limit forbids this (paid) spend
	SoftSignal   bool   // spend is in the soft zone (prefer cheaper route)
	Reason       string // human-readable explanation (§19.6)
	LimitUSD     USD    // the effective hard limit that applied (0 if none)
	RemainingUSD USD    // remaining under the effective limit (<=0 when blocked)
	// IncludedPermitted reports whether a subscription-included route is still
	// allowed even when HardBlocked is true (§23 hard limit).
	IncludedPermitted bool
}

// AggregatedSummary is the roll-up of recorded usage, keeping included and paid
// cost strictly separate (§23) and preserving the coarsest confidence so the
// dashboard never shows estimated totals as exact (AC-18).
type AggregatedSummary struct {
	WindowFrom   time.Time
	WindowTo     time.Time
	InputTokens  int
	OutputTokens int
	CachedTokens int
	ImageGens    int
	IncludedCost USD // subscription-covered (no marginal cost)
	PaidCost     USD // API cost actually spent
	TotalCost    USD // IncludedCost + PaidCost
	CoarsestConf quota.Confidence
	PerProvider  map[string]ProviderCost
	PerProject   map[string]ProviderCost
	PerEngine    map[string]ProviderCost
	Records      int
}

// ProviderCost breaks down cost for one provider/project/engine bucket.
type ProviderCost struct {
	IncludedCost USD
	PaidCost     USD
	InputTokens  int
	OutputTokens int
	CachedTokens int
	ImageGens    int
}

// Controller records usage and evaluates spending limits. It is deterministic
// (rule §22.6) and safe for concurrent use.
type Controller struct {
	mu      sync.Mutex
	limits  Limits
	records []UsageRecord
	now     func() time.Time
}

// New constructs a Controller with the given limits and clock.
func New(limits Limits, now func() time.Time) *Controller {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if limits.SoftFraction <= 0 || limits.SoftFraction >= 1 {
		limits.SoftFraction = 0.8
	}
	return &Controller{limits: limits, now: now}
}

// Limits returns the configured limits.
func (c *Controller) Limits() Limits {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.limits
}

// SetLimits replaces the limits (e.g. when policy/config changes).
func (c *Controller) SetLimits(l Limits) {
	if l.SoftFraction <= 0 || l.SoftFraction >= 1 {
		l.SoftFraction = 0.8
	}
	c.mu.Lock()
	c.limits = l
	c.mu.Unlock()
}

// Record appends a usage record. The timestamp is normalised to UTC.
func (c *Controller) Record(r UsageRecord) {
	if r.At.IsZero() {
		r.At = c.now()
	} else {
		r.At = r.At.UTC()
	}
	if r.ProviderType == "" {
		r.ProviderType = ProviderCoding
	}
	c.mu.Lock()
	c.records = append(c.records, r)
	c.mu.Unlock()
}

// Spend computes the paid spend (excluding subscription-included usage) within
// the current daily/monthly/project/task windows for the given scope. Included
// usage is reported separately and never counts toward a paid hard limit.
func (c *Controller) Spend(scope Scope) (paidToday, paidThisMonth, paidProjectToday, paidTask USD) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	dayStart := startOfDay(now)
	monthStart := startOfMonth(now)
	for _, r := range c.records {
		if r.Included {
			continue // subscription-included usage is not paid spend
		}
		if scope.ProviderType != "" && r.ProviderType != scope.ProviderType {
			continue
		}
		if !r.At.Before(dayStart) && !r.At.After(now) {
			paidToday += r.CostUSD
			if scope.ProjectID != "" && r.ProjectID == scope.ProjectID {
				paidProjectToday += r.CostUSD
			}
		}
		if !r.At.Before(monthStart) && !r.At.After(now) {
			paidThisMonth += r.CostUSD
		}
		if scope.TaskID != "" && r.TaskID == scope.TaskID {
			paidTask += r.CostUSD
		}
	}
	return
}

// CanAfford evaluates whether a prospective paid spend is allowed. The
// effective hard limit is the most restrictive applicable scope (global daily,
// global monthly, project daily, image daily, or per-task). A hard block still
// permits subscription-included routes when requested (§23).
//
// hard=true enforces the limit; hard=false reports a soft signal only.
func (c *Controller) CanAfford(scope Scope, amount USD, paid, hard bool) Decision {
	paidToday, paidMonth, paidProj, paidTask := c.Spend(scope)
	limits := c.Limits()

	// Image providers are bounded by their own daily image budget, separate
	// from coding budgets (§23, §14.4).
	if scope.ProviderType == ProviderImage && limits.ImageDailyUSD > 0 {
		remaining := limits.ImageDailyUSD - paidToday
		d := decideOne("image daily", limits.ImageDailyUSD, remaining, amount, paid, hard, limits.SoftFraction)
		if !d.Allowed {
			return d
		}
	}

	type cand struct {
		name  string
		limit USD
		spent USD
	}
	candidates := []cand{}
	if limits.GlobalDailyUSD > 0 {
		candidates = append(candidates, cand{"global daily", limits.GlobalDailyUSD, paidToday})
	}
	if limits.GlobalMonthlyUSD > 0 {
		candidates = append(candidates, cand{"global monthly", limits.GlobalMonthlyUSD, paidMonth})
	}
	if scope.ProjectID != "" {
		if v, ok := limits.ProjectDailyUSD[scope.ProjectID]; ok && v > 0 {
			candidates = append(candidates, cand{"project daily", v, paidProj})
		}
	}
	taskLim := limits.LookupTaskLimit(scope.TaskID, scope.TaskRisk)
	if scope.TaskID != "" && taskLim > 0 {
		candidates = append(candidates, cand{"task", taskLim, paidTask})
	}

	// Most restrictive (smallest remaining) decides.
	var worst *cand
	worstRemaining := USD(0)
	first := true
	for i := range candidates {
		rem := candidates[i].limit - candidates[i].spent
		if first || rem < worstRemaining {
			worst = &candidates[i]
			worstRemaining = rem
			first = false
		}
	}
	if worst == nil {
		// No applicable limit: allowed, no soft signal.
		return Decision{Allowed: true, Reason: "no applicable budget limit"}
	}
	d := decideOne(worst.name, worst.limit, worstRemaining, amount, paid, hard, limits.SoftFraction)
	return d
}

// decideOne computes the decision for a single limiting scope.
func decideOne(name string, limit, remaining, amount USD, paid, hard bool, softFrac float64) Decision {
	d := Decision{
		LimitUSD:          limit,
		RemainingUSD:      remaining - amount,
		IncludedPermitted: true, // included routes are always permitted by policy (§23)
	}
	if !paid {
		// Subscription-included usage: never blocked by a paid hard limit.
		d.Allowed = true
		d.Reason = "subscription-included usage (not counted against paid budget)"
		return d
	}
	if remaining <= 0 {
		d.Allowed = false
		if hard {
			d.HardBlocked = true
			d.Reason = name + " hard budget exhausted; new paid run forbidden (§23)"
		} else {
			d.Reason = name + " budget at zero (soft)"
		}
		return d
	}
	if remaining-amount < 0 {
		d.Allowed = false
		if hard {
			d.HardBlocked = true
			d.Reason = name + " hard budget would be exceeded by this paid run (§23)"
		} else {
			d.Reason = name + " budget would be exceeded (soft)"
		}
		return d
	}
	// Soft signal: projected spend crosses the soft fraction of the limit.
	softThreshold := limit * USD(softFrac)
	if (limit - remaining + amount) >= softThreshold {
		d.SoftSignal = true
		d.Reason = name + " soft threshold reached; prefer cheaper route / fewer variants (§23)"
	} else {
		d.Reason = name + " budget OK"
	}
	d.Allowed = true
	return d
}

// Summary aggregates recorded usage over [from, to], keeping included and paid
// cost separate (§23) and tracking the coarsest confidence (AC-18).
func (c *Controller) Summary(from, to time.Time) AggregatedSummary {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := AggregatedSummary{
		WindowFrom:   from,
		WindowTo:     to,
		PerProvider:  map[string]ProviderCost{},
		PerProject:   map[string]ProviderCost{},
		PerEngine:    map[string]ProviderCost{},
		CoarsestConf: quota.ConfExact,
	}
	seen := false
	for _, r := range c.records {
		if !from.IsZero() && r.At.Before(from) {
			continue
		}
		if !to.IsZero() && r.At.After(to) {
			continue
		}
		seen = true
		s.Records++
		s.InputTokens += r.InputTokens
		s.OutputTokens += r.OutputTokens
		s.CachedTokens += r.CachedTokens
		s.ImageGens += r.ImageGens
		if r.Included {
			s.IncludedCost += r.CostUSD
		} else {
			s.PaidCost += r.CostUSD
		}
		bucket(s.PerProvider, string(r.ProviderType), r)
		bucket(s.PerProject, r.ProjectID, r)
		bucket(s.PerEngine, r.Engine, r)
		s.CoarsestConf = coarsest(s.CoarsestConf, r.Confidence)
	}
	if !seen {
		s.CoarsestConf = quota.ConfUnknown
	}
	s.TotalCost = s.IncludedCost + s.PaidCost
	return s
}

func bucket(m map[string]ProviderCost, key string, r UsageRecord) {
	if key == "" {
		key = "-"
	}
	p := m[key]
	p.IncludedCost += boolUSD(r.Included, r.CostUSD)
	p.PaidCost += boolUSD(!r.Included, r.CostUSD)
	p.InputTokens += r.InputTokens
	p.OutputTokens += r.OutputTokens
	p.CachedTokens += r.CachedTokens
	p.ImageGens += r.ImageGens
	m[key] = p
}

func boolUSD(cond bool, v USD) USD {
	if cond {
		return v
	}
	return 0
}

// coarsest returns the lower-precision confidence (EXACT > PROVIDER_REPORTED >
// ESTIMATED > INFERRED > UNKNOWN). Aggregates never look more precise than
// their least-precise component (AC-18).
func coarsest(a, b quota.Confidence) quota.Confidence {
	if rank(b) < rank(a) {
		return b
	}
	return a
}

func rank(c quota.Confidence) int {
	switch c {
	case quota.ConfExact:
		return 4
	case quota.ConfProviderReported:
		return 3
	case quota.ConfEstimated:
		return 2
	case quota.ConfInferred:
		return 1
	default:
		return 0
	}
}

// SortedRecords returns a copy of records sorted by time (stable for display).
func (c *Controller) SortedRecords() []UsageRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]UsageRecord, len(c.records))
	copy(out, c.records)
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// Count returns the number of recorded usage events.
func (c *Controller) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.records)
}

// ---- window helpers ----

func startOfDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func startOfMonth(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}
