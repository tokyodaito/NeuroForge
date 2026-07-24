package cli

import (
	"os"
)

// isTerminal reports whether f refers to an interactive terminal, using only
// the standard library. A file is treated as a terminal when it is a char
// device (covers TTYs) — sufficient to decide whether the TUI may enter
// full-screen mode, and to degrade gracefully in CI (pipes/files return false).
func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
