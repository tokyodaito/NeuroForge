// Package workgraph builds the work DAG and manages semantic leases.
//
// STATUS: leases implemented (M3); work-graph domain model, DAG validation, AC
// mapping, deterministic decomposition and stable serialization implemented
// (M14-04); durable work graph, TTL/expiring leases, readiness, claim/renew/
// release/expire scheduling implemented (M14-05).
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §18.3, §18.4): the work-graph DAG and
// its durable substrate, plus the lease layer that prevents concurrent work
// packages from modifying the same file paths or semantic resources (schema,
// navigation graph, subscription contract, design system, build
// configuration).
//
// Boundaries: leases are advisory records stored in SQLite; this package does
// not itself perform Git mutations.
package workgraph

import (
	"context"
	"errors"
	"fmt"
	"time"

	"neuroforge/internal/storage"
)

// SemanticResource is one of the §18.4 semantic resource classes that can be
// leased to prevent concurrent conflicting modifications.
type SemanticResource string

const (
	SemDatabaseSchema       SemanticResource = "database_schema"
	SemNavigationGraph      SemanticResource = "navigation_graph"
	SemSubscriptionContract SemanticResource = "subscription_contract"
	SemDesignSystem         SemanticResource = "design_system"
	SemBuildConfiguration   SemanticResource = "build_configuration"
)

// AllSemanticResources is the complete §18.4 set.
var AllSemanticResources = []SemanticResource{
	SemDatabaseSchema,
	SemNavigationGraph,
	SemSubscriptionContract,
	SemDesignSystem,
	SemBuildConfiguration,
}

// IsValidSemantic reports whether r is a known §18.4 semantic resource.
func IsValidSemantic(r SemanticResource) bool {
	for _, x := range AllSemanticResources {
		if x == r {
			return true
		}
	}
	return false
}

// LeaseKind classifies a lease as a file path or a semantic resource.
type LeaseKind string

const (
	LeasePath     LeaseKind = "path"
	LeaseSemantic LeaseKind = "semantic"
)

// Lease is an advisory lock on a file path or semantic resource.
type Lease struct {
	ID          int64
	Scope       string // "project" | "workspace"
	ScopeID     string
	Kind        LeaseKind
	Resource    string
	WorkspaceID string
	State       string // "active" | "released" | "expired"
	CreatedAt   time.Time
	ReleasedAt  time.Time
	ExpiresAt   time.Time // zero = perpetual
}

// IsExpired reports whether the lease is logically expired at now. A perpetual
// lease (ExpiresAt.IsZero()) is never expired by this check. A lease whose
// ExpiresAt is in the past IS expired even if its State column still reads
// "active": the sweeper is best-effort and HasActiveLease already excludes
// such rows, so the readiness calculator can rely on this predicate to
// predict blocking without waiting for the sweeper (defence-in-depth).
func (l Lease) IsExpired(now time.Time) bool {
	if l.ExpiresAt.IsZero() {
		return false
	}
	return !now.Before(l.ExpiresAt)
}

// ErrLeaseConflict is returned when an active lease on the same resource blocks
// a new acquisition (spec §18.4: conflicts block — BLOCKED_LEASE).
//
// Concrete conflict reasons are wrapped via [ConflictReason] so callers can
// surface an explainable cause ("path X held by workspace Y until Z") to the
// dispatcher / user (mandatory AC: "Conflicting lease blocks execution with
// explainable cause").
var ErrLeaseConflict = fmt.Errorf("workgraph: lease conflict (resource already leased)")

// ConflictReason describes one concrete lease conflict: a held resource, the
// workspace that holds it, and (when known) the expiry of that hold. It is the
// structured payload behind [ErrLeaseConflict].
type ConflictReason struct {
	Kind        LeaseKind
	Resource    string
	WorkspaceID string
	HeldBy      string // human-readable description (e.g. expires-at)
}

// ConflictError is the typed wrapper carried by [ErrLeaseConflict] so a caller
// can errors.As it and surface each conflict individually. The outer error
// string contains every reason (joined) for human reading; the Reasons slice
// is the machine-readable form.
type ConflictError struct {
	Reasons []ConflictReason
}

func (e *ConflictError) Error() string {
	if len(e.Reasons) == 0 {
		return "workgraph: lease conflict"
	}
	out := make([]string, 0, len(e.Reasons))
	for _, r := range e.Reasons {
		out = append(out, fmt.Sprintf("%s %q held by workspace %q%s",
			r.Kind, r.Resource, r.WorkspaceID, suffixIfNonEmpty(r.HeldBy)))
	}
	return "workgraph: lease conflict: " + joinSemis(out)
}

