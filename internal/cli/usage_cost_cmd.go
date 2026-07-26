package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"strings"
	"time"

	"neuroforge/internal/budget"
	"neuroforge/internal/quota"
	"neuroforge/internal/router/fakes"
)

// runUsage implements `forge usage` — aggregated usage with included vs paid
// cost strictly separated (spec §14.4, §23) and confidence-tagged so estimated
// totals never look exact (AC-18).
func (a *App) runUsage(args []string) int {
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	days := fs.Int("days", 1, "window in days (1=today, 30=month)")
	if err := fs.Parse(args); err != nil {
		return ExitErr
	}

	bc := fakes.DefaultBudgetController()
	seedDemoUsage(bc) // deterministic demo records so the command is demonstrable

	now := time.Now().UTC()
	var from time.Time
	switch {
	case *days >= 30:
		from = startOfMonthCLI(now)
	default:
		from = startOfDayCLI(now)
	}
	if *days > 1 && *days < 30 {
		from = now.AddDate(0, 0, -(*days - 1))
		from = startOfDayCLI(from)
	}
	s := bc.Summary(from, now)

	if *jsonOut {
		b, _ := json.MarshalIndent(usageJSON(s), "", "  ")
		fmt.Fprintln(a.Out, string(b))
		return ExitOK
	}

	fmt.Fprintln(a.Out, boldPlain+"USAGE"+reset)
	fmt.Fprintf(a.Out, "%s  window: %s .. %s\n\n", dimPlain, from.Format("2006-01-02"), now.Format("2006-01-02 15:04")+reset)

	fmt.Fprintf(a.Out, "Coding input       %s\n", humanTokens(s.InputTokens))
	fmt.Fprintf(a.Out, "Cached input       %s\n", humanTokens(s.CachedTokens))
	fmt.Fprintf(a.Out, "Coding output      %s\n", humanTokens(s.OutputTokens))
	fmt.Fprintf(a.Out, "Image generations  %d\n", s.ImageGens)
	fmt.Fprintf(a.Out, "Included cost      %s  %s\n", money(s.IncludedCost), dimPlain+"(subscription-covered, no marginal cost)"+reset)
	fmt.Fprintf(a.Out, "Paid API cost      %s\n", money(s.PaidCost))
	fmt.Fprintf(a.Out, "Estimated total    %s  %s\n", money(s.TotalCost), quota.ConfidenceTag(s.CoarsestConf))
	fmt.Fprintln(a.Out)
	if len(s.PerEngine) > 0 {
		fmt.Fprintln(a.Out, "BY ENGINE")
		for eng, pc := range s.PerEngine {
			fmt.Fprintf(a.Out, "  %-12s included %s  paid %s  in %s  out %s\n",
				eng, money(pc.IncludedCost), money(pc.PaidCost),
				humanTokens(pc.InputTokens), humanTokens(pc.OutputTokens))
		}
	}
	return ExitOK
}

