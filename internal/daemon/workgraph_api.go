package daemon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"neuroforge/internal/transport"
	"neuroforge/internal/workgraph"
)

// This file implements transport.WorkGraphAPI for the production daemon
// (M14-05). It is the daemon-mediated work-graph inspection path (spec
// §18.3): GET /tasks/{id}/workgraph loads the persisted graph through
// workgraph.WorkGraphStore, snapshots the project-scoped active leases through
// workgraph.LeaseManager, and computes the per-package readiness verdicts
// through workgraph.ComputeReadiness — so a single round-trip returns the
// graph AND its dispatchability map.
//
// Layering: this adapter owns DTO conversion + the load+readiness+lease
// composition; it does not own business logic. All mutations go through the
// store / scheduler / lease manager; this surface is read-only inspection.

// workGraphAPIAdapter implements transport.WorkGraphAPI over *Services.
type workGraphAPIAdapter struct {
	svc *Services
}

func newWorkGraphAPIAdapter(svc *Services) *workGraphAPIAdapter {
	return &workGraphAPIAdapter{svc: svc}
}

// GetWorkGraph implements transport.WorkGraphAPI.GetWorkGraph. Returns the
// task's work graph (packages + per-package readiness + the active leases in
// scope) as observed at call time.
func (a *workGraphAPIAdapter) GetWorkGraph(ctx context.Context, taskID string) (transport.WorkGraphDTO, error) {
	if a.svc.Graphs == nil {
		return transport.WorkGraphDTO{}, errors.New("workgraph store not configured")
	}
	if taskID == "" {
		return transport.WorkGraphDTO{}, fmt.Errorf("task_id is required")
	}

	// Resolve the project ID once from the task row. The lease layer scopes
	// leases to (scope="project", scope_id=projectID) so that two packages in
	// DIFFERENT tasks of the SAME project cannot concurrently lease the same
	// path or semantic resource (spec §18.4). Using the task ID here (the
	// M14-05 MAJOR-1 defect) would weaken isolation to per-task and let
	// cross-task conflicts through. The task service is always wired in
	// production (NewServices); a nil Tasks means the daemon is misconfigured.
	if a.svc.Tasks == nil {
		return transport.WorkGraphDTO{}, errors.New("task service not configured")
	}
	task, err := a.svc.Tasks.Get(ctx, taskID)
	if err != nil {
		return transport.WorkGraphDTO{}, err
	}
	projectID := task.ProjectID

	graph, err := a.svc.Graphs.LoadValidated(ctx, taskID)
	if err != nil {
		return transport.WorkGraphDTO{}, err
	}

	// Compute readiness against the current active leases. The readiness
	// calculator needs the active-lease snapshot; we read it through the
	// lease manager, scoped to the project (matching the scope Claim uses so
	// a lease held by a package's workspace in any task of this project is
	// observed here).
	var leases []workgraph.Lease
	if a.svc.Leases != nil {
		leases, err = a.svc.Leases.ListActiveByProject(ctx, projectID)
		if err != nil {
			return transport.WorkGraphDTO{}, fmt.Errorf("list leases: %w", err)
		}
	}
	readiness := workgraph.ComputeReadiness(graph, leases, time.Now())

	// Build the per-package DTOs with the readiness verdict attached.
	byID := make(map[string]workgraph.Readiness, len(readiness))
	for _, r := range readiness {
		byID[r.PackageID] = r
	}
	packages := make([]transport.WorkPackageDTO, 0, len(graph.Packages()))
	for _, p := range graph.Packages() {
		pkgDTO := workPackageToDTO(p)
		if r, ok := byID[p.ID]; ok {
			rDTO := transport.ReadinessDTO{
				Ready:          r.Ready,
				BlockedReasons: sliceOrNilEmpty(r.BlockedReasons),
			}
			pkgDTO.Readiness = &rDTO
		}
		packages = append(packages, pkgDTO)
	}

	dto := transport.WorkGraphDTO{
		TaskID:   taskID,
		Packages: packages,
	}
	// Attach the active-lease snapshot as advisory context (the readiness
	// verdicts already encode the path-lease conflicts; this list lets a UI
	// show "what's currently held" without an extra round-trip).
	for _, l := range leases {
		dto.ActiveLeases = append(dto.ActiveLeases, transport.LeaseDTO{
			ID:          l.ID,
			Kind:        string(l.Kind),
			Resource:    l.Resource,
			WorkspaceID: l.WorkspaceID,
			State:       l.State,
			CreatedAt:   l.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
			ExpiresAt:   formatExpiry(l.ExpiresAt),
		})
	}
	// Also expose a flat readiness array for callers that want a quick
	// scan (the same info is embedded in each package DTO).
	for _, r := range readiness {
		dto.Readiness = append(dto.Readiness, transport.ReadinessSummary{
			PackageID:      r.PackageID,
			Ready:          r.Ready,
			BlockedReasons: sliceOrNilEmpty(r.BlockedReasons),
		})
	}
	return dto, nil
}

// workPackageToDTO converts a domain WorkPackage to its wire DTO. Pure /
// side-effect-free. Declared here (not in transport) because the daemon owns
// the mapping between domain types and wire DTOs.
func workPackageToDTO(p workgraph.WorkPackage) transport.WorkPackageDTO {
	dto := transport.WorkPackageDTO{
		ID:            p.ID,
		TaskID:        p.TaskID,
		Stage:         string(p.Stage),
		Title:         p.Title,
		Objective:     p.Objective,
		AcceptedACIDs: sliceOrNilEmpty(p.AcceptedACIDs),
		AllowedScope:  sliceOrNilEmpty(p.AllowedScope),
		Dependencies:  sliceOrNilEmpty(p.Dependencies),
		State:         string(p.State),
	}
	for _, att := range p.Attempts {
		dto.Attempts = append(dto.Attempts, transport.AttemptDTO{
			Index:         att.Index,
			State:         string(att.State),
			StartedAt:     formatTime(att.StartedAt),
			FinishedAt:    formatTime(att.FinishedAt),
			FailureReason: att.FailureReason,
			ExitCode:      att.ExitCode,
			AgentRunID:    att.AgentRunID,
		})
	}
	return dto
}

// formatTime renders a time.Time as RFC3339, or "" for the zero value.
// Mirrors the spec_api convention (spec_api.go:specificationToDTO).
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02T15:04:05Z07:00")
}

// formatExpiry renders an absolute expiry time, or "" if the lease is
// perpetual (zero).
func formatExpiry(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02T15:04:05Z07:00")
}
