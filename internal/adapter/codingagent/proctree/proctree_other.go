//go:build !unix && !windows

package proctree

import "os/exec"

// NewGroupCommand returns a plain Cmd on platforms without process-group
// support (e.g. js/wasm). Process-group cancellation is best-effort here.
func NewGroupCommand(name string, arg ...string) *exec.Cmd {
	return exec.Command(name, arg...)
}

// KillGroup terminates just the leader process on platforms without
// process-group support.
func KillGroup(cmd *exec.Cmd, _ Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
