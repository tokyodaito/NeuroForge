//go:build !unix && !windows

package plugin

import "os"

// processAlive reports whether a process with the given pid exists, on platforms
// without a Unix signal-0 probe or a Windows OpenProcess handle (e.g. js/wasm).
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p
	return true
}
