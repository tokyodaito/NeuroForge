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

// ensureDaemonDirs is like ensureDaemon but takes pre-resolved dirs.
func ensureDaemonDirs(dirs daemon.Dirs) (*transport.Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Try to connect first.
	if cli, err := daemon.Connect(ctx, dirs); err == nil {
		return cli, nil
	}

	// Daemon not running — start it.
	if err := dirs.Ensure(); err != nil {
		return nil, fmt.Errorf("ensure dirs: %w", err)
	}
	if err := daemon.Start(ctx, dirs); err != nil && err != daemon.ErrAlreadyRunning {
		return nil, fmt.Errorf("start daemon: %w", err)
	}

	cli, err := daemon.Connect(ctx, dirs)
	if err != nil {
		return nil, fmt.Errorf("daemon did not become ready: %w", err)
	}
	return cli, nil
}
