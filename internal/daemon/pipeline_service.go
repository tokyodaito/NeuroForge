package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
	"neuroforge/internal/artifacts"
	"neuroforge/internal/audit"
	"neuroforge/internal/pipeline"
	"neuroforge/internal/project"
	"neuroforge/internal/review"
	"neuroforge/internal/runapp"
	"neuroforge/internal/storage"
	"neuroforge/internal/supervisor"
	"neuroforge/internal/task"
	"neuroforge/internal/testengine"
	"neuroforge/internal/transport"
	"neuroforge/internal/workgraph"
	"neuroforge/internal/workspace"
)

// This file implements the daemon-side composition root for the durable
// pipeline (internal/pipeline, M14-06) — the production `forge run` path.
//
// The PipelineService owns:
//   - the pipeline Store (durable run/stage state) and Driver (routing only);
//   - the concrete stage handlers (pipeline_handlers.go) that compose the
//     existing subsystems: task compiler (compile), work graph (plan/ready),
//     workspace manager + supervisor (execute/repair), test engine (verify),
//     review engine (review) and runapp.Service.Finalize (finalize — the
//     crash-consistent terminal chokepoint, reused not reimplemented);
//   - restart recovery (ResumeActiveRuns) and the emergency stop;
//   - durable per-run params so every handler is restart-safe (nothing the
//     handlers need lives only in memory).
//
// Cancellation semantics: a user cancel (POST /tasks/{id}/pipeline/cancel or
// SIGINT on `forge run`) is durable — Store.Cancel makes the run terminal and
// restart recovery never resumes it. An emergency stop cancels in-flight
// agent work and fails the in-flight stage as `interrupted`; runs between
// stages stay active and Drive refuses to start new stages until the flag is
// cleared.
type PipelineService struct {
	store  *pipeline.Store
	driver *pipeline.Driver

	tasks    *task.Backlog
	projects *project.Registry
	specs    *task.SpecificationStore
	graphs   *workgraph.WorkGraphStore
	leases   *workgraph.LeaseManager
	sched    *workgraph.Scheduler
	wm       *workspace.Manager
	sup      *supervisor.Supervisor
	fin      *runapp.Service
	usage    runapp.UsageSink
	runner   testengine.Runner
	reviewer review.Reviewer // nil → AgentReviewer over the supervisor
	rec      *audit.Recorder
	logger   *slog.Logger

	artifacts *artifacts.Store
	paramsDir string // <artifacts>/pipeline-params/<task-id>.json

	mu      sync.Mutex
	cancels map[string]context.CancelFunc // taskID → cancel of the active agent run ctx
	driving map[string]bool               // single-flight: one Drive per task
	lastFin map[string]runapp.FinalizeResult
}

// PipelineDeps are the daemon-owned dependencies of a PipelineService.
type PipelineDeps struct {
	DB       *storage.DB
	Recorder *audit.Recorder
	Logger   *slog.Logger
	Dirs     Dirs
	Tasks    *task.Backlog
	Projects *project.Registry
	Specs    *task.SpecificationStore
	Graphs   *workgraph.WorkGraphStore
	Leases   *workgraph.LeaseManager
	WM       *workspace.Manager
	Sup      *supervisor.Supervisor
	Usage    runapp.UsageSink
	// Reviewer, when non-nil, overrides the default AgentReviewer (tests).
	Reviewer review.Reviewer
	// Runner, when non-nil, overrides the default ShellRunner (tests).
	Runner testengine.Runner
}

