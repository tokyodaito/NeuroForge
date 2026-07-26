package runapp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
	"neuroforge/internal/audit"
	"neuroforge/internal/storage"
	"neuroforge/internal/task"
	"neuroforge/internal/workspace"
)

// SupervisorRunner is the subset of *supervisor.Supervisor the runapp service
// consumes (so runapp does not import the supervisor package — the daemon
// wires the concrete supervisor in). The Run method blocks until the run
// reaches a terminal state (run.completed/failed/cancelled) or the timeout
// fires (synthesized as run.failed(TIMEOUT)).
type SupervisorRunner interface {
	// Run launches one adapter run and returns the terminal outcome.
	Run(ctx context.Context, req SupervisorRequest, workspacePath string) (SupervisorResult, error)
}

// SupervisorRequest is the supervisor-side request shape (mirrors
// supervisor.RunRequest minus the import).
type SupervisorRequest struct {
	WorkspaceID string
	Engine      string
	Model       string
	Prompt      string
	Timeout     time.Duration
}

// SupervisorResult is the supervisor-side result shape (mirrors
// supervisor.RunResult minus the import).
type SupervisorResult struct {
	Handle    protocol.RunHandle
	Outcome   protocol.NormalizedEvent
	Events    []protocol.NormalizedEvent
	Failed    bool
	Cancelled bool
}

// WorkspaceCreator creates a workspace (worktree) for a task. Implemented by
// *workspace.Manager.
type WorkspaceCreator interface {
	Create(ctx context.Context, req workspace.CreateRequest) (workspace.Workspace, error)
}

// RunRequest is the input to [Service.Run] — the single end-to-end entry
// point of the minimal reliable run.
type RunRequest struct {
	// ProjectID is the resolved project id (already registered).
	ProjectID string
	// ProjectPath is the resolved project filesystem path (primary checkout,
	// read-only).
	ProjectPath string
	// TaskID is the existing task id (the caller has already created it via
	// the task backlog). FR-3.
	TaskID string
	// Description is the prompt body forwarded to the adapter (FR-6).
	Description string
	// Engine is the adapter engine id (default "opencode").
	Engine string
	// Model is the model id forwarded to the adapter (default
	// "zai-coding-plan/glm-5.2").
	Model string
	// Timeout is the hard wall-clock timeout for the run (default 10m).
	Timeout time.Duration
}

// RunResult is the structured result consumed by the CLI. It mirrors the
// OUTCOME_CONTRACT.md §3 JSON shape so the CLI can marshal it directly.
type RunResult struct {
	Outcome        Outcome
	TaskID         string
	WorkspaceID    string
	RunID          string
	WorkspacePath  string
	BaseSHA        string
	ActualHEADSHA  string
	Engine         string
	Model          string
	ChangedFiles   []string
	CommitSHA      string
	ResultBranch   string
	NextAction     string
	Error          string
	ErrorClass     string
	FinalizeResult *FinalizeResult
}

// RunOptions configures a Service with the concrete dependencies the daemon
// owns. All fields are required; NewServiceWithRunner panics on nil.
type RunOptions struct {
	Workspaces WorkspaceCreator
	Supervisor SupervisorRunner
	Tasks      TaskBacklog
	Audit      *audit.Recorder
	DB         *storage.DB
	Usage      UsageSink
	Now        func() time.Time
}

// NewServiceWithRunner constructs a Service that can both Finalize (S3) and
// Run (S7) end-to-end. The daemon wires the real supervisor into
// RunOptions.Supervisor; tests inject a stub.
func NewServiceWithRunner(opts RunOptions) *Service {
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	// Build a Service with the finalize-only deps filled in; wsManager is
	// re-used as the WorkspaceManager / RefCreator via a thin adapter that
	// also implements WorkspaceCreator.
	svc := &Service{
		wm:      &wsManagerShim{creator: opts.Workspaces, manager: asManager(opts.Workspaces)},
		tasks:   opts.Tasks,
		audit:   opts.Audit,
		db:      opts.DB,
		usage:   opts.Usage,
		now:     now,
		refs:    &wsManagerShim{creator: opts.Workspaces, manager: asManager(opts.Workspaces)},
		taskRev: opts.Tasks,
		creator: opts.Workspaces,
		sup:     opts.Supervisor,
	}
	return svc
}

