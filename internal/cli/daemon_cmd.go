package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"neuroforge/internal/daemon"
)

// runDaemon dispatches `forge daemon <subcommand>`.
func (a *App) runDaemon(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(a.Err, "Usage: forge daemon <run|start|stop|status|logs> [flags]")
		fmt.Fprintln(a.Err, "Run 'forge help' for details.")
		return ExitErr
	}
	dirs, err := a.resolveDirs()
	if err != nil {
		a.errf("resolve runtime dir: %v", err)
		return ExitErr
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "run":
		return a.daemonRun(dirs, rest)
	case "start":
		return a.daemonStart(dirs, rest)
	case "stop":
		return a.daemonStop(dirs, rest)
	case "status":
		return a.daemonStatus(dirs, rest)
	case "logs":
		return a.daemonLogs(dirs, rest)
	case "-h", "--help":
		fmt.Fprintln(a.Out, daemonUsage)
		return ExitOK
	default:
		fmt.Fprintf(a.Err, "%s: unknown daemon subcommand %q\n", a.Name, sub)
		fmt.Fprintln(a.Err, daemonUsage)
		return ExitErr
	}
}

const daemonUsage = `Usage: forge daemon <subcommand>

Subcommands:
  run     Run the daemon in the foreground until interrupted (used by 'start').
  start   Start the daemon as a detached background process (idempotent).
  stop    Stop a running daemon (graceful, then forceful).
  status  Print daemon lifecycle status (exit 0 if running, 1 otherwise).
  logs    Print the daemon structured log.`

// daemonRun runs the daemon in the foreground (the process body invoked by
// 'start' as a detached child, or directly for debugging). It blocks until
// SIGINT/SIGTERM.
func (a *App) daemonRun(dirs daemon.Dirs, args []string) int {
	fs := flag.NewFlagSet("daemon run", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	addr := fs.String("addr", "127.0.0.1:0", "loopback listen address (loopback only)")
	// runtime-home is written into argv by daemon.Start so OS process samplers
	// can attribute a PID to a single NeuroForge home (BF-F-01 test isolation).
	// If provided it must match the resolved home; it does not override env.
	runtimeHome := fs.String("runtime-home", "", "runtime home path (process identity; must match NEUROFORGE_HOME)")
	if err := fs.Parse(args); err != nil {
		return ExitErr
	}
	if *runtimeHome != "" && filepath.Clean(*runtimeHome) != filepath.Clean(dirs.Root) {
		fmt.Fprintf(a.Err, "%s: --runtime-home %q does not match resolved home %q\n", a.Name, *runtimeHome, dirs.Root)
		return ExitErr
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// SIGTERM is unix-only; on non-unix the handler below is a harmless noop.
	ctx = withTerminationSignals(ctx)

	if err := daemon.Run(ctx, daemon.RunConfig{Dirs: dirs, Addr: *addr}); err != nil {
		fmt.Fprintf(a.Err, "%s: daemon stopped: %v\n", a.Name, err)
		// A graceful shutdown (context cancellation) is not a CLI error.
		if isContextDone(ctx) {
			return ExitOK
		}
		return ExitErr
	}
	return ExitOK
}

func (a *App) daemonStart(dirs daemon.Dirs, args []string) int {
	fs := flag.NewFlagSet("daemon start", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	timeout := fs.Duration("timeout", daemon.DefaultReadyTimeout, "max time to wait for readiness")
	if err := fs.Parse(args); err != nil {
		return ExitErr
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout+2*time.Second)
	defer cancel()

	if daemon.GetStatus(ctx, dirs).State == daemon.StatusRunning {
		fmt.Fprintln(a.Out, "daemon already running")
		return ExitOK
	}
	if err := daemon.Start(ctx, dirs); err != nil {
		if err == daemon.ErrAlreadyRunning {
			fmt.Fprintln(a.Out, "daemon already running")
			return ExitOK
		}
		a.errf("start: %v", err)
		return ExitErr
	}
	st := daemon.GetStatus(ctx, dirs)
	fmt.Fprintf(a.Out, "daemon started (pid %d) at %s\n", st.PID, st.Addr)
	return ExitOK
}

func (a *App) daemonStop(dirs daemon.Dirs, args []string) int {
	fs := flag.NewFlagSet("daemon stop", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	timeout := fs.Duration("timeout", daemon.DefaultStopTimeout, "max time to wait for graceful exit")
	if err := fs.Parse(args); err != nil {
		return ExitErr
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout+3*time.Second)
	defer cancel()

	st := daemon.GetStatus(ctx, dirs)
	if st.State != daemon.StatusRunning && st.State != daemon.StatusUnhealthy {
		// Not running: idempotent no-op.
		daemon.CleanRuntimeFiles(dirs)
		fmt.Fprintln(a.Out, "daemon not running")
		return ExitOK
	}
	if err := daemon.Stop(ctx, dirs); err != nil {
		if err == daemon.ErrNotRunning {
			fmt.Fprintln(a.Out, "daemon not running")
			return ExitOK
		}
		a.errf("stop: %v", err)
		return ExitErr
	}
	fmt.Fprintln(a.Out, "daemon stopped")
	return ExitOK
}

func (a *App) daemonStatus(dirs daemon.Dirs, args []string) int {
	fs := flag.NewFlagSet("daemon status", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return ExitErr
	}

	st := daemon.GetStatus(context.Background(), dirs)
	if *jsonOut {
		fmt.Fprintln(a.Out, statusJSON(st))
	} else {
		fmt.Fprintln(a.Out, statusText(st))
	}
	if st.State == daemon.StatusRunning {
		return ExitOK
	}
	return ExitErr
}

func (a *App) daemonLogs(dirs daemon.Dirs, args []string) int {
	fs := flag.NewFlagSet("daemon logs", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	follow := fs.Bool("f", false, "follow the live event stream (SSE) if the daemon is running")
	n := fs.Int("n", 0, "print only the last N bytes of the log file")
	if err := fs.Parse(args); err != nil {
		return ExitErr
	}

	if *follow {
		st := daemon.GetStatus(context.Background(), dirs)
		if st.State == daemon.StatusRunning && st.Addr != "" {
			return daemonStreamEvents(a, dirs, st)
		}
		fmt.Fprintln(a.Err, "daemon not running; tailing log file instead. Press Ctrl-C to stop.")
	}

	data, err := daemon.ReadLogs(dirs, int64(*n))
	if err != nil {
		a.errf("read logs: %v", err)
		return ExitErr
	}
	_, _ = a.Out.Write(data)
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		fmt.Fprintln(a.Out)
	}
	return ExitOK
}

func statusText(st daemon.Status) string {
	switch st.State {
	case daemon.StatusRunning:
		h := st.Health
		ver := ""
		if h != nil && h.Version != "" {
			ver = "  version=" + h.Version
		}
		return fmt.Sprintf("state=running  pid=%d  addr=%s%s", st.PID, st.Addr, ver)
	case daemon.StatusUnhealthy:
		return fmt.Sprintf("state=unhealthy  pid=%d  note=%s", st.PID, st.Note)
	case daemon.StatusStale:
		return fmt.Sprintf("state=stale  pid=%d  note=%s", st.PID, st.Note)
	case daemon.StatusCorrupted:
		return fmt.Sprintf("state=corrupted  note=%s", st.Note)
	default:
		return "state=absent  (no daemon running)"
	}
}

func statusJSON(st daemon.Status) string {
	return fmt.Sprintf(`{"state":%q,"pid":%d,"addr":%q,"note":%q}`,
		st.State, st.PID, st.Addr, st.Note)
}

func isContextDone(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}
