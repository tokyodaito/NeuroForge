//go:build unix

package plugin

import "syscall"

// processAlive reports whether a process with the given pid exists (sends
// signal 0). Used to assert the plugin process group was terminated.
func processAlive(pid int) bool {
	if err := syscall.Kill(pid, syscall.Signal(0)); err == nil {
		return true
	}
	return false
}
