package imageprovider

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"neuroforge/internal/adapter/imageprovider/protocol"
)

// Registry maps image-provider ids to [Adapter] implementations (spec §14,
// ADR-0006). Adding a new image provider is purely additive: registering an
// adapter never requires changes to the scheduler, schema, dashboard, design
// engine or routing core (rule §13.3 applies by analogy). The Registry is the
// only place the core learns which image providers exist.
//
// Real image calls are opt-in (rule §33: no real providers in CI). The default
// registry therefore ships with the fake provider; GPT Image and Nano Banana
// are registered only when their accounts are configured (§14.1).
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
// is already registered (no silent override). priority orders providers in
// listings (higher first); equal priorities are alphabetical by id.
func (r *Registry) Register(a Adapter, priority int) error {
	if a == nil {
		return fmt.Errorf("imageprovider: cannot register a nil adapter")
	}
	id := a.ID()
	if id == "" {
		return fmt.Errorf("imageprovider: adapter has empty ID")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[id]; ok {
		return fmt.Errorf("imageprovider: adapter %q already registered", id)
	}
	r.byID[id] = a
	r.priority[id] = priority
	return nil
}

// MustRegister panics on registration error. Intended for wiring code with
// known-good adapters.
func (r *Registry) MustRegister(a Adapter, priority int) {
	if err := r.Register(a, priority); err != nil {
		panic(err)
	}
}

// Lookup returns the adapter for an id, or false if unknown.
func (r *Registry) Lookup(id string) (Adapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.byID[id]
	return a, ok
}

// IDs returns the registered provider ids in display order (priority desc, id
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

// ModelsByTier returns the registered adapters capable of producing the given
// tier, in display order. The router consults this when selecting an image
// route (§19, §14.3). It never returns adapters whose model list errors.
func (r *Registry) ModelsByTier(ctx context.Context, tier protocol.ImageTier) []protocol.ImageModel {
	out := []protocol.ImageModel{}
	for _, a := range r.All() {
		models, err := a.ListModels(ctx, protocol.Account{})
		if err != nil {
			continue
		}
		for _, m := range models {
			if m.Tier == tier {
				out = append(out, m)
			}
		}
	}
	return out
}

// defaultRegistry is the package-level registry used by daemon wiring. The
// daemon registers built-in adapters (always the fake; GPT Image / Nano Banana
// only when configured) into it during startup.
var defaultRegistry = NewRegistry()

// Default returns the package-level registry.
func Default() *Registry { return defaultRegistry }
