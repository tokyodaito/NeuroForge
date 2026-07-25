package visualharness

import (
	"fmt"
	"sort"
	"sync"
)

// Registry maps harness ids to [Harness] implementations. Adding a harness is
// purely additive: registering one never requires changes to the visual
// engine, schema or dashboard.
type Registry struct {
	mu       sync.RWMutex
	byID     map[string]Harness
	priority map[string]int
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{byID: map[string]Harness{}, priority: map[string]int{}}
}

// Register adds a harness.
func (r *Registry) Register(h Harness, priority int) error {
	if h == nil {
		return fmt.Errorf("visualharness: cannot register a nil harness")
	}
	id := h.ID()
	if id == "" {
		return fmt.Errorf("visualharness: harness has empty ID")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[id]; ok {
		return fmt.Errorf("visualharness: harness %q already registered", id)
	}
	r.byID[id] = h
	r.priority[id] = priority
	return nil
}

// MustRegister panics on registration error.
func (r *Registry) MustRegister(h Harness, priority int) {
	if err := r.Register(h, priority); err != nil {
		panic(err)
	}
}

// Lookup returns the harness for an id.
func (r *Registry) Lookup(id string) (Harness, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.byID[id]
	return h, ok
}

// All returns every registered harness in display order.
func (r *Registry) All() []Harness {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := r.IDs()
	out := make([]Harness, 0, len(ids))
	for _, id := range ids {
		out = append(out, r.byID[id])
	}
	return out
}

// IDs returns the registered harness ids in display order (priority desc, id
// asc).
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

// Len reports the number of registered harnesses.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byID)
}

// LookupPlatform returns the first registered harness for a platform.
func (r *Registry) LookupPlatform(p Platform) (Harness, bool) {
	for _, h := range r.All() {
		if h.Platform() == p {
			return h, true
		}
	}
	return nil, false
}

// defaultRegistry is the package-level registry used by daemon wiring.
var defaultRegistry = NewRegistry()

// Default returns the package-level registry.
func Default() *Registry { return defaultRegistry }
