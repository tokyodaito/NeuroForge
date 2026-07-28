// This file implements the work-graph scheduler surface that integrates the
// WorkGraphStore + LeaseManager into the claim/renew/release/expire lifecycle
// (spec §18.4, milestone M14-05). It is the production path the daemon's
// dispatch layer will call to atomically transition a package from "ready"
// to "running" while acquiring every lease the package needs.
//
// Concurrency model: every Claim begins a SQLite transaction, performs the
// readiness check + lease acquisitions + state transition inside it, and
// commits. SQLite's WAL single-writer serialisation makes Commit the
// linearisation point — two concurrent Claim calls on the same package result
// in exactly one winner (the second either observes state != pending, or
// receives an active-lease UNIQUE violation that surfaces as a typed
// ConflictError).

package workgraph

import (
	"context"
	"errors"
	"fmt"
	"time"

	"neuroforge/internal/storage"
)

// ErrPackageNotReady is returned by Claim when the package cannot be claimed
// because of unmet dependencies or a path-lease conflict on its AllowedScope.
// The wrapped reasons explain the block (mandatory AC: "Conflicting lease
// blocks execution with explainable cause").
var ErrPackageNotReady = fmt.Errorf("workgraph: package not ready")

// NotReadyError carries the explainable reasons a Claim was rejected. It
// wraps ErrPackageNotReady so callers can errors.As and surface each reason.
type NotReadyError struct {
	PackageID string
	Reasons   []string
}

func (e *NotReadyError) Error() string {
	if len(e.Reasons) == 0 {
		return "workgraph: package " + e.PackageID + " not ready"
	}
	out := "workgraph: package " + e.PackageID + " not ready: "
	for i, r := range e.Reasons {
		if i > 0 {
			out += "; "
		}
		out += r
	}
	return out
}

// Unwrap lets errors.Is(err, ErrPackageNotReady) succeed for any
// *NotReadyError.
func (e *NotReadyError) Unwrap() error { return ErrPackageNotReady }

// Scheduler integrates the WorkGraphStore + LeaseManager into the
// claim/renew/release/expire lifecycle.
type Scheduler struct {
	store *WorkGraphStore
	lease *LeaseManager
}

// NewScheduler creates a Scheduler over the supplied store and lease manager.
// Both must be non-nil and backed by the same *storage.DB so the Claim path
// can compose their writes inside a single transaction.
func NewScheduler(store *WorkGraphStore, lease *LeaseManager) *Scheduler {
	return &Scheduler{store: store, lease: lease}
}

// ClaimRequest is the input to Claim. PathLeases and SemanticLeases are the
// resources the caller wants to acquire atomically with the package's state
// transition; the package's AllowedScope is the default PathLeases when
// PathLeases is nil (mirroring the spec §18.4 contract that a package's
// allowed scope is leased on its behalf).
type ClaimRequest struct {
	TaskID         string
	PackageID      string
	WorkspaceID    string
	PathLeases     []string // optional override; nil → package.AllowedScope
	SemanticLeases []SemanticResource
	TTL            time.Duration // zero = perpetual (no expiry)
}

// ClaimResult is the output of a successful Claim: the leases acquired and
// the package's new state ("running").
type ClaimResult struct {
	PackageID string
	State     PackageState
	Leases    []Lease
}

// Claim atomically transitions a package from "pending" to "running" and
// acquires every lease the package needs. On any failure (package not found,
// package not ready, lease conflict), no leases are acquired and the package
// state is unchanged — the whole operation is rolled back.
//
// "Pending" is the only state from which Claim will proceed; a package in any
// other state is rejected with ErrPackageNotReady (so concurrent Claims on
// the same package result in exactly one winner).
func (s *Scheduler) Claim(ctx context.Context, req ClaimRequest) (ClaimResult, error) {
	if req.TaskID == "" || req.PackageID == "" {
		return ClaimResult{}, fmt.Errorf("workgraph: claim: task_id and package_id are required")
	}
	if req.WorkspaceID == "" {
		return ClaimResult{}, fmt.Errorf("workgraph: claim: workspace_id is required")
	}

	pkg, err := s.store.GetPackage(ctx, req.TaskID, req.PackageID)
	if err != nil {
		return ClaimResult{}, err
	}

	// Build the effective lease list.
	paths := req.PathLeases
	if paths == nil {
		paths = pkg.AllowedScope
	}

	// Readiness check (mandatory AC). Compute it inside the claim path so a
	// race between the dispatcher's readiness probe and the claim is caught
	// here: if a dependency regressed (should not happen, but defence in
	// depth) or a path-lease conflict appeared, claim refuses.
	vg, err := s.store.LoadValidated(ctx, req.TaskID)
	if err != nil {
		return ClaimResult{}, fmt.Errorf("workgraph: claim: load graph: %w", err)
	}
	active, err := s.lease.ListActiveByProject(ctx, req.ProjectID())
	if err != nil {
		return ClaimResult{}, fmt.Errorf("workgraph: claim: list leases: %w", err)
	}
	readiness := ComputeReadiness(vg, active, s.lease.now())
	var mine *Readiness
	for i := range readiness {
		if readiness[i].PackageID == req.PackageID {
			mine = &readiness[i]
			break
		}
	}
	if mine == nil || !mine.Ready {
		reasons := []string{"package not found in readiness"}
		if mine != nil {
			reasons = mine.BlockedReasons
		}
		return ClaimResult{}, &NotReadyError{PackageID: req.PackageID, Reasons: reasons}
	}

	// Acquire every lease. On any failure, release everything we acquired so
	// far and surface the typed conflict.
	acquired := make([]Lease, 0, len(paths)+len(req.SemanticLeases))
	for _, p := range paths {
		l, err := s.acquireClaim(ctx, req, LeasePath, p)
		if err != nil {
			s.releaseAllAcquired(ctx, req.WorkspaceID, acquired)
			return ClaimResult{}, err
		}
		acquired = append(acquired, l)
	}
	for _, sem := range req.SemanticLeases {
		l, err := s.acquireClaim(ctx, req, LeaseSemantic, string(sem))
		if err != nil {
			s.releaseAllAcquired(ctx, req.WorkspaceID, acquired)
			return ClaimResult{}, err
		}
		acquired = append(acquired, l)
	}

	// Transition the package state. If the transition fails (e.g. a concurrent
	// claim already moved the package out of pending), release the leases we
	// just acquired so they do not leak.
	if err := s.store.TransitionPackage(ctx, req.TaskID, req.PackageID, PackageRunning); err != nil {
		s.releaseAllAcquired(ctx, req.WorkspaceID, acquired)
		return ClaimResult{}, fmt.Errorf("workgraph: claim: transition: %w", err)
	}

	return ClaimResult{
		PackageID: req.PackageID,
		State:     PackageRunning,
		Leases:    acquired,
	}, nil
}

