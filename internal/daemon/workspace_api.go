package daemon

import (
	"context"
	"fmt"
	"log/slog"

	"neuroforge/internal/audit"
	"neuroforge/internal/storage"
	"neuroforge/internal/supervisor"
	"neuroforge/internal/task"
	"neuroforge/internal/transport"
	"neuroforge/internal/workgraph"
	"neuroforge/internal/workspace"
)

// ProjectPathResolver returns the filesystem path of a registered project by
// id. It is injected into the WorkspaceService so it does not need a direct
// dependency on the project registry (avoiding import cycles).
type ProjectPathResolver func(ctx context.Context, projectID string) (string, error)

// WorkspaceService is the daemon-side service that orchestrates workspace
// creation, agent runs, checkpoints, result branches and review lifecycle. It
// is the single owner of mutable workspace state; the CLI and TUI reach it only
// through the loopback API.
type WorkspaceService struct {
	wm             *workspace.Manager
	leases         *workgraph.LeaseManager
	supervisor     *supervisor.Supervisor
	tasks          *task.Backlog
	audit          *audit.Recorder
	logger         *slog.Logger
	resolveProject ProjectPathResolver
}

// NewWorkspaceService constructs a WorkspaceService.
func NewWorkspaceService(wm *workspace.Manager, leases *workgraph.LeaseManager, sup *supervisor.Supervisor, tasks *task.Backlog, rec *audit.Recorder, logger *slog.Logger, resolveProject ProjectPathResolver) *WorkspaceService {
	return &WorkspaceService{
		wm: wm, leases: leases, supervisor: sup, tasks: tasks,
		audit: rec, logger: logger, resolveProject: resolveProject,
	}
}

// CreateWorkspace creates a worktree for a task.
func (s *WorkspaceService) CreateWorkspace(ctx context.Context, req transport.CreateWorkspaceRequest) (transport.WorkspaceDTO, error) {
	t, err := s.tasks.Get(ctx, req.TaskID)
	if err != nil {
		return transport.WorkspaceDTO{}, fmt.Errorf("workspace: task not found: %w", err)
	}
	projPath, err := s.resolveProject(ctx, t.ProjectID)
	if err != nil {
		return transport.WorkspaceDTO{}, fmt.Errorf("workspace: resolve project: %w", err)
	}

	ws, err := s.wm.Create(ctx, workspace.CreateRequest{
		ProjectID:     t.ProjectID,
		ProjectPath:   projPath,
		TaskID:        t.ID,
		WorkPackageID: req.WorkPackageID,
		BaseBranch:    req.BaseBranch,
	})
	if err != nil {
		return transport.WorkspaceDTO{}, err
	}
	return workspaceToDTO(ws), nil
}

// RunWorkspace runs the agent inside a workspace, then checkpoints.
func (s *WorkspaceService) RunWorkspace(ctx context.Context, id string, req transport.RunWorkspaceRequest) (transport.WorkspaceDTO, error) {
	ws, err := s.wm.Get(ctx, id)
	if err != nil {
		return transport.WorkspaceDTO{}, err
	}
	if ws.State != workspace.StateActive {
		return transport.WorkspaceDTO{}, fmt.Errorf("workspace: cannot run %q (state=%s, need active)", id, ws.State)
	}

	engine := req.Engine
	if engine == "" {
		engine = "fake"
	}

	result, err := s.supervisor.Run(ctx, supervisor.RunRequest{
		WorkspaceID: ws.ID,
		Engine:      engine,
		Model:       req.Model,
		Prompt:      req.Prompt,
		Timeout:     req.Timeout,
	}, ws.Path)
	if err != nil {
		_ = s.wm.SetRunInfo(ctx, ws.ID, engine, req.Model, "", "")
		return transport.WorkspaceDTO{}, err
	}

	_ = s.wm.SetRunInfo(ctx, ws.ID, engine, req.Model, result.Handle.RunID, result.Handle.SessionID)

	if !result.Failed && !result.Cancelled {
		if _, cpErr := s.wm.Checkpoint(ctx, ws.ID, workspace.MomentFirstDiff, "agent run completed"); cpErr != nil {
			s.logger.Warn("checkpoint after run failed", "err", cpErr)
		}
	}

	updated, _ := s.wm.Get(ctx, id)
	if result.Failed {
		updated.State = workspace.StateFailed
	}
	return workspaceToDTO(updated), nil
}

