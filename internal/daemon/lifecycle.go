package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"neuroforge/internal/transport"
)

// DefaultReadyTimeout is how long Start waits for the spawned daemon to become
// healthy before reporting failure.
const DefaultReadyTimeout = 8 * time.Second

// DefaultStopTimeout is how long Stop waits for graceful shutdown before
// escalating to a signal/kill.
const DefaultStopTimeout = 8 * time.Second

// Start spawns a detached daemon process (via `forge daemon run`) if no healthy
// daemon is already running. It is idempotent: a repeated Start against a live,
// healthy daemon returns ErrAlreadyRunning and never launches a second process.
// Stale or corrupted runtime files are reclaimed first.
//
// BF-05 / R-2.3 (dual-daemon race): the liveness check is RETRIED a few times
// before we conclude the daemon is down. Under heavy concurrent load a live
// daemon's Health endpoint can be momentarily slow; without the retry, two CLIs
// could each decide the daemon is dead, clobber its runtime files
// (cleanRuntimeFiles), and spawn a second daemon. The retry makes the decision
// authoritative so cleanRuntimeFiles never runs against a live daemon.
func Start(ctx context.Context, dirs Dirs) error {
	if isReachableAndHealthyRetried(ctx, dirs, 4, 150*time.Millisecond) {
		return ErrAlreadyRunning
	}
	// Reclaim stale/corrupted runtime so the new start is clean.
	cleanRuntimeFiles(dirs)
	if err := dirs.Ensure(); err != nil {
		return err
	}

	exe, err := forgeExecutable()
	if err != nil {
		return err
	}

	// Append a startup separator to the log so `forge daemon logs` shows a
	// clear boundary between runs.
	if err := AppendLog(dirs, fmt.Sprintf("\n--- daemon starting %s ---\n", time.Now().UTC().Format(time.RFC3339))); err != nil {
		return fmt.Errorf("open log file: %w", err)
	}

	logf, err := os.OpenFile(dirs.LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open log for child: %w", err)
	}

	cmd := exec.Command(exe, "daemon", "run")
	cmd.Dir = dirs.Root
	cmd.Env = childEnv(dirs.Root)
	cmd.Stdin = nil
	cmd.Stdout = logf
	cmd.Stderr = logf
	detach(cmd) // detach: new session on unix, survives parent exit

	// Final defence against a dual-spawn (BF-05): a daemon may have become
	// healthy while we prepared the spawn. Re-check immediately before
	// launching the process and bail out if so, never clobbering a live
	// daemon's runtime files with a second instance.
	if isReachableAndHealthyRetried(ctx, dirs, 2, 100*time.Millisecond) {
		_ = logf.Close()
		return ErrAlreadyRunning
	}

	if err := cmd.Start(); err != nil {
		_ = logf.Close()
		return fmt.Errorf("spawn daemon: %w", err)
	}
	// The child holds its own dup of the log fd; close the parent's copy.
	_ = logf.Close()
	// Fully release the child: we intentionally do not Wait on it, so the
	// daemon survives this CLI process exiting.
	_ = cmd.Process.Release()

	if err := waitForReady(ctx, dirs, DefaultReadyTimeout); err != nil {
		return err
	}
	return nil
}

// forgeExecutable returns the path to the running forge binary, used to spawn
// the daemon child.
func forgeExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate forge executable: %w", err)
	}
	if _, err := os.Stat(exe); err != nil {
		return "", fmt.Errorf("forge executable missing: %w", err)
	}
	return exe, nil
}

// childEnv builds the environment for the daemon child: the parent environment
// with NEUROFORGE_HOME forced to root and the child marker set. No secrets or
// provider credentials are added (spec §29.2 — the daemon env is allowlisted).
func childEnv(root string) []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+2)
	for _, kv := range env {
		if strings.HasPrefix(kv, EnvHome+"=") || strings.HasPrefix(kv, EnvChild+"=") {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, EnvHome+"="+root, EnvChild+"=1")
	return out
}

// waitForReady polls the loopback API until the daemon is healthy or the
// timeout elapses.
func waitForReady(ctx context.Context, dirs Dirs, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if isReachableAndHealthy(ctx, dirs) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("daemon did not become healthy within %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(80 * time.Millisecond):
		}
	}
}

// Stop gracefully stops a running daemon: it requests /shutdown over the
// loopback API, then waits for the process to exit. If the graceful path fails
// or times out, it escalates to SIGTERM and finally SIGKILL (os.Process.Kill).
// Stale runtime files are always cleaned up.
func Stop(ctx context.Context, dirs Dirs) error {
	st := probeStatus(ctx, dirs)
	switch st.State {
	case StatusAbsent, StatusStale, StatusCorrupted:
		cleanRuntimeFiles(dirs)
		return fmt.Errorf("%w: %s", ErrNotRunning, st.State)
	}

	// Graceful: POST /shutdown.
	if st.Addr != "" {
		token, _ := readToken(dirs)
		cli := transport.NewClient(st.Addr, token)
		shutdownCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_ = cli.Shutdown(shutdownCtx) // best effort
		cancel()
	}

	if st.PID > 0 {
		if !waitForProcessExit(st.PID, DefaultStopTimeout) {
			// Escalate: SIGTERM, then Kill.
			terminateProcess(st.PID)
			if !waitForProcessExit(st.PID, 3*time.Second) {
				killProcess(st.PID)
				_ = waitForProcessExit(st.PID, 2*time.Second)
			}
		}
	}
	cleanRuntimeFiles(dirs)
	return nil
}

// waitForProcessExit polls processAlive until the process is gone or the
// timeout elapses. Returns true if the process exited.
func waitForProcessExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if !processAlive(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(60 * time.Millisecond)
	}
}

// killProcess hard-kills a process via the cross-platform os.Process.Kill.
func killProcess(pid int) {
	if pid <= 0 {
		return
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = p.Kill()
}

// GetStatus returns the current lifecycle status snapshot.
func GetStatus(ctx context.Context, dirs Dirs) Status {
	return probeStatus(ctx, dirs)
}

// Connect returns a transport.Client for the running daemon, or an error if the
// daemon is not reachable. It reads the address and token from the runtime
// files. Both CLI and TUI use this to reach the daemon API (ADR-0004: the TUI
// never touches SQLite directly).
func Connect(ctx context.Context, dirs Dirs) (*transport.Client, error) {
	addr, err := ReadAddr(dirs)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotRunning, err)
	}
	token, err := ReadToken(dirs)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotRunning, err)
	}
	if addr == "" || token == "" {
		return nil, ErrNotRunning
	}
	cli := transport.NewClient(addr, token)
	if _, err := cli.Health(ctx); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotRunning, err)
	}
	return cli, nil
}
