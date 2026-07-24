//go:build !unix && !windows

// Package daemon process helpers for platforms without Unix signal support and
// without a dedicated Windows implementation (e.g. js/wasm).
//
// STATUS: limited. Unix (darwin/linux) process supervision is fully implemented
// in process_unix.go and Windows in process_windows.go. On other platforms the
// daemon Run loop, loopback API, durable storage and audit are platform-
// independent and fully functional; only detached start/stop liveness probing is
// reduced here. This is an explicitly-marked limitation (rule §36.25), not a
// hidden stub.
package daemon

import (
	"os"
	"os/exec"
)

func processAlive(pid int) bool {
	// On Windows, os.FindProcess always succeeds and does not validate the pid.
	// We cannot reliably probe liveness without the Windows API; treat the pid
	// as potentially alive so we never start a second daemon speculatively.
	return pid > 0
}

func detach(cmd *exec.Cmd) {
	// No-op on Windows for M0. A detached background daemon on Windows will be
	// implemented with a job object / CREATE_NO_WINDOW in a later milestone.
}

func terminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}

func terminateProcess(pid int) {
	// Graceful signal-based termination is not available on Windows; the
	// lifecycle falls back to os.Process.Kill (cross-platform).
	_ = pid
}
