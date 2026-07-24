package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/fake"
	"neuroforge/internal/audit"
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
			&attemptReconciler{wm: wsManager}, // M7: recover in-flight attempts (AC-27)
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

	// 4b. Lease manager + supervisor (M3, spec §17/§18/§12).
	// The workspace manager was created at step 2a (before reconciliation).
	// The supervisor runs agents with an allowlisted environment (AC-28).
	leaseManager := workgraph.NewLeaseManager(db)

	// Register the fake coding agent so the supervisor can run it (rule §36.6).
	adapterRegistry := codingagent.Default()
	if !hasAdapter(adapterRegistry, "fake") {
		adapterRegistry.MustRegister(fake.New(fake.AdapterOptions{Installed: true}), 0)
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
	}, bus, logger)
	if err != nil {
		return fmt.Errorf("transport server: %w", err)
	}
	srv.SetVersion(version.Version)

	// 5. Bind the loopback listener BEFORE writing runtime files, so the
	// address advertised to clients is real.
	addr, err := srv.Listen()
	if err != nil {
		return fmt.Errorf("transport listen: %w", err)
	}
	baseURL := "http://" + addr.String()

	// 6. Write runtime files (mode 0o600) so the CLI/TUI can reach us.
	if err := writeRuntimeFiles(cfg.Dirs, os.Getpid(), token, baseURL); err != nil {
		return fmt.Errorf("write runtime files: %w", err)
	}
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