// NewPipelineService builds the durable pipeline service: Store + Driver with
// concrete handlers, the reused runapp.Service (Finalize only), the shell
// test runner, and the work-graph scheduler.
func NewPipelineService(deps PipelineDeps) (*PipelineService, error) {
	logger := deps.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	}
	art, err := artifacts.New(deps.Dirs.ArtifactsDir)
	if err != nil {
		return nil, fmt.Errorf("pipeline service: artifacts store: %w", err)
	}
	paramsDir := filepath.Join(deps.Dirs.ArtifactsDir, "pipeline-params")
	if err := os.MkdirAll(paramsDir, 0o700); err != nil {
		return nil, fmt.Errorf("pipeline service: params dir: %w", err)
	}
	s := &PipelineService{
		tasks:     deps.Tasks,
		projects:  deps.Projects,
		specs:     deps.Specs,
		graphs:    deps.Graphs,
		leases:    deps.Leases,
		sched:     workgraph.NewScheduler(deps.Graphs, deps.Leases),
		wm:        deps.WM,
		sup:       deps.Sup,
		usage:     deps.Usage,
		runner:    deps.Runner,
		reviewer:  deps.Reviewer,
		rec:       deps.Recorder,
		logger:    logger,
		artifacts: art,
		paramsDir: paramsDir,
		cancels:   map[string]context.CancelFunc{},
		driving:   map[string]bool{},
		lastFin:   map[string]runapp.FinalizeResult{},
	}
	if s.runner == nil {
		s.runner = testengine.NewShellRunner(testengine.ShellRunnerOptions{Logger: logger})
	}
	s.fin = runapp.NewService(runapp.Options{
		Workspaces: deps.WM,
		Tasks:      deps.Tasks,
		Audit:      deps.Recorder,
		DB:         deps.DB,
		Usage:      deps.Usage,
	})
	s.store = pipeline.NewStore(deps.DB, logger)
	drv, err := pipeline.NewDriver(s.store, pipeline.Handlers{
		Compile:  s.handleCompile,
		Plan:     s.handlePlan,
		Ready:    s.handleReady,
		Finalize: s.handleFinalize,
		Execute:  s.handleExecute,
		Verify:   s.handleVerify,
		Review:   s.handleReview,
		Repair:   s.handleRepair,
	}, logger)
	if err != nil {
		return nil, fmt.Errorf("pipeline service: driver: %w", err)
	}
	s.driver = drv
	return s, nil
}

// pipelineParams is the durable per-run input. It is written before the run
// is created so every stage handler can rebuild its full context after a
// daemon restart (restart-safety: handlers hold no run-specific memory).
type pipelineParams struct {
	ProjectID    string `json:"project_id"`
	ProjectPath  string `json:"project_path"`
	Description  string `json:"description"`
	Engine       string `json:"engine"`
	Model        string `json:"model"`
	BaseBranch   string `json:"base_branch,omitempty"`
	TimeoutNanos int64  `json:"timeout_nanos,omitempty"`
}

func (s *PipelineService) saveParams(taskID string, p pipelineParams) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.paramsDir, taskID+".json"), b, 0o600)
}

func (s *PipelineService) loadParams(taskID string) (pipelineParams, error) {
	b, err := os.ReadFile(filepath.Join(s.paramsDir, taskID+".json"))
	if err != nil {
		return pipelineParams{}, fmt.Errorf("pipeline: load run params for task %s: %w", taskID, err)
	}
	var p pipelineParams
	if err := json.Unmarshal(b, &p); err != nil {
		return pipelineParams{}, fmt.Errorf("pipeline: parse run params for task %s: %w", taskID, err)
	}
	return p, nil
}

// finalizeRecord is the durable copy of the finalize outcome, written by the
// finalize handler so the status endpoint can render a completed run after a
// restart (when the in-memory lastFin entry is gone).
type finalizeRecord struct {
	Outcome      string   `json:"outcome"`
	WorkspaceID  string   `json:"workspace_id"`
	CommitSHA    string   `json:"commit_sha"`
	ResultBranch string   `json:"result_branch"`
	ChangedFiles []string `json:"changed_files"`
}

