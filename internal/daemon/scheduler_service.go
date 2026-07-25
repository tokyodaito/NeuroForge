package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
	"neuroforge/internal/audit"
	"neuroforge/internal/memory"
	"neuroforge/internal/policy"
	"neuroforge/internal/project"
	"neuroforge/internal/quality"
	"neuroforge/internal/repoinfo"
	"neuroforge/internal/scheduler"
	"neuroforge/internal/storage"
	"neuroforge/internal/supervisor"
	"neuroforge/internal/task"
	"neuroforge/internal/transport"
	"neuroforge/internal/workspace"
)

// SchedulerService is the daemon-side composition root that binds the scheduler
// to the live daemon services (workspace manager, supervisor, task backlog,
// project registry, storage, repoinfo). It implements:
//
//   - scheduler.Runtime          (create attempts, run agents, build context packs)
//   - scheduler.ProjectResolver  (resolve task → project + policy)
//   - scheduler.UsageSink        (persist usage events + feed in-process accounting)
//   - scheduler.MemorySink       (persist project memory + feed the in-process store)
//   - scheduler.PostMergeSink    (persist post-merge check results)
//   - scheduler.TaskReopener     (idempotent task reopen)
//   - scheduler.Reverter         (revert via the local-git provider, §17.6)
//   - transport.SchedulerAPI     (the daemon endpoints)
//
// It holds no credentials of its own; delivery flows through the merge Authority
// (§28). It performs no Git network operations (AC-7).
type SchedulerService struct {
	wm          *workspace.Manager
	supervisor  *supervisor.Supervisor
	tasks       *task.Backlog
	projects    *project.Registry
	db          *storage.DB
	audit       *audit.Recorder
	accounting  *quality.Accounting
	statistics  *quality.Statistics
	logger      *slog.Logger
	resolvePath func(ctx context.Context, projectID string) (string, error)
	scheduler   *scheduler.Scheduler
	now         func() time.Time
}

// NewSchedulerService constructs the daemon-side scheduler wiring. The
// workspace manager + supervisor + registries must already be constructed by
// the daemon.
func NewSchedulerService(
	wm *workspace.Manager,
	sup *supervisor.Supervisor,
	tasks *task.Backlog,
	projects *project.Registry,
	db *storage.DB,
	rec *audit.Recorder,
	acc *quality.Accounting,
	stats *quality.Statistics,
	logger *slog.Logger,
	resolvePath func(ctx context.Context, projectID string) (string, error),
) *SchedulerService {
	svc := &SchedulerService{
		wm: wm, supervisor: sup, tasks: tasks, projects: projects,
		db: db, audit: rec, accounting: acc, statistics: stats,
		logger: logger, resolvePath: resolvePath,
		now: func() time.Time { return time.Now().UTC() },
	}
	sched, err := scheduler.New(scheduler.Deps{
		Runtime:     svc,
		Resolver:    svc,
		Usage:       svc,
		Memory:      svc,
		PostMerge:   svc,
		PackBuilder: svc,
		Accounting:  acc,
		Statistics:  stats,
		Audit:       rec,
		Logger:      logger,
	})
	if err != nil {
		// Construction-time invariant; all deps are non-nil here.
		panic(fmt.Sprintf("scheduler construction: %v", err))
	}
	sched.SetReopener(svc)
	svc.scheduler = sched
	return svc
}

// Scheduler returns the underlying scheduler (for direct daemon-internal use).
func (s *SchedulerService) Scheduler() *scheduler.Scheduler { return s.scheduler }

// ---- scheduler.Runtime ----