// asManager returns *workspace.Manager if w is one, else nil. Used to route
// Get/UpdateStateTx/InspectWorktree/EnsureResultRef via the concrete manager
// when the daemon wires the real one.
func asManager(w WorkspaceCreator) *workspace.Manager {
	if m, ok := w.(*workspace.Manager); ok {
		return m
	}
	return nil
}

// wsManagerShim adapts a *workspace.Manager (which implements both
// WorkspaceCreator and WorkspaceManager) to the WorkspaceManager +
// RefCreator interfaces runapp's Finalize expects. When the daemon wires a
// real manager, Get/UpdateStateTx/InspectWorktree/EnsureResultRef go through
// it directly; when a test injects a custom WorkspaceManager (e.g. the
// finalize unit tests), the shim fields are nil and the manager field is
// used.
type wsManagerShim struct {
	creator WorkspaceCreator
	manager *workspace.Manager
}

func (s *wsManagerShim) Get(ctx context.Context, id string) (workspace.Workspace, error) {
	if s.manager == nil {
		return workspace.Workspace{}, errors.New("runapp: workspace manager not wired")
	}
	return s.manager.Get(ctx, id)
}

func (s *wsManagerShim) UpdateStateTx(ctx context.Context, tx *storage.Tx, id string, state workspace.State, headSHA, resultBranch, resultSHA string) error {
	if s.manager == nil {
		return errors.New("runapp: workspace manager not wired")
	}
	return s.manager.UpdateStateTx(ctx, tx, id, state, headSHA, resultBranch, resultSHA)
}

func (s *wsManagerShim) InspectWorktree(ctx context.Context, ws workspace.Workspace) (workspace.Inspection, error) {
	if s.manager == nil {
		return workspace.Inspection{}, errors.New("runapp: workspace manager not wired")
	}
	return s.manager.InspectWorktree(ctx, ws)
}

func (s *wsManagerShim) EnsureResultRef(ctx context.Context, ws workspace.Workspace, headSHA string) (string, error) {
	if s.manager == nil {
		return "", errors.New("runapp: workspace manager not wired")
	}
	return s.manager.EnsureResultRef(ctx, ws, headSHA)
}

func (s *wsManagerShim) DeleteResultRef(ctx context.Context, ws workspace.Workspace) error {
	if s.manager == nil {
		return errors.New("runapp: workspace manager not wired")
	}
	return s.manager.DeleteResultRef(ctx, ws)
}

func (s *wsManagerShim) ResolveResultRef(ctx context.Context, taskID, dir string) (string, error) {
	if s.manager == nil {
		return "", errors.New("runapp: workspace manager not wired")
	}
	return s.manager.ResolveResultRef(ctx, taskID, dir)
}

