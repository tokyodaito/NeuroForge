package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// ---- Workspace client methods ----

// ListWorkspaces calls GET /workspaces?task={taskID}&project={projectID}.
func (c *Client) ListWorkspaces(ctx context.Context, taskID, projectID string) ([]WorkspaceDTO, error) {
	path := "/workspaces"
	q := ""
	if taskID != "" {
		q += "task=" + taskID + "&"
	}
	if projectID != "" {
		q += "project=" + projectID + "&"
	}
	if q != "" {
		path += "?" + q[:len(q)-1] // strip trailing &
	}
	var out []WorkspaceDTO
	if err := c.getJSON(ctx, path, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateWorkspace calls POST /workspaces.
func (c *Client) CreateWorkspace(ctx context.Context, req CreateWorkspaceRequest) (WorkspaceDTO, error) {
	var out WorkspaceDTO
	if err := c.postJSON(ctx, "/workspaces", req, &out); err != nil {
		return WorkspaceDTO{}, err
	}
	return out, nil
}

// GetWorkspace calls GET /workspaces/{id}.
func (c *Client) GetWorkspace(ctx context.Context, id string) (WorkspaceDTO, error) {
	var out WorkspaceDTO
	if err := c.getJSON(ctx, "/workspaces/"+id, &out); err != nil {
		return WorkspaceDTO{}, err
	}
	return out, nil
}

// DeleteWorkspace calls DELETE /workspaces/{id}.
func (c *Client) DeleteWorkspace(ctx context.Context, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.BaseURL+"/workspaces/"+id, nil)
	if err != nil {
		return err
	}
	c.setAuth(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotRunning, err)
	}
	defer resp.Body.Close()
	return checkResponse(resp)
}

// RunWorkspace calls POST /workspaces/{id}/run.
func (c *Client) RunWorkspace(ctx context.Context, id string, req RunWorkspaceRequest) (WorkspaceDTO, error) {
	var out WorkspaceDTO
	if err := c.postJSON(ctx, "/workspaces/"+id+"/run", req, &out); err != nil {
		return WorkspaceDTO{}, err
	}
	return out, nil
}

// CheckpointWorkspace calls POST /workspaces/{id}/checkpoint.
func (c *Client) CheckpointWorkspace(ctx context.Context, id string, req CheckpointRequest) (WorkspaceDTO, error) {
	var out WorkspaceDTO
	if err := c.postJSON(ctx, "/workspaces/"+id+"/checkpoint", req, &out); err != nil {
		return WorkspaceDTO{}, err
	}
	return out, nil
}

// CreateResult calls POST /workspaces/{id}/result.
func (c *Client) CreateResult(ctx context.Context, id string) (WorkspaceDTO, error) {
	var out WorkspaceDTO
	if err := c.postAction(ctx, "/workspaces/"+id+"/result", &out); err != nil {
		return WorkspaceDTO{}, err
	}
	return out, nil
}

// ReviewWorkspace calls POST /workspaces/{id}/review.
func (c *Client) ReviewWorkspace(ctx context.Context, id string, req ReviewRequest) (WorkspaceDTO, error) {
	var out WorkspaceDTO
	if err := c.postJSON(ctx, "/workspaces/"+id+"/review", req, &out); err != nil {
		return WorkspaceDTO{}, err
	}
	return out, nil
}

// DiffWorkspace calls GET /workspaces/{id}/diff.
func (c *Client) DiffWorkspace(ctx context.Context, id string) (DiffResponse, error) {
	var out DiffResponse
	if err := c.getJSON(ctx, "/workspaces/"+id+"/diff", &out); err != nil {
		return DiffResponse{}, err
	}
	return out, nil
}

// PatchWorkspace calls GET /workspaces/{id}/patch.
func (c *Client) PatchWorkspace(ctx context.Context, id string) (PatchResponse, error) {
	var out PatchResponse
	if err := c.getJSON(ctx, "/workspaces/"+id+"/patch", &out); err != nil {
		return PatchResponse{}, err
	}
	return out, nil
}

// ListCheckpoints calls GET /workspaces/{id}/checkpoints.
func (c *Client) ListCheckpoints(ctx context.Context, id string) ([]CheckpointDTO, error) {
	var out []CheckpointDTO
	if err := c.getJSON(ctx, "/workspaces/"+id+"/checkpoints", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// postJSONWithTimeout sends a POST with a custom timeout (used for long runs).
func (c *Client) postJSONWithTimeout(ctx context.Context, path string, body any, dst any) error {
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	c.setAuth(req)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotRunning, err)
	}
	defer resp.Body.Close()
	if err := checkResponse(resp); err != nil {
		return err
	}
	return decodeBody(resp, dst)
}