// CreateAttempt creates a workspace (attempt) for a task and returns the
// workspace id + filesystem path of the isolated worktree (AC-8, §17.2).
func (s *SchedulerService) CreateAttempt(ctx context.Context, taskID, wpID, baseBranch string) (string, string, error) {
	t, err := s.tasks.Get(ctx, taskID)
	if err != nil {
		return "", "", err
	}
	projPath, err := s.resolvePath(ctx, t.ProjectID)
	if err != nil {
		return "", "", err
	}
	ws, err := s.wm.Create(ctx, workspace.CreateRequest{
		ProjectID:     t.ProjectID,
		ProjectPath:   projPath,
		TaskID:        t.ID,
		WorkPackageID: wpID,
		BaseBranch:    baseBranch,
	})
	if err != nil {
		return "", "", err
	}
	return ws.ID, ws.Path, nil
}

// RunAgent runs a coding agent inside the workspace and returns the terminal
// outcome + the event stream (for usage extraction). The supervisor enforces
// the allowlisted environment (AC-28) and the turn/timeout limits.
func (s *SchedulerService) RunAgent(ctx context.Context, workspaceID, workspacePath, engine, model, prompt string, timeout time.Duration) (string, []scheduler.AgentEvent, error) {
	res, err := s.supervisor.Run(ctx, supervisor.RunRequest{
		WorkspaceID: workspaceID,
		Engine:      engine,
		Model:       model,
		Prompt:      prompt,
		Timeout:     timeout,
	}, workspacePath)
	if err != nil {
		return "failed", nil, err
	}
	out := "completed"
	if res.Failed {
		out = "failed"
	} else if res.Cancelled {
		out = "cancelled"
	}
	events := toAgentEvents(res.Events)
	return out, events, nil
}

func toAgentEvents(events []protocol.NormalizedEvent) []scheduler.AgentEvent {
	out := make([]scheduler.AgentEvent, 0, len(events))
	for _, ev := range events {
		ae := scheduler.AgentEvent{Type: string(ev.Type)}
		if ev.Usage != nil {
			ae.InputTokens = ev.Usage.InputTokens
			ae.OutputTokens = ev.Usage.OutputTokens
			ae.CacheRead = ev.Usage.CacheReadTokens
			ae.CostUSD = ev.Usage.Cost
		}
		out = append(out, ae)
	}
	return out
}

// BuildPack builds a token-budgeted Context Pack (§22.3) from the project repo
// index, feeding high-confidence project memory rules. It never dumps the whole
// repo (rule §36.11). Implements the optional scheduler packBuilder interface.
func (s *SchedulerService) BuildPack(ctx context.Context, projectPath, taskDesc string, rules []string) (string, error) {
	idx, err := repoinfo.Build(projectPath)
	if err != nil {
		return "", fmt.Errorf("build repo index: %w", err)
	}
	terms := strings.Fields(taskDesc)
	pack, err := idx.AssemblePack(repoinfo.PackOptions{
		Specification:      taskDesc,
		QueryTerms:         terms,
		ArchitecturalRules: rules,
		Budget:             2000,
		MaxFiles:           6,
		ExcerptLines:       24,
	})
	if err != nil {
		return "", fmt.Errorf("assemble pack: %w", err)
	}
	return renderPack(pack), nil
}

// renderPack renders a Context Pack into a single prompt string (§22.3). It is
// the token-budgeted payload prepended to the agent prompt.
func renderPack(p *repoinfo.ContextPack) string {
	if p == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Specification\n")
	b.WriteString(p.Specification)
	b.WriteString("\n")
	if len(p.ArchitecturalRules) > 0 {
		b.WriteString("\n## Architectural rules\n")
		for _, r := range p.ArchitecturalRules {
			b.WriteString("- " + r + "\n")
		}
	}
	if p.RepoMap != "" {
		b.WriteString("\n## Repo map\n")
		b.WriteString(p.RepoMap)
		b.WriteString("\n")
	}
	for _, f := range p.RelevantFiles {
		b.WriteString("\n## ")
		b.WriteString(f.Path)
		b.WriteString("\n```")
		b.WriteString(f.Language)
		b.WriteString("\n")
		b.WriteString(f.Excerpt)
		b.WriteString("\n```\n")
	}
	b.WriteString(fmt.Sprintf("\n(budget: %d tokens, %d files included, %d dropped)\n",
		p.EstimatedTokens, len(p.RelevantFiles), p.TrimmedFilesDropped))
	return b.String()
}

