package codingagent

import (
	"fmt"
	"sort"
	"sync"
)

// Registry maps engine ids to [Adapter] implementations (spec §13.3, AC-6).
// Adding a new coding agent must be purely additive: registering an adapter
// never requires changes to the scheduler, schema, dashboard or routing core.
// The Registry is the only place the core learns which engines exist.
type Registry struct {
	mu       sync.RWMutex
	byID     map[string]Adapter
	priority map[string]int
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{byID: map[string]Adapter{}, priority: map[string]int{}}
}

// Register adds an adapter. It returns an error if an adapter with the same id
// is already registered, so duplicates are caught explicitly (no silent
// override). priority orders engines in listings (higher first); equal
// priorities are alphabetical by id.
func (r *Registry) Register(a Adapter, priority int) error {
	if a == nil {
		return fmt.Errorf("codingagent: cannot register a nil adapter")
	}
	id := a.ID()
	if id == "" {
		return fmt.Errorf("codingagent: adapter has empty ID")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[id]; ok {
		return fmt.Errorf("codingagent: adapter %q already registered", id)
	}
	r.byID[id] = a
	r.priority[id] = priority
	return nil
}

// MustRegister is a convenience that panics on registration error. Intended for
// wiring code with hard-coded, known-good adapters.
func (r *Registry) MustRegister(a Adapter, priority int) {
	if err := r.Register(a, priority); err != nil {
		panic(err)
	}
}

// Lookup returns the adapter for an engine id, or false if unknown.
func (r *Registry) Lookup(id string) (Adapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.byID[id]
	return a, ok
}

// IDs returns the registered engine ids in display order (priority desc, then
// id asc).
func (r *Registry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.byID))
	for id := range r.byID {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		pi, pj := r.priority[ids[i]], r.priority[ids[j]]
		if pi != pj {
			return pi > pj
		}
		return ids[i] < ids[j]
	})
	return ids
}

// All returns every registered adapter in display order.
func (r *Registry) All() []Adapter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := r.IDs()
	out := make([]Adapter, 0, len(ids))
	for _, id := range ids {
		out = append(out, r.byID[id])
	}
	return out
}

// Len reports the number of registered adapters.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byID)
}

// defaultRegistry is the package-level registry used by the daemon wiring. The
// daemon registers built-in adapters (fake, declarative-loaded, plugins) into
// it during startup.
var defaultRegistry = NewRegistry()

// Default returns the package-level registry.
func Default() *Registry { return defaultRegistry }
