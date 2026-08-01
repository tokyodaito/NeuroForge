package builtin_test

import (
	"context"
	"errors"
	"sort"
	"testing"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/builtin"
)

// canonicalIDs is the complete set of first-party NeuroForge coding engines
// (spec §12, AC-5). The integration must surface exactly these six.
var canonicalIDs = map[string]bool{
	builtin.IDCodex:    true,
	builtin.IDClaude:   true,
	builtin.IDGemini:   true,
	builtin.IDKimi:     true,
	builtin.IDGrok:     true,
	builtin.IDOpenCode: true,
}

func newRegistry(t *testing.T) *codingagent.Registry {
	t.Helper()
	reg := codingagent.NewRegistry()
	if err := builtin.RegisterAll(reg); err != nil {
		t.Fatalf("RegisterAll failed: %v", err)
	}
	return reg
}

// TestRegisterAll_Discovery verifies the integration discovers all six
// first-party adapters and nothing else (AC-5).
func TestRegisterAll_Discovery(t *testing.T) {
	reg := newRegistry(t)
	if got := reg.Len(); got != 6 {
		t.Fatalf("registry Len = %d, want 6", got)
	}

	ids := reg.IDs()
	if len(ids) != 6 {
		t.Fatalf("registry IDs len = %d, want 6", len(ids))
	}
	for _, id := range ids {
		if !canonicalIDs[id] {
			t.Errorf("unexpected registered engine id %q", id)
		}
	}
	// Every canonical id must be discoverable via Lookup.
	for id := range canonicalIDs {
		if _, ok := reg.Lookup(id); !ok {
			t.Errorf("canonical engine %q not registered", id)
		}
	}
}