// Checkpoint creates a checkpoint commit.
func (s *WorkspaceService) Checkpoint(ctx context.Context, id, moment, message string) (transport.WorkspaceDTO, error) {
	if _, err := s.wm.Checkpoint(ctx, id, workspace.CheckpointMoment(moment), message); err != nil {
		return transport.WorkspaceDTO{}, err
	}
	ws, _ := s.wm.Get(ctx, id)
	return workspaceToDTO(ws), nil
}

// CreateResult creates the local result branch.
func (s *WorkspaceService) CreateResult(ctx context.Context, id string) (transport.WorkspaceDTO, error) {
	ws, err := s.wm.CreateResult(ctx, id)
	if err != nil {
		return transport.WorkspaceDTO{}, err
	}
	return workspaceToDTO(ws), nil
}

// Review applies a review action.
func (s *WorkspaceService) Review(ctx context.Context, id, action string) (transport.WorkspaceDTO, error) {
	ws, err := s.wm.Review(ctx, id, workspace.ReviewAction(action))
	if err != nil {
		return transport.WorkspaceDTO{}, err
	}
	return workspaceToDTO(ws), nil
}

// Diff returns the diff.
func (s *WorkspaceService) Diff(ctx context.Context, id string) (string, error) {
	return s.wm.Diff(ctx, id)
}

// Patch exports the result patch.
func (s *WorkspaceService) Patch(ctx context.Context, id string) (string, error) {
	return s.wm.ExportPatch(ctx, id)
}

// Get returns a workspace.
func (s *WorkspaceService) Get(ctx context.Context, id string) (transport.WorkspaceDTO, error) {
	ws, err := s.wm.Get(ctx, id)
	if err != nil {
		return transport.WorkspaceDTO{}, err
	}
	return workspaceToDTO(ws), nil
}

// ListByTask lists workspaces for a task.
func (s *WorkspaceService) ListByTask(ctx context.Context, taskID string) ([]transport.WorkspaceDTO, error) {
	rows, err := s.wm.ListByTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	return workspacesToDTOs(rows), nil
}

// ListByProject lists workspaces for a project.
func (s *WorkspaceService) ListByProject(ctx context.Context, projectID string) ([]transport.WorkspaceDTO, error) {
	rows, err := s.wm.ListByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return workspacesToDTOs(rows), nil
}

// Delete removes a workspace.
func (s *WorkspaceService) Delete(ctx context.Context, id string) error {
	return s.wm.Delete(ctx, id)
}

// ListCheckpoints returns checkpoints for a workspace.
func (s *WorkspaceService) ListCheckpoints(ctx context.Context, id string) ([]transport.CheckpointDTO, error) {
	cps, err := s.wm.ListCheckpoints(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]transport.CheckpointDTO, len(cps))
	for i, c := range cps {
		out[i] = transport.CheckpointDTO{
			ID:        c.ID,
			CommitSHA: c.CommitSHA,
			Moment:    string(c.Moment),
			Message:   c.Message,
			CreatedAt: c.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		}
	}
	return out, nil
}

func workspacesToDTOs(rows []workspace.Workspace) []transport.WorkspaceDTO {
	out := make([]transport.WorkspaceDTO, len(rows))
	for i, ws := range rows {
		out[i] = workspaceToDTO(ws)
	}
	return out
}

func workspaceToDTO(ws workspace.Workspace) transport.WorkspaceDTO {
	return transport.WorkspaceDTO{
		ID:            ws.ID,
		ProjectID:     ws.ProjectID,
		TaskID:        ws.TaskID,
		WorkPackageID: ws.WorkPackageID,
		Attempt:       ws.Attempt,
		Path:          ws.Path,
		Branch:        ws.Branch,
		ResultBranch:  ws.ResultBranch,
		BaseSHA:       ws.BaseSHA,
		HeadSHA:       ws.HeadSHA,
		ResultSHA:     ws.ResultSHA,
		State:         string(ws.State),
		Engine:        ws.Engine,
		Model:         ws.Model,
		RunID:         ws.RunID,
		SessionID:     ws.SessionID,
		CreatedAt:     ws.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:     ws.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

// _ ensures storage is referenced.
var _ = storage.ErrWorkspaceNotFound
