//go:build windows

package proctree

import (
	"fmt"
	"os/exec"
	"syscall"
)

// CREATE_NEW_PROCESS_GROUP flag value (see CreateProcess docs).
const createNewProcessGroup = 0x00000200

// NewGroupCommand returns a Cmd configured to create a new Windows process
// group so [KillGroup] can terminate the whole tree.
func NewGroupCommand(name string, arg ...string) *exec.Cmd {
	cmd := exec.Command(name, arg...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
	return cmd
}

// KillGroup terminates the process tree led by cmd via taskkill /T /F. The
// signal argument is ignored (taskkill is always forceful).
func KillGroup(cmd *exec.Cmd, _ Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	kill := exec.Command("taskkill", "/PID", itoa(cmd.Process.Pid), "/T", "/F")
	if err := kill.Run(); err != nil {
		return fmt.Errorf("taskkill: %w", err)
	}
	return nil
}

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