func (s *PipelineService) saveFinalizeRecord(taskID string, fin runapp.FinalizeResult) {
	rec := finalizeRecord{
		Outcome:      string(fin.Outcome),
		WorkspaceID:  fin.WorkspaceID,
		CommitSHA:    fin.CommitSHA,
		ResultBranch: fin.ResultBranch,
		ChangedFiles: fin.ChangedFiles,
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	if err := os.WriteFile(filepath.Join(s.paramsDir, taskID+".finalize.json"), b, 0o600); err != nil {
		s.logger.Warn("pipeline: persist finalize record failed", "task", taskID, "err", err)
	}
}

func (s *PipelineService) loadFinalizeRecord(taskID string) (finalizeRecord, bool) {
	b, err := os.ReadFile(filepath.Join(s.paramsDir, taskID+".finalize.json"))
	if err != nil {
		return finalizeRecord{}, false
	}
	var rec finalizeRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return finalizeRecord{}, false
	}
	return rec, true
}

// ---- single-flight ----

// beginDrive claims the right to drive taskID; false means another goroutine
// (a concurrent RunPipeline or the recovery kick) already drives it.
func (s *PipelineService) beginDrive(taskID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.driving[taskID] {
		return false
	}
	s.driving[taskID] = true
	return true
}

func (s *PipelineService) endDrive(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.driving, taskID)
}

// ---- agent-run cancellation registry ----

func (s *PipelineService) registerRunCancel(taskID string, cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancels[taskID] = cancel
}

func (s *PipelineService) unregisterRunCancel(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cancels, taskID)
}

func (s *PipelineService) cancelAgentRun(taskID string) {
	s.mu.Lock()
	cancel, ok := s.cancels[taskID]
	s.mu.Unlock()
	if ok {
		cancel()
	}
}

func (s *PipelineService) cancelAllAgentRuns() {
	s.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(s.cancels))
	for _, c := range s.cancels {
		cancels = append(cancels, c)
	}
	s.mu.Unlock()
	for _, c := range cancels {
		c()
	}
}

// ---- transport.PipelineAPI ----

