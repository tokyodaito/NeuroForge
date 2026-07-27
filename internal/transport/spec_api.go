package transport

import (
	"context"
	"errors"
	"net/http"
)

// This file implements the spec-management API surface (M14-03): the
// daemon-mediated Task Compiler path. Endpoints live under
// /tasks/{id}/specification because a specification is a per-task resource
// (spec §18.1, §9). The pure compiler (task.Compile, M14-02) is wrapped by the
// daemon adapter (internal/daemon/spec_api.go) which compiles the task's
// description and durably persists the result through task.SpecificationStore
// (M14-01).
//
// Layering: transport owns DTOs + handlers + the wire contract only; the
// daemon owns the compile + save + audit + version-allocation policy.

// ---- DTOs ----

// AcceptanceCriterionDTO is the wire representation of a single acceptance
// criterion (spec §27). The ID is the durable handle for evidence linkage and
// Merge Governor accounting.
type AcceptanceCriterionDTO struct {
	ID        string `json:"id"`
	Statement string `json:"statement"`
}

// VisualRequirementsDTO mirrors task.VisualRequirements (spec §15, §16).
type VisualRequirementsDTO struct {
	Required   bool     `json:"required"`
	Viewport   string   `json:"viewport,omitempty"`
	Theme      string   `json:"theme,omitempty"`
	Locale     string   `json:"locale,omitempty"`
	Density    string   `json:"density,omitempty"`
	References []string `json:"references,omitempty"`
}

// SpecificationDTO is the wire representation of a durable, versioned task
// specification (spec §18.1). It is the contract the daemon and CLI/TUI agree
// on for compiled-task specifications.
type SpecificationDTO struct {
	TaskID             string                   `json:"task_id"`
	Version            int                      `json:"version"`
	Objective          string                   `json:"objective"`
	AcceptanceCriteria []AcceptanceCriterionDTO `json:"acceptance_criteria"`
	NonGoals           []string                 `json:"non_goals,omitempty"`
	Assumptions        []string                 `json:"assumptions,omitempty"`
	Constraints        []string                 `json:"constraints,omitempty"`
	Risk               string                   `json:"risk"`
	Complexity         string                   `json:"complexity"`
	ProposedScope      []string                 `json:"proposed_scope,omitempty"`
	VisualRequirements VisualRequirementsDTO    `json:"visual_requirements"`
	Locked             bool                     `json:"locked"`
	LockedAt           string                   `json:"locked_at,omitempty"`
	LockedBy           string                   `json:"locked_by,omitempty"`
	CreatedAt          string                   `json:"created_at"`
	CreatedBy          string                   `json:"created_by,omitempty"`
}

// CompileSpecRequest is the body of POST /tasks/{id}/specification/compile. The
// task ID comes from the URL path; the body is intentionally minimal so the
// compile step is idempotent and reproducible from the task's durable state.
//
// LockedBy is an optional provenance hint recorded on the resulting audit
// event chain. If empty the daemon substitutes a default actor.
type CompileSpecRequest struct {
	TaskID   string `json:"-"`
	LockedBy string `json:"locked_by,omitempty"`
}

// CompileSpecResultDTO is the response from POST /tasks/{id}/specification/compile.
//
// Created reports whether a NEW specification version was persisted by this
// call. The compile-and-save operation is idempotent: a second call against the
// same task whose compiled content is byte-identical to the latest version
// returns that latest version unchanged with Created=false.
type CompileSpecResultDTO struct {
	Specification      SpecificationDTO   `json:"specification"`
	Confidence         string             `json:"confidence"`
	UncertaintyReasons []string           `json:"uncertainty_reasons,omitempty"`
	Clarifications     []ClarificationDTO `json:"clarifications,omitempty"`
	RiskReasons        []string           `json:"risk_reasons,omitempty"`
	ComplexityReasons  []string           `json:"complexity_reasons,omitempty"`
	Created            bool               `json:"created"`
}

