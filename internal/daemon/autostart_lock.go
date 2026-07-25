package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// LockAutostart acquires a short exclusive file lock on the autostart lock
// file under dirs.Root. It is used by the CLI's ensureDaemonDirs to serialize
// concurrent cold-start attempts so two CLIs cannot both spawn a daemon
// (B-11 / REQUIREMENTS.md R-2.3). The returned unlock function MUST be
// invoked (typically via defer) to release the lock.
//
// The lock is implemented as a non-blocking flock(2) on a dedicated file
// (autostart.lock) under the NeuroForge home. We use a dedicated lock file
// (not the pid file) so the lock state is independent of the daemon's own
// lifecycle bookkeeping. The lock is advisory: cooperative CLIs honour it,
// the daemon itself never blocks on it.
//
// On platforms where flock is unavailable the lock is a no-op (the underlying
// guarantee is still provided by the post-spawn health re-check inside
// ensureDaemonDirs).
func LockAutostart(ctx context.Context, dirs Dirs) (func(), error) {
	return lockFile(ctx, filepath.Join(dirs.Root, "autostart.lock"))
}

// lockFile acquires a non-blocking exclusive flock on the named file, retrying
// briefly. It is the shared implementation of the CLI autostart lock and the
// daemon bind-claim lock (BF-05). The returned unlock function MUST be invoked.
func lockFile(ctx context.Context, lockPath string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("lock: mkdir: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("lock: open: %w", err)
	}

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(15 * time.Second)
	}
	for {
		if err := flockTry(f.Fd()); err == nil {
			return func() {
				_ = flockUnlock(f.Fd())
				_ = f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			// Platforms without flock support: no-op lock (the daemon-side
			// bind re-check still guards against dual daemons).
			return func() {}, nil
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("lock: timed out waiting for another forge process")
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(40 * time.Millisecond):
		}
	}
}
