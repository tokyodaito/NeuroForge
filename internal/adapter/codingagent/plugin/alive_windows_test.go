//go:build windows

package plugin

import "syscall"

// Win32 constants for process liveness probing (stable values; defined locally
// because PROCESS_QUERY_LIMITED_INFORMATION is not exported by syscall on every
// Go version).
const (
	queryLimitedInfo = 0x1000
	errAccessDenied  = syscall.Errno(5)
)

// processAlive reports whether a process with the given pid exists. os.FindProcess
// always succeeds on Windows and does not validate the pid, so we open a query
// handle: success or ERROR_ACCESS_DENIED means the process is still alive.
// Used to assert the plugin process group was terminated after Close.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := syscall.OpenProcess(queryLimitedInfo, false, uint32(pid))
	if err == nil {
		_ = syscall.CloseHandle(handle)
		return true
	}
	return err == errAccessDenied
}
