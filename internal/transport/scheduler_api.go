package transport

import (
	"context"
	"net/http"
	"time"
)

// ---- DTOs ----

// DispatchTaskRequest is the body of POST /tasks/{id}/dispatch.
type DispatchTaskRequest struct {
	TaskID           string        `json:"task_id"`
	Engine           string        `json:"engine,omitempty"`
	Model            string        `json:"model,omitempty"`
	WorkPackageID    string        `json:"work_package_id,omitempty"`
	BaseBranch       string        `json:"base_branch,omitempty"`
	Timeout          time.Duration `json:"timeout,omitempty"`
	BuildContextPack bool          `json:"build_context_pack,omitempty"`
}

// DispatchResultDTO is the response from POST /tasks/{id}/dispatch.
type DispatchResultDTO struct {
	TaskID           string `json:"task_id"`
	ProjectID        string `json:"project_id"`
	WorkspaceID      string `json:"workspace_id"`
	Outcome          string `json:"outcome"`
	UsageEvents      int    `json:"usage_events"`
	EstimatedTokens  int    `json:"estimated_tokens"`
	ContextPackBuilt bool   `json:"context_pack_built"`
	MemoryLearned    bool   `json:"memory_learned"`
}

// PostMergeRequest is the body of POST /tasks/{id}/post-merge.
type PostMergeRequest struct {
	TaskID     string `json:"task_id"`
	CommitSHA  string `json:"commit_sha"`
	BaseBranch string `json:"base_branch"`
	Number     int    `json:"number"`
	// Checks lets the caller inject deterministic smoke checks (for testing +
	// scripted regressions). Empty → a single "merge-present" check runs.
	Checks []SmokeCheckSpec `json:"checks,omitempty"`
}

// SmokeCheckSpec describes one deterministic post-merge smoke check to run.
type SmokeCheckSpec struct {
	Name       string `json:"name"`
	WantStatus string `json:"want_status"` // passed | failed | skipped | error
	Detail     string `json:"detail,omitempty"`
}

// PostMergeResultDTO is the response from POST /tasks/{id}/post-merge.
type PostMergeResultDTO struct {
	TaskID     string `json:"task_id"`
	CommitSHA  string `json:"commit_sha,omitempty"`
	Decision   string `json:"decision"`
	AllPassed  bool   `json:"all_passed"`
	Reverted   bool   `json:"reverted"`
	RevertSHA  string `json:"revert_sha,omitempty"`
	OccurredAt string `json:"occurred_at"`
}

// ReopenTaskRequest is the body of POST /tasks/{id}/reopen.
type ReopenTaskRequest struct {
	Reason string `json:"reason"`
}

// UsageTotalsDTO is the response from GET /projects/{id}/usage.
type UsageTotalsDTO struct {
	ProjectID        string  `json:"project_id"`
	CodingInput      int     `json:"coding_input"`
	CachedInput      int     `json:"cached_input"`
	CodingOutput     int     `json:"coding_output"`
	ImageGenerations int     `json:"image_generations"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`
	EventCount       int     `json:"event_count"`
}

// MemoryRecordDTO is one project memory record (§22.9).
type MemoryRecordDTO struct {
	Category   string `json:"category"`
	Key        string `json:"key"`
	Value      string `json:"value"`
	Confidence string `json:"confidence"`
	Source     string `json:"source,omitempty"`
	Version    int    `json:"version"`
	LearnedAt  string `json:"learned_at"`
}

// LearnMemoryRequest is the body of POST /projects/{id}/memory.
type LearnMemoryRequest struct {
	ProjectID  string `json:"project_id"`
	Category   string `json:"category"`
	Key        string `json:"key"`
	Value      string `json:"value"`
	Confidence string `json:"confidence,omitempty"`
}

// ModelStatsDTO is the per-model quality statistics (§19.1).
type ModelStatsDTO struct {
	Engine      string  `json:"engine"`
	Model       string  `json:"model"`
	Attempts    int     `json:"attempts"`
	Successes   int     `json:"successes"`
	SuccessRate float64 `json:"success_rate"`
}

// QualityStatsDTO is the response from GET /quality/stats.
type QualityStatsDTO struct {
	OverallSuccessRate float64         `json:"overall_success_rate"`
	ByModel            []ModelStatsDTO `json:"by_model"`
	Totals             UsageTotalsDTO  `json:"totals"`
}

