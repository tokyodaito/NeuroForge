//go:build !unix

package plugin

import "os"

// processAlive reports whether a process with the given pid exists.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p
	return true
}