// Unwrap lets errors.Is(err, ErrLeaseConflict) succeed for any *ConflictError.
func (e *ConflictError) Unwrap() error { return ErrLeaseConflict }

func suffixIfNonEmpty(s string) string {
	if s == "" {
		return ""
	}
	return " (" + s + ")"
}

func joinSemis(items []string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += "; "
		}
		out += s
	}
	return out
}

// LeaseManager owns the acquisition and release of path and semantic leases.
// It is backed by durable storage so leases survive daemon restarts.
type LeaseManager struct {
	db  *storage.DB
	now func() time.Time
}

// NewLeaseManager creates a LeaseManager backed by db.
func NewLeaseManager(db *storage.DB) *LeaseManager {
	return &LeaseManager{
		db:  db,
		now: func() time.Time { return time.Now().UTC() },
	}
}

// nowTS returns the manager's clock as an RFC3339Nano string. Used as the
// single source of truth for created_at / released_at / expires_at timestamps
// in this package.
func (lm *LeaseManager) nowTS() string {
	return lm.now().UTC().Format(time.RFC3339Nano)
}

// AcquirePath attempts to lease a file path for a workspace with no expiry
// (perpetual until released). Returns [ErrLeaseConflict] if another active
// workspace already holds it.
func (lm *LeaseManager) AcquirePath(ctx context.Context, projectID, workspaceID, path string) (Lease, error) {
	return lm.acquire(ctx, "project", projectID, string(LeasePath), path, workspaceID, time.Time{})
}

// AcquirePathTTL is the expiring-lease variant of [AcquirePath]. The lease
// expires at now+ttl; a sweeper ([ExpireLeases]) transitions it to
// state='expired' eventually, but [HasActiveLease] already treats it as
// expired once the deadline passes so a slow sweeper cannot falsely block
// execution. A ttl <= 0 means "perpetual" (no expiry) — this mirrors the
// semantic of [AcquirePath] and avoids the footgun where ttl=0 would otherwise
// produce a lease that is born expired.
func (lm *LeaseManager) AcquirePathTTL(ctx context.Context, projectID, workspaceID, path string, ttl time.Duration) (Lease, error) {
	return lm.acquire(ctx, "project", projectID, string(LeasePath), path, workspaceID, expiryFromTTL(lm.now(), ttl))
}

// AcquireSemantic attempts to lease a semantic resource for a workspace with
// no expiry (perpetual until released).
func (lm *LeaseManager) AcquireSemantic(ctx context.Context, projectID, workspaceID string, res SemanticResource) (Lease, error) {
	if !IsValidSemantic(res) {
		return Lease{}, fmt.Errorf("workgraph: invalid semantic resource %q", res)
	}
	return lm.acquire(ctx, "project", projectID, string(LeaseSemantic), string(res), workspaceID, time.Time{})
}

// AcquireSemanticTTL is the expiring-lease variant of [AcquireSemantic]. A
// ttl <= 0 means perpetual (see [AcquirePathTTL]).
func (lm *LeaseManager) AcquireSemanticTTL(ctx context.Context, projectID, workspaceID string, res SemanticResource, ttl time.Duration) (Lease, error) {
	if !IsValidSemantic(res) {
		return Lease{}, fmt.Errorf("workgraph: invalid semantic resource %q", res)
	}
	return lm.acquire(ctx, "project", projectID, string(LeaseSemantic), string(res), workspaceID, expiryFromTTL(lm.now(), ttl))
}

// expiryFromTTL converts a ttl to an absolute expiry time. A ttl <= 0 produces
// the zero time, which [Lease.IsExpired] treats as perpetual. This is the
// single source of truth for the ttl→expiry mapping in this package.
func expiryFromTTL(now time.Time, ttl time.Duration) time.Time {
	if ttl <= 0 {
		return time.Time{}
	}
	return now.Add(ttl)
}