// RunPipeline implements transport.PipelineAPI.RunPipeline. It creates the
// task, persists the run params, creates the durable run and drives it
// synchronously to a terminal/wait state, then settles the workspace + task
// terminal state (reusing runapp.Finalize's crash-consistent protocol).
func (s *PipelineService) RunPipeline(ctx context.Context, req transport.PipelineRunRequest) (transport.PipelineRunResultDTO, error) {
	if req.ProjectID == "" {
		return transport.PipelineRunResultDTO{}, fmt.Errorf("pipeline run: project id is required")
	}
	if req.Description == "" {
		return transport.PipelineRunResultDTO{Engine: req.Engine, Model: req.Model,
			Error: "description is required", ErrorClass: "EMPTY_PROMPT"}, nil
	}
	p, err := s.projects.Get(ctx, req.ProjectID)
	if err != nil {
		return transport.PipelineRunResultDTO{Error: fmt.Sprintf("project: %v", err),
			ErrorClass: "NOT_A_REPO"}, err
	}
	engine := req.Engine
	if engine == "" {
		engine = "opencode"
	}
	if _, ok := s.sup.Adapters().Lookup(engine); !ok {
		return transport.PipelineRunResultDTO{Engine: engine, Model: req.Model,
			Error:      fmt.Sprintf("unknown engine %q", engine),
			ErrorClass: "UNKNOWN_ENGINE"}, fmt.Errorf("pipeline run: unknown engine %q", engine)
	}

	t, err := s.tasks.Add(ctx, task.AddRequest{ProjectID: p.ID, Description: req.Description})
	if err != nil {
		return transport.PipelineRunResultDTO{Engine: engine, Model: req.Model,
			Error:      fmt.Sprintf("create task: %v", err),
			ErrorClass: "INTERNAL_ERROR"}, err
	}

	params := pipelineParams{
		ProjectID:    p.ID,
		ProjectPath:  p.Path,
		Description:  req.Description,
		Engine:       engine,
		Model:        req.Model,
		BaseBranch:   req.BaseBranch,
		TimeoutNanos: int64(time.Duration(req.TimeoutSeconds) * time.Second),
	}
	if err := s.saveParams(t.ID, params); err != nil {
		return transport.PipelineRunResultDTO{Error: fmt.Sprintf("persist run params: %v", err),
			ErrorClass: "INTERNAL_ERROR"}, err
	}
	if _, err := s.store.CreateRun(ctx, t.ID, p.ID, req.MaxRepairAttempts); err != nil {
		return transport.PipelineRunResultDTO{Error: fmt.Sprintf("create pipeline run: %v", err),
			ErrorClass: "INTERNAL_ERROR"}, err
	}
	// Dispatch the task (NEW → RUNNING); the terminal transition is applied by
	// the finalize stage / settleRun.
	if _, err := s.tasks.Transition(ctx, t.ID, task.ActionDispatch); err != nil {
		s.logger.Warn("pipeline: task dispatch transition failed", "task", t.ID, "err", err)
	}
	s.audit(ctx, "pipeline.run_started", t.ID, audit.Payload(
		"project", p.ID, "engine", engine, "model", req.Model))

	if !s.beginDrive(t.ID) {
		return transport.PipelineRunResultDTO{TaskID: t.ID, Engine: engine, Model: req.Model,
			Error:      "run already in progress for this task",
			ErrorClass: "INTERNAL_ERROR"}, fmt.Errorf("pipeline run: task %s already driving", t.ID)
	}
	driveErr := s.driver.Drive(ctx, t.ID)
	s.endDrive(t.ID)

	// Settle the workspace + task terminal state on a detached context: when
	// the client went away (SIGINT), the run's terminal persistence must still
	// complete (BF-02 semantics, unchanged from the runapp path).
	settleCtx := context.WithoutCancel(ctx)
	run, rerr := s.store.CurrentRun(settleCtx, t.ID)
	if rerr == nil {
		s.settleRun(settleCtx, t.ID, run)
	}

	dto, berr := s.buildDTO(settleCtx, t.ID)
	if berr != nil {
		return transport.PipelineRunResultDTO{}, berr
	}
	switch {
	case errors.Is(driveErr, pipeline.ErrEmergencyStopped):
		// The run is NOT failed — it stays active at its current stage and
		// resumes once the stop is cleared (next daemon start re-drives it).
		dto.Error = driveErr.Error()
		dto.ErrorClass = "EMERGENCY_STOP"
		dto.NextAction = "run parked by emergency stop; clear it with `forge estop off` (the run resumes on the next daemon start)"
	case driveErr != nil && dto.Error == "" && !pipeline.IsTerminalRunState(pipeline.RunState(dto.RunState)):
		dto.Error = driveErr.Error()
		if dto.ErrorClass == "" {
			dto.ErrorClass = "INTERNAL_ERROR"
		}
		if dto.Outcome == "" {
			dto.Outcome = "failed"
		}
	}
	return dto, nil
}

// PipelineStatus implements transport.PipelineAPI.PipelineStatus.
func (s *PipelineService) PipelineStatus(ctx context.Context, taskID string) (transport.PipelineRunResultDTO, error) {
	if taskID == "" {
		return transport.PipelineRunResultDTO{}, fmt.Errorf("pipeline status: task id is required")
	}
	return s.buildDTO(ctx, taskID)
}

// CancelPipeline implements transport.PipelineAPI.CancelPipeline. Durable and
// idempotent: Store.Cancel makes the run terminal (never resumed by restart
// recovery); any in-flight agent run for the task is cancelled; the workspace
// is settled as cancelled.
func (s *PipelineService) CancelPipeline(ctx context.Context, taskID string) (transport.PipelineRunResultDTO, error) {
	if taskID == "" {
		return transport.PipelineRunResultDTO{}, fmt.Errorf("pipeline cancel: task id is required")
	}
	if err := s.store.Cancel(ctx, taskID, "cancelled via API"); err != nil {
		if errors.Is(err, pipeline.ErrRunNotFound) {
			return transport.PipelineRunResultDTO{}, fmt.Errorf("pipeline cancel: run for task %s not found", taskID)
		}
		return transport.PipelineRunResultDTO{}, err
	}
	s.cancelAgentRun(taskID)
	s.audit(ctx, "pipeline.run_cancelled", taskID, nil)
	if run, err := s.store.CurrentRun(ctx, taskID); err == nil {
		s.settleRun(ctx, taskID, run)
	}
	return s.buildDTO(ctx, taskID)
}