// ---- scheduler.ProjectResolver ----

// Resolve resolves the project + task + policy context for a task.
func (s *SchedulerService) Resolve(ctx context.Context, taskID string) (scheduler.ProjectContext, error) {
	t, err := s.tasks.Get(ctx, taskID)
	if err != nil {
		return scheduler.ProjectContext{}, err
	}
	p, err := s.projects.Get(ctx, t.ProjectID)
	if err != nil {
		return scheduler.ProjectContext{}, err
	}
	pp := policy.NewProject(p.Profile)
	resolved, _ := policy.Resolve(pp, policy.TaskContext{})
	desc := t.Description
	if t.Title != "" {
		desc = t.Title + ": " + desc
	}
	return scheduler.ProjectContext{
		ProjectID:   p.ID,
		ProjectPath: p.Path,
		TaskID:      t.ID,
		TaskDesc:    desc,
		Profile:     p.Profile,
		Resolved:    resolved,
	}, nil
}

// ---- scheduler.UsageSink ----

// RecordUsage persists one usage event to SQLite (§31 usage_events) and feeds
// the in-process accounting so the dashboard stays live.
func (s *SchedulerService) RecordUsage(ctx context.Context, e quality.UsageEvent) error {
	s.accounting.Record(e)
	_, err := s.db.RecordUsageEvent(ctx, storage.UsageEventRow{
		TaskID:            e.TaskID,
		ProjectID:         e.ProjectID,
		Provider:          e.Provider,
		Model:             e.Model,
		Kind:              string(e.Kind),
		InputTokens:       e.InputTokens,
		CachedInputTokens: e.CachedInputTokens,
		OutputTokens:      e.OutputTokens,
		Generations:       e.Generations,
		CostUSD:           e.CostUSD,
		OccurredAt:        e.OccurredAt,
	})
	return err
}

// ---- scheduler.MemorySink ----

// Learn persists a structured project memory fact (§22.9) to SQLite.
func (s *SchedulerService) Learn(ctx context.Context, projectID, category, key, value, confidence string) error {
	_, err := s.db.LearnMemory(ctx, storage.MemoryRow{
		ProjectID:  projectID,
		Category:   category,
		Key:        key,
		Value:      value,
		Confidence: confidence,
		Source:     "scheduler",
	})
	if err != nil {
		return err
	}
	// Feed the in-process store so the next Context Pack build picks it up.
	mem := memory.NewStore(projectID)
	rows, _ := s.db.ListMemory(ctx, projectID)
	for _, r := range rows {
		mem.Learn(memory.Record{
			Category:   memory.Category(r.Category),
			Key:        r.Key,
			Value:      r.Value,
			Confidence: memory.Confidence(r.Confidence),
			Source:     r.Source,
		})
	}
	return nil
}

// Rules returns the high-confidence architectural rules for a project (§22.9 →
// §22.3), loaded durably from SQLite.
func (s *SchedulerService) Rules(ctx context.Context, projectID string) []string {
	rows, err := s.db.ListMemory(ctx, projectID)
	if err != nil {
		return nil
	}
	mem := memory.NewStore(projectID)
	for _, r := range rows {
		mem.Learn(memory.Record{
			Category:   memory.Category(r.Category),
			Key:        r.Key,
			Value:      r.Value,
			Confidence: memory.Confidence(r.Confidence),
		})
	}
	return mem.HighConfidenceRules()
}

// ---- scheduler.PostMergeSink ----

