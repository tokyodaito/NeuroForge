// Package workgraph builds the work DAG and manages semantic leases.
//
// STATUS: partially implemented (milestone M3 — leases).
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §18.3, §18.4): the full work-graph DAG
// decomposition lands in a later milestone; M3 implements the lease layer that
// prevents concurrent work packages from modifying the same file paths or
// semantic resources (schema, navigation graph, subscription contract, design
// system, build configuration).
//
// Boundaries: leases are advisory records stored in SQLite; this package does
// not itself perform Git mutations.
package workgraph

import (
	"context"
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
	State       string // "active" | "released"
	CreatedAt   time.Time
	ReleasedAt  time.Time
}

// ErrLeaseConflict is returned when an active lease on the same resource blocks
// a new acquisition (spec §18.4: conflicts block — BLOCKED_LEASE).
var ErrLeaseConflict = fmt.Errorf("workgraph: lease conflict (resource already leased)")

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

// AcquirePath attempts to lease a file path for a workspace. Returns
// ErrLeaseConflict if another active workspace already holds it.
func (lm *LeaseManager) AcquirePath(ctx context.Context, projectID, workspaceID, path string) (Lease, error) {
	return lm.acquire(ctx, "project", projectID, string(LeasePath), path, workspaceID)
}

// AcquireSemantic attempts to lease a semantic resource for a workspace.
func (lm *LeaseManager) AcquireSemantic(ctx context.Context, projectID, workspaceID string, res SemanticResource) (Lease, error) {
	if !IsValidSemantic(res) {
		return Lease{}, fmt.Errorf("workgraph: invalid semantic resource %q", res)
	}
	return lm.acquire(ctx, "project", projectID, string(LeaseSemantic), string(res), workspaceID)
}

func (lm *LeaseManager) acquire(ctx context.Context, scope, scopeID, kind, resource, workspaceID string) (Lease, error) {
	// Check for an existing active lease held by a DIFFERENT workspace.
	conflict, err := lm.db.HasActiveLease(ctx, scope, scopeID, kind, resource, workspaceID)
	if err != nil {
		return Lease{}, fmt.Errorf("workgraph: check lease: %w", err)
	}
	if conflict {
		return Lease{}, fmt.Errorf("%w: %s %s", ErrLeaseConflict, kind, resource)
	}

	now := lm.now()
	id, err := lm.db.CreateLease(ctx, storage.Lease{
		Scope:       scope,
		ScopeID:     scopeID,
		Kind:        kind,
		Resource:    resource,
		WorkspaceID: workspaceID,
		State:       "active",
		CreatedAt:   now.Format(time.RFC3339Nano),
	})
	if err != nil {
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
	}, nil
}

// ReleaseAll releases every active lease held by a workspace.
func (lm *LeaseManager) ReleaseAll(ctx context.Context, workspaceID string) (int64, error) {
	n, err := lm.db.ReleaseLeasesByWorkspace(ctx, workspaceID, lm.now().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("workgraph: release leases: %w", err)
	}
	return n, nil
}

// ListActiveByProject returns all active leases for a project.
func (lm *LeaseManager) ListActiveByProject(ctx context.Context, projectID string) ([]Lease, error) {
	rows, err := lm.db.ListActiveLeasesByScope(ctx, "project", projectID)
	if err != nil {
		return nil, err
	}
	return leasesFromStorage(rows), nil
}

// ListByWorkspace returns all leases (active + released) for a workspace.
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
		}
	}
	return out
}