// runCost implements `forge cost` — cost report across scopes (spec §23).
func (a *App) runCost(args []string) int {
	fs := flag.NewFlagSet("cost", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	project := fs.String("project", "", "filter by project id")
	if err := fs.Parse(args); err != nil {
		return ExitErr
	}

	bc := fakes.DefaultBudgetController()
	seedDemoUsage(bc)
	now := time.Now().UTC()
	dayStart := startOfDayCLI(now)
	monthStart := startOfMonthCLI(now)

	today := bc.Summary(dayStart, now)
	month := bc.Summary(monthStart, now)

	paidToday, paidMonth, paidProj, _ := bc.Spend(budget.Scope{ProjectID: *project, ProviderType: budget.ProviderCoding})

	if *jsonOut {
		out := map[string]any{
			"today":        usageJSON(today),
			"month":        usageJSON(month),
			"paid_today":   paidToday,
			"paid_month":   paidMonth,
			"paid_project": paidProj,
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(a.Out, string(b))
		return ExitOK
	}

	lim := bc.Limits()
	fmt.Fprintln(a.Out, boldPlain+"COST REPORT"+reset)
	fmt.Fprintln(a.Out)
	fmt.Fprintf(a.Out, "Today       paid %s  /  limit %s\n", money(paidToday), money(lim.GlobalDailyUSD))
	fmt.Fprintf(a.Out, "This month  paid %s  /  limit %s\n", money(paidMonth), money(lim.GlobalMonthlyUSD))
	if *project != "" {
		pjLim := lim.ProjectDailyUSD[*project]
		fmt.Fprintf(a.Out, "Project %s   paid %s  /  limit %s\n", *project, money(paidProj), money(pjLim))
	}
	fmt.Fprintln(a.Out)
	fmt.Fprintf(a.Out, "Included (subscription)  today %s   month %s\n", money(today.IncludedCost), money(month.IncludedCost))
	fmt.Fprintf(a.Out, "Paid API                 today %s   month %s\n", money(today.PaidCost), money(month.PaidCost))
	fmt.Fprintln(a.Out, dimPlain+"Image budget is tracked separately (§14.4/§23)."+reset)
	return ExitOK
}

type usageJSONOut struct {
	From         time.Time         `json:"from"`
	To           time.Time         `json:"to"`
	InputTokens  int               `json:"input_tokens"`
	OutputTokens int               `json:"output_tokens"`
	CachedTokens int               `json:"cached_tokens"`
	ImageGens    int               `json:"image_gens"`
	IncludedCost float64           `json:"included_cost_usd"`
	PaidCost     float64           `json:"paid_cost_usd"`
	TotalCost    float64           `json:"total_cost_usd"`
	CoarsestConf string            `json:"coarsest_confidence"`
	PerEngine    map[string]pcJSON `json:"per_engine"`
}

type pcJSON struct {
	IncludedCost float64 `json:"included_cost_usd"`
	PaidCost     float64 `json:"paid_cost_usd"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CachedTokens int     `json:"cached_tokens"`
	ImageGens    int     `json:"image_gens"`
}

func usageJSON(s budget.AggregatedSummary) usageJSONOut {
	out := usageJSONOut{
		From: s.WindowFrom, To: s.WindowTo,
		InputTokens: s.InputTokens, OutputTokens: s.OutputTokens,
		CachedTokens: s.CachedTokens, ImageGens: s.ImageGens,
		IncludedCost: float64(s.IncludedCost), PaidCost: float64(s.PaidCost),
		TotalCost: float64(s.TotalCost), CoarsestConf: string(s.CoarsestConf),
		PerEngine: map[string]pcJSON{},
	}
	for eng, pc := range s.PerEngine {
		out.PerEngine[eng] = pcJSON{
			IncludedCost: float64(pc.IncludedCost), PaidCost: float64(pc.PaidCost),
			InputTokens: pc.InputTokens, OutputTokens: pc.OutputTokens,
			CachedTokens: pc.CachedTokens, ImageGens: pc.ImageGens,
		}
	}
	return out
}

// seedDemoUsage records a deterministic, clearly-fake set of usage events so
// `forge usage` / `forge cost` are demonstrable without a live run. All engines
// and models are fakes (rule §36.5: no real paid models).
//
// Timestamps are clamped into [startOfDayUTC(now), now] so a default `--days 1`
// window never goes empty near UTC midnight (demo offsets of -1h/-2h would
// otherwise fall on the previous UTC day and drop CoarsestConf to UNKNOWN).
func seedDemoUsage(bc *budget.Controller) {
	now := time.Now().UTC()
	records := []budget.UsageRecord{
		{Engine: "alpha", Model: "alpha-pro", Tier: "STANDARD", Account: quota.AccountID{Engine: "alpha", Account: "alpha-api"},
			InputTokens: 1_200_000, CachedTokens: 980_000, OutputTokens: 141_000, CostUSD: 6.81, Included: false,
			Confidence: quota.ConfEstimated, ProviderType: budget.ProviderCoding, At: demoUsageAt(now, 2*time.Hour)},
		{Engine: "alpha", Model: "alpha-lite", Tier: "SMALL", Account: quota.AccountID{Engine: "alpha", Account: "alpha-sub"},
			InputTokens: 320_000, OutputTokens: 22_000, CostUSD: 0, Included: true,
			Confidence: quota.ConfProviderReported, ProviderType: budget.ProviderCoding, At: demoUsageAt(now, 1*time.Hour)},
	}
	for _, r := range records {
		bc.Record(r)
	}
}

// demoUsageAt returns now-behind, floored to the start of the UTC day so the
// point always lies inside the default "today" usage window.
func demoUsageAt(now time.Time, behind time.Duration) time.Time {
	now = now.UTC()
	t := now.Add(-behind)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if t.Before(day) {
		return day
	}
	return t
}

func humanTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return trimZeroCLI(fmt.Sprintf("%.2f", float64(n)/1_000_000)) + "M"
	case n >= 1_000:
		return trimZeroCLI(fmt.Sprintf("%.2f", float64(n)/1_000)) + "k"
	}
	return fmt.Sprintf("%d", n)
}

// trimZeroCLI strips trailing ".0" / ".00" from a formatted number.
func trimZeroCLI(s string) string {
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
	}
	return s
}

func money(v budget.USD) string {
	return fmt.Sprintf("$%.2f", float64(v))
}

func startOfDayCLI(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func startOfMonthCLI(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}