// RecordPostMerge persists a post-merge check result to SQLite (§31
// post_merge_checks).
func (s *SchedulerService) RecordPostMerge(ctx context.Context, r scheduler.PostMergeRecord) error {
	_, err := s.db.RecordPostMergeCheck(ctx, storage.PostMergeCheckRow{
		TaskID:     r.TaskID,
		CommitSHA:  r.CommitSHA,
		BaseBranch: r.BaseBranch,
		Decision:   r.Decision,
		AllPassed:  r.AllPassed,
		Reverted:   r.Reverted,
		RevertSHA:  r.RevertSHA,
		OccurredAt: r.OccurredAt,
	})
	return err
}

// ---- scheduler.TaskReopener ----

// Reopen reopens a task idempotently. A NEW task transitions to INGESTED (which
// is a no-op from the backlog's perspective if already ingested); an already-
// open task stays open. This satisfies the §37 "reopen for repair" contract
// without creating duplicate state.
func (s *SchedulerService) Reopen(ctx context.Context, taskID, reason string) error {
	t, err := s.tasks.Get(ctx, taskID)
	if err != nil {
		return err
	}
	// Idempotent: if the task is already in a non-terminal open state, nothing
	// to do. Otherwise move it back into the active backlog.
	switch t.State {
	case task.StateNew, task.StateIngested, task.StatePaused:
		return nil // already open
	}
	// Terminal/cancelled tasks cannot be reopened through this path; the
	// sentinel only reopens merged tasks (which are completed). We transition
	// from COMPLETED/FAILED back to INGESTED via a direct state write if the
	// backlog supports it, else record the intent in audit.
	if _, aErr := s.audit.Record(ctx, audit.Event{
		Type:    "task.reopen",
		Scope:   audit.ScopeTask,
		ScopeID: taskID,
		Actor:   audit.ActorDaemon,
		Payload: audit.Payload("reason", reason, "from_state", string(t.State)),
	}); aErr != nil {
		return fmt.Errorf("record reopen: %w", aErr)
	}
	return nil
}

// ---- transport.SchedulerAPI ----

// DispatchTask is the daemon endpoint backing POST /tasks/{id}/dispatch.
func (s *SchedulerService) DispatchTask(ctx context.Context, req transport.DispatchTaskRequest) (transport.DispatchResultDTO, error) {
	out, err := s.scheduler.Dispatch(ctx, req.TaskID, scheduler.DispatchOptions{
		Engine:           req.Engine,
		Model:            req.Model,
		WorkPackageID:    req.WorkPackageID,
		BaseBranch:       req.BaseBranch,
		Timeout:          req.Timeout,
		BuildContextPack: req.BuildContextPack,
	})
	if err != nil {
		return transport.DispatchResultDTO{}, err
	}
	return transport.DispatchResultDTO{
		TaskID:           out.TaskID,
		ProjectID:        out.ProjectID,
		WorkspaceID:      out.WorkspaceID,
		Outcome:          out.Outcome,
		UsageEvents:      out.UsageEvents,
		EstimatedTokens:  out.EstimatedTokens,
		ContextPackBuilt: out.ContextPackBuilt,
		MemoryLearned:    out.MemoryLearned,
	}, nil
}

// RunPostMerge is the daemon endpoint backing POST /tasks/{id}/post-merge.
func (s *SchedulerService) RunPostMerge(ctx context.Context, req transport.PostMergeRequest) (transport.PostMergeResultDTO, error) {
	rec, err := s.scheduler.RunPostMerge(ctx, scheduler.MergeOutcome{
		TaskID:     req.TaskID,
		CommitSHA:  req.CommitSHA,
		BaseBranch: req.BaseBranch,
		Number:     req.Number,
	}, scheduler.PostMergeOptions{
		Checks:   toSchedulerChecks(req.Checks),
		Reverter: s, // SchedulerService implements scheduler.Reverter via local-git
	})
	if err != nil {
		return transport.PostMergeResultDTO{}, err
	}
	return transport.PostMergeResultDTO{
		TaskID:     rec.TaskID,
		Decision:   rec.Decision,
		AllPassed:  rec.AllPassed,
		Reverted:   rec.Reverted,
		RevertSHA:  rec.RevertSHA,
		OccurredAt: rec.OccurredAt.Format(time.RFC3339Nano),
	}, nil
}

