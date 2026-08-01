package transport

import (
	"context"
	"encoding/json"
	"net/http"
)

// PipelineRunRequest is the body of POST /projects/{id}/pipeline/run — the
// durable-pipeline run endpoint. It creates a task and drives it through the
// durable pipeline stages (compile → plan → ready → execute → verify → review
// → finalize, with a bounded repair loop). The call is synchronous: it returns
// when the run reaches a terminal or wait state.
type PipelineRunRequest struct {
	ProjectID         string `json:"-"`
	Description       string `json:"description"`
	Engine            string `json:"engine,omitempty"`
	Model             string `json:"model,omitempty"`
	BaseBranch        string `json:"base_branch,omitempty"`
	TimeoutSeconds    int64  `json:"timeout_seconds,omitempty"`
	MaxRepairAttempts int    `json:"max_repair_attempts,omitempty"`
}

// PipelineStageRecordDTO is one durable stage-history entry of a pipeline run.
type PipelineStageRecordDTO struct {
	Stage           string `json:"stage"`
	Attempt         int    `json:"attempt"`
	Status          string `json:"status"` // entered | completed | failed
	FailureCategory string `json:"failure_category,omitempty"`
	Reason          string `json:"reason,omitempty"`
	EvidenceRef     string `json:"evidence_ref,omitempty"`
}

// PipelineRunResultDTO is the response of the pipeline run/status/cancel
// endpoints. The first block mirrors RunTaskResultDTO (OUTCOME_CONTRACT.md §3)
// so the `forge run` UX and exit-code mapping are unchanged by the transport
// switch; the second block carries the durable pipeline state.
type PipelineRunResultDTO struct {
	Outcome       string   `json:"outcome"`
	TaskID        string   `json:"task_id"`
	WorkspaceID   string   `json:"workspace_id"`
	RunID         string   `json:"run_id"`
	WorkspacePath string   `json:"workspace_path"`
	BaseSHA       string   `json:"base_sha"`
	ActualHeadSHA string   `json:"actual_head_sha"`
	Engine        string   `json:"engine"`
	Model         string   `json:"model"`
	ChangedFiles  []string `json:"changed_files"`
	CommitSHA     string   `json:"commit_sha"`
	ResultBranch  string   `json:"result_branch"`
	NextAction    string   `json:"next_action"`
	Error         string   `json:"error"`
	ErrorClass    string   `json:"error_class"`

	RunState        string                   `json:"run_state"`
	CurrentStage    string                   `json:"current_stage"`
	FailureCategory string                   `json:"failure_category"`
	FailureReason   string                   `json:"failure_reason"`
	ResultRef       string                   `json:"result_ref"`
	StageRecords    []PipelineStageRecordDTO `json:"stage_records"`
}

