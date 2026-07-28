// This file implements the readiness calculation (spec §18.3, §18.4,
// milestone M14-05). For each work package in a ValidatedWorkGraph, given a
// snapshot of active leases held by other workspaces, it computes whether
// the package is eligible to be dispatched ("ready") and, if not, the
// concrete reasons that block it.
//
// Readiness is a pure function of (graph, active leases, now). It performs no
// I/O and mutates no state. The dispatcher / scheduler calls it before
// claiming a package so it can pick a runnable package and surface a
// human-readable reason for every non-runnable one.
//
// Blocking factors (mandatory ACs):
//
//   - Package state is not "pending": terminal / running / blocked / ready
//     packages are not (re-)dispatchable from the readiness calculator's
//     perspective. The calculator does not promote "ready" → "running"; that
//     is the claim operation's job.
//   - A dependency has not reached state="succeeded": the dependent package
//     stays not-runnable with an explainable cause naming the lagging
//     dependency and its current state.
//   - A path in the package's AllowedScope is currently leased by another
//     workspace: the package is blocked with an explainable cause naming the
//     path and the holding workspace.
//
// Semantic lease enforcement is done at claim time (the claim operation
// attempts to acquire the semantic resources the package needs and returns a
// typed conflict if any is unavailable). Readiness reports path-lease
// conflicts statically because AllowedScope is part of the package definition;
// semantic needs are runtime-supplied and thus checked at claim.

package workgraph

import (
	"fmt"
	"sort"
	"time"
)

// Readiness is the verdict for one package: ready, or not-ready with the
// concrete reasons. Reasons are stable strings suitable for both humans and
// machine consumers; one reason per blocking factor.
type Readiness struct {
	PackageID      string
	State          PackageState
	Ready          bool
	BlockedReasons []string
}

// HasReason reports whether the readiness verdict contains a reason whose
// text contains substr. Used in tests to assert a specific explainable cause
// without coupling to formatting details.
func (r Readiness) HasReason(substr string) bool {
	for _, reason := range r.BlockedReasons {
		if containsCI(reason, substr) {
			return true
		}
	}
	return false
}

// containsCI is a case-insensitive substring contains, kept local to avoid
// pulling strings.ToLower (which allocates) into the hot path of every lease
// snapshot. The comparison is byte-wise ASCII-only, which is sufficient for
// the deterministic reason strings this package emits.
func containsCI(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	if len(substr) > len(s) {
		return false
	}
	for i := 0; i+len(substr) <= len(s); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			a := s[i+j]
			b := substr[j]
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// ComputeReadiness computes the readiness verdict for every package in v. The
// activeLeases slice is the snapshot of currently-held active leases (typically
// from LeaseManager.ListActiveByProject); now is the reference clock for
// expiry checks.
//
// Deterministic: packages are returned in canonical (sorted-by-ID) order, and
// the blocked-reasons slice for a package is sorted lexicographically so two
// computations over the same inputs produce byte-identical results.
func ComputeReadiness(v *ValidatedWorkGraph, activeLeases []Lease, now time.Time) []Readiness {
	if v == nil {
		return nil
	}
	packages := v.Packages()
	stateByID := make(map[string]PackageState, len(packages))
	for _, p := range packages {
		stateByID[p.ID] = p.State
	}

	out := make([]Readiness, 0, len(packages))
	for _, p := range packages {
		out = append(out, packageReadiness(p, stateByID, activeLeases, now))
	}
	// The validated graph already returns packages in canonical (sorted-by-ID)
	// order, so the output is deterministic without an explicit re-sort. We
	// still sort defensively in case a future change to Packages() relaxes
	// that ordering guarantee.
	sort.SliceStable(out, func(i, j int) bool { return out[i].PackageID < out[j].PackageID })
	return out
}

func packageReadiness(p WorkPackage, stateByID map[string]PackageState, activeLeases []Lease, now time.Time) Readiness {
	r := Readiness{PackageID: p.ID, State: p.State}

	// Terminal / non-pending states are not (re-)dispatchable. The reasons
	// distinguish terminal from in-flight so a human can tell "done" apart
	// from "already running".
	if p.State.IsTerminal() {
		r.Ready = false
		r.BlockedReasons = append(r.BlockedReasons, fmt.Sprintf(
			"package state %q is terminal", p.State))
		return r
	}
	if p.State != PackagePending {
		r.Ready = false
		r.BlockedReasons = append(r.BlockedReasons, fmt.Sprintf(
			"package state %q is not pending", p.State))
		return r
	}

	// Dependency readiness: every dependency must be in state="succeeded".
	// Reasons are emitted in the dependency list's order (which is canonical
	// for a ValidatedWorkGraph — sorted lexicographically by the validator).
	for _, dep := range p.Dependencies {
		depState, ok := stateByID[dep]
		if !ok {
			// Defence-in-depth: the validator rejects missing edges, so this
			// branch is unreachable in production. We still block rather than
			// silently dispatch.
			r.BlockedReasons = append(r.BlockedReasons, fmt.Sprintf(
				"dependency %q is not in the graph", dep))
			continue
		}
		if depState != PackageSucceeded {
			r.BlockedReasons = append(r.BlockedReasons, fmt.Sprintf(
				"dependency %q not succeeded (state=%s)", dep, depState))
		}
	}

	// Path-lease conflict: any active lease on a path in AllowedScope held by
	// another workspace blocks the package. A logically-expired lease (state
	// "active" but ExpiresAt in the past) does NOT block: it is treated as
	// released by the calculator, matching HasActiveLease's defence-in-depth.
	for _, path := range p.AllowedScope {
		for _, lease := range activeLeases {
			if lease.Kind != LeasePath {
				continue
			}
			if lease.Resource != path {
				continue
			}
			if lease.IsExpired(now) {
				continue
			}
			r.BlockedReasons = append(r.BlockedReasons, fmt.Sprintf(
				"path %q held by workspace %q", path, lease.WorkspaceID))
		}
	}

	// Canonicalise the reason order so the verdict is deterministic.
	sort.Strings(r.BlockedReasons)
	r.Ready = len(r.BlockedReasons) == 0
	return r
}
