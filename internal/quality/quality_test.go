package quality_test

import (
	"testing"
	"time"

	"neuroforge/internal/quality"
)

func TestTokenAccounting(t *testing.T) {
	a := quality.NewAccounting()
	a.Record(quality.UsageEvent{
		TaskID: "T1", ProjectID: "P1", Provider: "codex", Model: "m1",
		Kind: quality.UsageCoding, InputTokens: 1000, CachedInputTokens: 400,
		OutputTokens: 200, CostUSD: 0.5,
	})
	a.Record(quality.UsageEvent{
		TaskID: "T1", ProjectID: "P1", Provider: "gpt-image", Model: "img",
		Kind: quality.UsageImage, Generations: 3, CostUSD: 0.2,
	})
	snap := a.Snapshot()
	if snap.CodingInput != 1000 {
		t.Errorf("CodingInput = %d want 1000", snap.CodingInput)
	}
	if snap.CachedInput != 400 {
		t.Errorf("CachedInput = %d want 400", snap.CachedInput)
	}
	if snap.CodingOutput != 200 {
		t.Errorf("CodingOutput = %d want 200", snap.CodingOutput)
	}
	if snap.ImageGenerations != 3 {
		t.Errorf("ImageGenerations = %d want 3", snap.ImageGenerations)
	}
	if snap.EstimatedCostUSD < 0.69 || snap.EstimatedCostUSD > 0.71 {
		t.Errorf("cost = %v want ~0.7", snap.EstimatedCostUSD)
	}
}

func TestCacheHitRatio(t *testing.T) {
	a := quality.NewAccounting()
	a.Record(quality.UsageEvent{Kind: quality.UsageCoding, InputTokens: 600, CachedInputTokens: 400})
	r := a.Snapshot().CacheHitRatio()
	if r < 0.39 || r > 0.41 {
		t.Errorf("cache hit ratio = %v want ~0.4", r)
	}
}

func TestSnapshotForProjectAndDay(t *testing.T) {
	day1 := time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC)
	a := quality.NewAccounting()
	a.Record(quality.UsageEvent{ProjectID: "P1", Kind: quality.UsageCoding, InputTokens: 100, OccurredAt: day1})
	a.Record(quality.UsageEvent{ProjectID: "P2", Kind: quality.UsageCoding, InputTokens: 50, OccurredAt: day1})
	a.Record(quality.UsageEvent{ProjectID: "P1", Kind: quality.UsageCoding, InputTokens: 30, OccurredAt: day1.Add(48 * time.Hour)})
	p1 := a.SnapshotFor(quality.Filter{ProjectID: "P1"})
	if p1.CodingInput != 130 {
		t.Errorf("P1 total = %d want 130", p1.CodingInput)
	}
	p1day1 := a.SnapshotFor(quality.Filter{ProjectID: "P1", Day: day1})
	if p1day1.CodingInput != 100 {
		t.Errorf("P1 day1 = %d want 100", p1day1.CodingInput)
	}
}

func TestSuccessRateByModel(t *testing.T) {
	s := quality.NewStatistics()
	s.Record(quality.TaskOutcome{Engine: "codex", Model: "m1", RouteTier: "STANDARD", Outcome: quality.OutcomeSuccess})
	s.Record(quality.TaskOutcome{Engine: "codex", Model: "m1", RouteTier: "STANDARD", Outcome: quality.OutcomeFailure, RepairIterations: 2, Findings: 3})
	s.Record(quality.TaskOutcome{Engine: "claude", Model: "m2", RouteTier: "HEAVY", Outcome: quality.OutcomeSuccess})
	stats := s.SuccessRateByModel()
	if len(stats) != 2 {
		t.Fatalf("expected 2 model groups, got %d", len(stats))
	}
	var codexM1 *quality.Stats
	for i := range stats {
		if stats[i].Engine == "codex" && stats[i].Model == "m1" {
			codexM1 = &stats[i]
		}
	}
	if codexM1 == nil {
		t.Fatal("codex/m1 group missing")
	}
	if codexM1.Attempts != 2 || codexM1.Successes != 1 {
		t.Errorf("codex/m1 attempts/successes = %d/%d", codexM1.Attempts, codexM1.Successes)
	}
	if codexM1.SuccessRate < 0.49 || codexM1.SuccessRate > 0.51 {
		t.Errorf("codex/m1 success rate = %v want 0.5", codexM1.SuccessRate)
	}
	if codexM1.AvgRepair != 1.0 {
		t.Errorf("avg repair = %v want 1.0", codexM1.AvgRepair)
	}
}

func TestOverallSuccessRate(t *testing.T) {
	s := quality.NewStatistics()
	if r := s.OverallSuccessRate(); r != 0 {
		t.Errorf("empty overall = %v want 0", r)
	}
	s.Record(quality.TaskOutcome{Outcome: quality.OutcomeSuccess})
	s.Record(quality.TaskOutcome{Outcome: quality.OutcomeSuccess})
	s.Record(quality.TaskOutcome{Outcome: quality.OutcomeFailure})
	if r := s.OverallSuccessRate(); r < 0.66 || r > 0.67 {
		t.Errorf("overall = %v want ~0.667", r)
	}
}

func TestSuccessRateByEngineDeterministic(t *testing.T) {
	s := quality.NewStatistics()
	for i := 0; i < 5; i++ {
		s.Record(quality.TaskOutcome{Engine: "codex", Outcome: quality.OutcomeSuccess})
	}
	for i := 0; i < 5; i++ {
		s.Record(quality.TaskOutcome{Engine: "kimi", Outcome: quality.OutcomeFailure})
	}
	stats := s.SuccessRateByEngine()
	if len(stats) != 2 {
		t.Fatalf("expected 2 engines, got %d", len(stats))
	}
	// Sorted by engine id.
	if stats[0].Engine != "codex" {
		t.Errorf("not sorted: %v", stats)
	}
	if stats[0].SuccessRate != 1.0 || stats[1].SuccessRate != 0 {
		t.Errorf("success rates wrong: %+v", stats)
	}
}
