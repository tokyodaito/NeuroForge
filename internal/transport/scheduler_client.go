package transport

import (
	"context"
	"fmt"
	"net/http"
)

// ---- Scheduler client methods ----

// DispatchTask calls POST /tasks/{id}/dispatch.
func (c *Client) DispatchTask(ctx context.Context, id string, req DispatchTaskRequest) (DispatchResultDTO, error) {
	req.TaskID = id
	var out DispatchResultDTO
	if err := c.postJSON(ctx, "/tasks/"+id+"/dispatch", req, &out); err != nil {
		return DispatchResultDTO{}, err
	}
	return out, nil
}

// RunPostMerge calls POST /tasks/{id}/post-merge.
func (c *Client) RunPostMerge(ctx context.Context, id string, req PostMergeRequest) (PostMergeResultDTO, error) {
	req.TaskID = id
	var out PostMergeResultDTO
	if err := c.postJSON(ctx, "/tasks/"+id+"/post-merge", req, &out); err != nil {
		return PostMergeResultDTO{}, err
	}
	return out, nil
}

// ListPostMergeChecks calls GET /tasks/{id}/post-merge.
func (c *Client) ListPostMergeChecks(ctx context.Context, id string) ([]PostMergeResultDTO, error) {
	var out []PostMergeResultDTO
	if err := c.getJSON(ctx, "/tasks/"+id+"/post-merge", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ReopenTask calls POST /tasks/{id}/reopen.
func (c *Client) ReopenTask(ctx context.Context, id, reason string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/tasks/"+id+"/reopen", nil)
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

// ProjectUsage calls GET /projects/{id}/usage.
func (c *Client) ProjectUsage(ctx context.Context, id string) (UsageTotalsDTO, error) {
	var out UsageTotalsDTO
	if err := c.getJSON(ctx, "/projects/"+id+"/usage", &out); err != nil {
		return UsageTotalsDTO{}, err
	}
	return out, nil
}

// ListMemory calls GET /projects/{id}/memory.
func (c *Client) ListMemory(ctx context.Context, id string) ([]MemoryRecordDTO, error) {
	var out []MemoryRecordDTO
	if err := c.getJSON(ctx, "/projects/"+id+"/memory", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// LearnMemory calls POST /projects/{id}/memory.
func (c *Client) LearnMemory(ctx context.Context, id string, req LearnMemoryRequest) (MemoryRecordDTO, error) {
	req.ProjectID = id
	var out MemoryRecordDTO
	if err := c.postJSON(ctx, "/projects/"+id+"/memory", req, &out); err != nil {
		return MemoryRecordDTO{}, err
	}
	return out, nil
}

// QualityStats calls GET /quality/stats.
func (c *Client) QualityStats(ctx context.Context) (QualityStatsDTO, error) {
	var out QualityStatsDTO
	if err := c.getJSON(ctx, "/quality/stats", &out); err != nil {
		return QualityStatsDTO{}, err
	}
	return out, nil
}
