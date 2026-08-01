package transport

import (
	"context"
	"fmt"
)

// RunPipeline calls POST /projects/{id}/pipeline/run — the durable-pipeline
// `forge run` endpoint. The daemon creates a task and drives it through the
// durable pipeline stages synchronously.
func (c *Client) RunPipeline(ctx context.Context, projectID string, req PipelineRunRequest) (PipelineRunResultDTO, error) {
	req.ProjectID = projectID
	var out PipelineRunResultDTO
	if err := c.postJSON(ctx, "/projects/"+projectID+"/pipeline/run", req, &out); err != nil {
		return PipelineRunResultDTO{}, err
	}
	return out, nil
}

// PipelineStatus calls GET /tasks/{id}/pipeline.
func (c *Client) PipelineStatus(ctx context.Context, taskID string) (PipelineRunResultDTO, error) {
	var out PipelineRunResultDTO
	if err := c.getJSON(ctx, "/tasks/"+taskID+"/pipeline", &out); err != nil {
		return PipelineRunResultDTO{}, err
	}
	return out, nil
}

// CancelPipeline calls POST /tasks/{id}/pipeline/cancel. Cancellation is
// durable and idempotent: a cancelled run is never resumed by restart
// recovery.
func (c *Client) CancelPipeline(ctx context.Context, taskID string) (PipelineRunResultDTO, error) {
	var out PipelineRunResultDTO
	if err := c.postJSON(ctx, "/tasks/"+taskID+"/pipeline/cancel", map[string]any{}, &out); err != nil {
		return PipelineRunResultDTO{}, err
	}
	return out, nil
}

// SetEmergencyStop calls POST /estop. While on, every in-flight agent run is
// cancelled and new pipeline drive attempts are refused; while off, queued
// runs may be re-driven (explicit resume).
func (c *Client) SetEmergencyStop(ctx context.Context, on bool, reason string) (EstopDTO, error) {
	var out EstopDTO
	if err := c.postJSON(ctx, "/estop", EstopDTO{On: on, Reason: reason}, &out); err != nil {
		return EstopDTO{}, err
	}
	return out, nil
}

// EmergencyStopStatus calls GET /estop.
func (c *Client) EmergencyStopStatus(ctx context.Context) (EstopDTO, error) {
	var out EstopDTO
	if err := c.getJSON(ctx, "/estop", &out); err != nil {
		return EstopDTO{}, fmt.Errorf("estop status: %w", err)
	}
	return out, nil
}