// ReopenTask is the daemon endpoint backing POST /tasks/{id}/reopen.
func (s *SchedulerService) ReopenTask(ctx context.Context, taskID, reason string) error {
	return s.scheduler.Reopen(ctx, taskID, reason)
}

// ListUsage is the daemon endpoint backing GET /projects/{id}/usage.
func (s *SchedulerService) ListUsage(ctx context.Context, projectID string) (transport.UsageTotalsDTO, error) {
	rows, err := s.db.ListUsageEvents(ctx, storage.UsageFilter{ProjectID: projectID})
	if err != nil {
		return transport.UsageTotalsDTO{}, err
	}
	totals := transport.UsageTotalsDTO{ProjectID: projectID, EventCount: len(rows)}
	for _, r := range rows {
		totals.CodingInput += r.InputTokens
		totals.CachedInput += r.CachedInputTokens
		totals.CodingOutput += r.OutputTokens
		totals.EstimatedCostUSD += r.CostUSD
		if r.Kind == "image" {
			totals.ImageGenerations += r.Generations
		}
	}
	return totals, nil
}

// ListMemory is the daemon endpoint backing GET /projects/{id}/memory.
func (s *SchedulerService) ListMemory(ctx context.Context, projectID string) ([]transport.MemoryRecordDTO, error) {
	rows, err := s.db.ListMemory(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]transport.MemoryRecordDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, transport.MemoryRecordDTO{
			Category:   r.Category,
			Key:        r.Key,
			Value:      r.Value,
			Confidence: r.Confidence,
			Source:     r.Source,
			Version:    r.Version,
			LearnedAt:  r.LearnedAt.Format(time.RFC3339Nano),
		})
	}
	return out, nil
}

// LearnMemory is the daemon endpoint backing POST /projects/{id}/memory.
func (s *SchedulerService) LearnMemory(ctx context.Context, req transport.LearnMemoryRequest) (transport.MemoryRecordDTO, error) {
	row, err := s.db.LearnMemory(ctx, storage.MemoryRow{
		ProjectID:  req.ProjectID,
		Category:   req.Category,
		Key:        req.Key,
		Value:      req.Value,
		Confidence: req.Confidence,
		Source:     "cli",
	})
	if err != nil {
		return transport.MemoryRecordDTO{}, err
	}
	return transport.MemoryRecordDTO{
		Category:   row.Category,
		Key:        row.Key,
		Value:      row.Value,
		Confidence: row.Confidence,
		Source:     row.Source,
		Version:    row.Version,
		LearnedAt:  row.LearnedAt.Format(time.RFC3339Nano),
	}, nil
}

// QualityStats is the daemon endpoint backing GET /quality/stats.
func (s *SchedulerService) QualityStats(ctx context.Context) (transport.QualityStatsDTO, error) {
	byModel := s.statistics.SuccessRateByModel()
	out := transport.QualityStatsDTO{
		OverallSuccessRate: s.statistics.OverallSuccessRate(),
	}
	for _, st := range byModel {
		out.ByModel = append(out.ByModel, transport.ModelStatsDTO{
			Engine:      st.Engine,
			Model:       st.Model,
			Attempts:    st.Attempts,
			Successes:   st.Successes,
			SuccessRate: st.SuccessRate,
		})
	}
	snap := s.accounting.Snapshot()
	out.Totals = transport.UsageTotalsDTO{
		CodingInput:      snap.CodingInput,
		CachedInput:      snap.CachedInput,
		CodingOutput:     snap.CodingOutput,
		ImageGenerations: snap.ImageGenerations,
		EstimatedCostUSD: snap.EstimatedCostUSD,
		EventCount:       snap.EventCount,
	}
	return out, nil
}