// SchedulerAPI is implemented by the daemon; the transport server delegates the
// scheduler / post-merge / quality / memory endpoints to it.
type SchedulerAPI interface {
	DispatchTask(ctx context.Context, req DispatchTaskRequest) (DispatchResultDTO, error)
	RunPostMerge(ctx context.Context, req PostMergeRequest) (PostMergeResultDTO, error)
	ReopenTask(ctx context.Context, taskID, reason string) error
	ListUsage(ctx context.Context, projectID string) (UsageTotalsDTO, error)
	ListMemory(ctx context.Context, projectID string) ([]MemoryRecordDTO, error)
	LearnMemory(ctx context.Context, req LearnMemoryRequest) (MemoryRecordDTO, error)
	QualityStats(ctx context.Context) (QualityStatsDTO, error)
	ListPostMergeChecks(ctx context.Context, taskID string) ([]PostMergeResultDTO, error)
}

// registerSchedulerRoutes wires the scheduler/post-merge/quality/memory endpoints
// onto mux. Each handler requires the bearer token.
func (s *Server) registerSchedulerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /tasks/{id}/dispatch", s.withToken(s.handleDispatchTask))
	mux.HandleFunc("POST /tasks/{id}/post-merge", s.withToken(s.handlePostMerge))
	mux.HandleFunc("POST /tasks/{id}/reopen", s.withToken(s.handleReopenTask))
	mux.HandleFunc("GET /tasks/{id}/post-merge", s.withToken(s.handleListPostMergeChecks))
	mux.HandleFunc("GET /projects/{id}/usage", s.withToken(s.handleProjectUsage))
	mux.HandleFunc("GET /projects/{id}/memory", s.withToken(s.handleListMemory))
	mux.HandleFunc("POST /projects/{id}/memory", s.withToken(s.handleLearnMemory))
	mux.HandleFunc("GET /quality/stats", s.withToken(s.handleQualityStats))
}

// ---- handlers ----

func (s *Server) handleDispatchTask(w http.ResponseWriter, r *http.Request) {
	if s.cfg.SchedulerAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "scheduler API not configured")
		return
	}
	id := r.PathValue("id")
	var req DispatchTaskRequest
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	req.TaskID = id
	out, err := s.cfg.SchedulerAPI.DispatchTask(r.Context(), req)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePostMerge(w http.ResponseWriter, r *http.Request) {
	if s.cfg.SchedulerAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "scheduler API not configured")
		return
	}
	id := r.PathValue("id")
	var req PostMergeRequest
	if r.ContentLength > 0 {
		_ = decodeJSON(r, &req)
	}
	req.TaskID = id
	out, err := s.cfg.SchedulerAPI.RunPostMerge(r.Context(), req)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleListPostMergeChecks(w http.ResponseWriter, r *http.Request) {
	if s.cfg.SchedulerAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "scheduler API not configured")
		return
	}
	id := r.PathValue("id")
	rows, err := s.cfg.SchedulerAPI.ListPostMergeChecks(r.Context(), id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if rows == nil {
		rows = []PostMergeResultDTO{}
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) handleReopenTask(w http.ResponseWriter, r *http.Request) {
	if s.cfg.SchedulerAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "scheduler API not configured")
		return
	}
	id := r.PathValue("id")
	var req ReopenTaskRequest
	if r.ContentLength > 0 {
		_ = decodeJSON(r, &req)
	}
	if err := s.cfg.SchedulerAPI.ReopenTask(r.Context(), id, req.Reason); err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reopened": id})
}

func (s *Server) handleProjectUsage(w http.ResponseWriter, r *http.Request) {
	if s.cfg.SchedulerAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "scheduler API not configured")
		return
	}
	id := r.PathValue("id")
	out, err := s.cfg.SchedulerAPI.ListUsage(r.Context(), id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleListMemory(w http.ResponseWriter, r *http.Request) {
	if s.cfg.SchedulerAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "scheduler API not configured")
		return
	}
	id := r.PathValue("id")
	rows, err := s.cfg.SchedulerAPI.ListMemory(r.Context(), id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if rows == nil {
		rows = []MemoryRecordDTO{}
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) handleLearnMemory(w http.ResponseWriter, r *http.Request) {
	if s.cfg.SchedulerAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "scheduler API not configured")
		return
	}
	id := r.PathValue("id")
	var req LearnMemoryRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	req.ProjectID = id
	out, err := s.cfg.SchedulerAPI.LearnMemory(r.Context(), req)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleQualityStats(w http.ResponseWriter, r *http.Request) {
	if s.cfg.SchedulerAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "scheduler API not configured")
		return
	}
	out, err := s.cfg.SchedulerAPI.QualityStats(r.Context())
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
