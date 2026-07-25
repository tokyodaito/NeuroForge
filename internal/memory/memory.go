// Package memory implements the structured project memory (spec §22.9).
//
// STATUS: implemented for milestone M12.
//
// Scope: NeuroForge keeps ONLY structured knowledge about a project — never an
// unbounded transcript. Each memory record has a typed category (architecture
// fact, build command, design-system rule, known failure, accepted decision,
// provider quirk), a source, a confidence, a scope, the commit SHA it was
// learned from, and an expiration policy (§22.9).
//
// Memory is durable: it is persisted (the daemon stores records in the project
// memory store) and queryable when assembling a Context Pack (§22.3
// "architectural rules"). The store is append-with-replacement: re-learning a
// fact at the same key updates its value and bumps the version; it never grows
// unboundedly.
//
// The package is pure domain logic and never calls an LLM (rule §22.6).
package memory

import (
	"sort"
	"time"
)

// Category is the typed class of a memory record (§22.9).
type Category string

const (
	CatArchitectureFact Category = "architecture_fact"
	CatBuildCommand     Category = "build_command"
	CatDesignSystemRule Category = "design_system_rule"
	CatKnownFailure     Category = "known_failure"
	CatAcceptedDecision Category = "accepted_decision"
	CatProviderQuirk    Category = "provider_quirk"
)

// IsValid reports whether c is a known category.
func (c Category) IsValid() bool {
	switch c {
	case CatArchitectureFact, CatBuildCommand, CatDesignSystemRule,
		CatKnownFailure, CatAcceptedDecision, CatProviderQuirk:
		return true
	}
	return false
}

// Confidence is the trust level of a memory record (mirrors the quota
// confidence vocabulary, §20.1): high confidence records are surfaced
// preferentially; low-confidence records are shown with a caveat.
type Confidence string

const (
	ConfidenceHigh       Confidence = "high"
	ConfidenceMedium     Confidence = "medium"
	ConfidenceLow        Confidence = "low"
	ConfidenceUnverified Confidence = "unverified"
)

// ExpirationPolicy controls when a memory record becomes stale (§22.9
// "expiration policy"). Records tied to a moving target (a build command, a
// provider quirk) expire and are re-verified; accepted decisions are permanent.
type ExpirationPolicy string

const (
	ExpPermanent      ExpirationPolicy = "permanent"
	ExpOnCommitChange ExpirationPolicy = "on_commit_change"
	ExpTTL            ExpirationPolicy = "ttl"
)

// Record is one structured project memory entry (§22.9).
type Record struct {
	Key        string
	Category   Category
	Value      string
	Source     string
	Confidence Confidence
	Scope      string
	CommitSHA  string
	Expiration ExpirationPolicy
	ExpiresAt  time.Time // only for ExpTTL
	LearnedAt  time.Time
	Version    int
}

// IsExpired reports whether the record is stale as of now.
func (r Record) IsExpired(now time.Time) bool {
	switch r.Expiration {
	case ExpPermanent:
		return false
	case ExpTTL:
		return !r.ExpiresAt.IsZero() && now.After(r.ExpiresAt)
	}
	return false
}

// Store holds the structured project memory. It is safe for concurrent use.
// The store is keyed by (Category, Key) so re-learning a fact at the same key
// updates it rather than duplicating.
type Store struct {
	projectID string
	records   map[string]Record
	clock     func() time.Time
}

// NewStore returns an empty in-memory store for a project. The daemon wraps this
// with a durable backing store (the §31 project_memory table).
func NewStore(projectID string) *Store {
	return &Store{
		projectID: projectID,
		records:   map[string]Record{},
		clock:     func() time.Time { return time.Now().UTC() },
	}
}

// SetClock injects a clock (tests).
func (s *Store) SetClock(now func() time.Time) {
	if now != nil {
		s.clock = now
	}
}

func keyFor(cat Category, k string) string { return string(cat) + "::" + k }

// Learn records or updates a memory fact. Re-learning the same key bumps the
// version and refreshes LearnedAt. It validates the category.
func (s *Store) Learn(r Record) (Record, error) {
	if r.Category == "" || !r.Category.IsValid() {
		return Record{}, ErrInvalidCategory
	}
	if r.Key == "" {
		return Record{}, ErrEmptyKey
	}
	if r.Confidence == "" {
		r.Confidence = ConfidenceMedium
	}
	if r.Expiration == "" {
		r.Expiration = ExpPermanent
	}
	k := keyFor(r.Category, r.Key)
	r.Version = s.records[k].Version + 1
	r.LearnedAt = s.clock()
	s.records[k] = r
	return r, nil
}

// Get returns a record by category+key.
func (s *Store) Get(cat Category, k string) (Record, bool) {
	r, ok := s.records[keyFor(cat, k)]
	return r, ok
}

// All returns all records in deterministic (category, key) order, dropping
// expired TTL records.
func (s *Store) All() []Record {
	now := s.clock()
	out := make([]Record, 0, len(s.records))
	for _, r := range s.records {
		if r.IsExpired(now) {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// ByCategory returns the records for a category, deterministically ordered.
func (s *Store) ByCategory(cat Category) []Record {
	all := s.All()
	out := all[:0]
	for _, r := range all {
		if r.Category == cat {
			out = append(out, r)
		}
	}
	return out
}

// Forget removes a record (e.g. when a known failure is resolved and accepted).
func (s *Store) Forget(cat Category, k string) bool {
	key := keyFor(cat, k)
	_, ok := s.records[key]
	delete(s.records, key)
	return ok
}

// ForgetCategory removes all records of a category.
func (s *Store) ForgetCategory(cat Category) int {
	n := 0
	for k, r := range s.records {
		if r.Category == cat {
			delete(s.records, k)
			n++
		}
	}
	return n
}

// PruneStale removes on_commit_change records whose commit is no longer the
// current head (the caller passes the current HEAD), and expired TTL records.
// Returns the number pruned.
func (s *Store) PruneStale(currentHEAD string) int {
	now := s.clock()
	n := 0
	for k, r := range s.records {
		if r.IsExpired(now) {
			delete(s.records, k)
			n++
			continue
		}
		if r.Expiration == ExpOnCommitChange && r.CommitSHA != "" && r.CommitSHA != currentHEAD {
			delete(s.records, k)
			n++
		}
	}
	return n
}

// HighConfidenceRules returns the architectural/design rules that are
// high-confidence — these are what a Context Pack (§22.3) surfaces as stable
// "architectural rules".
func (s *Store) HighConfidenceRules() []string {
	var out []string
	for _, r := range s.All() {
		if r.Confidence == ConfidenceHigh &&
			(r.Category == CatArchitectureFact || r.Category == CatDesignSystemRule ||
				r.Category == CatAcceptedDecision) {
			out = append(out, r.Value)
		}
	}
	return out
}

// KnownFailures returns the known-failure entries (used by §22.3 "recent
// failures" in a Context Pack).
func (s *Store) KnownFailures() []string {
	var out []string
	for _, r := range s.ByCategory(CatKnownFailure) {
		out = append(out, r.Value)
	}
	return out
}

// Sentinel errors.
var (
	ErrInvalidCategory = errString("memory: invalid category")
	ErrEmptyKey        = errString("memory: empty key")
)

type errString string

func (e errString) Error() string { return string(e) }