// MarshalJSON renders the runapp-compatible block with the OUTCOME_CONTRACT.md
// §3 null semantics (nullable fields as JSON null when empty, changed_files as
// [] not null) so existing CLI consumers keep working; pipeline fields are
// always present (empty string / [] when unset).
func (d PipelineRunResultDTO) MarshalJSON() ([]byte, error) {
	changed := d.ChangedFiles
	if changed == nil {
		changed = []string{}
	}
	records := d.StageRecords
	if records == nil {
		records = []PipelineStageRecordDTO{}
	}
	return json.Marshal(struct {
		Outcome         string                   `json:"outcome"`
		TaskID          string                   `json:"task_id"`
		WorkspaceID     string                   `json:"workspace_id"`
		RunID           *string                  `json:"run_id"`
		WorkspacePath   string                   `json:"workspace_path"`
		BaseSHA         string                   `json:"base_sha"`
		ActualHeadSHA   *string                  `json:"actual_head_sha"`
		Engine          string                   `json:"engine"`
		Model           string                   `json:"model"`
		ChangedFiles    []string                 `json:"changed_files"`
		CommitSHA       *string                  `json:"commit_sha"`
		ResultBranch    *string                  `json:"result_branch"`
		NextAction      string                   `json:"next_action"`
		Error           *string                  `json:"error"`
		ErrorClass      *string                  `json:"error_class"`
		RunState        string                   `json:"run_state"`
		CurrentStage    string                   `json:"current_stage"`
		FailureCategory string                   `json:"failure_category"`
		FailureReason   string                   `json:"failure_reason"`
		ResultRef       string                   `json:"result_ref"`
		StageRecords    []PipelineStageRecordDTO `json:"stage_records"`
	}{
		Outcome:         d.Outcome,
		TaskID:          d.TaskID,
		WorkspaceID:     d.WorkspaceID,
		RunID:           nullableString(d.RunID),
		WorkspacePath:   d.WorkspacePath,
		BaseSHA:         d.BaseSHA,
		ActualHeadSHA:   nullableString(d.ActualHeadSHA),
		Engine:          d.Engine,
		Model:           d.Model,
		ChangedFiles:    changed,
		CommitSHA:       nullableString(d.CommitSHA),
		ResultBranch:    nullableString(d.ResultBranch),
		NextAction:      d.NextAction,
		Error:           nullableString(d.Error),
		ErrorClass:      nullableString(d.ErrorClass),
		RunState:        d.RunState,
		CurrentStage:    d.CurrentStage,
		FailureCategory: d.FailureCategory,
		FailureReason:   d.FailureReason,
		ResultRef:       d.ResultRef,
		StageRecords:    records,
	})
}

// EstopDTO is the emergency-stop flag state (GET/POST /estop).
type EstopDTO struct {
	On     bool   `json:"on"`
	Reason string `json:"reason"`
}

// PipelineAPI is implemented by the daemon; the transport server delegates the
// durable-pipeline endpoints to it.
type PipelineAPI interface {
	RunPipeline(ctx context.Context, req PipelineRunRequest) (PipelineRunResultDTO, error)
	PipelineStatus(ctx context.Context, taskID string) (PipelineRunResultDTO, error)
	CancelPipeline(ctx context.Context, taskID string) (PipelineRunResultDTO, error)
	SetEmergencyStop(ctx context.Context, on bool, reason string) (EstopDTO, error)
	EmergencyStopStatus(ctx context.Context) (EstopDTO, error)
}

// registerPipelineRoutes wires the durable-pipeline endpoints onto mux.
func (s *Server) registerPipelineRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /projects/{id}/pipeline/run", s.withToken(s.handlePipelineRun))
	mux.HandleFunc("GET /tasks/{id}/pipeline", s.withToken(s.handlePipelineStatus))
	mux.HandleFunc("POST /tasks/{id}/pipeline/cancel", s.withToken(s.handlePipelineCancel))
	mux.HandleFunc("POST /estop", s.withToken(s.handleEstopSet))
	mux.HandleFunc("GET /estop", s.withToken(s.handleEstopGet))
}

func (s *Server) handlePipelineRun(w http.ResponseWriter, r *http.Request) {
	if s.cfg.PipelineAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "pipeline API not configured")
		return
	}
	id := r.PathValue("id")
	var req PipelineRunRequest
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	req.ProjectID = id
	out, err := s.cfg.PipelineAPI.RunPipeline(r.Context(), req)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePipelineStatus(w http.ResponseWriter, r *http.Request) {
	if s.cfg.PipelineAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "pipeline API not configured")
		return
	}
	out, err := s.cfg.PipelineAPI.PipelineStatus(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePipelineCancel(w http.ResponseWriter, r *http.Request) {
	if s.cfg.PipelineAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "pipeline API not configured")
		return
	}
	out, err := s.cfg.PipelineAPI.CancelPipeline(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleEstopSet(w http.ResponseWriter, r *http.Request) {
	if s.cfg.PipelineAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "pipeline API not configured")
		return
	}
	var req EstopDTO
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	out, err := s.cfg.PipelineAPI.SetEmergencyStop(r.Context(), req.On, req.Reason)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleEstopGet(w http.ResponseWriter, r *http.Request) {
	if s.cfg.PipelineAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "pipeline API not configured")
		return
	}
	out, err := s.cfg.PipelineAPI.EmergencyStopStatus(r.Context())
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