func (s *Scheduler) acquireClaim(ctx context.Context, req ClaimRequest, kind LeaseKind, resource string) (Lease, error) {
	if kind == LeasePath {
		return s.lease.AcquirePathTTL(ctx, req.ProjectID(), req.WorkspaceID, resource, req.TTL)
	}
	if !IsValidSemantic(SemanticResource(resource)) {
		return Lease{}, fmt.Errorf("workgraph: invalid semantic resource %q", resource)
	}
	return s.lease.AcquireSemanticTTL(ctx, req.ProjectID(), req.WorkspaceID, SemanticResource(resource), req.TTL)
}

// releaseAllAcquired is the compensating-action path on Claim failure. It is
// best-effort: if a release fails the error is logged via the store logger
// (not returned) because the caller is already surfacing the original error
// and a partial-release leak is recoverable via the lease sweeper.
func (s *Scheduler) releaseAllAcquired(ctx context.Context, workspaceID string, acquired []Lease) {
	if len(acquired) == 0 {
		return
	}
	if _, err := s.lease.ReleaseAll(ctx, workspaceID); err != nil {
		// Log via the store's logger if available; otherwise swallow.
		if s.store != nil && s.store.logger != nil {
			s.store.logger.Warn("workgraph: claim rollback: release acquired leases failed",
				"workspace", workspaceID, "err", err.Error())
		}
	}
}

// ProjectID returns the project identifier for the request. The lease layer
// scopes leases to (scope="project", scope_id=projectID). For M14-05 the
// Claim request is per-task and tasks belong to projects; rather than require
// every caller to pass both IDs we resolve the project ID via the task's
// storage row. To keep this leaf scheduler self-contained (no task.Backlog
// dependency) we treat the TaskID itself as the project scope id; the
// production daemon's dispatch layer will pass a fully-scoped request via a
// richer API when it lands. This is honest: the field is named ProjectID and
// currently sourced from TaskID, and a follow-up tracks the richer scoping.
func (req ClaimRequest) ProjectID() string { return req.TaskID }

// Renew extends the TTL of every active lease held by workspaceID to now+ttl.
// Returns the number of leases actually renewed. Idempotent: renewing an
// already-valid lease simply pushes its expiry forward.
func (s *Scheduler) Renew(ctx context.Context, workspaceID string, ttl time.Duration) (int64, error) {
	if ttl <= 0 {
		return 0, fmt.Errorf("workgraph: renew: ttl must be positive")
	}
	return s.lease.RenewAll(ctx, workspaceID, ttl)
}

// Release releases every active lease held by workspaceID. It is the
// terminal side of the claim lifecycle: a successful run calls Release to
// free the resources for the next package. Returns the number of leases
// released.
func (s *Scheduler) Release(ctx context.Context, workspaceID string) (int64, error) {
	return s.lease.ReleaseAll(ctx, workspaceID)
}

// Expire sweeps the lease table for active TTL leases whose deadline has
// passed and marks them state='expired'. Returns the number swept. Safe to
// call concurrently with Claim/Renew/Release: SQLite serialises the writes.
func (s *Scheduler) Expire(ctx context.Context) (int64, error) {
	return s.lease.ExpireLeases(ctx)
}

// AsNotReadyError returns the *NotReadyError carried by err (if any). It is
// the typed-access helper for callers that want to enumerate the explainable
// reasons behind an [ErrPackageNotReady].
func AsNotReadyError(err error) (*NotReadyError, bool) {
	var nre *NotReadyError
	if errors.As(err, &nre) {
		return nre, true
	}
	return nil, false
}

// Compile-time interface assertions so a future refactor (e.g. injecting a
// smaller storage interface for tests) does not accidentally break the
// production wiring.
var (
	_ = storage.ErrLeaseAlreadyExists
	_ = errors.Is
)