// Run drives one attempt end-to-end (FR-5, FR-8, S7). It is the single owner
// of the sequence:
//
//  1. create the workspace (worktree) for the task (FR-4);
//  2. dispatch the task into RUNNING (STATE_MACHINE.md §4.2);
//  3. launch one production adapter via the supervisor (FR-5);
//  4. wait for the single terminal adapter event (FR-8);
//  5. inspect the worktree's actual Git state (S1, FR-9);
//  6. persist usage events emitted by the adapter (KF-10);
//  7. finalize workspace + task + audit atomically (S3/S4/S5).
//
// It returns a structured Result the CLI maps to its human + JSON output. The
// function does NOT import the scheduler, failover, postmerge, review or merge
// packages (NFR-7).
func (s *Service) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	if s.creator == nil {
		return RunResult{ErrorClass: "INTERNAL_ERROR", Error: "runapp: workspace creator not wired"}, errors.New("runapp: nil workspace creator")
	}
	if s.sup == nil {
		return RunResult{ErrorClass: "INTERNAL_ERROR", Error: "runapp: supervisor not wired"}, errors.New("runapp: nil supervisor")
	}
	// Apply default engine/model/timeout (REQUIREMENTS.md §1.1).
	engine := req.Engine
	if engine == "" {
		engine = "opencode"
	}
	model := req.Model
	if model == "" {
		model = "zai-coding-plan/glm-5.2"
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}

	// 1. Create the workspace (worktree).
	ws, err := s.creator.Create(ctx, workspace.CreateRequest{
		ProjectID:   req.ProjectID,
		ProjectPath: req.ProjectPath,
		TaskID:      req.TaskID,
	})
	if err != nil {
		return RunResult{
			TaskID: req.TaskID, Engine: engine, Model: model,
			Error:      fmt.Sprintf("create workspace: %v", err),
			ErrorClass: "INTERNAL_ERROR",
		}, fmt.Errorf("runapp: create workspace: %w", err)
	}

	// 2. Dispatch the task into RUNNING (idempotent: if the caller already
	// dispatched, the transition is a no-op via the idempotent finalize path
	// — but here we use Transition which may fail if the task is already
	// RUNNING. We tolerate that as non-fatal.)
	if _, terr := s.tasks.Transition(ctx, req.TaskID, task.ActionDispatch); terr != nil {
		// The task may already be RUNNING (re-entry); ignore that case.
		var ite *task.ErrInvalidTransition
		if !errors.As(terr, &ite) {
			return RunResult{
				TaskID: req.TaskID, WorkspaceID: ws.ID, Engine: engine, Model: model,
				Error:      fmt.Sprintf("dispatch task: %v", terr),
				ErrorClass: "INTERNAL_ERROR",
			}, fmt.Errorf("runapp: dispatch task: %w", terr)
		}
	}

	// 3 + 4. Launch + wait for terminal.
	res, err := s.sup.Run(ctx, SupervisorRequest{
		WorkspaceID: ws.ID,
		Engine:      engine,
		Model:       model,
		Prompt:      req.Description,
		Timeout:     timeout,
	}, ws.Path)
	if err != nil {
		// Finalize as a failure even if the supervisor could not start.
		ins, _ := s.wm.InspectWorktree(ctx, ws)
		_, _ = s.Finalize(ctx, FinalizeRequest{
			WorkspaceID: ws.ID, TaskID: req.TaskID,
			TerminalEvent: protocol.NormalizedEvent{
				Type:    protocol.EventRunFailed,
				Failure: &protocol.FailurePayload{Class: protocol.FailureInternalError, Reason: err.Error()},
			},
			Inspection: ins,
			Engine:     engine, Model: model,
		})
		return RunResult{
			TaskID: req.TaskID, WorkspaceID: ws.ID,
			WorkspacePath: ws.Path, BaseSHA: ws.BaseSHA,
			Engine: engine, Model: model,
			Error:      fmt.Sprintf("supervisor run: %v", err),
			ErrorClass: "ADAPTER_FAILED",
		}, nil
	}

	// 5. Post-run inspection (FR-9).
	//
	// BF-02: the run has reached its terminal event. From here on we persist
	// the outcome on a DETACHED context so it is durable even if the caller
	// (the CLI) has already disconnected after a user SIGINT — the request
	// context may be cancelled, but the terminal DB state + result ref must
	// still be written. The client connection is ephemeral; the result is not.
	finCtx, finCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer finCancel()
	wsFresh, _ := s.wm.Get(finCtx, ws.ID)
	ins, insErr := s.wm.InspectWorktree(finCtx, wsFresh)
	if insErr != nil {
		// Finalize as a failure classified as GIT_INSPECT_FAILED.
		_, _ = s.Finalize(finCtx, FinalizeRequest{
			WorkspaceID: ws.ID, TaskID: req.TaskID,
			TerminalEvent: res.Outcome,
			Inspection:    workspace.Inspection{},
			Engine:        engine, Model: model,
			RunID: res.Handle.RunID,
		})
		return RunResult{
			TaskID: req.TaskID, WorkspaceID: ws.ID, RunID: res.Handle.RunID,
			WorkspacePath: ws.Path, BaseSHA: ws.BaseSHA,
			Engine: engine, Model: model,
			Error:      fmt.Sprintf("git inspect: %v", insErr),
			ErrorClass: "GIT_INSPECT_FAILED",
		}, nil
	}

	// 6. Persist usage events from the run (KF-10). Best-effort: never blocks
	// finalize.
	if s.usage != nil {
		s.recordUsage(finCtx, req.TaskID, req.ProjectID, engine, model, res.Events)
	}

	// 7. Finalize (S3/S4/S5).
	fin, err := s.Finalize(finCtx, FinalizeRequest{
		WorkspaceID:   ws.ID,
		TaskID:        req.TaskID,
		TerminalEvent: res.Outcome,
		Inspection:    ins,
		Engine:        engine,
		Model:         model,
		RunID:         res.Handle.RunID,
	})
	if err != nil {
		return RunResult{
			TaskID: req.TaskID, WorkspaceID: ws.ID, RunID: res.Handle.RunID,
			WorkspacePath: ws.Path, BaseSHA: ws.BaseSHA,
			Engine: engine, Model: model,
			Error:      fmt.Sprintf("finalize: %v", err),
			ErrorClass: "INTERNAL_ERROR",
		}, fmt.Errorf("runapp: finalize: %w", err)
	}

	finalResult := fin
	return RunResult{
		Outcome:        fin.Outcome,
		TaskID:         req.TaskID,
		WorkspaceID:    ws.ID,
		RunID:          res.Handle.RunID,
		WorkspacePath:  ws.Path,
		BaseSHA:        ws.BaseSHA,
		ActualHEADSHA:  fin.ActualHEAD,
		Engine:         engine,
		Model:          model,
		ChangedFiles:   fin.ChangedFiles,
		CommitSHA:      fin.CommitSHA,
		ResultBranch:   fin.ResultBranch,
		NextAction:     NextActionFor(fin.Outcome, req.TaskID, ws.ID),
		FinalizeResult: &finalResult,
	}, nil
}

