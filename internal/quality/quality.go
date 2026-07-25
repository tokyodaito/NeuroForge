// Package quality implements token accounting and quality statistics (spec §6.1,
// §14.4, §19.1, milestone M12-7).
//
// STATUS: implemented for milestone M12.
//
// Scope:
//
//   - Token accounting: record per-run token usage (coding input/output/cached
//     and image input/output/generations) and aggregate it by task, project,
//     provider and day (§6.1 USAGE TODAY, §14.4). Cached input is accounted
//     separately from uncached input (§22.8 prompt cache) so the dashboard can
//     show the cache hit benefit.
//   - Quality statistics: record per-task outcomes (success/failure, repair
//     iterations, finding counts, route taken) and aggregate success rates per
//     model, engine and route (§19.1 "model success rate" / "agent engine
//     success rate"). These are deterministic counters fed back into routing.
//
// The package is pure domain logic and never calls an LLM (rule §22.6). It is
// safe for concurrent use. The daemon persists events to the §31 usage_events
// table; this package provides the in-process aggregation and the deterministic
// arithmetic.
package quality

import (
	"sort"
	"sync"
	"time"
)

// UsageEvent is one token-usage observation from a coding/image run (§6.1,
// §14.4). The provider's normalized `usage.updated` event maps onto this.
type UsageEvent struct {
	TaskID            string
	ProjectID         string
	Provider          string // engine id (coding) or provider id (image)
	Model             string
	Kind              UsageKind
	InputTokens       int
	CachedInputTokens int // §22.8 prompt-cache hit
	OutputTokens      int
	Generations       int // image generations count
	CostUSD           float64
	OccurredAt        time.Time
}

// UsageKind separates coding usage from image usage (§14.4).
type UsageKind string

const (
	UsageCoding UsageKind = "coding"
	UsageImage  UsageKind = "image"
)

// Totals is an aggregated usage snapshot (§6.1 USAGE TODAY).
type Totals struct {
	CodingInput       int
	CachedInput       int
	CodingOutput      int
	ImageInputTokens  int
	ImageOutputTokens int
	ImageGenerations  int
	EstimatedCostUSD  float64
	EventCount        int
}

// Add accumulates an event into the totals.
func (t *Totals) Add(e UsageEvent) {
	t.EventCount++
	switch e.Kind {
	case UsageCoding:
		t.CodingInput += e.InputTokens
		t.CachedInput += e.CachedInputTokens
		t.CodingOutput += e.OutputTokens
	case UsageImage:
		t.ImageInputTokens += e.InputTokens
		t.ImageOutputTokens += e.OutputTokens
		t.ImageGenerations += e.Generations
	}
	t.EstimatedCostUSD += e.CostUSD
}

// Accounting records token usage events and aggregates them. Safe for
// concurrent use.
type Accounting struct {
	mu     sync.Mutex
	events []UsageEvent
}

// NewAccounting returns an empty accounting store.
func NewAccounting() *Accounting { return &Accounting{} }

// Record appends a usage event. OccurredAt is defaulted to now if zero.
func (a *Accounting) Record(e UsageEvent) {
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now().UTC()
	}
	a.mu.Lock()
	a.events = append(a.events, e)
	a.mu.Unlock()
}

// Snapshot returns the aggregate totals over all events.
func (a *Accounting) Snapshot() Totals {
	a.mu.Lock()
	defer a.mu.Unlock()
	var t Totals
	for _, e := range a.events {
		t.Add(e)
	}
	return t
}

// SnapshotFor returns totals filtered by the given filter (project and/or day).
func (a *Accounting) SnapshotFor(f Filter) Totals {
	a.mu.Lock()
	defer a.mu.Unlock()
	var t Totals
	for _, e := range a.events {
		if !f.match(e) {
			continue
		}
		t.Add(e)
	}
	return t
}

// Filter selects a subset of usage events.
type Filter struct {
	ProjectID string
	TaskID    string
	Provider  string
	Day       time.Time // midnight of the day (UTC)
}

func (f Filter) match(e UsageEvent) bool {
	if f.ProjectID != "" && e.ProjectID != f.ProjectID {
		return false
	}
	if f.TaskID != "" && e.TaskID != f.TaskID {
		return false
	}
	if f.Provider != "" && e.Provider != f.Provider {
		return false
	}
	if !f.Day.IsZero() {
		ed := e.OccurredAt.UTC()
		if ed.Year() != f.Day.Year() || ed.Month() != f.Day.Month() || ed.Day() != f.Day.Day() {
			return false
		}
	}
	return true
}

