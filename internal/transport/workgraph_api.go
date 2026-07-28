package transport

import (
	"context"
	"net/http"
)

// This file implements the work-graph inspection API surface (spec §18.3,
// milestone M14-05). Endpoints live under /tasks/{id}/workgraph because a work
// graph is a per-task resource. The daemon adapter
// (internal/daemon/workgraph_api.go) loads the graph + computes readiness
// through workgraph.WorkGraphStore + workgraph.LeaseManager.
//
// Layering: transport owns DTOs + handlers + the wire contract only; the
// daemon owns the load + readiness + lease-snapshot policy.

// ---- DTOs ----

// AttemptDTO mirrors workgraph.Attempt on the wire (M14-05).
type AttemptDTO struct {
	Index         int    `json:"index"`
	State         string `json:"state"`
	StartedAt     string `json:"started_at,omitempty"`
	FinishedAt    string `json:"finished_at,omitempty"`
	FailureReason string `json:"failure_reason,omitempty"`
	ExitCode      int    `json:"exit_code,omitempty"`
	AgentRunID    string `json:"agent_run_id,omitempty"`
}

// WorkPackageDTO is the wire representation of one work package.
type WorkPackageDTO struct {
	ID            string        `json:"id"`
	TaskID        string        `json:"task_id"`
	Stage         string        `json:"stage"`
	Title         string        `json:"title"`
	Objective     string        `json:"objective"`
	AcceptedACIDs []string      `json:"accepted_ac_ids"`
	AllowedScope  []string      `json:"allowed_scope,omitempty"`
	Dependencies  []string      `json:"dependencies,omitempty"`
	State         string        `json:"state"`
	Attempts      []AttemptDTO  `json:"attempts,omitempty"`
	Readiness     *ReadinessDTO `json:"readiness,omitempty"`
}

// ReadinessDTO mirrors workgraph.Readiness on the wire.
type ReadinessDTO struct {
	Ready          bool     `json:"ready"`
	BlockedReasons []string `json:"blocked_reasons,omitempty"`
}

// LeaseDTO mirrors workgraph.Lease on the wire (subset suitable for inspection).
type LeaseDTO struct {
	ID          int64  `json:"id"`
	Kind        string `json:"kind"`
	Resource    string `json:"resource"`
	WorkspaceID string `json:"workspace_id"`
	State       string `json:"state"`
	CreatedAt   string `json:"created_at"`
	ExpiresAt   string `json:"expires_at,omitempty"`
}

// WorkGraphDTO is the wire representation of a task's work graph, including
// per-package readiness verdicts and the active leases in scope.
type WorkGraphDTO struct {
	TaskID       string             `json:"task_id"`
	Packages     []WorkPackageDTO   `json:"packages"`
	ActiveLeases []LeaseDTO         `json:"active_leases,omitempty"`
	Readiness    []ReadinessSummary `json:"readiness,omitempty"`
}

// ReadinessSummary is the readiness verdict for one package, in the packages
// order, so a UI can render the whole graph at once.
type ReadinessSummary struct {
	PackageID      string   `json:"package_id"`
	Ready          bool     `json:"ready"`
	BlockedReasons []string `json:"blocked_reasons,omitempty"`
}

// WorkGraphAPI is implemented by the daemon; the transport server delegates
// the GET /tasks/{id}/workgraph endpoint to it. Nil WorkGraphAPI makes the
// endpoint return 503 so test servers can opt out.
type WorkGraphAPI interface {
	GetWorkGraph(ctx context.Context, taskID string) (WorkGraphDTO, error)
}

// registerWorkGraphRoutes wires the /tasks/{id}/workgraph endpoint onto mux.
// Each handler requires the bearer token (via withToken) and delegates to the
// configured WorkGraphAPI. Nil WorkGraphAPI yields HTTP 503.
func (s *Server) registerWorkGraphRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /tasks/{id}/workgraph", s.withToken(s.handleGetWorkGraph))
}

// ---- handlers ----

func (s *Server) handleGetWorkGraph(w http.ResponseWriter, r *http.Request) {
	if s.cfg.WorkGraphAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "workgraph API not configured")
		return
	}
	id := r.PathValue("id")
	out, err := s.cfg.WorkGraphAPI.GetWorkGraph(r.Context(), id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
