package daemon

import (
	"context"
	"fmt"
	"log/slog"

	"neuroforge/internal/audit"
	"neuroforge/internal/policy"
	"neuroforge/internal/project"
	"neuroforge/internal/storage"
	"neuroforge/internal/task"
	"neuroforge/internal/transport"
	"neuroforge/internal/workgraph"
)

// Services bundles the domain services that the daemon wires to the transport
// API. It is constructed once during daemon startup and shared across all
// request handlers.
type Services struct {
	Projects *project.Registry
	Tasks    *task.Backlog
	Specs    *task.SpecificationStore
	Graphs   *workgraph.WorkGraphStore
	Leases   *workgraph.LeaseManager
	Bus      *transport.Bus
}

// NewServices constructs the domain services from durable storage + audit.
func NewServices(db *storage.DB, rec *audit.Recorder, artifactsDir string, bus *transport.Bus, logger *slog.Logger) *Services {
	return &Services{
		Projects: project.NewRegistry(db, rec, logger),
		Tasks:    task.NewBacklog(db, rec, artifactsDir, logger),
		Specs:    task.NewSpecificationStore(db, rec, logger),
		Graphs:   workgraph.NewWorkGraphStore(db, rec, logger),
		Leases:   workgraph.NewLeaseManager(db),
		Bus:      bus,
	}
}

// apiAdapter implements transport.ProjectAPI and transport.TaskAPI by delegating
// to the domain Services. It converts between wire DTOs and domain types.
type apiAdapter struct {
	svc *Services
}

// newAPIAdapter returns an adapter that implements both transport.ProjectAPI and
// transport.TaskAPI.
func newAPIAdapter(svc *Services) *apiAdapter {
	return &apiAdapter{svc: svc}
}

// ---- ProjectAPI ----

func (a *apiAdapter) ListProjects(ctx context.Context) ([]transport.ProjectDTO, error) {
	projects, err := a.svc.Projects.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]transport.ProjectDTO, len(projects))
	for i, p := range projects {
		out[i] = projectToDTO(p)
	}
	return out, nil
}

func (a *apiAdapter) AddProject(ctx context.Context, req transport.AddProjectRequest) (transport.ProjectDTO, error) {
	if req.Path == "" {
		return transport.ProjectDTO{}, fmt.Errorf("path is required")
	}
	p, err := a.svc.Projects.Add(ctx, project.AddRequest{
		Path:    req.Path,
		Name:    req.Name,
		Profile: policy.Profile(req.Profile),
	})
	if err != nil {
		return transport.ProjectDTO{}, err
	}
	a.svc.Bus.Publish("project.added", map[string]any{"id": p.ID, "name": p.Name})
	return projectToDTO(p), nil
}

func (a *apiAdapter) GetProject(ctx context.Context, id string) (transport.ProjectDTO, error) {
	p, err := a.svc.Projects.Get(ctx, id)
	if err != nil {
		return transport.ProjectDTO{}, err
	}
	return projectToDTO(p), nil
}

func (a *apiAdapter) RemoveProject(ctx context.Context, id string) error {
	if err := a.svc.Projects.Remove(ctx, id); err != nil {
		return err
	}
	a.svc.Bus.Publish("project.removed", map[string]any{"id": id})
	return nil
}

func (a *apiAdapter) StartProject(ctx context.Context, id string) (transport.ProjectDTO, error) {
	return a.projectTransition(ctx, id, "start")
}

func (a *apiAdapter) PauseProject(ctx context.Context, id string) (transport.ProjectDTO, error) {
	return a.projectTransition(ctx, id, "pause")
}

func (a *apiAdapter) StopProject(ctx context.Context, id string) (transport.ProjectDTO, error) {
	return a.projectTransition(ctx, id, "stop")
}

func (a *apiAdapter) projectTransition(ctx context.Context, id string, action string) (transport.ProjectDTO, error) {
	act := project.Action(action)
	p, err := a.svc.Projects.Transition(ctx, id, act)
	if err != nil {
		return transport.ProjectDTO{}, err
	}
	a.svc.Bus.Publish("project.state_changed", map[string]any{
		"id": p.ID, "action": action, "state": string(p.State),
	})
	return projectToDTO(p), nil
}

