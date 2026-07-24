package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// ---- Project client methods ----

// ListProjects calls GET /projects.
func (c *Client) ListProjects(ctx context.Context) ([]ProjectDTO, error) {
	var out []ProjectDTO
	if err := c.getJSON(ctx, "/projects", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// AddProject calls POST /projects.
func (c *Client) AddProject(ctx context.Context, req AddProjectRequest) (ProjectDTO, error) {
	var out ProjectDTO
	if err := c.postJSON(ctx, "/projects", req, &out); err != nil {
		return ProjectDTO{}, err
	}
	return out, nil
}

// GetProject calls GET /projects/{id}.
func (c *Client) GetProject(ctx context.Context, id string) (ProjectDTO, error) {
	var out ProjectDTO
	if err := c.getJSON(ctx, "/projects/"+id, &out); err != nil {
		return ProjectDTO{}, err
	}
	return out, nil
}

// RemoveProject calls DELETE /projects/{id}.
func (c *Client) RemoveProject(ctx context.Context, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.BaseURL+"/projects/"+id, nil)
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

// StartProject calls POST /projects/{id}/start.
func (c *Client) StartProject(ctx context.Context, id string) (ProjectDTO, error) {
	return c.projectAction(ctx, id, "start")
}

// PauseProject calls POST /projects/{id}/pause.
func (c *Client) PauseProject(ctx context.Context, id string) (ProjectDTO, error) {
	return c.projectAction(ctx, id, "pause")
}

// StopProject calls POST /projects/{id}/stop.
func (c *Client) StopProject(ctx context.Context, id string) (ProjectDTO, error) {
	return c.projectAction(ctx, id, "stop")
}

func (c *Client) projectAction(ctx context.Context, id, action string) (ProjectDTO, error) {
	var out ProjectDTO
	if err := c.postAction(ctx, fmt.Sprintf("/projects/%s/%s", id, action), &out); err != nil {
		return ProjectDTO{}, err
	}
	return out, nil
}

// ---- Task client methods ----

// ListTasks calls GET /tasks?project={projectID}. If projectID is empty, lists
// all tasks.
func (c *Client) ListTasks(ctx context.Context, projectID string) ([]TaskDTO, error) {
	path := "/tasks"
	if projectID != "" {
		path += "?project=" + projectID
	}
	var out []TaskDTO
	if err := c.getJSON(ctx, path, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// AddTask calls POST /tasks.
func (c *Client) AddTask(ctx context.Context, req AddTaskRequest) (TaskDTO, error) {
	var out TaskDTO
	if err := c.postJSON(ctx, "/tasks", req, &out); err != nil {
		return TaskDTO{}, err
	}
	return out, nil
}

// GetTask calls GET /tasks/{id}.
func (c *Client) GetTask(ctx context.Context, id string) (TaskDTO, error) {
	var out TaskDTO
	if err := c.getJSON(ctx, "/tasks/"+id, &out); err != nil {
		return TaskDTO{}, err
	}
	return out, nil
}

// PauseTask calls POST /tasks/{id}/pause.
func (c *Client) PauseTask(ctx context.Context, id string) (TaskDTO, error) {
	var out TaskDTO
	if err := c.postAction(ctx, "/tasks/"+id+"/pause", &out); err != nil {
		return TaskDTO{}, err
	}
	return out, nil
}

// CancelTask calls POST /tasks/{id}/cancel.
func (c *Client) CancelTask(ctx context.Context, id string) (TaskDTO, error) {
	var out TaskDTO
	if err := c.postAction(ctx, "/tasks/"+id+"/cancel", &out); err != nil {
		return TaskDTO{}, err
	}
	return out, nil
}

// ---- helpers ----

// postJSON sends a POST request with a JSON body and decodes the response.
func (c *Client) postJSON(ctx context.Context, path string, body any, dst any) error {
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

// postAction sends a POST request with no body and decodes the response.
func (c *Client) postAction(ctx context.Context, path string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	c.setAuth(req)
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

// checkResponse reads the response and returns an error if the status is not
// 2xx. The error message includes the response body for diagnostics.
func checkResponse(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := readBody(resp)
	return &APIError{Code: resp.StatusCode, Msg: parseErrorMessage(body, resp.StatusCode)}
}

func decodeBody(resp *http.Response, dst any) error {
	body, err := readBody(resp)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