// SetEmergencyStop implements transport.PipelineAPI.SetEmergencyStop. Turning
// the stop on cancels every in-flight agent run; new drive attempts are
// refused by the driver until the flag is cleared (explicit resume).
func (s *PipelineService) SetEmergencyStop(ctx context.Context, on bool, reason string) (transport.EstopDTO, error) {
	if err := s.store.SetEmergencyStop(ctx, on, reason); err != nil {
		return transport.EstopDTO{}, err
	}
	if on {
		s.cancelAllAgentRuns()
	}
	s.audit(ctx, "pipeline.estop", "", audit.Payload("on", on, "reason", reason))
	return transport.EstopDTO{On: on, Reason: reason}, nil
}

// EmergencyStopStatus implements transport.PipelineAPI.EmergencyStopStatus.
func (s *PipelineService) EmergencyStopStatus(ctx context.Context) (transport.EstopDTO, error) {
	on, reason, err := s.store.EmergencyStop(ctx)
	if err != nil {
		return transport.EstopDTO{}, err
	}
	return transport.EstopDTO{On: on, Reason: reason}, nil
}

// ---- restart recovery ----

// ResumeActiveRuns re-drives every non-terminal run after a daemon restart.
// Cancelled runs are terminal and therefore never listed (durable cancel
// semantics). When the emergency stop is on, runs stay parked until the flag
// is cleared and a later restart resumes them. Each run is re-driven in its
// own goroutine under the daemon context; the per-task single-flight (and the
// driver's per-task mutex) prevents double-driving a run that a synchronous
// RunPipeline is already driving.
func (s *PipelineService) ResumeActiveRuns(ctx context.Context) {
	on, reason, err := s.store.EmergencyStop(ctx)
	if err != nil {
		s.logger.Warn("pipeline: recovery: read estop failed", "err", err)
		return
	}
	if on {
		s.logger.Info("pipeline: recovery: emergency stop engaged; active runs stay parked", "reason", reason)
		return
	}
	runs, err := s.store.ListActiveRuns(ctx)
	if err != nil {
		s.logger.Warn("pipeline: recovery: list active runs failed", "err", err)
		return
	}
	for _, run := range runs {
		if pipeline.IsWaitState(run.State) {
			if run.CurrentStage == pipeline.StageReady {
				// Parked while claiming (lease conflict at ready): the stage
				// cursor is already at ready, so a stage Transition would be an
				// idempotent no-op that never re-activates the run. Re-activate
				// it directly; the driver re-runs the ready stage.
				if err := s.store.SetRunState(ctx, run.TaskID, pipeline.RunStateChange{To: pipeline.RunActive}); err != nil {
					s.logger.Warn("pipeline: recovery: re-activate blocked run failed", "task", run.TaskID, "err", err)
					continue
				}
			} else {
				// The only legal resume from a wait state re-enters ready (which
				// also re-activates the run).
				if _, err := s.store.Transition(ctx, run.TaskID, pipeline.StageReady, "resume after restart", ""); err != nil {
					s.logger.Warn("pipeline: recovery: resume wait state failed", "task", run.TaskID, "err", err)
					continue
				}
			}
		} else {
			// Store-documented re-drive path: record the interruption, bump the
			// attempt, re-enter the current stage at the new attempt.
			if err := s.store.MarkInterrupted(ctx, run.TaskID, "daemon restarted mid-stage"); err != nil {
				s.logger.Warn("pipeline: recovery: mark interrupted failed", "task", run.TaskID, "err", err)
				continue
			}
			if _, err := s.store.IncrementStageAttempt(ctx, run.TaskID); err != nil {
				s.logger.Warn("pipeline: recovery: increment attempt failed", "task", run.TaskID, "err", err)
				continue
			}
			if _, err := s.store.Transition(ctx, run.TaskID, run.CurrentStage, "re-drive after restart", ""); err != nil {
				s.logger.Warn("pipeline: recovery: re-enter stage failed", "task", run.TaskID, "err", err)
				continue
			}
		}
		s.audit(ctx, "pipeline.run_resumed", run.TaskID, audit.Payload("stage", string(run.CurrentStage)))
		go s.driveAndSettle(ctx, run.TaskID)
	}
}