// ---- TaskAPI ----

func (a *apiAdapter) ListTasks(ctx context.Context, projectID string) ([]transport.TaskDTO, error) {
	var tasks []task.Task
	var err error
	if projectID != "" {
		tasks, err = a.svc.Tasks.ListByProject(ctx, projectID)
	} else {
		tasks, err = a.svc.Tasks.ListAll(ctx)
	}
	if err != nil {
		return nil, err
	}
	out := make([]transport.TaskDTO, len(tasks))
	for i, t := range tasks {
		out[i] = taskToDTO(t)
	}
	return out, nil
}

func (a *apiAdapter) AddTask(ctx context.Context, req transport.AddTaskRequest) (transport.TaskDTO, error) {
	if req.ProjectID == "" {
		return transport.TaskDTO{}, fmt.Errorf("project_id is required")
	}
	if req.Description == "" && len(req.Attachments) == 0 {
		return transport.TaskDTO{}, fmt.Errorf("description or attachment is required")
	}

	addReq := task.AddRequest{
		ProjectID:   req.ProjectID,
		Description: req.Description,
		Title:       req.Title,
		Priority:    task.Priority(req.Priority),
	}
	for _, att := range req.Attachments {
		addReq.Attachments = append(addReq.Attachments, task.AttachmentInput{
			Path:     att.Path,
			Filename: att.Filename,
			Role:     task.AttachmentRole(att.Role),
		})
	}

	t, err := a.svc.Tasks.Add(ctx, addReq)
	if err != nil {
		return transport.TaskDTO{}, err
	}
	a.svc.Bus.Publish("task.created", map[string]any{
		"id": t.ID, "project": t.ProjectID, "title": t.Title,
	})
	return taskToDTO(t), nil
}

func (a *apiAdapter) GetTask(ctx context.Context, id string) (transport.TaskDTO, error) {
	t, err := a.svc.Tasks.Get(ctx, id)
	if err != nil {
		return transport.TaskDTO{}, err
	}
	return taskToDTO(t), nil
}

func (a *apiAdapter) PauseTask(ctx context.Context, id string) (transport.TaskDTO, error) {
	return a.taskTransition(ctx, id, "pause")
}

func (a *apiAdapter) CancelTask(ctx context.Context, id string) (transport.TaskDTO, error) {
	return a.taskTransition(ctx, id, "cancel")
}

func (a *apiAdapter) taskTransition(ctx context.Context, id string, action string) (transport.TaskDTO, error) {
	act := task.Action(action)
	t, err := a.svc.Tasks.Transition(ctx, id, act)
	if err != nil {
		return transport.TaskDTO{}, err
	}
	a.svc.Bus.Publish("task.state_changed", map[string]any{
		"id": t.ID, "action": action, "state": string(t.State),
	})
	return taskToDTO(t), nil
}

// ---- DTO converters ----

func projectToDTO(p project.Project) transport.ProjectDTO {
	return transport.ProjectDTO{
		ID:        p.ID,
		Name:      p.Name,
		Path:      p.Path,
		Remote:    p.Remote,
		State:     string(p.State),
		Profile:   string(p.Profile),
		CreatedAt: p.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt: p.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func taskToDTO(t task.Task) transport.TaskDTO {
	dto := transport.TaskDTO{
		ID:          t.ID,
		ProjectID:   t.ProjectID,
		Title:       t.Title,
		Description: t.Description,
		Priority:    string(t.Priority),
		State:       string(t.State),
		CreatedAt:   t.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:   t.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	for _, a := range t.Attachments {
		dto.Attachments = append(dto.Attachments, transport.AttachmentDTO{
			Hash:     a.Hash,
			Filename: a.Filename,
			MimeType: a.MimeType,
			Size:     a.Size,
			Role:     string(a.Role),
		})
	}
	return dto
}
