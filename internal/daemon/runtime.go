package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"neuroforge/internal/transport"
)

// LifecycleStatus describes the discovered state of a daemon runtime dir.
type LifecycleStatus string

const (
	StatusAbsent    LifecycleStatus = "absent"    // no PID file present
	StatusRunning   LifecycleStatus = "running"   // alive PID + healthy API
	StatusUnhealthy LifecycleStatus = "unhealthy" // alive PID, API not responding
	StatusStale     LifecycleStatus = "stale"     // PID file present but process dead
	StatusCorrupted LifecycleStatus = "corrupted" // PID/runtime files unparseable
)

// Status is a snapshot of the daemon's runtime state, as best-effort discovered
// from disk and the loopback API.
type Status struct {
	State  LifecycleStatus
	PID    int
	Addr   string
	Health *transport.HealthResponse
	Note   string
}

// Errors used by the lifecycle.
var (
	ErrAlreadyRunning = errors.New("daemon: already running")
	ErrNotRunning     = errors.New("daemon: not running")
)

// writeRuntimeFiles writes the PID, token and addr files with mode 0o600. It is
// called by the daemon once it has bound the loopback listener.
func writeRuntimeFiles(dirs Dirs, pid int, token, addr string) error {
	files := map[string]string{
		dirs.PIDFile:   strconv.Itoa(pid),
		dirs.TokenFile: token,
		dirs.AddrFile:  addr,
	}
	for path, content := range files {
		if err := writeFileSecret(path, content); err != nil {
			return err
		}
	}
	return nil
}

// writeFileSecret writes content to path, creating it mode 0o600 (and chmod to
// guarantee 0o600 regardless of umask).
func writeFileSecret(path, content string) error {
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("daemon: write %q: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("daemon: chmod %q: %w", path, err)
	}
	return nil
}

// cleanRuntimeFiles removes the PID, token and addr files. It is best-effort
// and never fails the caller on a missing file.
func cleanRuntimeFiles(dirs Dirs) {
	for _, p := range []string{dirs.PIDFile, dirs.TokenFile, dirs.AddrFile} {
		_ = os.Remove(p)
	}
}

// CleanRuntimeFiles is the exported form of cleanRuntimeFiles for CLI use.
func CleanRuntimeFiles(dirs Dirs) { cleanRuntimeFiles(dirs) }

// readPID reads and parses the PID file. Missing file -> (0, nil).
func readPID(dirs Dirs) (int, error) {
	b, err := os.ReadFile(dirs.PIDFile)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, fmt.Errorf("daemon: parse pid %q: %w", strings.TrimSpace(string(b)), err)
	}
	return pid, nil
}

// readToken reads the token file. Missing file -> "".
func readToken(dirs Dirs) (string, error) {
	b, err := os.ReadFile(dirs.TokenFile)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// ReadToken is the exported form of readToken for CLI/TUI consumers.
func ReadToken(dirs Dirs) (string, error) { return readToken(dirs) }

// readAddr reads the addr file. Missing file -> "".
func readAddr(dirs Dirs) (string, error) {
	b, err := os.ReadFile(dirs.AddrFile)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// ReadAddr is the exported form of readAddr for CLI/TUI consumers.
func ReadAddr(dirs Dirs) (string, error) { return readAddr(dirs) }

// probeStatus discovers the daemon lifecycle status from disk + API.
func probeStatus(ctx context.Context, dirs Dirs) Status {
	pid, err := readPID(dirs)
	if err != nil {
		return Status{State: StatusCorrupted, Note: "pid file unreadable: " + err.Error()}
	}
	if pid == 0 {
		return Status{State: StatusAbsent, Note: "no pid file"}
	}
	if !processAlive(pid) {
		return Status{State: StatusStale, PID: pid, Note: "process not running (stale pid)"}
	}
	addr, _ := readAddr(dirs)
	token, _ := readToken(dirs)
	if addr == "" || token == "" {
		return Status{State: StatusCorrupted, PID: pid, Note: "pid alive but addr/token missing"}
	}
	cli := transport.NewClient(addr, token)
	healthCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	hr, err := cli.Health(healthCtx)
	if err != nil {
		return Status{State: StatusUnhealthy, PID: pid, Addr: addr, Note: "pid alive but API unreachable: " + err.Error()}
	}
	return Status{State: StatusRunning, PID: pid, Addr: addr, Health: &hr, Note: "running"}
}

// isReachableAndHealthy reports whether a live, healthy daemon owns dirs.
func isReachableAndHealthy(ctx context.Context, dirs Dirs) bool {
	return probeStatus(ctx, dirs).State == StatusRunning
}

// isReachableAndHealthyRetried polls liveness a few times so a live-but-momentarily-slow
// daemon is not mistaken for dead (BF-05 dual-daemon race). It returns true as
// soon as the daemon is reachable+healthy, or false after `tries` attempts.
func isReachableAndHealthyRetried(ctx context.Context, dirs Dirs, tries int, interval time.Duration) bool {
	for i := 0; i < tries; i++ {
		if isReachableAndHealthy(ctx, dirs) {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		if i < tries-1 {
			select {
			case <-ctx.Done():
				return false
			case <-time.After(interval):
			}
		}
	}
	return false
}

// ReadLogs returns the contents of the daemon log file. If n > 0, only the last
// n bytes are returned (tail). Missing file -> empty (no error).
func ReadLogs(dirs Dirs, n int64) ([]byte, error) {
	b, err := os.ReadFile(dirs.LogFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if n > 0 && int64(len(b)) > n {
		b = b[int64(len(b))-n:]
	}
	return b, nil
}

// AppendLog writes a startup banner separator to the daemon log file (used when
// starting, so a human can find the start of a run in `forge daemon logs`).
func AppendLog(dirs Dirs, line string) error {
	f, err := os.OpenFile(dirs.LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line)
	// ensure parent dir exists for the next read
	_ = filepath.Dir(dirs.LogFile)
	return err
}
