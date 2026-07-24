package codex

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// detect resolves the Codex binary and runs "codex --version" once; subsequent
// calls return the cached result. It is concurrency-safe.
func (a *Adapter) detect(ctx context.Context) protocol.DetectionResult {
	a.detMu.Lock()
	defer a.detMu.Unlock()
	if a.detDone {
		return a.det
	}
	a.det, a.detStderr, a.detVersion = a.probe(ctx)
	a.detDone = true
	return a.det
}

// cachedVersion returns the version parsed by the last detect, or a zero
// (invalid) version before any detect has run.
func (a *Adapter) cachedVersion() parsedVersion {
	a.detMu.Lock()
	defer a.detMu.Unlock()
	return a.detVersion
}

// probe performs a single detection cycle (not cached).
func (a *Adapter) probe(ctx context.Context) (protocol.DetectionResult, string, parsedVersion) {
	noVer := parsedVersion{major: -1, minor: -1, patch: -1}
	binary := a.opts.BinaryPath
	if binary == "" {
		path, err := a.opts.lookup("codex")
		if err != nil {
			return protocol.DetectionResult{Detail: "codex not found on PATH: " + err.Error()}, "", noVer
		}
		binary = path
	}

	argv := versionArgv(binary)
	proc, err := a.runner.Start(argv, "", buildAgentEnv(nil))
	if err != nil {
		return protocol.DetectionResult{
			Installed: false, Path: binary,
			Detail: "codex found at " + binary + " but could not be launched: " + err.Error(),
		}, "", parsedVersion{major: -1, minor: -1, patch: -1, raw: binary}
	}

	stdout, stderr, exitCode := readProbe(proc)
	pv := parseCodexVersion(stdout)

	// "codex --version" exiting 0 (or emitting any stdout) means the engine is
	// present and runnable. A non-zero exit with no output means it is not
	// usable in this environment.
	installed := exitCode == 0 || stdout != ""
	detail := "codex detected at " + binary
	if !installed {
		detail = "codex binary at " + binary + " did not report a version (exit " + itoa(exitCode) + ")"
	} else if !pv.valid {
		detail += " (version string unparsable; capabilities reported conservatively)"
	}
	return protocol.DetectionResult{
		Installed: installed,
		Path:      binary,
		Version:   pv.raw,
		Detail:    detail,
	}, stderr, pv
}

// readProbe drains the probe process stdout/stderr and waits for exit. stdout is
// read concurrently so the version pipe can never deadlock.
func readProbe(proc Proc) (stdout, stderr string, exitCode int) {
	type out struct{ data []byte }
	ch := make(chan out, 1)
	go func() {
		b, _ := io.ReadAll(proc.Stdout())
		ch <- out{b}
	}()
	code, waitStderr := proc.Wait()
	so := <-ch
	return strings.TrimSpace(string(so.data)), strings.TrimSpace(redact(waitStderr)), code
}

// itoa is a dependency-free int -> string (keeps this package stdlib-only).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// --- run state (cancellation, timeout, session capture) ---

type cancelReason int

const (
	reasonUser cancelReason = iota
	reasonTimeout
)

// runState tracks one live run for cancellation, timeout and session capture.
type runState struct {
	proc      Proc
	cancel    context.CancelFunc
	timer     *time.Timer
	bootstrap chan struct{}

	mu        sync.Mutex
	reason    cancelReason
	sessionID string
	booted    bool
}

func (rs *runState) setReason(r cancelReason) {
	rs.mu.Lock()
	rs.reason = r
	rs.mu.Unlock()
}

func (rs *runState) reasonOf() cancelReason {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.reason
}

func (rs *runState) setSession(sid string) {
	rs.mu.Lock()
	if rs.sessionID == "" {
		rs.sessionID = sid
	}
	rs.mu.Unlock()
}

func (rs *runState) session() string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.sessionID
}

// signalBootstrap closes the bootstrap channel once, unblocking Start's session
// capture. Safe to call repeatedly.
func (rs *runState) signalBootstrap() {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.booted {
		return
	}
	rs.booted = true
	close(rs.bootstrap)
}

// errBinaryMissing is returned when the Codex binary cannot be found.
var errBinaryMissing = errors.New("codex: binary not found")