func (lm *LeaseManager) acquire(ctx context.Context, scope, scopeID, kind, resource, workspaceID string, expiresAt time.Time) (Lease, error) {
	// Fast path: there is already an active lease for this exact resource.
	// Re-acquiring the holder's own lease is idempotent (return it); a
	// different workspace's lease is a conflict (return ConflictError with the
	// explainable cause). This pre-check is the common sequential path.
	//
	// A row whose state='active' but expires_at is in the past is logically
	// expired: sweep it to state='expired' inline so the new acquire can
	// proceed. This matches HasActiveLease's defence-in-depth and ensures a
	// slow background sweeper does not falsely block execution.
	now := lm.now()
	if existing, err := lm.db.GetActiveLease(ctx, scope, scopeID, kind, resource); err == nil {
		expires, _ := time.Parse(time.RFC3339Nano, existing.ExpiresAt)
		if expires.IsZero() || now.Before(expires) {
			if existing.WorkspaceID == workspaceID {
				return leaseFromStorage(existing), nil
			}
			return Lease{}, lm.conflictFromRow(existing)
		}
		// Logically expired but state still 'active'. Sweep it inline so we can
		// insert a fresh row beneath the partial UNIQUE index. The sweeper
		// would do this eventually; doing it here makes the reclaim latency
		// independent of the sweeper's cadence (mandatory AC: "lease expiry +
		// reclaim" must be observable without waiting on a cron).
		if _, sweepErr := lm.db.ExpireLeases(ctx, now.Format(time.RFC3339Nano)); sweepErr != nil {
			return Lease{}, fmt.Errorf("workgraph: sweep expired lease before acquire: %w", sweepErr)
		}
	}

	// Cold path: no active lease observed; attempt to insert one. The
	// partial UNIQUE index idx_leases_unique_active_resource is the
	// linearisation point: under concurrent claims, exactly one writer's
	// INSERT commits; every other writer receives ErrLeaseAlreadyExists and
	// re-reads (below) to surface the explainable conflict.
	expires := ""
	if !expiresAt.IsZero() {
		expires = expiresAt.UTC().Format(time.RFC3339Nano)
	}
	id, err := lm.db.CreateLease(ctx, storage.Lease{
		Scope:       scope,
		ScopeID:     scopeID,
		Kind:        kind,
		Resource:    resource,
		WorkspaceID: workspaceID,
		State:       "active",
		CreatedAt:   now.Format(time.RFC3339Nano),
		ExpiresAt:   expires,
	})
	if err != nil {
		if errors.Is(err, storage.ErrLeaseAlreadyExists) {
			// Lost the race. Re-read the winning row to surface the
			// explainable cause (or, if it's our own row, return it
			// idempotently — can happen if a same-workspace concurrent
			// caller won).
			existing, lookupErr := lm.db.GetActiveLease(ctx, scope, scopeID, kind, resource)
			if lookupErr == nil {
				if existing.WorkspaceID == workspaceID {
					return leaseFromStorage(existing), nil
				}
				return Lease{}, lm.conflictFromRow(existing)
			}
			// Could not re-read: surface a generic conflict so the block is
			// not masked (the AC requires an explainable cause; "unknown
			// conflicting lease" is more honest than no error).
			return Lease{}, &ConflictError{Reasons: []ConflictReason{{
				Kind:     LeaseKind(kind),
				Resource: resource,
			}}}
		}
		return Lease{}, fmt.Errorf("workgraph: create lease: %w", err)
	}
	return Lease{
		ID:          id,
		Scope:       scope,
		ScopeID:     scopeID,
		Kind:        LeaseKind(kind),
		Resource:    resource,
		WorkspaceID: workspaceID,
		State:       "active",
		CreatedAt:   now,
		ExpiresAt:   expiresAt,
	}, nil
}

// conflictFromRow builds the typed ConflictError for a held lease row. The
// cause names the resource, the holding workspace, and (when known) the
// expiry of the hold so the dispatcher / user can act on the conflict.
func (lm *LeaseManager) conflictFromRow(held storage.Lease) error {
	ce := ConflictReason{
		Kind:        LeaseKind(held.Kind),
		Resource:    held.Resource,
		WorkspaceID: held.WorkspaceID,
	}
	if held.ExpiresAt != "" {
		ce.HeldBy = "expires at " + held.ExpiresAt
	}
	return &ConflictError{Reasons: []ConflictReason{ce}}
}

// leaseFromStorage converts a storage.Lease to the domain Lease type. Kept
// local to this file because the package-level leasesFromStorage returns a
// slice and is used by List*; the single conversion here is the cold path of
// the conflict lookup.
func leaseFromStorage(r storage.Lease) Lease {
	created, _ := time.Parse(time.RFC3339Nano, r.CreatedAt)
	released, _ := time.Parse(time.RFC3339Nano, r.ReleasedAt)
	expires, _ := time.Parse(time.RFC3339Nano, r.ExpiresAt)
	return Lease{
		ID:          r.ID,
		Scope:       r.Scope,
		ScopeID:     r.ScopeID,
		Kind:        LeaseKind(r.Kind),
		Resource:    r.Resource,
		WorkspaceID: r.WorkspaceID,
		State:       r.State,
		CreatedAt:   created,
		ReleasedAt:  released,
		ExpiresAt:   expires,
	}
}

