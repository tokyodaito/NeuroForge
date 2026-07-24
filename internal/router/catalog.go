package router

import (
	"sort"

	"neuroforge/internal/quota"
)

// Catalog maps abstract model tiers (§19.2) to concrete provider-supplied
// models offered through an account. The catalog never hard-codes a model name
// (rule §36.8): entries come from configuration / adapter-supplied
// ModelDescriptors and remain fully opaque to the router core. Engine, model
// and account are kept distinct in each entry (§12.1, §19).
type Catalog struct {
	entries []CatalogEntry
}

// CatalogEntry binds a tier to one concrete (engine, model, account) triple
// plus the economic/capability facts the deterministic scorer needs.
type CatalogEntry struct {
	Tier    Tier
	Engine  string
	Model   string // provider-supplied; opaque to the core (rule §36.8)
	Account string

	// CostPer1MInputUSD / CostPer1MOutputUSD are the list prices per 1M tokens.
	// Zero means "unknown cost"; the scorer treats unknown cost conservatively.
	CostPer1MInputUSD  float64
	CostPer1MOutputUSD float64

	// ContextWindow / MaxOutput in tokens (<=0 unknown).
	ContextWindow int
	MaxOutput     int

	// SupportsImages for multimodal routing (§19.1 image input signal).
	SupportsImages bool

	// SubscriptionIncluded marks routes served from a subscription quota with
	// no marginal paid cost (§23). The router prefers these for soft-budget
	// signals and the budget controller accounts their usage separately.
	SubscriptionIncluded bool

	// Priority biases within-tier selection when all else is equal (higher
	// first). Useful for expressing "preferred engine" without hard-coding.
	Priority int

	// Disabled entries are skipped by the scorer (e.g. a withdrawn model).
	Disabled bool
}

// ID is the stable identity of an entry within the catalog.
func (e CatalogEntry) ID() string { return e.Engine + "/" + e.Model + "/" + e.Account }

// Account returns the quota-scoped account identifier.
func (e CatalogEntry) AccountID() quota.AccountID {
	return quota.AccountID{Engine: e.Engine, Account: e.Account}
}

// NewCatalog constructs an empty catalog.
func NewCatalog() *Catalog { return &Catalog{} }

// Add appends an entry. Duplicate (tier, engine, model, account) triples are
// rejected to keep routing deterministic.
func (c *Catalog) Add(e CatalogEntry) bool {
	for _, ex := range c.entries {
		if ex.Tier == e.Tier && ex.ID() == e.ID() {
			return false
		}
	}
	c.entries = append(c.entries, e)
	return true
}

// Remove drops an entry by identity.
func (c *Catalog) Remove(id string) int {
	n := 0
	out := c.entries[:0]
	for _, ex := range c.entries {
		if ex.ID() == id {
			n++
			continue
		}
		out = append(out, ex)
	}
	c.entries = out
	return n
}

// Entries returns a defensive copy of all entries.
func (c *Catalog) Entries() []CatalogEntry {
	out := make([]CatalogEntry, len(c.entries))
	copy(out, c.entries)
	return out
}

// ByTier returns the enabled entries for a tier, sorted by descending Priority
// then by ID for stable display. Exhausted/unavailable accounts are NOT filtered
// here (the quota manager owns that decision); the scorer applies it.
func (c *Catalog) ByTier(t Tier) []CatalogEntry {
	var out []CatalogEntry
	for _, e := range c.entries {
		if e.Disabled || e.Tier != t {
			continue
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return out[i].ID() < out[j].ID()
	})
	return out
}

// Find returns the entry for a (engine, model, account) triple, if present.
func (c *Catalog) Find(engine, model, account string) (CatalogEntry, bool) {
	for _, e := range c.entries {
		if e.Engine == engine && e.Model == model && e.Account == account {
			return e, true
		}
	}
	return CatalogEntry{}, false
}

// Accounts returns the distinct account identifiers referenced by the catalog.
func (c *Catalog) Accounts() []quota.AccountID {
	seen := map[quota.AccountID]bool{}
	var out []quota.AccountID
	for _, e := range c.entries {
		if !seen[e.AccountID()] {
			seen[e.AccountID()] = true
			out = append(out, e.AccountID())
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Engine != out[j].Engine {
			return out[i].Engine < out[j].Engine
		}
		return out[i].Account < out[j].Account
	})
	return out
}

// Size returns the number of entries.
func (c *Catalog) Size() int { return len(c.entries) }