// TestIDs_CanonicalOrder verifies the built-in id list is complete and in the
// declared priority order (spec presentation order).
func TestIDs_CanonicalOrder(t *testing.T) {
	want := []string{
		builtin.IDCodex,
		builtin.IDClaude,
		builtin.IDGemini,
		builtin.IDKimi,
		builtin.IDGrok,
		builtin.IDOpenCode,
	}
	got := builtin.IDs()
	if len(got) != len(want) {
		t.Fatalf("IDs len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("IDs[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

// TestRegisteredIDs_Unique verifies engine ids are unique across the registry
// (no two adapters may share an id — spec §12.1, AC-6 stability).
func TestRegisteredIDs_Unique(t *testing.T) {
	reg := newRegistry(t)
	ids := reg.IDs()
	seen := make(map[string]int, len(ids))
	for _, id := range ids {
		seen[id]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("engine id %q registered %d times, want 1", id, n)
		}
	}
}

// TestRegisteredAdapters_SatisfyInterface verifies every discovered adapter is
// usable purely through the common codingagent.Adapter interface — the only
// surface the scheduler/supervisor/routing core is permitted to touch (spec
// §13.3, ADR-0005). No concrete adapter type is referenced here.
func TestRegisteredAdapters_SatisfyInterface(t *testing.T) {
	reg := newRegistry(t)
	for _, a := range reg.All() {
		var _ codingagent.Adapter = a // compile-time + runtime interface check
		if a.ID() == "" {
			t.Error("registered adapter has empty ID")
		}
	}
}

// TestDispatch_ViaCommonInterface models the scheduler/supervisor dispatch path
// (supervisor.Run resolves an engine via Registry.Lookup then drives it as a
// codingagent.Adapter). It proves the core can route to any of the six engines
// with zero provider-specific code: the same lookup-and-call works for all.
func TestDispatch_ViaCommonInterface(t *testing.T) {
	// Hermetic PATH: Detect probes the local PATH for provider CLIs. On a host
	// where a real engine binary is installed (e.g. opencode), Detect would
	// spawn the real CLI, which can hang. An empty temp dir makes "binary
	// absent" the deterministic result — which Detect must tolerate anyway.
	t.Setenv("PATH", t.TempDir())

	reg := newRegistry(t)

	// Simulate route resolution for a batch of requested engines, including a
	// duplicate to exercise the cache-like path and an unknown to prove the
	// miss path is a clean error rather than a crash.
	requested := []string{
		builtin.IDCodex, builtin.IDOpenCode, builtin.IDGemini,
		builtin.IDClaude, builtin.IDKimi, builtin.IDGrok, builtin.IDCodex,
	}
	resolved := make([]codingagent.Adapter, 0, len(requested))
	for _, engine := range requested {
		a, ok := reg.Lookup(engine)
		if !ok {
			t.Fatalf("dispatch: registry could not resolve engine %q", engine)
		}
		// The scheduler relies only on Adapter.ID() and Capabilities(); both are
		// part of the common interface and must work uniformly.
		if a.ID() != engine {
			t.Errorf("dispatch: Lookup(%q) returned adapter with ID %q", engine, a.ID())
		}
		resolved = append(resolved, a)
	}
	if len(resolved) != len(requested) {
		t.Fatalf("resolved %d adapters, want %d", len(resolved), len(requested))
	}

	// An unknown engine must be a clean miss, not a panic — the scheduler turns
	// this into a deterministic error.
	if _, ok := reg.Lookup("no-such-engine"); ok {
		t.Error("Lookup of unknown engine unexpectedly succeeded")
	}

	// Every resolved adapter must honour the context-carrying metadata methods of
	// the common interface without provider-specific knowledge. Detect must be
	// safe to call offline (it only probes the local PATH).
	ctx := context.Background()
	for _, a := range resolved {
		_ = a.Detect(ctx)       // never panics; binary absence is a normal result
		_ = a.Capabilities(ctx) // static capability profile
	}
}

// TestRegisterAll_RejectsDuplicates verifies that re-registering the built-ins
// into a registry that already has them fails (uniqueness is enforced, never a
// silent override).
func TestRegisterAll_RejectsDuplicates(t *testing.T) {
	reg := newRegistry(t)
	err := builtin.RegisterAll(reg)
	if err == nil {
		t.Fatal("second RegisterAll returned nil error, want duplicate-registration error")
	}
}

// TestRegisterAll_NilRegistryErrors verifies the guard against a nil registry.
func TestRegisterAll_NilRegistryErrors(t *testing.T) {
	if err := builtin.RegisterAll(nil); err == nil {
		t.Fatal("RegisterAll(nil) returned nil error")
	}
}

// TestRegisterAll_PartialRegistrationIsObservable confirms that a second
// RegisterAll fails at the first duplicate but the first batch remains
// registered (no rollback of prior good registrations).
func TestRegisterAll_PartialRegistrationIsObservable(t *testing.T) {
	reg := newRegistry(t)
	_ = builtin.RegisterAll(reg) // expected to fail on duplicates
	// The original six must still be present.
	if reg.Len() != 6 {
		t.Errorf("after failed re-register, registry Len = %d, want 6", reg.Len())
	}
}

// TestSortedIDsAreStable is a sanity check that sorting the registered ids
// yields exactly the canonical set (defends against accidental id drift).
func TestSortedIDsAreStable(t *testing.T) {
	reg := newRegistry(t)
	ids := reg.IDs()
	sort.Strings(ids)
	want := []string{
		builtin.IDClaude, builtin.IDCodex, builtin.IDGemini,
		builtin.IDGrok, builtin.IDKimi, builtin.IDOpenCode,
	}
	if len(ids) != len(want) {
		t.Fatalf("sorted ids len = %d, want %d (%v)", len(ids), len(want), ids)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Errorf("sorted ids[%d] = %q, want %q", i, ids[i], want[i])
		}
	}
}

// TestRegisterAll_ErrorIsNotTemporary ensures the duplicate error is not
// swallowed as nil anywhere upstream by confirming it satisfies the error
// interface and carries a non-empty message.
func TestRegisterAll_ErrorIsNotTemporary(t *testing.T) {
	reg := newRegistry(t)
	err := builtin.RegisterAll(reg)
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, context.Canceled) {
		t.Error("duplicate error should not wrap context.Canceled")
	}
	if err.Error() == "" {
		t.Error("duplicate error has empty message")
	}
}