// ReleaseAll releases every active lease held by a workspace.
func (lm *LeaseManager) ReleaseAll(ctx context.Context, workspaceID string) (int64, error) {
	n, err := lm.db.ReleaseLeasesByWorkspace(ctx, workspaceID, lm.nowTS())
	if err != nil {
		return 0, fmt.Errorf("workgraph: release leases: %w", err)
	}
	return n, nil
}

// RenewAll extends the expiry of every active TTL lease held by a workspace to
// now+ttl. Perpetual leases (no expiry) are left untouched. Returns the number
// of leases actually extended. Idempotent: renewing an already-valid lease
// simply pushes its expiry forward.
func (lm *LeaseManager) RenewAll(ctx context.Context, workspaceID string, ttl time.Duration) (int64, error) {
	newExpiry := lm.now().Add(ttl).UTC().Format(time.RFC3339Nano)
	n, err := lm.db.RenewWorkspaceLeases(ctx, workspaceID, newExpiry)
	if err != nil {
		return 0, fmt.Errorf("workgraph: renew leases: %w", err)
	}
	return n, nil
}

// ExpireLeases sweeps the lease table for active TTL leases whose deadline has
// passed and marks them state='expired'. Returns the number swept. Perpetual
// leases are never expired by this call. Idempotent: re-running it against an
// already-swept table is a no-op.
//
// This is the auditable sweeper. [HasActiveLease] already excludes
// logically-expired rows, so a slow or stalled sweeper cannot falsely block
// execution; the sweeper's job is to convert the soft-expired state into a
// durable state='expired' row that ListActive* stops returning.
func (lm *LeaseManager) ExpireLeases(ctx context.Context) (int64, error) {
	n, err := lm.db.ExpireLeases(ctx, lm.nowTS())
	if err != nil {
		return 0, fmt.Errorf("workgraph: expire leases: %w", err)
	}
	return n, nil
}

// ListActiveByProject returns all active leases for a project. Logically
// expired rows (state='active' but expires_at in the past) are filtered out
// here so callers see only leases that genuinely block; the storage sweeper
// (ExpireLeases) is best-effort.
func (lm *LeaseManager) ListActiveByProject(ctx context.Context, projectID string) ([]Lease, error) {
	rows, err := lm.db.ListActiveLeasesByScope(ctx, "project", projectID)
	if err != nil {
		return nil, err
	}
	out := leasesFromStorage(rows)
	now := lm.now()
	filtered := out[:0]
	for _, l := range out {
		if l.IsExpired(now) {
			continue
		}
		filtered = append(filtered, l)
	}
	return filtered, nil
}

// ListByWorkspace returns all leases (active + released + expired) for a workspace.
func (lm *LeaseManager) ListByWorkspace(ctx context.Context, workspaceID string) ([]Lease, error) {
	rows, err := lm.db.ListLeasesByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	return leasesFromStorage(rows), nil
}

func leasesFromStorage(rows []storage.Lease) []Lease {
	out := make([]Lease, len(rows))
	for i, r := range rows {
		created, _ := time.Parse(time.RFC3339Nano, r.CreatedAt)
		released, _ := time.Parse(time.RFC3339Nano, r.ReleasedAt)
		expires, _ := time.Parse(time.RFC3339Nano, r.ExpiresAt)
		out[i] = Lease{
			ID:          r.ID,
			Scope:       r.Scope,
			ScopeID:     r.ScopeID,
			Kind:        LeaseKind(r.Kind),
			Resource:    r.Resource,
			WorkspaceID: r.WorkspaceID,
			State:       r.State,
			CreatedAt:   created,
			ReleasedAt:  released,
			ExpiresAt:   expires,
		}
	}
	return out
}

// AsConflictError returns the *ConflictError carried by err (if any). It is
// the typed-access helper for callers that want to enumerate the explainable
// causes behind an [ErrLeaseConflict].
func AsConflictError(err error) (*ConflictError, bool) {
	var ce *ConflictError
	if errors.As(err, &ce) {
		return ce, true
	}
	return nil, false
}
