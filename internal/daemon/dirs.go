package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// EnvHome overrides the NeuroForge runtime root directory. Tests and the CLI
// use it to point the daemon at a temporary location.
const EnvHome = "NEUROFORGE_HOME"

// EnvChild marks a daemon process that was spawned by `forge daemon start` (so
// it knows it should not, e.g., re-fork). It carries no secret.
const EnvChild = "NEUROFORGE_DAEMON_CHILD"

// Dirs describes the daemon's on-disk runtime layout. Every file that holds
// runtime state or a secret lives here with restrictive permissions.
type Dirs struct {
	Root         string // NEUROFORGE_HOME (default ~/.neuroforge)
	StateDB      string // SQLite database file
	LogFile      string // daemon structured log (append)
	PIDFile      string // PID of the running daemon
	TokenFile    string // loopback API bearer token (secret)
	AddrFile     string // base URL of the loopback API
	ArtifactsDir string // content-addressed artifacts (future)
}

// DefaultDirs resolves the runtime layout from NEUROFORGE_HOME, falling back to
// ~/.neuroforge. The path is made absolute.
func DefaultDirs() (Dirs, error) {
	root := os.Getenv(EnvHome)
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Dirs{}, fmt.Errorf("daemon: resolve home dir: %w", err)
		}
		root = filepath.Join(home, ".neuroforge")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return Dirs{}, fmt.Errorf("daemon: absolutise %q: %w", root, err)
	}
	return Dirs{
		Root:         root,
		StateDB:      filepath.Join(root, "state.db"),
		LogFile:      filepath.Join(root, "logs", "daemon.log"),
		PIDFile:      filepath.Join(root, "run", "daemon.pid"),
		TokenFile:    filepath.Join(root, "run", "daemon.token"),
		AddrFile:     filepath.Join(root, "run", "daemon.addr"),
		ArtifactsDir: filepath.Join(root, "artifacts"),
	}, nil
}

// WithRoot returns a Dirs rooted at root (helper for tests).
func WithRoot(root string) Dirs {
	return Dirs{
		Root:         root,
		StateDB:      filepath.Join(root, "state.db"),
		LogFile:      filepath.Join(root, "logs", "daemon.log"),
		PIDFile:      filepath.Join(root, "run", "daemon.pid"),
		TokenFile:    filepath.Join(root, "run", "daemon.token"),
		AddrFile:     filepath.Join(root, "run", "daemon.addr"),
		ArtifactsDir: filepath.Join(root, "artifacts"),
	}
}

// Ensure creates the runtime directories with mode 0o700 (owner-only).
func (d Dirs) Ensure() error {
	for _, dir := range []string{
		d.Root,
		filepath.Dir(d.LogFile),
		filepath.Dir(d.PIDFile),
		d.ArtifactsDir,
	} {
		if err := mkdirPrivate(dir); err != nil {
			return err
		}
	}
	return nil
}

// mkdirPrivate creates dir (and parents) with mode 0o700. Existing directories
// are left as-is. 0o700 has no group/other bits, so it is immune to umask
// loosening.
func mkdirPrivate(dir string) error {
	if dir == "" || dir == "." {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("daemon: mkdir %q: %w", dir, err)
	}
	return nil
}

// ErrIncompatibleHome is returned when the resolved home path is not usable.
var ErrIncompatibleHome = errors.New("daemon: incompatible home directory")
