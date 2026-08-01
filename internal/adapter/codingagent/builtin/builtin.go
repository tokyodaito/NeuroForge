package builtin

import (
	"fmt"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/claude"
	"neuroforge/internal/adapter/codingagent/codex"
	"neuroforge/internal/adapter/codingagent/gemini"
	"neuroforge/internal/adapter/codingagent/grok"
	"neuroforge/internal/adapter/codingagent/kimi"
	"neuroforge/internal/adapter/codingagent/opencode"
)

// Canonical NeuroForge engine identifiers for the first-party adapters (spec
// §12.1, §12). These mirror each adapter's ID() return value and are the
// stability contract the rest of the core addresses engines by. The discovery
// test in this package verifies they stay in sync with the adapters.
const (
	IDCodex    = "codex"
	IDClaude   = "claude"
	IDGemini   = "gemini"
	IDKimi     = "kimi"
	IDGrok     = "grok"
	IDOpenCode = "opencode"
)

// Priority constants order the built-in engines in listings (higher first).
// They reflect the spec's presentation order (§6.1 providers, §7.4 toolchain)
// and are otherwise arbitrary.
const (
	PriorityCodex    = 600
	PriorityClaude   = 500
	PriorityGemini   = 400
	PriorityKimi     = 300
	PriorityGrok     = 200
	PriorityOpenCode = 100
)

// constructor pairs an adapter constructor with its registration priority and
// the canonical id it is expected to report. build must not perform network or
// I/O — it only assembles the adapter with its default options.
type constructor struct {
	id       string
	priority int
	build    func() (codingagent.Adapter, error)
}

// constructors returns the built-in engine constructors in priority order.
// Each entry defers to the engine package's own New; no provider-specific
// logic lives here (spec §13.3). Per-engine option overrides come from the
// caller (e.g. the daemon wires its artifacts dir into OpenCode, L4).
func constructors(opts Options) []constructor {
	return []constructor{
		{IDCodex, PriorityCodex, func() (codingagent.Adapter, error) {
			return codex.New(codex.Options{}), nil
		}},
		{IDClaude, PriorityClaude, func() (codingagent.Adapter, error) {
			return claude.New(claude.Options{})
		}},
		{IDGemini, PriorityGemini, func() (codingagent.Adapter, error) {
			return gemini.New(gemini.Options{}), nil
		}},
		{IDKimi, PriorityKimi, func() (codingagent.Adapter, error) {
			return kimi.New(kimi.Options{}), nil
		}},
		{IDGrok, PriorityGrok, func() (codingagent.Adapter, error) {
			return grok.New(grok.Options{}), nil
		}},
		{IDOpenCode, PriorityOpenCode, func() (codingagent.Adapter, error) {
			return opencode.New(opts.OpenCode), nil
		}},
	}
}

// Options carries per-engine construction overrides for daemon wiring. The
// zero value means every engine is built with its default options.
type Options struct {
	// OpenCode overrides the OpenCode adapter options (e.g. ArtifactsDir so
	// malformed agent output lands in the daemon's artifact store instead of
	// the OS temp dir — review finding L4).
	OpenCode opencode.Options
}

// RegisterAll constructs every built-in coding-agent adapter with its default
// options and registers it into reg. It returns an error if any constructor
// fails, if an adapter reports an unexpected id, or if an id collides with an
// already-registered adapter. Partial registration is possible: adapters
// registered before a failure remain registered.
//
// The fake agent and any declarative/plugin adapters are registered separately
// by the daemon; this function only owns the first-party engines.
func RegisterAll(reg *codingagent.Registry) error {
	return RegisterAllWith(reg, Options{})
}

// RegisterAllWith is [RegisterAll] with per-engine option overrides.
func RegisterAllWith(reg *codingagent.Registry, opts Options) error {
	if reg == nil {
		return fmt.Errorf("builtin: nil registry")
	}
	for _, c := range constructors(opts) {
		a, err := c.build()
		if err != nil {
			return fmt.Errorf("builtin: construct %s: %w", c.id, err)
		}
		if a.ID() != c.id {
			return fmt.Errorf("builtin: id mismatch for %s: adapter reported %q", c.id, a.ID())
		}
		if err := reg.Register(a, c.priority); err != nil {
			return fmt.Errorf("builtin: register %s: %w", c.id, err)
		}
	}
	return nil
}

// MustRegisterAll is a convenience that panics on registration error. Intended
// for daemon wiring with known-good defaults.
func MustRegisterAll(reg *codingagent.Registry) {
	if err := RegisterAll(reg); err != nil {
		panic(err)
	}
}

// IDs returns the built-in engine ids in registration (priority) order. It does
// not require a registry and is useful for discovery tests and listings.
func IDs() []string {
	cs := constructors(Options{})
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.id
	}
	return out
}