// driveAndSettle drives one run to a terminal/wait state and settles the
// workspace + task afterwards. Used by the recovery path; RunPipeline inlines
// the same steps synchronously.
func (s *PipelineService) driveAndSettle(ctx context.Context, taskID string) {
	if !s.beginDrive(taskID) {
		return
	}
	defer s.endDrive(taskID)
	if err := s.driver.Drive(ctx, taskID); err != nil && !errors.Is(err, pipeline.ErrEmergencyStopped) {
		s.logger.Warn("pipeline: recovery drive failed", "task", taskID, "err", err)
	}
	if run, err := s.store.CurrentRun(ctx, taskID); err == nil {
		s.settleRun(ctx, taskID, run)
	}
}

// settleRun persists the terminal workspace + task state for a terminal
// pipeline run. The completed path is already settled by the finalize stage;
// failed/cancelled/repair-exhausted runs are settled here through the same
// crash-consistent runapp.Finalize chokepoint. Runs with no workspace yet
// (failure before execute) only need the task terminal transition.
func (s *PipelineService) settleRun(ctx context.Context, taskID string, run *pipeline.Run) {
	if !pipeline.IsTerminalRunState(run.State) {
		return
	}
	// A terminal run must not hold its semantic leases beyond its lifetime:
	// release every lease the run's workspace claimed (review finding H4 —
	// leases were claimed and never released, so a finished run blocked
	// competing work until the TTL expired).
	s.releaseRunLeases(ctx, taskID)
	ws, err := s.workspaceForTask(ctx, taskID)
	if err != nil {
		s.logger.Warn("pipeline: settle: list workspaces failed", "task", taskID, "err", err)
		return
	}
	if ws == nil {
		// No workspace was ever created: move the task to its terminal state.
		if t, terr := s.tasks.Get(ctx, taskID); terr == nil && !task.IsTerminal(t.State) {
			action := task.ActionFail
			if run.State == pipeline.RunCancelled {
				action = task.ActionCancel
			}
			if _, terr := s.tasks.Transition(ctx, taskID, action); terr != nil {
				s.logger.Warn("pipeline: settle: task terminal transition failed", "task", taskID, "err", terr)
			}
		}
		return
	}
	if isWorkspaceTerminalState(ws.State) {
		return
	}
	var ev protocol.NormalizedEvent
	switch run.State {
	case pipeline.RunCancelled:
		ev = protocol.NormalizedEvent{Type: protocol.EventRunCancelled}
	default: // failed / repair_exhausted
		ev = protocol.NormalizedEvent{Type: protocol.EventRunFailed}
		if run.FailureCategory == pipeline.FailureProviderTimeout {
			ev.Failure = &protocol.FailurePayload{Class: protocol.FailureTimeout, Reason: run.FailureReason}
		}
	}
	insp, ierr := s.wm.InspectWorktree(ctx, *ws)
	if ierr != nil {
		s.logger.Warn("pipeline: settle: inspect failed", "task", taskID, "err", ierr)
		insp = workspace.Inspection{}
	}
	params, _ := s.loadParams(taskID)
	if _, ferr := s.fin.Finalize(ctx, runapp.FinalizeRequest{
		WorkspaceID:   ws.ID,
		TerminalEvent: ev,
		Inspection:    insp,
		TaskID:        taskID,
		Engine:        params.Engine,
		Model:         params.Model,
		RunID:         ws.RunID,
	}); ferr != nil {
		s.logger.Warn("pipeline: settle: finalize failed", "task", taskID, "err", ferr)
	}
}

