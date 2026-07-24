package daemon

import (
	"context"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/transport"
)

// workspaceAPIAdapter implements transport.WorkspaceAPI by delegating to the
// WorkspaceService. It converts between wire DTOs and service calls.
type workspaceAPIAdapter struct {
	svc *WorkspaceService
}

func newWorkspaceAPIAdapter(svc *WorkspaceService) *workspaceAPIAdapter {
	return &workspaceAPIAdapter{svc: svc}
}

func (a *workspaceAPIAdapter) CreateWorkspace(ctx context.Context, req transport.CreateWorkspaceRequest) (transport.WorkspaceDTO, error) {
	return a.svc.CreateWorkspace(ctx, req)
}

func (a *workspaceAPIAdapter) GetWorkspace(ctx context.Context, id string) (transport.WorkspaceDTO, error) {
	return a.svc.Get(ctx, id)
}

func (a *workspaceAPIAdapter) ListWorkspaces(ctx context.Context, taskID, projectID string) ([]transport.WorkspaceDTO, error) {
	if taskID != "" {
		return a.svc.ListByTask(ctx, taskID)
	}
	if projectID != "" {
		return a.svc.ListByProject(ctx, projectID)
	}
	// No filter: list all (not typically exposed, but useful for debugging).
	return nil, nil
}

func (a *workspaceAPIAdapter) DeleteWorkspace(ctx context.Context, id string) error {
	return a.svc.Delete(ctx, id)
}

func (a *workspaceAPIAdapter) RunWorkspace(ctx context.Context, id string, req transport.RunWorkspaceRequest) (transport.WorkspaceDTO, error) {
	return a.svc.RunWorkspace(ctx, id, req)
}

func (a *workspaceAPIAdapter) CheckpointWorkspace(ctx context.Context, id string, req transport.CheckpointRequest) (transport.WorkspaceDTO, error) {
	return a.svc.Checkpoint(ctx, id, req.Moment, req.Message)
}

func (a *workspaceAPIAdapter) CreateResult(ctx context.Context, id string) (transport.WorkspaceDTO, error) {
	return a.svc.CreateResult(ctx, id)
}

func (a *workspaceAPIAdapter) ReviewWorkspace(ctx context.Context, id string, req transport.ReviewRequest) (transport.WorkspaceDTO, error) {
	return a.svc.Review(ctx, id, req.Action)
}

func (a *workspaceAPIAdapter) DiffWorkspace(ctx context.Context, id string) (transport.DiffResponse, error) {
	diff, err := a.svc.Diff(ctx, id)
	if err != nil {
		return transport.DiffResponse{}, err
	}
	return transport.DiffResponse{Diff: diff}, nil
}

func (a *workspaceAPIAdapter) PatchWorkspace(ctx context.Context, id string) (transport.PatchResponse, error) {
	patch, err := a.svc.Patch(ctx, id)
	if err != nil {
		return transport.PatchResponse{}, err
	}
	return transport.PatchResponse{Patch: patch}, nil
}

func (a *workspaceAPIAdapter) ListCheckpoints(ctx context.Context, id string) ([]transport.CheckpointDTO, error) {
	return a.svc.ListCheckpoints(ctx, id)
}

// hasAdapter reports whether the registry already has an adapter with the given
// id (used to avoid double-registration on daemon restart in the same process).
func hasAdapter(r *codingagent.Registry, id string) bool {
	_, ok := r.Lookup(id)
	return ok
}
