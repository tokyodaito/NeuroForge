package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
	"neuroforge/internal/audit"
	"neuroforge/internal/project"
	"neuroforge/internal/quality"
	"neuroforge/internal/runapp"
	"neuroforge/internal/storage"
	"neuroforge/internal/supervisor"
	"neuroforge/internal/task"
	"neuroforge/internal/transport"
	"neuroforge/internal/workspace"
)

// RunAppService is the daemon-side composition root for the user-facing
// `forge run` command. It adapts the runapp application service to the
// transport API, bridging the concrete daemon-owned dependencies (workspace
// manager, supervisor, task backlog, project registry, storage, audit,
// accounting) into runapp's interfaces.
//
// The service is the daemon-side counterpart of the runapp.Service — it
// exists only to satisfy transport.RunAppAPI without forcing runapp to import
// the daemon, supervisor or transport packages (NFR-7 + import-cycle safety).
type RunAppService struct {
	svc        *runapp.Service
	projects   *project.Registry
	tasks      *task.Backlog
	wm         *workspace.Manager
	sup        *supervisor.Supervisor
	accounting *quality.Accounting
	logger     *slog.Logger
}

// NewRunAppService wires the daemon-owned dependencies into a runapp.Service.
// The returned service implements transport.RunAppAPI.
func NewRunAppService(
	wm *workspace.Manager,
	sup *supervisor.Supervisor,
	tasks *task.Backlog,
	projects *project.Registry,
	db *storage.DB,
	rec *audit.Recorder,
	acc *quality.Accounting,
	logger *slog.Logger,
) *RunAppService {
	svc := runapp.NewServiceWithRunner(runapp.RunOptions{
		Workspaces: wm,
		Supervisor: &supervisorRunnerAdapter{sup: sup},
		Tasks:      tasks,
		Audit:      rec,
		DB:         db,
		Usage:      &usageSinkAdapter{tasks: tasks, projects: projects, db: db, accounting: acc},
	})
	return &RunAppService{
		svc: svc, projects: projects, tasks: tasks, wm: wm,
		sup: sup, accounting: acc, logger: logger,
	}
}

// RunTask implements transport.RunAppAPI. It resolves the project + base
// branch, validates the engine, and delegates to runapp.Service.Run. The
// result is mapped to the transport DTO (OUTCOME_CONTRACT.md §3).
func (s *RunAppService) RunTask(ctx context.Context, req transport.RunTaskRequest) (transport.RunTaskResultDTO, error) {
	if req.ProjectID == "" {
		return transport.RunTaskResultDTO{}, fmt.Errorf("run: project id is required")
	}
	if req.Description == "" {
		return transport.RunTaskResultDTO{Engine: req.Engine, Model: req.Model,
			Error: "description is required", ErrorClass: "EMPTY_PROMPT"}, nil
	}
	p, err := s.projects.Get(ctx, req.ProjectID)
	if err != nil {
		return transport.RunTaskResultDTO{Error: fmt.Sprintf("project: %v", err),
			ErrorClass: "NOT_A_REPO"}, err
	}

	// Validate the engine is known (FR-1.2 / UNKNOWN_ENGINE).
	engine := req.Engine
	if engine == "" {
		engine = "opencode"
	}
	if _, ok := s.sup.Adapters().Lookup(engine); !ok {
		return transport.RunTaskResultDTO{Engine: engine, Model: req.Model,
			Error:      fmt.Sprintf("unknown engine %q", engine),
			ErrorClass: "UNKNOWN_ENGINE"}, fmt.Errorf("run: unknown engine %q", engine)
	}

	// Create the task in the backlog. The user-facing run creates a NEW task
	// per invocation (REQUIREMENTS.md §1.1; the minimal run does not reuse
	// task ids across retries — OUTCOME_CONTRACT.md §5).
	t, err := s.tasks.Add(ctx, task.AddRequest{
		ProjectID:   p.ID,
		Description: req.Description,
	})
	if err != nil {
		return transport.RunTaskResultDTO{Engine: engine, Model: req.Model,
			Error:      fmt.Sprintf("create task: %v", err),
			ErrorClass: "INTERNAL_ERROR"}, err
	}

	// Drive the run end-to-end via runapp.Service.Run.
	res, err := s.svc.Run(ctx, runapp.RunRequest{
		ProjectID:   p.ID,
		ProjectPath: p.Path,
		TaskID:      t.ID,
		Description: req.Description,
		Engine:      engine,
		Model:       req.Model,
		Timeout:     req.Timeout,
	})
	if err != nil && res.Error == "" {
		res.Error = err.Error()
	}
	if res.ErrorClass == "" && err != nil {
		res.ErrorClass = "INTERNAL_ERROR"
	}
	if res.ChangedFiles == nil {
		res.ChangedFiles = []string{}
	}
	return transport.RunTaskResultDTO{
		Outcome:       string(res.Outcome),
		TaskID:        res.TaskID,
		WorkspaceID:   res.WorkspaceID,
		RunID:         res.RunID,
		WorkspacePath: res.WorkspacePath,
		BaseSHA:       res.BaseSHA,
		ActualHeadSHA: res.ActualHEADSHA,
		Engine:        res.Engine,
		Model:         res.Model,
		ChangedFiles:  res.ChangedFiles,
		CommitSHA:     res.CommitSHA,
		ResultBranch:  res.ResultBranch,
		NextAction:    res.NextAction,
		Error:         res.Error,
		ErrorClass:    res.ErrorClass,
	}, nil
}

