package transport

import (
	"context"
)

// RunTask calls POST /projects/{id}/run — the user-facing `forge run` endpoint.
// The daemon creates a task + workspace, runs one production adapter, and
// finalizes the result atomically (FR-1..FR-14).
func (c *Client) RunTask(ctx context.Context, projectID string, req RunTaskRequest) (RunTaskResultDTO, error) {
	req.ProjectID = projectID
	var out RunTaskResultDTO
	if err := c.postJSON(ctx, "/projects/"+projectID+"/run", req, &out); err != nil {
		return RunTaskResultDTO{}, err
	}
	return out, nil
}
