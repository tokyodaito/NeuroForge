//go:build unix

package daemon

import (
	"os"
	"os/exec"
	"syscall"
)

// processAlive reports whether a process with the given PID currently exists.
// It uses signal 0 (no actual signal delivered), the POSIX idiom.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// detach configures cmd so the child survives the parent exiting: it starts a
// new session with no controlling terminal. Used by `forge daemon start`.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// terminationSignals returns the signals that trigger a graceful daemon
// shutdown.
func terminationSignals() []os.Signal {
	return []os.Signal{syscall.SIGTERM, os.Interrupt}
}

// terminateProcess sends SIGTERM to a process (best effort; forceful kill is
// done via os.Process.Kill which is cross-platform).
func terminateProcess(pid int) {
	if pid <= 0 {
		return
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = p.Signal(syscall.SIGTERM)
}
