package daemon

import (
	"testing"

	"neuroforge/internal/adapter/codingagent/builtin"
)

// canonicalEngines is the complete set of engines the daemon MUST register so
// that `forge workspace run --engine <e>` resolves for every first-party
// provider plus the fake smoke agent.
var canonicalEngines = map[string]bool{
	"fake":             true,
	builtin.IDCodex:    true,
	builtin.IDClaude:   true,
	builtin.IDGemini:   true,
	builtin.IDKimi:     true,
	builtin.IDGrok:     true,
	builtin.IDOpenCode: true,
}

// TestBuildAdapterRegistry_HasAllSevenEngines proves the daemon registers the
// fake agent plus all six first-party production adapters (blocker 1 fix).
func TestBuildAdapterRegistry_HasAllSevenEngines(t *testing.T) {
	reg, err := buildAdapterRegistry()
	if err != nil {
		t.Fatalf("buildAdapterRegistry: %v", err)
	}
	if got := reg.Len(); got != 7 {
		t.Fatalf("registry Len = %d, want 7 (fake + 6 production): %v", got, reg.IDs())
	}
	for id := range canonicalEngines {
		if a, ok := reg.Lookup(id); !ok {
			t.Errorf("engine %q not registered; have %v", id, reg.IDs())
		} else if a.ID() != id {
			t.Errorf("engine %q reported id %q", id, a.ID())
		}
	}
}

// TestBuildAdapterRegistry_EngineIDsUnique verifies no id collides across the
// daemon registry (spec §12.1, AC-6 stability — required uniqueness test).
func TestBuildAdapterRegistry_EngineIDsUnique(t *testing.T) {
	reg, err := buildAdapterRegistry()
	if err != nil {
		t.Fatalf("buildAdapterRegistry: %v", err)
	}
	ids := reg.IDs()
	seen := make(map[string]int, len(ids))
	for _, id := range ids {
		seen[id]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("engine id %q registered %d times, want exactly 1", id, n)
		}
	}
}

// TestBuildAdapterRegistry_UnknownEngineRejected proves a dispatch to an
// unregistered engine is a clean miss (the supervisor turns this into the
// "unknown engine" error rather than a panic or silent fallback).
func TestBuildAdapterRegistry_UnknownEngineRejected(t *testing.T) {
	reg, err := buildAdapterRegistry()
	if err != nil {
		t.Fatalf("buildAdapterRegistry: %v", err)
	}
	if _, ok := reg.Lookup("no-such-engine"); ok {
		t.Fatal("Lookup of unknown engine unexpectedly succeeded")
	}
}

// TestBuildAdapterRegistry_FreshPerCall proves repeated daemon starts (in-process
// restart, integration tests) do not accumulate duplicate registrations: each
// buildAdapterRegistry returns an independent registry with no shared mutable
// package state.
func TestBuildAdapterRegistry_FreshPerCall(t *testing.T) {
	r1, err := buildAdapterRegistry()
	if err != nil {
		t.Fatalf("first buildAdapterRegistry: %v", err)
	}
	r2, err := buildAdapterRegistry()
	if err != nil {
		t.Fatalf("second buildAdapterRegistry: %v", err)
	}
	if r1 == r2 {
		t.Fatal("buildAdapterRegistry returned the same registry pointer twice; must be fresh per call")
	}
	if r1.Len() != r2.Len() {
		t.Fatalf("registry lens differ across calls: %d vs %d", r1.Len(), r2.Len())
	}
}

// TestBuildAdapterRegistry_OpenCodeReachable confirms the engine the real E2E
// targets (opencode) is present and addressable purely through the common
// interface — the only surface the supervisor/scheduler may touch (spec §13.3).
func TestBuildAdapterRegistry_OpenCodeReachable(t *testing.T) {
	reg, err := buildAdapterRegistry()
	if err != nil {
		t.Fatalf("buildAdapterRegistry: %v", err)
	}
	a, ok := reg.Lookup("opencode")
	if !ok {
		t.Fatal("opencode engine not registered")
	}
	if a.ID() != "opencode" {
		t.Fatalf("opencode adapter reported id %q", a.ID())
	}
}
