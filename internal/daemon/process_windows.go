//go:build windows

package daemon

import (
	"os"
	"os/exec"
	"syscall"
)

// Win32 access mask and error codes used for process liveness probing. These are
// stable Process and System Error Code values; they are defined locally (rather
// than referenced from the syscall package) because PROCESS_QUERY_LIMITED_
// INFORMATION is not exported by syscall on every supported Go version.
const (
	processQueryLimitedInfo = 0x1000 // PROCESS_QUERY_LIMITED_INFORMATION
	errAccessDenied         = syscall.Errno(5)
	errInvalidParameter     = syscall.Errno(87)
)

// processAlive reports whether a process with the given PID currently exists.
//
// os.FindProcess always succeeds on Windows and does not validate the PID, so it
// cannot be used for liveness probing. Instead we open a query handle to the
// process: success (or ERROR_ACCESS_DENIED, meaning the process exists but is
// protected) means alive; ERROR_INVALID_PARAMETER means the PID does not map to a
// running process. This lets the single-instance guard and stale-PID reclaim work
// correctly on Windows (it never starts a second daemon speculatively, and it
// never mistakes a crashed daemon's leftover PID file for a live owner).
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := syscall.OpenProcess(processQueryLimitedInfo, false, uint32(pid))
	if err == nil {
		_ = syscall.CloseHandle(handle)
		return true
	}
	// The process exists but we are not allowed to query it: treat it as alive
	// so we conservatively refuse to start a competing daemon.
	if err == errAccessDenied {
		return true
	}
	// ERROR_INVALID_PARAMETER (and any other failure) indicates no such process.
	return false
}

// detach configures the daemon child so it runs detached from the CLI that
// launched it: in its own process group and without allocating a console window.
// This is the Windows counterpart of the Unix setsid detach in process_unix.go.
// The child keeps its stdout/stderr handles (redirected to the daemon log file by
// the caller), so CREATE_NO_WINDOW does not affect logging.
func detach(cmd *exec.Cmd) {
	const (
		createNoWindow        = 0x08000000
		createNewProcessGroup = 0x00000200
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNoWindow | createNewProcessGroup,
	}
}

// terminationSignals returns the signals that trigger a graceful daemon
// shutdown. Windows has no SIGTERM equivalent; shutdown is driven by Ctrl+C
// (os.Interrupt) and the authenticated loopback /shutdown HTTP endpoint.
func terminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}

// terminateProcess is a no-op on Windows: graceful SIGTERM-style termination is
// unavailable. The lifecycle requests /shutdown over the loopback API and, as a
// last resort, escalates to os.Process.Kill (cross-platform) in lifecycle.go.
func terminateProcess(pid int) {
	_ = pid
}
