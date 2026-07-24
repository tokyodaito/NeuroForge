//go:build unix

package cli

import (
	"os"
	"syscall"
)

func sendSIGTERM(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(syscall.SIGTERM)
}
