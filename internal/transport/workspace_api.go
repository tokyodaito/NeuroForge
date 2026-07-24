package transport

import (
	"context"
	"net/http"
	"time"
)

// WorkspaceDTO is the wire representation of a workspace.
type WorkspaceDTO struct {
	ID            string `json:"id"`
	ProjectID     string `json:"project_id"`
	TaskID        string `json:"task_id"`
	WorkPackageID string `json:"work_package_id"`
	Attempt       int    `json:"attempt"`
	Path          string `json:"path"`
	Branch        string `json:"branch"`
	ResultBranch  string `json:"result_branch,omitempty"`
	BaseSHA       string `json:"base_sha"`
	HeadSHA       string `json:"head_sha"`
	ResultSHA     string `json:"result_sha,omitempty"`
	State         string `json:"state"`
	Engine        string `json:"engine,omitempty"`
	Model         string `json:"model,omitempty"`
	RunID         string `json:"run_id,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

// CheckpointDTO is the wire representation of a checkpoint.
type CheckpointDTO struct {
	ID        int64  `json:"id"`
	CommitSHA string `json:"commit_sha"`
	Moment    string `json:"moment"`
	Message   string `json:"message"`
	CreatedAt string `json:"created_at"`
}

// CreateWorkspaceRequest is the body of POST /workspaces.
type CreateWorkspaceRequest struct {
	TaskID        string `json:"task_id"`
	WorkPackageID string `json:"work_package_id,omitempty"`
	BaseBranch    string `json:"base_branch,omitempty"`
}

// RunWorkspaceRequest is the body of POST /workspaces/{id}/run.
type RunWorkspaceRequest struct {
	Engine  string        `json:"engine,omitempty"`
	Model   string        `json:"model,omitempty"`
	Prompt  string        `json:"prompt,omitempty"`
	Timeout time.Duration `json:"timeout,omitempty"`
}

// CheckpointRequest is the body of POST /workspaces/{id}/checkpoint.
type CheckpointRequest struct {
	Moment  string `json:"moment"`
	Message string `json:"message"`
}

// ReviewRequest is the body of POST /workspaces/{id}/review.
type ReviewRequest struct {
	Action string `json:"action"` // keep | reject | ask
}

// DiffResponse wraps a diff string.
type DiffResponse struct {
	Diff string `json:"diff"`
}

// PatchResponse wraps a patch string.
type PatchResponse struct {
	Patch string `json:"patch"`
}

// WorkspaceAPI is implemented by the daemon; the transport server delegates to
// it.
type WorkspaceAPI interface {
	CreateWorkspace(ctx context.Context, req CreateWorkspaceRequest) (WorkspaceDTO, error)
	GetWorkspace(ctx context.Context, id string) (WorkspaceDTO, error)
	ListWorkspaces(ctx context.Context, taskID, projectID string) ([]WorkspaceDTO, error)
	DeleteWorkspace(ctx context.Context, id string) error
	RunWorkspace(ctx context.Context, id string, req RunWorkspaceRequest) (WorkspaceDTO, error)
	CheckpointWorkspace(ctx context.Context, id string, req CheckpointRequest) (WorkspaceDTO, error)
	CreateResult(ctx context.Context, id string) (WorkspaceDTO, error)
	ReviewWorkspace(ctx context.Context, id string, req ReviewRequest) (WorkspaceDTO, error)
	DiffWorkspace(ctx context.Context, id string) (DiffResponse, error)
	PatchWorkspace(ctx context.Context, id string) (PatchResponse, error)
	ListCheckpoints(ctx context.Context, id string) ([]CheckpointDTO, error)
}

// registerWorkspaceRoutes wires the workspace endpoints onto mux.
func (s *Server) registerWorkspaceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /workspaces", s.withToken(s.handleListWorkspaces))
	mux.HandleFunc("POST /workspaces", s.withToken(s.handleCreateWorkspace))
	mux.HandleFunc("GET /workspaces/{id}", s.withToken(s.handleGetWorkspace))
	mux.HandleFunc("DELETE /workspaces/{id}", s.withToken(s.handleDeleteWorkspace))
	mux.HandleFunc("POST /workspaces/{id}/run", s.withToken(s.handleRunWorkspace))
	mux.HandleFunc("POST /workspaces/{id}/checkpoint", s.withToken(s.handleCheckpointWorkspace))
	mux.HandleFunc("POST /workspaces/{id}/result", s.withToken(s.handleCreateResult))
	mux.HandleFunc("POST /workspaces/{id}/review", s.withToken(s.handleReviewWorkspace))
	mux.HandleFunc("GET /workspaces/{id}/diff", s.withToken(s.handleDiffWorkspace))
	mux.HandleFunc("GET /workspaces/{id}/patch", s.withToken(s.handlePatchWorkspace))
	mux.HandleFunc("GET /workspaces/{id}/checkpoints", s.withToken(s.handleListCheckpoints))
}

// ---- handlers ----

func (s *Server) handleListWorkspaces(w http.ResponseWriter, r *http.Request) {
	if s.cfg.WorkspaceAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "workspace API not configured")
		return
	}
	taskID := r.URL.Query().Get("task")
	projectID := r.URL.Query().Get("project")
	workspaces, err := s.cfg.WorkspaceAPI.ListWorkspaces(r.Context(), taskID, projectID)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if workspaces == nil {
		workspaces = []WorkspaceDTO{}
	}
	writeJSON(w, http.StatusOK, workspaces)
}

func (s *Server) handleCreateWorkspace(w http.ResponseWriter, r *http.Request) {
	if s.cfg.WorkspaceAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "workspace API not configured")
		return
	}
	var req CreateWorkspaceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ws, err := s.cfg.WorkspaceAPI.CreateWorkspace(r.Context(), req)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, ws)
}

func (s *Server) handleGetWorkspace(w http.ResponseWriter, r *http.Request) {
	if s.cfg.WorkspaceAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "workspace API not configured")
		return
	}
	id := r.PathValue("id")
	ws, err := s.cfg.WorkspaceAPI.GetWorkspace(r.Context(), id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ws)
}

func (s *Server) handleDeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	if s.cfg.WorkspaceAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "workspace API not configured")
		return
	}
	id := r.PathValue("id")
	if err := s.cfg.WorkspaceAPI.DeleteWorkspace(r.Context(), id); err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

func (s *Server) handleRunWorkspace(w http.ResponseWriter, r *http.Request) {
	if s.cfg.WorkspaceAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "workspace API not configured")
		return
	}
	id := r.PathValue("id")
	var req RunWorkspaceRequest
	if r.ContentLength > 0 {
		_ = decodeJSON(r, &req)
	}
	ws, err := s.cfg.WorkspaceAPI.RunWorkspace(r.Context(), id, req)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ws)
}

func (s *Server) handleCheckpointWorkspace(w http.ResponseWriter, r *http.Request) {
	if s.cfg.WorkspaceAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "workspace API not configured")
		return
	}
	id := r.PathValue("id")
	var req CheckpointRequest
	if r.ContentLength > 0 {
		_ = decodeJSON(r, &req)
	}
	if req.Moment == "" {
		req.Moment = "manual"
	}
	ws, err := s.cfg.WorkspaceAPI.CheckpointWorkspace(r.Context(), id, req)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ws)
}

func (s *Server) handleCreateResult(w http.ResponseWriter, r *http.Request) {
	if s.cfg.WorkspaceAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "workspace API not configured")
		return
	}
	id := r.PathValue("id")
	ws, err := s.cfg.WorkspaceAPI.CreateResult(r.Context(), id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ws)
}

func (s *Server) handleReviewWorkspace(w http.ResponseWriter, r *http.Request) {
	if s.cfg.WorkspaceAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "workspace API not configured")
		return
	}
	id := r.PathValue("id")
	var req ReviewRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ws, err := s.cfg.WorkspaceAPI.ReviewWorkspace(r.Context(), id, req)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ws)
}

func (s *Server) handleDiffWorkspace(w http.ResponseWriter, r *http.Request) {
	if s.cfg.WorkspaceAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "workspace API not configured")
		return
	}
	id := r.PathValue("id")
	resp, err := s.cfg.WorkspaceAPI.DiffWorkspace(r.Context(), id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handlePatchWorkspace(w http.ResponseWriter, r *http.Request) {
	if s.cfg.WorkspaceAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "workspace API not configured")
		return
	}
	id := r.PathValue("id")
	resp, err := s.cfg.WorkspaceAPI.PatchWorkspace(r.Context(), id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleListCheckpoints(w http.ResponseWriter, r *http.Request) {
	if s.cfg.WorkspaceAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "workspace API not configured")
		return
	}
	id := r.PathValue("id")
	cps, err := s.cfg.WorkspaceAPI.ListCheckpoints(r.Context(), id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if cps == nil {
		cps = []CheckpointDTO{}
	}
	writeJSON(w, http.StatusOK, cps)
}