// ClarificationDTO mirrors task.Clarification (spec §9.7).
type ClarificationDTO struct {
	Question string   `json:"question"`
	Reason   string   `json:"reason,omitempty"`
	Options  []string `json:"options,omitempty"`
}

// LockSpecRequest is the body of POST /tasks/{id}/specification/lock.
type LockSpecRequest struct {
	TaskID   string `json:"-"`
	Version  int    `json:"version"`
	LockedBy string `json:"locked_by,omitempty"`
}

// SpecAPI is implemented by the daemon; the transport server delegates the
// /tasks/{id}/specification* endpoints to it. Compile is the daemon-mediated
// Task Compiler path (task.Compile + task.SpecificationStore.Save with
// idempotent re-compile semantics); Get/GetLatest/ListVersions/Lock delegate
// to task.SpecificationStore.
type SpecAPI interface {
	CompileSpec(ctx context.Context, req CompileSpecRequest) (CompileSpecResultDTO, error)
	GetSpecification(ctx context.Context, taskID string, version int) (SpecificationDTO, error)
	ListSpecificationVersions(ctx context.Context, taskID string) ([]int, error)
	LockSpecification(ctx context.Context, req LockSpecRequest) (SpecificationDTO, error)
}

// registerSpecRoutes wires the /tasks/{id}/specification* endpoints onto mux.
// Each handler requires the bearer token (via withToken) and delegates to the
// configured SpecAPI. Nil SpecAPI yields HTTP 503 so test servers (and any
// future embedder) can opt out.
func (s *Server) registerSpecRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /tasks/{id}/specification/compile", s.withToken(s.handleCompileSpec))
	mux.HandleFunc("GET /tasks/{id}/specification", s.withToken(s.handleGetSpecification))
	mux.HandleFunc("GET /tasks/{id}/specification/versions", s.withToken(s.handleListSpecVersions))
	mux.HandleFunc("POST /tasks/{id}/specification/lock", s.withToken(s.handleLockSpec))
}

// ---- handlers ----

func (s *Server) handleCompileSpec(w http.ResponseWriter, r *http.Request) {
	if s.cfg.SpecAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "spec API not configured")
		return
	}
	id := r.PathValue("id")
	var req CompileSpecRequest
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	req.TaskID = id
	out, err := s.cfg.SpecAPI.CompileSpec(r.Context(), req)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetSpecification(w http.ResponseWriter, r *http.Request) {
	if s.cfg.SpecAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "spec API not configured")
		return
	}
	id := r.PathValue("id")
	version := 0
	if v := r.URL.Query().Get("version"); v != "" {
		// Parse but do not silently coerce: a non-integer version is a client
		// bug and must surface as 400, not a misleading "specification not
		// found" later.
		n, err := parseIntStrict(v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "version query param must be a non-negative integer")
			return
		}
		version = n
	}
	out, err := s.cfg.SpecAPI.GetSpecification(r.Context(), id, version)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleListSpecVersions(w http.ResponseWriter, r *http.Request) {
	if s.cfg.SpecAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "spec API not configured")
		return
	}
	id := r.PathValue("id")
	versions, err := s.cfg.SpecAPI.ListSpecificationVersions(r.Context(), id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if versions == nil {
		versions = []int{}
	}
	writeJSON(w, http.StatusOK, versions)
}

func (s *Server) handleLockSpec(w http.ResponseWriter, r *http.Request) {
	if s.cfg.SpecAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "spec API not configured")
		return
	}
	id := r.PathValue("id")
	var req LockSpecRequest
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	req.TaskID = id
	out, err := s.cfg.SpecAPI.LockSpecification(r.Context(), req)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// parseIntStrict parses a non-negative integer. Returns an error on overflow,
// sign, or non-digit content. Used for ?version=N parsing.
func parseIntStrict(s string) (int, error) {
	if s == "" {
		return 0, errors.New("empty integer")
	}
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, errors.New("non-digit character")
		}
		n = n*10 + int(c-'0')
		if n < 0 {
			return 0, errors.New("integer overflow")
		}
	}
	return n, nil
}