// NextActionFor returns a concrete suggested next command for an outcome
// (OUTCOME_CONTRACT.md §2 / §3.1).
func NextActionFor(o Outcome, taskID, wsID string) string {
	switch o {
	case OutcomeCompletedWithCommit:
		return "forge task show " + taskID
	case OutcomeCompletedWithUncommittedChanges:
		return "forge workspace diff " + wsID
	case OutcomeCompletedNoChanges:
		return "rephrase the task and run again"
	case OutcomeFailed:
		return "check the agent output; re-run with a clearer task description"
	case OutcomeCancelled:
		return "re-run when ready"
	case OutcomeTimedOut:
		return "raise --timeout, or split the task into smaller steps"
	case OutcomeInterrupted:
		return "re-run; the daemon was restarted mid-run"
	}
	return ""
}

// recordUsage extracts usage events from the run's event stream and persists
// them via the UsageSink (KF-10). It is best-effort and never returns an
// error that would abort the run — usage recording is observability, not a
// correctness gate.
func (s *Service) recordUsage(ctx context.Context, taskID, projectID, engine, model string, events []protocol.NormalizedEvent) {
	now := s.now()
	for _, ev := range events {
		if ev.Type != protocol.EventUsageUpdated || ev.Usage == nil {
			continue
		}
		ue := UsageEvent{
			TaskID:            taskID,
			ProjectID:         projectID,
			Provider:          engine,
			Model:             model,
			Kind:              "coding",
			InputTokens:       int64(ev.Usage.InputTokens),
			CachedInputTokens: int64(ev.Usage.CacheReadTokens),
			OutputTokens:      int64(ev.Usage.OutputTokens),
			CostUSD:           ev.Usage.Cost,
			OccurredAt:        now,
		}
		if err := s.usage.RecordUsage(ctx, ue); err != nil {
			if s.audit != nil {
				_, _ = s.audit.Record(ctx, audit.Event{
					Type:  "run.usage_persist_failed",
					Scope: audit.ScopeTask, ScopeID: taskID, Actor: audit.ActorDaemon,
					Payload: map[string]any{"err": err.Error(), "engine": engine, "model": model},
				})
			}
		}
	}
}
