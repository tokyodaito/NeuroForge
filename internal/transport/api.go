package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// ProjectDTO is the wire representation of a project.
type ProjectDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Remote    string `json:"remote"`
	State     string `json:"state"`
	Profile   string `json:"profile"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// AddProjectRequest is the body of POST /projects.
type AddProjectRequest struct {
	Path    string `json:"path"`
	Name    string `json:"name,omitempty"`
	Profile string `json:"profile,omitempty"`
}

// TaskDTO is the wire representation of a task.
type TaskDTO struct {
	ID          string          `json:"id"`
	ProjectID   string          `json:"project_id"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description"`
	Priority    string          `json:"priority"`
	State       string          `json:"state"`
	Attachments []AttachmentDTO `json:"attachments,omitempty"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
}

// AttachmentDTO is the wire representation of a task attachment.
type AttachmentDTO struct {
	Hash     string `json:"hash"`
	Filename string `json:"filename"`
	MimeType string `json:"mime_type"`
	Size     int64  `json:"size"`
	Role     string `json:"role"`
}

// AddTaskRequest is the body of POST /tasks.
type AddTaskRequest struct {
	ProjectID   string             `json:"project_id"`
	Description string             `json:"description"`
	Title       string             `json:"title,omitempty"`
	Priority    string             `json:"priority,omitempty"`
	Attachments []AddAttachmentReq `json:"attachments,omitempty"`
}

// AddAttachmentReq references a local file to be stored as an attachment.
type AddAttachmentReq struct {
	Path     string `json:"path"`
	Filename string `json:"filename,omitempty"`
	Role     string `json:"role,omitempty"`
}

// APIError carries a typed error from the daemon so the client can distinguish
// e.g. "not found" from "invalid input".
type APIError struct {
	Code int
	Msg  string
}

func (e *APIError) Error() string { return e.Msg }

// ProjectAPI is implemented by the daemon; the transport server delegates to it.
type ProjectAPI interface {
	ListProjects(ctx context.Context) ([]ProjectDTO, error)
	AddProject(ctx context.Context, req AddProjectRequest) (ProjectDTO, error)
	GetProject(ctx context.Context, id string) (ProjectDTO, error)
	RemoveProject(ctx context.Context, id string) error
	StartProject(ctx context.Context, id string) (ProjectDTO, error)
	PauseProject(ctx context.Context, id string) (ProjectDTO, error)
	StopProject(ctx context.Context, id string) (ProjectDTO, error)
}

// TaskAPI is implemented by the daemon; the transport server delegates to it.
type TaskAPI interface {
	ListTasks(ctx context.Context, projectID string) ([]TaskDTO, error)
	AddTask(ctx context.Context, req AddTaskRequest) (TaskDTO, error)
	GetTask(ctx context.Context, id string) (TaskDTO, error)
	PauseTask(ctx context.Context, id string) (TaskDTO, error)
	CancelTask(ctx context.Context, id string) (TaskDTO, error)
}

// registerAPIRoutes wires the project and task endpoints onto mux. Each handler
// requires the bearer token (via withToken) and delegates to the configured API.
func (s *Server) registerAPIRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /projects", s.withToken(s.handleListProjects))
	mux.HandleFunc("POST /projects", s.withToken(s.handleAddProject))
	mux.HandleFunc("GET /projects/{id}", s.withToken(s.handleGetProject))
	mux.HandleFunc("DELETE /projects/{id}", s.withToken(s.handleRemoveProject))
	mux.HandleFunc("POST /projects/{id}/start", s.withToken(s.handleStartProject))
	mux.HandleFunc("POST /projects/{id}/pause", s.withToken(s.handlePauseProject))
	mux.HandleFunc("POST /projects/{id}/stop", s.withToken(s.handleStopProject))

	mux.HandleFunc("GET /tasks", s.withToken(s.handleListTasks))
	mux.HandleFunc("POST /tasks", s.withToken(s.handleAddTask))
	mux.HandleFunc("GET /tasks/{id}", s.withToken(s.handleGetTask))
	mux.HandleFunc("POST /tasks/{id}/pause", s.withToken(s.handlePauseTask))
	mux.HandleFunc("POST /tasks/{id}/cancel", s.withToken(s.handleCancelTask))
}

// ---- project handlers ----

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	if s.cfg.ProjectAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "project API not configured")
		return
	}
	projects, err := s.cfg.ProjectAPI.ListProjects(r.Context())
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if projects == nil {
		projects = []ProjectDTO{}
	}
	writeJSON(w, http.StatusOK, projects)
}

func (s *Server) handleAddProject(w http.ResponseWriter, r *http.Request) {
	if s.cfg.ProjectAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "project API not configured")
		return
	}
	var req AddProjectRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	p, err := s.cfg.ProjectAPI.AddProject(r.Context(), req)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	if s.cfg.ProjectAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "project API not configured")
		return
	}
	id := r.PathValue("id")
	p, err := s.cfg.ProjectAPI.GetProject(r.Context(), id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleRemoveProject(w http.ResponseWriter, r *http.Request) {
	if s.cfg.ProjectAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "project API not configured")
		return
	}
	id := r.PathValue("id")
	if err := s.cfg.ProjectAPI.RemoveProject(r.Context(), id); err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": id})
}

func (s *Server) handleStartProject(w http.ResponseWriter, r *http.Request) {
	s.projectAction(w, r, func(api ProjectAPI, id string) (ProjectDTO, error) {
		return api.StartProject(r.Context(), id)
	})
}

func (s *Server) handlePauseProject(w http.ResponseWriter, r *http.Request) {
	s.projectAction(w, r, func(api ProjectAPI, id string) (ProjectDTO, error) {
		return api.PauseProject(r.Context(), id)
	})
}

func (s *Server) handleStopProject(w http.ResponseWriter, r *http.Request) {
	s.projectAction(w, r, func(api ProjectAPI, id string) (ProjectDTO, error) {
		return api.StopProject(r.Context(), id)
	})
}

func (s *Server) projectAction(w http.ResponseWriter, r *http.Request,
	fn func(ProjectAPI, string) (ProjectDTO, error)) {
	if s.cfg.ProjectAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "project API not configured")
		return
	}
	id := r.PathValue("id")
	p, err := fn(s.cfg.ProjectAPI, id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// ---- task handlers ----

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	if s.cfg.TaskAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "task API not configured")
		return
	}
	projectID := r.URL.Query().Get("project")
	tasks, err := s.cfg.TaskAPI.ListTasks(r.Context(), projectID)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if tasks == nil {
		tasks = []TaskDTO{}
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (s *Server) handleAddTask(w http.ResponseWriter, r *http.Request) {
	if s.cfg.TaskAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "task API not configured")
		return
	}
	var req AddTaskRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	t, err := s.cfg.TaskAPI.AddTask(r.Context(), req)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	if s.cfg.TaskAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "task API not configured")
		return
	}
	id := r.PathValue("id")
	t, err := s.cfg.TaskAPI.GetTask(r.Context(), id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handlePauseTask(w http.ResponseWriter, r *http.Request) {
	s.taskAction(w, r, func(api TaskAPI, id string) (TaskDTO, error) {
		return api.PauseTask(r.Context(), id)
	})
}

func (s *Server) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	s.taskAction(w, r, func(api TaskAPI, id string) (TaskDTO, error) {
		return api.CancelTask(r.Context(), id)
	})
}

func (s *Server) taskAction(w http.ResponseWriter, r *http.Request,
	fn func(TaskAPI, string) (TaskDTO, error)) {
	if s.cfg.TaskAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "task API not configured")
		return
	}
	id := r.PathValue("id")
	t, err := fn(s.cfg.TaskAPI, id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// ---- helpers ----

func decodeJSON(r *http.Request, dst any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return errors.New("request body is required")
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return errors.New("invalid JSON: " + err.Error())
	}
	return nil
}

// writeAPIError translates a domain error into an appropriate HTTP status code.
func writeAPIError(w http.ResponseWriter, err error) {
	msg := err.Error()
	code := http.StatusInternalServerError

	switch {
	case strings.Contains(msg, "not found"):
		code = http.StatusNotFound
	case strings.Contains(msg, "already registered"):
		code = http.StatusConflict
	case strings.Contains(msg, "not a Git repository"):
		code = http.StatusBadRequest
	case strings.Contains(msg, "invalid transition"):
		code = http.StatusConflict
	case strings.Contains(msg, "is locked"):
		// ErrSpecificationLocked ("specification is locked") and any future
		// locked-state conflict map to 409 Conflict, matching the existing
		// conflict semantics (already registered / invalid transition). Without
		// this case the error would surface as 500, hiding the lock conflict
		// from clients (M14-03).
		code = http.StatusConflict
	case strings.Contains(msg, "is required"):
		code = http.StatusBadRequest
	case strings.Contains(msg, "invalid"):
		code = http.StatusBadRequest
	}

	writeJSON(w, code, map[string]any{"error": msg})
}