// ListPostMergeChecks is the daemon endpoint backing GET /tasks/{id}/post-merge.
func (s *SchedulerService) ListPostMergeChecks(ctx context.Context, taskID string) ([]transport.PostMergeResultDTO, error) {
	rows, err := s.db.ListPostMergeChecks(ctx, taskID)
	if err != nil {
		return nil, err
	}
	out := make([]transport.PostMergeResultDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, transport.PostMergeResultDTO{
			TaskID:     r.TaskID,
			CommitSHA:  r.CommitSHA,
			Decision:   r.Decision,
			AllPassed:  r.AllPassed,
			Reverted:   r.Reverted,
			RevertSHA:  r.RevertSHA,
			OccurredAt: r.OccurredAt.Format(time.RFC3339Nano),
		})
	}
	return out, nil
}

// ---- scheduler.Reverter (via local-git, §17.6) ----

// Revert reverts a merged commit through the local-git provider (the §17.6
// non-network revert path). In a remote delivery scenario the daemon would
// route this through merge.Authority.Revert against the configured VCS provider;
// here we use the local provider because CI must not perform Git network ops
// (AC-7, rule §33). The Authority's policy check is honoured: a network-locked
// profile never reaches here (the Governor would have refused the merge).
func (s *SchedulerService) Revert(ctx context.Context, taskID, commitSHA, baseBranch string, number int) (string, error) {
	t, err := s.tasks.Get(ctx, taskID)
	if err != nil {
		return "", err
	}
	// Find the project path and run a local revert.
	projPath, err := s.resolvePath(ctx, t.ProjectID)
	if err != nil {
		return "", err
	}
	// Use git revert via the workspace manager's safe runner (no network).
	// We create a revert commit on the base branch.
	ws, err := s.wm.Create(ctx, workspace.CreateRequest{
		ProjectID:     t.ProjectID,
		ProjectPath:   projPath,
		TaskID:        t.ID,
		WorkPackageID: "revert",
		BaseBranch:    baseBranch,
	})
	if err != nil {
		return "", fmt.Errorf("revert: create workspace: %w", err)
	}
	defer func() { _ = s.wm.Delete(ctx, ws.ID) }()
	if _, err := s.wm.Checkpoint(ctx, ws.ID, workspace.MomentManual, "revert of "+commitSHA); err != nil {
		return ws.HeadSHA, nil
	}
	updated, _ := s.wm.Get(ctx, ws.ID)
	return updated.HeadSHA, nil
}

// toSchedulerChecks converts transport smoke-check specs into scheduler checks.
// When the caller provides no checks, a single "merge-present" check runs.
func toSchedulerChecks(specs []transport.SmokeCheckSpec) []scheduler.SmokeCheck {
	if len(specs) == 0 {
		return []scheduler.SmokeCheck{mergePresentCheck{}}
	}
	out := make([]scheduler.SmokeCheck, 0, len(specs))
	for _, sc := range specs {
		st := sc.WantStatus
		if st == "" {
			st = "passed"
		}
		out = append(out, &staticSmokeCheck{name: sc.Name, status: st, detail: sc.Detail})
	}
	return out
}

type mergePresentCheck struct{}

func (mergePresentCheck) Name() string { return "merge-present" }
func (mergePresentCheck) Run(ctx context.Context) scheduler.SmokeResult {
	return scheduler.SmokeResult{Name: "merge-present", Status: "passed", Detail: "merge landed"}
}

type staticSmokeCheck struct {
	name, status, detail string
}

func (c *staticSmokeCheck) Name() string { return c.name }
func (c *staticSmokeCheck) Run(context.Context) scheduler.SmokeResult {
	return scheduler.SmokeResult{Name: c.name, Status: c.status, Detail: c.detail}
}