// CacheHitRatio reports the fraction of coding input tokens served from the
// prompt cache (§22.8). 0 when no coding input.
func (t Totals) CacheHitRatio() float64 {
	total := t.CodingInput + t.CachedInput
	if total == 0 {
		return 0
	}
	return float64(t.CachedInput) / float64(total)
}

// --- quality statistics ---

// Outcome is the result of a finished task attempt (§19.1 success-rate signals).
type Outcome string

const (
	OutcomeSuccess Outcome = "success"
	OutcomeFailure Outcome = "failure"
)

// TaskOutcome records the quality dimensions of a finished task.
type TaskOutcome struct {
	TaskID           string
	ProjectID        string
	Engine           string
	Model            string
	RouteTier        string
	Outcome          Outcome
	Complexity       string // C0..C4
	Risk             string // R0..R4
	RepairIterations int
	Findings         int
	BlockerFindings  int
	TestPassRate     float64 // 0..1
	VisualScore      float64 // 0..1
	TokensUsed       int
	CostUSD          float64
	DurationSec      float64
	OccurredAt       time.Time
}

// Stats is the aggregate quality view per engine/model/route.
type Stats struct {
	Engine      string
	Model       string
	RouteTier   string
	Attempts    int
	Successes   int
	Failures    int
	AvgRepair   float64
	AvgFindings float64
	SuccessRate float64
}

// Statistics aggregates task outcomes into per-route success rates (§19.1).
type Statistics struct {
	mu       sync.Mutex
	outcomes []TaskOutcome
}

// NewStatistics returns an empty quality-statistics store.
func NewStatistics() *Statistics { return &Statistics{} }

// Record appends a task outcome.
func (s *Statistics) Record(o TaskOutcome) {
	if o.OccurredAt.IsZero() {
		o.OccurredAt = time.Now().UTC()
	}
	s.mu.Lock()
	s.outcomes = append(s.outcomes, o)
	s.mu.Unlock()
}

// SuccessRateByModel returns per-(engine,model) success statistics (§19.1
// "model success rate"). Deterministic and sorted by model id.
func (s *Statistics) SuccessRateByModel() []Stats {
	return s.aggregate(func(o TaskOutcome) string { return o.Engine + "/" + o.Model })
}

// SuccessRateByEngine returns per-engine success statistics (§19.1 "agent engine
// success rate").
func (s *Statistics) SuccessRateByEngine() []Stats {
	return s.aggregate(func(o TaskOutcome) string { return o.Engine })
}

// SuccessRateByRoute returns per-route-tier success statistics.
func (s *Statistics) SuccessRateByRoute() []Stats {
	return s.aggregate(func(o TaskOutcome) string { return o.RouteTier })
}

func (s *Statistics) aggregate(key func(TaskOutcome) string) []Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	groups := map[string]*Stats{}
	var order []string
	for _, o := range s.outcomes {
		k := key(o)
		st, ok := groups[k]
		if !ok {
			st = &Stats{Engine: o.Engine, Model: o.Model, RouteTier: o.RouteTier}
			groups[k] = st
			order = append(order, k)
		}
		st.Attempts++
		if o.Outcome == OutcomeSuccess {
			st.Successes++
		} else {
			st.Failures++
		}
		st.AvgRepair += float64(o.RepairIterations)
		st.AvgFindings += float64(o.Findings)
	}
	out := make([]Stats, 0, len(order))
	for _, k := range order {
		st := *groups[k]
		if st.Attempts > 0 {
			st.AvgRepair /= float64(st.Attempts)
			st.AvgFindings /= float64(st.Attempts)
			st.SuccessRate = float64(st.Successes) / float64(st.Attempts)
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Engine+"/"+out[i].Model < out[j].Engine+"/"+out[j].Model
	})
	return out
}

// OverallSuccessRate returns the global success fraction over all recorded
// outcomes (0 when none).
func (s *Statistics) OverallSuccessRate() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.outcomes) == 0 {
		return 0
	}
	success := 0
	for _, o := range s.outcomes {
		if o.Outcome == OutcomeSuccess {
			success++
		}
	}
	return float64(success) / float64(len(s.outcomes))
}