// releaseRunLeases releases every active lease held by the run's workspace.
// Best-effort: a release failure is logged, never fatal — the bounded lease
// TTL is the backstop.
func (s *PipelineService) releaseRunLeases(ctx context.Context, taskID string) {
	if s.leases == nil {
		return
	}
	ws, err := s.workspaceForTask(ctx, taskID)
	if err != nil || ws == nil {
		return
	}
	if n, rerr := s.leases.ReleaseAll(ctx, ws.ID); rerr != nil {
		s.logger.Warn("pipeline: release run leases failed", "task", taskID, "workspace", ws.ID, "err", rerr)
	} else if n > 0 {
		s.logger.Info("pipeline: released run leases", "task", taskID, "workspace", ws.ID, "count", n)
	}
}

func isWorkspaceTerminalState(st workspace.State) bool {
	switch st {
	case workspace.StateCompleted, workspace.StateFailed, workspace.StateCancelled,
		workspace.StateTimedOut, workspace.StateKept, workspace.StateRejected,
		workspace.StateDeleted, workspace.StateQuarantined:
		return true
	}
	return false
}

// workspaceForTask returns the latest workspace created for the task, or nil
// when the run never reached the ready stage.
func (s *PipelineService) workspaceForTask(ctx context.Context, taskID string) (*workspace.Workspace, error) {
	wss, err := s.wm.ListByTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if len(wss) == 0 {
		return nil, nil
	}
	return &wss[len(wss)-1], nil
}

// ---- DTO mapping ----

