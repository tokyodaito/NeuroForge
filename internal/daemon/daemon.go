package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"neuroforge/internal/audit"
	"neuroforge/internal/pipeline"
	"neuroforge/internal/quality"
	"neuroforge/internal/storage"
	"neuroforge/internal/supervisor"
	"neuroforge/internal/transport"
	"neuroforge/internal/version"
	"neuroforge/internal/workgraph"
	"neuroforge/internal/workspace"
)

// RunConfig configures a daemon Run. Zero values use safe defaults.
type RunConfig struct {
	Dirs        Dirs
	Addr        string       // loopback listen address; default "127.0.0.1:0"
	Token       string       // if empty, a random token is generated
	Reconcilers []Reconciler // if nil, DefaultReconcilers() is used (extension point)
	Logger      *slog.Logger // optional override; otherwise JSON to stderr
}

// Run is the daemon process entry point. It blocks until ctx is cancelled, a
// termination signal (SIGTERM/SIGINT) is received, or a client calls
// /shutdown. It guarantees graceful shutdown: the transport stops accepting,
// the audit log records the stop, and the database is closed before returning.
//
// State is durable before any external action: the database and audit log are
// opened and the runtime files are written only after the loopback listener is
// bound (spec §11.4).
func Run(ctx context.Context, cfg RunConfig) (retErr error) {
	if err := cfg.Dirs.Ensure(); err != nil {
		return err
	}

	logger := cfg.Logger
	if logger == nil {
		logger = newLogger(os.Stderr)
	}
	logger = logger.With("component", "daemon", "pid", os.Getpid())

	// BF-05 / R-2.3 (dual-daemon guard): if a healthy daemon is ALREADY serving
	// this home, this process is a redundant spawn (a concurrent CLI lost the
	// autostart race's timing window). Exit cleanly instead of binding a second
	// listener and clobbering the runtime files. This is the daemon-side
	// backstop behind the CLI's autostart lock; it makes dual-daemon creation
	// impossible even under adverse scheduling.
	if isReachableAndHealthyRetried(ctx, cfg.Dirs, 3, 100*time.Millisecond) {
		logger.Info("daemon already running for this home; exiting without binding")
		return nil
	}

	// Derive a cancellable context that signals and /shutdown can trigger.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Surface panics/errors in the audit log and runtime files.
	defer func() {
		if retErr != nil {
			logger.Error("daemon exited with error", "err", retErr)
		}
	}()

	// 1. Open durable storage and run migrations.
	db, err := storage.Open(runCtx, cfg.Dirs.StateDB, &storage.Options{Logger: logger})
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer func() {
		if cErr := db.Close(); cErr != nil && retErr == nil {
			retErr = fmt.Errorf("close storage: %w", cErr)
		}
	}()
	if err := db.Migrate(runCtx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	// 2. Open the append-only audit recorder.
	recorder := audit.NewRecorder(db, logger)

	// 2a. Create the workspace manager early so the workspace reconciler can
	// verify worktree integrity during startup reconciliation (AC-27).
	wsManager := workspace.NewManager(db, recorder, cfg.Dirs.WorkspacesDir, logger)

	// 3. Startup reconciliation: reconcile durable + ephemeral runtime state
	// against OS reality BEFORE we bind/claim the runtime dir. This is the
	// deterministic recovery point (AC-27 framework). A live-owner conflict
	// aborts startup so no duplicate daemon is created.
	reconcilers := cfg.Reconcilers
	if reconcilers == nil {
		reconcilers = WithExtraReconcilers(
			&workspaceReconciler{wm: wsManager},
			// BF-07: resume partial finalizations (intent without terminal
			// commit) BEFORE the attempt reconciler runs: a legacy runapp run
			// that crashed mid-finalize must be completed by the intent, not
			// marked failed (and its intent deleted uncompleted) by the
			// attempt reconciler (review finding M3).
			newFinalizeIntentReconciler(db, recorder, wsManager, logger),
			&attemptReconciler{wm: wsManager}, // M7: recover in-flight attempts (AC-27)
			// M14-06: mark in-flight pipeline stages interrupted; the re-drive
			// happens via PipelineService.ResumeActiveRuns once services exist.
			&pipelineReconciler{store: pipeline.NewStore(db, logger)},
		)
	}
	if _, err := Reconcile(runCtx, ReconcileTx{
		DB: db, Audit: recorder, Dirs: cfg.Dirs, Logger: logger,
	}, reconcilers); err != nil {
		return fmt.Errorf("reconcile: %w", err)
	}

	if _, err := recorder.Record(runCtx, audit.Event{
		Type:    "daemon.starting",
		Actor:   audit.ActorDaemon,
		Payload: audit.Payload("home", cfg.Dirs.Root),
	}); err != nil {
		return fmt.Errorf("audit starting: %w", err)
	}

	// 4. Internal event bus + loopback transport server.
	bus := transport.NewBus()
	defer bus.Close()

	// 4a. Domain services (project registry, task backlog) wired to the
	// transport API. The daemon is the single owner of mutable state
	// (ADR-0002); the CLI and TUI reach these services only through the
	// loopback API.
	services := NewServices(db, recorder, cfg.Dirs.ArtifactsDir, bus, logger)
	apiAdapter := newAPIAdapter(services)
	// 4a-2. Daemon-mediated Task Compiler adapter (M14-03): wraps the pure
	// task.Compile with durable persistence through task.SpecificationStore so
	// a compiled specification is reachable through the transport and survives
	// daemon restart.
	specAdapter := newSpecAPIAdapter(services)
	// 4a-3. Daemon-mediated Work-Graph inspection adapter (M14-05): wraps the
	// WorkGraphStore + LeaseManager + ComputeReadiness so a single
	// GET /tasks/{id}/workgraph round-trip returns the graph and its
	// dispatchability map. Durable state (graph + leases) survives daemon
	// restart (mandatory AC).
	workGraphAdapter := newWorkGraphAPIAdapter(services)

	// 4b. Lease manager + supervisor (M3, spec §17/§18/§12).
	// The workspace manager was created at step 2a (before reconciliation).
	// The supervisor runs agents with an allowlisted environment (AC-28).
	leaseManager := workgraph.NewLeaseManager(db)

	// Register the coding-agent engines the supervisor can dispatch. Every
	// first-party production adapter (codex, claude, gemini, kimi, grok,
	// opencode) is registered via the shared builtin registry, then the fake
	// agent is layered on top (rule §36.6: build the fake first; the six
	// production engines are surfaced from one place so the core never
	// references a provider by name — spec §13.3). A fresh registry is built
	// per daemon so repeated in-process starts (tests) never double-register.
	adapterRegistry, err := buildAdapterRegistry(cfg.Dirs.ArtifactsDir)
	if err != nil {
		return fmt.Errorf("register coding-agent adapters: %w", err)
	}

	sup := supervisor.New(supervisor.Options{
		Adapters: adapterRegistry,
		Audit:    recorder,
		Logger:   logger,
		FullEnv:  os.Environ(),
	})

	resolveProject := func(ctx context.Context, projectID string) (string, error) {
		p, err := services.Projects.Get(ctx, projectID)
		if err != nil {
			return "", err
		}
		return p.Path, nil
	}
	wsService := NewWorkspaceService(wsManager, leaseManager, sup, services.Tasks, recorder, logger, resolveProject)
	wsAdapter := newWorkspaceAPIAdapter(wsService)

	// 4c. Scheduler service (M12/M13 production wiring): composes the workspace
	// manager + supervisor + task backlog + project registry + storage with the
	// quality/memory/repoinfo/postmerge domain packages. This is the production
	// execution path — tasks flow scheduler → dispatcher → supervisor, with usage
	// events, Context Packs, project memory and quality statistics recorded on
	// the way, and the post-merge sentinel driven after a merge.
	accounting := quality.NewAccounting()
	statistics := quality.NewStatistics()
	schedSvc := NewSchedulerService(wsManager, sup, services.Tasks, services.Projects, db, recorder, accounting, statistics, logger, resolveProject)
	schedAdapter := schedSvc

	// 4c-2. RunApp service (stabilization track): the user-facing `forge run`
	// endpoint. Bypasses the scheduler/failover/postmerge/review/merge
	// subsystems (NFR-7) and drives one production adapter end-to-end via
	// runapp.Service.Run, with the post-run Git inspection, classifier,
	// atomic terminal persistence and idempotent result ref (FR-1..FR-14).
	runAppSvc := NewRunAppService(wsManager, sup, services.Tasks, services.Projects, db, recorder, accounting, logger)
	runAppAdapter := runAppSvc

	// 4c-3. Durable pipeline service (M14-06 production path): the pipeline
	// Store + Driver with concrete handlers composing the task compiler, work
	// graph, workspace manager, supervisor, test engine, review engine and the
	// runapp Finalize chokepoint. This backs the pipeline transport endpoints
	// and is the production path behind `forge run`.
	pipelineSvc, err := NewPipelineService(PipelineDeps{
		DB:       db,
		Recorder: recorder,
		Logger:   logger,
		Dirs:     cfg.Dirs,
		Tasks:    services.Tasks,
		Projects: services.Projects,
		Specs:    services.Specs,
		Graphs:   services.Graphs,
		Leases:   services.Leases,
		WM:       wsManager,
		Sup:      sup,
		Usage:    &usageSinkAdapter{tasks: services.Tasks, projects: services.Projects, db: db, accounting: accounting},
	})
	if err != nil {
		return fmt.Errorf("pipeline service: %w", err)
	}

	token := cfg.Token
	if token == "" {
		token, err = transport.GenerateToken()
		if err != nil {
			return fmt.Errorf("generate token: %w", err)
		}
	}
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:0"
	}

	srv, err := transport.NewServer(transport.Config{
		Addr:              cfg.Addr,
		Token:             token,
		OnShutdownRequest: cancel,
		AuditReader:       &auditReader{db: db},
		ProjectAPI:        apiAdapter,
		TaskAPI:           apiAdapter,
		WorkspaceAPI:      wsAdapter,
		SchedulerAPI:      schedAdapter,
		RunAppAPI:         runAppAdapter,
		SpecAPI:           specAdapter,
		WorkGraphAPI:      workGraphAdapter,
		PipelineAPI:       pipelineSvc,
	}, bus, logger)
	if err != nil {
		return fmt.Errorf("transport server: %w", err)
	}
	srv.SetVersion(version.Version)

	// 5. Bind the loopback listener BEFORE writing runtime files, so the
	// address advertised to clients is real.
	//
	// BF-05 / R-2.3 (dual-daemon guard): the bind + runtime-file write is the
	// atomic "I am THE daemon for this home" claim. Serialize it across daemon
	// processes with a dedicated bind.lock so two concurrently-spawned daemons
	// cannot both bind. Under the lock we re-check liveness one final time; a
	// daemon that became healthy while we waited causes this redundant process
	// to exit without binding. The lock is released immediately after the
	// runtime files are written.
	bindUnlock, berr := lockFile(runCtx, filepath.Join(cfg.Dirs.Root, "bind.lock"))
	if berr != nil {
		return fmt.Errorf("acquire bind lock: %w", berr)
	}
	if isReachableAndHealthyRetried(runCtx, cfg.Dirs, 3, 100*time.Millisecond) {
		bindUnlock()
		logger.Info("daemon became healthy while waiting for bind lock; exiting without binding")
		return nil
	}
	addr, err := srv.Listen()
	if err != nil {
		bindUnlock()
		return fmt.Errorf("transport listen: %w", err)
	}
	baseURL := "http://" + addr.String()

	// 6. Write runtime files (mode 0o600) so the CLI/TUI can reach us.
	if err := writeRuntimeFiles(cfg.Dirs, os.Getpid(), token, baseURL); err != nil {
		bindUnlock()
		return fmt.Errorf("write runtime files: %w", err)
	}
	bindUnlock()
	defer func() {
		// Remove runtime files on exit so a crashed-next-time start is clean.
		cleanRuntimeFiles(cfg.Dirs)
	}()

	logger.Info("daemon listening", "addr", baseURL)
	if _, err := recorder.Record(runCtx, audit.Event{
		Type:    "daemon.started",
		Actor:   audit.ActorDaemon,
		Payload: audit.Payload("addr", baseURL, "pid", os.Getpid()),
	}); err != nil {
		logger.Warn("audit daemon.started failed", "err", err)
	}
	bus.Publish("daemon.started", map[string]any{"addr": baseURL, "pid": os.Getpid()})

	// 7. Install signal handlers. signal.NotifyContext cancels sigCtx on signal
	// OR when runCtx is cancelled (by /shutdown or parent), and serves as the
	// blocking context below.
	sigCtx, stop := signal.NotifyContext(runCtx, terminationSignals()...)
	defer stop()

	// 7a. Pipeline restart recovery (M14-06): re-drive every non-terminal
	// durable run in the background. Cancelled runs are terminal and never
	// resume; when the emergency stop is on, runs stay parked. The reconciler
	// (step 3) already marked in-flight stages interrupted.
	pipelineSvc.ResumeActiveRuns(runCtx)

	// 8. Serve until cancelled, then shut down.
	serveErr := srv.Serve(sigCtx)

	if _, err := recorder.Record(context.Background(), audit.Event{
		Type:    "daemon.stopped",
		Actor:   audit.ActorDaemon,
		Payload: audit.Payload("reason", shutdownReason(runCtx)),
	}); err != nil {
		logger.Warn("audit daemon.stopped failed", "err", err)
	}
	bus.Publish("daemon.stopped", nil)
	logger.Info("daemon stopped", "pid", os.Getpid())

	if serveErr != nil && !errors.Is(serveErr, context.Canceled) {
		return serveErr
	}
	return nil
}

// newLogger returns a structured JSON logger writing to w. JSON is chosen so
// `forge daemon logs` yields machine-parseable structured records.
func newLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func shutdownReason(ctx context.Context) string {
	if err := ctx.Err(); err != nil {
		return err.Error()
	}
	return "shutdown request"
}
