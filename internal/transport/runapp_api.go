package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// RunTaskRequest is the body of POST /projects/{id}/run — the user-facing
// "forge run" endpoint. It creates a task + workspace + runs one production
// adapter, atomically finalizing the result (FR-1..FR-14). All fields are
// optional except Description; defaults are applied daemon-side per
// REQUIREMENTS.md §1.1.
type RunTaskRequest struct {
	ProjectID   string        `json:"-"`
	Description string        `json:"description"`
	Engine      string        `json:"engine,omitempty"`
	Model       string        `json:"model,omitempty"`
	BaseBranch  string        `json:"base_branch,omitempty"`
	Timeout     time.Duration `json:"timeout,omitempty"`
}

// RunTaskResultDTO is the response from POST /projects/{id}/run. It mirrors
// OUTCOME_CONTRACT.md §3 with stable field names. The field set is FIXED and
// every field is always present; nullable fields (run_id, actual_head_sha,
// commit_sha, result_branch, error, error_class) are rendered as JSON `null`
// when empty (invariant I.11) via a custom [RunTaskResultDTO.MarshalJSON].
//
// The struct keeps its plain field tags so request/response UNMARSHALING still
// populates every field; only MARSHALING is overridden to honour the contract's
// null semantics.
type RunTaskResultDTO struct {
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
}

// MarshalJSON renders the DTO exactly per OUTCOME_CONTRACT.md §3: a fixed field
// set, nullable fields as `null` when empty, and changed_files as `[]` not
// `null`. Centralizing this here means construction sites need no special
// handling and the wire contract is asserted in one place (invariant I.11).
// Unmarshaling is unaffected (it uses the plain struct tags above).
func (d RunTaskResultDTO) MarshalJSON() ([]byte, error) {
	changed := d.ChangedFiles
	if changed == nil {
		changed = []string{}
	}
	return json.Marshal(struct {
		Outcome       string   `json:"outcome"`
		TaskID        string   `json:"task_id"`
		WorkspaceID   string   `json:"workspace_id"`
		RunID         *string  `json:"run_id"`
		WorkspacePath string   `json:"workspace_path"`
		BaseSHA       string   `json:"base_sha"`
		ActualHeadSHA *string  `json:"actual_head_sha"`
		Engine        string   `json:"engine"`
		Model         string   `json:"model"`
		ChangedFiles  []string `json:"changed_files"`
		CommitSHA     *string  `json:"commit_sha"`
		ResultBranch  *string  `json:"result_branch"`
		NextAction    string   `json:"next_action"`
		Error         *string  `json:"error"`
		ErrorClass    *string  `json:"error_class"`
	}{
		Outcome:       d.Outcome,
		TaskID:        d.TaskID,
		WorkspaceID:   d.WorkspaceID,
		RunID:         nullableString(d.RunID),
		WorkspacePath: d.WorkspacePath,
		BaseSHA:       d.BaseSHA,
		ActualHeadSHA: nullableString(d.ActualHeadSHA),
		Engine:        d.Engine,
		Model:         d.Model,
		ChangedFiles:  changed,
		CommitSHA:     nullableString(d.CommitSHA),
		ResultBranch:  nullableString(d.ResultBranch),
		NextAction:    d.NextAction,
		Error:         nullableString(d.Error),
		ErrorClass:    nullableString(d.ErrorClass),
	})
}

// nullableString returns a pointer to s when non-empty, else nil (so the field
// marshals as JSON null per OUTCOME_CONTRACT.md §3.1).
func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// RunAppAPI is implemented by the daemon; the transport server delegates the
// /projects/{id}/run endpoint to it. Declared separately from SchedulerAPI so
// the daemon can wire it independently (the run-app path bypasses the
// scheduler; NFR-7).
type RunAppAPI interface {
	RunTask(ctx context.Context, req RunTaskRequest) (RunTaskResultDTO, error)
}

// registerRunAppRoutes wires the run-app endpoint onto mux.
func (s *Server) registerRunAppRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /projects/{id}/run", s.withToken(s.handleRunTask))
}

func (s *Server) handleRunTask(w http.ResponseWriter, r *http.Request) {
	if s.cfg.RunAppAPI == nil {
		writeErr(w, http.StatusServiceUnavailable, "run-app API not configured")
		return
	}
	id := r.PathValue("id")
	var req RunTaskRequest
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	req.ProjectID = id
	out, err := s.cfg.RunAppAPI.RunTask(r.Context(), req)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if out.ChangedFiles == nil {
		out.ChangedFiles = []string{}
	}
	writeJSON(w, http.StatusOK, out)
}