// buildDTO renders the transport DTO from durable state only (run + stage
// records + workspace + persisted finalize record), so it works identically
// for the synchronous run response and the post-restart status endpoint.
func (s *PipelineService) buildDTO(ctx context.Context, taskID string) (transport.PipelineRunResultDTO, error) {
	run, err := s.store.CurrentRun(ctx, taskID)
	if err != nil {
		return transport.PipelineRunResultDTO{}, err
	}
	records, err := s.store.StageRecords(ctx, taskID)
	if err != nil {
		return transport.PipelineRunResultDTO{}, err
	}
	dto := transport.PipelineRunResultDTO{
		TaskID:          run.TaskID,
		RunState:        string(run.State),
		CurrentStage:    string(run.CurrentStage),
		FailureCategory: string(run.FailureCategory),
		FailureReason:   run.FailureReason,
		ResultRef:       run.ResultRef,
		ChangedFiles:    []string{},
		StageRecords:    make([]transport.PipelineStageRecordDTO, 0, len(records)),
	}
	for _, r := range records {
		dto.StageRecords = append(dto.StageRecords, transport.PipelineStageRecordDTO{
			Stage:           string(r.Stage),
			Attempt:         r.Attempt,
			Status:          string(r.Status),
			FailureCategory: string(r.FailureCategory),
			Reason:          r.Reason,
			EvidenceRef:     r.EvidenceRef,
		})
	}
	params, _ := s.loadParams(taskID)
	dto.Engine = params.Engine
	dto.Model = params.Model

	var finRec finalizeRecord
	var haveFin bool
	s.mu.Lock()
	if fin, ok := s.lastFin[taskID]; ok {
		finRec = finalizeRecord{
			Outcome:      string(fin.Outcome),
			WorkspaceID:  fin.WorkspaceID,
			CommitSHA:    fin.CommitSHA,
			ResultBranch: fin.ResultBranch,
			ChangedFiles: fin.ChangedFiles,
		}
		haveFin = true
	}
	s.mu.Unlock()
	if !haveFin {
		finRec, haveFin = s.loadFinalizeRecord(taskID)
	}

	if ws, werr := s.workspaceForTask(ctx, taskID); werr == nil && ws != nil {
		dto.WorkspaceID = ws.ID
		dto.WorkspacePath = ws.Path
		dto.BaseSHA = ws.BaseSHA
		dto.ActualHeadSHA = ws.HeadSHA
		dto.RunID = ws.RunID
		if dto.Engine == "" {
			dto.Engine = ws.Engine
		}
		if dto.Model == "" {
			dto.Model = ws.Model
		}
		dto.ResultBranch = ws.ResultBranch
	}
	if haveFin {
		if finRec.WorkspaceID != "" {
			dto.WorkspaceID = finRec.WorkspaceID
		}
		dto.CommitSHA = finRec.CommitSHA
		if finRec.ResultBranch != "" {
			dto.ResultBranch = finRec.ResultBranch
		}
		if len(finRec.ChangedFiles) > 0 {
			dto.ChangedFiles = finRec.ChangedFiles
		}
	}

	switch run.State {
	case pipeline.RunCompleted:
		if haveFin && finRec.Outcome != "" {
			dto.Outcome = finRec.Outcome
		} else {
			dto.Outcome = "completed-with-commit"
		}
	case pipeline.RunCancelled:
		dto.Outcome = "cancelled"
		dto.ErrorClass = "CANCELLED"
		if dto.Error == "" {
			dto.Error = run.FailureReason
		}
	case pipeline.RunFailed, pipeline.RunRepairExhausted:
		dto.Error = run.FailureReason
		dto.ErrorClass = errorClassForCategory(run.FailureCategory)
		if run.FailureCategory == pipeline.FailureProviderTimeout {
			dto.Outcome = "timed-out"
		} else {
			dto.Outcome = "failed"
		}
	case pipeline.RunWaitingQuota:
		dto.Outcome = "failed"
		dto.Error = run.FailureReason
		dto.ErrorClass = errorClassForCategory(run.FailureCategory)
		dto.NextAction = "quota exhausted; the run resumes on the next daemon start after quota reset"
	case pipeline.RunBlocked:
		dto.Error = run.FailureReason
		dto.ErrorClass = "BLOCKED_LEASE"
		dto.NextAction = "a conflicting lease blocks the run; it resumes on the next daemon start after the lease is released or expires"
	default:
		// active / blocked: still in flight.
		dto.Outcome = ""
	}
	return dto, nil
}

// errorClassForCategory maps a pipeline failure category to the wire
// error_class vocabulary (OUTCOME_CONTRACT.md §3.1 style).
func errorClassForCategory(c pipeline.FailureCategory) string {
	switch c {
	case pipeline.FailureQuotaExceeded:
		return "PROVIDER_QUOTA"
	case pipeline.FailureRateLimited:
		return "PROVIDER_RATE_LIMIT"
	case pipeline.FailureAgentAuthUnavailable:
		return "PROVIDER_AUTH"
	case pipeline.FailureProviderTimeout:
		return "TIMEOUT"
	case pipeline.FailureCancelled:
		return "CANCELLED"
	case pipeline.FailureInterrupted:
		return "INTERRUPTED"
	case pipeline.FailureAgentUnavailable:
		return "AGENT_UNAVAILABLE"
	case pipeline.FailureInvalidAgentOutput:
		return "INVALID_AGENT_OUTPUT"
	case pipeline.FailurePolicyRejection:
		return "POLICY_REJECTION"
	}
	return "INTERNAL_ERROR"
}

func (s *PipelineService) audit(ctx context.Context, eventType, taskID string, payload map[string]any) {
	if s.rec == nil {
		return
	}
	if _, err := s.rec.Record(ctx, audit.Event{
		Type:    eventType,
		Scope:   audit.ScopeTask,
		ScopeID: taskID,
		Actor:   audit.ActorDaemon,
		Payload: payload,
	}); err != nil {
		s.logger.Warn("pipeline: audit failed", "type", eventType, "task", taskID, "err", err)
	}
}
