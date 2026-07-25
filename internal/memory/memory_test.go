package memory_test

import (
	"testing"
	"time"

	"neuroforge/internal/memory"
)

func TestLearnAndGet(t *testing.T) {
	s := memory.NewStore("proj-1")
	r, err := s.Learn(memory.Record{
		Key:        "layering",
		Category:   memory.CatArchitectureFact,
		Value:      "Adapters must not import core packages.",
		Source:     "AGENTS.md",
		Confidence: memory.ConfidenceHigh,
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != 1 {
		t.Errorf("version = %d want 1", r.Version)
	}
	got, ok := s.Get(memory.CatArchitectureFact, "layering")
	if !ok {
		t.Fatal("record not stored")
	}
	if got.Value != "Adapters must not import core packages." {
		t.Errorf("value mismatch: %q", got.Value)
	}
}

func TestRelearnBumpsVersion(t *testing.T) {
	s := memory.NewStore("proj-1")
	s.Learn(memory.Record{Key: "k", Category: memory.CatBuildCommand, Value: "make build"})
	r, _ := s.Learn(memory.Record{Key: "k", Category: memory.CatBuildCommand, Value: "make build-quick"})
	if r.Version != 2 {
		t.Errorf("version = %d want 2 on re-learn", r.Version)
	}
	got, _ := s.Get(memory.CatBuildCommand, "k")
	if got.Value != "make build-quick" {
		t.Errorf("value not updated: %q", got.Value)
	}
}

func TestInvalidCategory(t *testing.T) {
	s := memory.NewStore("proj-1")
	if _, err := s.Learn(memory.Record{Key: "k", Category: "bogus"}); err == nil {
		t.Errorf("expected error for invalid category")
	}
}

func TestAllDeterministicOrder(t *testing.T) {
	s := memory.NewStore("proj-1")
	s.Learn(memory.Record{Key: "z", Category: memory.CatArchitectureFact, Value: "z"})
	s.Learn(memory.Record{Key: "a", Category: memory.CatKnownFailure, Value: "a"})
	s.Learn(memory.Record{Key: "a", Category: memory.CatArchitectureFact, Value: "a"})
	all := s.All()
	// Category-sorted first, then key.
	if all[0].Category != memory.CatArchitectureFact || all[0].Key != "a" {
		t.Errorf("order wrong: %+v", all)
	}
	if all[1].Category != memory.CatArchitectureFact || all[1].Key != "z" {
		t.Errorf("order wrong: %+v", all)
	}
	if all[2].Category != memory.CatKnownFailure {
		t.Errorf("order wrong: %+v", all)
	}
}

func TestTTLExpiration(t *testing.T) {
	s := memory.NewStore("proj-1")
	base := time.Now()
	s.SetClock(func() time.Time { return base })
	s.Learn(memory.Record{
		Key:        "stale",
		Category:   memory.CatProviderQuirk,
		Value:      "old",
		Expiration: memory.ExpTTL,
		ExpiresAt:  base.Add(time.Hour),
	})
	if got := s.All(); len(got) != 1 {
		t.Fatalf("expected 1 record, got %d", len(got))
	}
	// Advance clock past expiry.
	s.SetClock(func() time.Time { return base.Add(2 * time.Hour) })
	if got := s.All(); len(got) != 0 {
		t.Errorf("expired TTL record not pruned from All(): %d", len(got))
	}
}

func TestPruneStaleOnCommitChange(t *testing.T) {
	s := memory.NewStore("proj-1")
	s.Learn(memory.Record{
		Key:        "cmd",
		Category:   memory.CatBuildCommand,
		Value:      "make build",
		CommitSHA:  "abc",
		Expiration: memory.ExpOnCommitChange,
	})
	if n := s.PruneStale("abc"); n != 0 {
		t.Errorf("should not prune when HEAD matches, pruned %d", n)
	}
	if n := s.PruneStale("def"); n != 1 {
		t.Errorf("should prune 1 on commit change, pruned %d", n)
	}
}

func TestHighConfidenceRules(t *testing.T) {
	s := memory.NewStore("proj-1")
	s.Learn(memory.Record{Key: "1", Category: memory.CatArchitectureFact, Value: "rule-1", Confidence: memory.ConfidenceHigh})
	s.Learn(memory.Record{Key: "2", Category: memory.CatDesignSystemRule, Value: "rule-2", Confidence: memory.ConfidenceHigh})
	s.Learn(memory.Record{Key: "3", Category: memory.CatKnownFailure, Value: "fail-3", Confidence: memory.ConfidenceHigh})
	s.Learn(memory.Record{Key: "4", Category: memory.CatArchitectureFact, Value: "low-conf", Confidence: memory.ConfidenceLow})
	rules := s.HighConfidenceRules()
	if len(rules) != 2 {
		t.Fatalf("expected 2 high-confidence rules, got %d (%v)", len(rules), rules)
	}
}

func TestKnownFailures(t *testing.T) {
	s := memory.NewStore("proj-1")
	s.Learn(memory.Record{Key: "f1", Category: memory.CatKnownFailure, Value: "race in worker pool"})
	fails := s.KnownFailures()
	if len(fails) != 1 || fails[0] != "race in worker pool" {
		t.Errorf("known failures = %v", fails)
	}
}

func TestForget(t *testing.T) {
	s := memory.NewStore("proj-1")
	s.Learn(memory.Record{Key: "k", Category: memory.CatKnownFailure, Value: "v"})
	if !s.Forget(memory.CatKnownFailure, "k") {
		t.Errorf("Forget returned false for existing record")
	}
	if _, ok := s.Get(memory.CatKnownFailure, "k"); ok {
		t.Errorf("record not forgotten")
	}
}
