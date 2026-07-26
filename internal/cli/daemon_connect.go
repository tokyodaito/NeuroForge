package cli

import (
	"context"
	"fmt"
	"time"

	"neuroforge/internal/daemon"
	"neuroforge/internal/transport"
)

// ensureDaemon connects to a running daemon, starting it first if necessary.
// It returns a transport.Client ready for API calls. Business commands use this
// so the user does not need to manually start the daemon.
func (a *App) ensureDaemon() (*transport.Client, error) {
	dirs, err := a.resolveDirs()
	if err != nil {
		return nil, err
	}
	return ensureDaemonDirs(dirs)
}

// autostartLockGuard is a short file lock held around the find/spawn sequence
// in ensureDaemonDirs so two CLIs starting from a cold home at the same time
// do not both spawn a daemon (B-11 / R-2.3). The lock is held only across the
// probe+spawn; release happens as soon as one client owns the daemon.
//
// We use a non-blocking flock on dirs.PIDFile (which is also the daemon's
// lifecycle file). The lock is released when the calling CLI process exits or
// explicitly releases it; the spawned daemon is NOT a child of the CLI (it
// detaches), so the lock does not affect the daemon's lifecycle.
var _ = struct{}{} // placeholder so the comment lives near the symbol

// ensureDaemonDirs is like ensureDaemon but takes pre-resolved dirs.
//
// It is race-clean (B-11): a short file lock around the probe+spawn sequence
// guarantees at most one CLI spawns a daemon when two CLIs start concurrently
// from the same cold home (REQUIREMENTS.md §3 R-2.3). A second CLI that
// arrives while the first is spawning sees the lock, waits briefly, and then
// reuses the now-running daemon instead of spawning a second one.
func ensureDaemonDirs(dirs daemon.Dirs) (*transport.Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Fast path: daemon already up + healthy.
	if cli, err := daemon.Connect(ctx, dirs); err == nil {
		return cli, nil
	}

	// Slow path: spawn (or reuse). Acquire the autostart lock so concurrent
	// CLIs serialize their spawn attempts.
	unlock, lerr := daemon.LockAutostart(ctx, dirs)
	if lerr != nil {
		return nil, fmt.Errorf("acquire autostart lock: %w", lerr)
	}
	defer unlock()

	// Re-check after acquiring the lock: another CLI may have started the
	// daemon while we were waiting. Retry a few times so a live-but-momentarily
	// slow daemon (Health sluggish under load) is not mistaken for absent and
	// re-spawned (BF-05 / R-2.3 dual-daemon race).
	if cli, ok := connectRetried(ctx, dirs, 4, 120*time.Millisecond); ok {
		return cli, nil
	}

	// Spawn the daemon.
	if err := dirs.Ensure(); err != nil {
		return nil, fmt.Errorf("ensure dirs: %w", err)
	}
	if err := daemon.Start(ctx, dirs); err != nil && err != daemon.ErrAlreadyRunning {
		return nil, fmt.Errorf("start daemon: %w", err)
	}

	// Wait for readiness (the lock prevents a second spawn racing us here).
	cli, err := daemon.Connect(ctx, dirs)
	if err != nil {
		return nil, fmt.Errorf("daemon did not become ready: %w", err)
	}
	return cli, nil
}

// connectRetried tries daemon.Connect up to `tries` times spaced by `interval`,
// returning the client on the first success. Used so a slow-but-live daemon is
// reused instead of re-spawned (BF-05).
func connectRetried(ctx context.Context, dirs daemon.Dirs, tries int, interval time.Duration) (*transport.Client, bool) {
	for i := 0; i < tries; i++ {
		if cli, err := daemon.Connect(ctx, dirs); err == nil {
			return cli, true
		}
		if ctx.Err() != nil {
			return nil, false
		}
		if i < tries-1 {
			select {
			case <-ctx.Done():
				return nil, false
			case <-time.After(interval):
			}
		}
	}
	return nil, false
}