// supervisorRunnerAdapter adapts *supervisor.Supervisor to runapp's
// SupervisorRunner interface (so runapp does not need to import the
// supervisor package).
type supervisorRunnerAdapter struct {
	sup *supervisor.Supervisor
}

func (a *supervisorRunnerAdapter) Run(ctx context.Context, req runapp.SupervisorRequest, workspacePath string) (runapp.SupervisorResult, error) {
	res, err := a.sup.Run(ctx, supervisor.RunRequest{
		WorkspaceID: req.WorkspaceID,
		Engine:      req.Engine,
		Model:       req.Model,
		Prompt:      req.Prompt,
		Timeout:     req.Timeout,
	}, workspacePath)
	if err != nil {
		return runapp.SupervisorResult{}, err
	}
	return runapp.SupervisorResult{
		Handle:    res.Handle,
		Outcome:   res.Outcome,
		Events:    res.Events,
		Failed:    res.Failed,
		Cancelled: res.Cancelled,
	}, nil
}

// usageSinkAdapter adapts the daemon's storage + accounting to runapp's
// UsageSink interface (so runapp does not need to import the storage / quality
// / project packages). This is the KF-10 fix path: usage from a runapp-driven
// production adapter run is persisted to usage_events.
type usageSinkAdapter struct {
	tasks      *task.Backlog
	projects   *project.Registry
	db         *storage.DB
	accounting *quality.Accounting
}

func (u *usageSinkAdapter) RecordUsage(ctx context.Context, e runapp.UsageEvent) error {
	// Resolve the project id from the task id if not provided (the runapp
	// path passes both, but be defensive).
	projectID := e.ProjectID
	if projectID == "" && e.TaskID != "" && u.tasks != nil {
		if t, err := u.tasks.Get(ctx, e.TaskID); err == nil {
			projectID = t.ProjectID
		}
	}
	if u.accounting != nil {
		u.accounting.Record(quality.UsageEvent{
			TaskID:            e.TaskID,
			ProjectID:         projectID,
			Provider:          e.Provider,
			Model:             e.Model,
			Kind:              quality.UsageKind(e.Kind),
			InputTokens:       int(e.InputTokens),
			CachedInputTokens: int(e.CachedInputTokens),
			OutputTokens:      int(e.OutputTokens),
			Generations:       int(e.Generations),
			CostUSD:           e.CostUSD,
			OccurredAt:        e.OccurredAt,
		})
	}
	if u.db == nil {
		return nil
	}
	_, err := u.db.RecordUsageEvent(ctx, storage.UsageEventRow{
		TaskID:            e.TaskID,
		ProjectID:         projectID,
		Provider:          e.Provider,
		Model:             e.Model,
		Kind:              e.Kind,
		InputTokens:       int(e.InputTokens),
		CachedInputTokens: int(e.CachedInputTokens),
		OutputTokens:      int(e.OutputTokens),
		Generations:       int(e.Generations),
		CostUSD:           e.CostUSD,
		OccurredAt:        e.OccurredAt,
	})
	return err
}

// _ keeps the protocol import referenced for future failure-class mapping.
var _ = protocol.EventRunCompleted

// _ keeps time referenced for default timeout constants.
var _ = time.Minute
