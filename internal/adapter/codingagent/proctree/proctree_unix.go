//go:build unix

package proctree

import (
	"os/exec"
	"syscall"
)

// NewGroupCommand returns a Cmd configured so the child and all its descendants
// run in their own process group (setpgid). This lets [KillGroup] terminate the
// whole group with one negative-pgid signal.
//
// On Linux the child additionally gets Pdeathsig=SIGKILL (see
// proctree_attrs_linux.go) so it cannot outlive the daemon process.
func NewGroupCommand(name string, arg ...string) *exec.Cmd {
	cmd := exec.Command(name, arg...)
	cmd.SysProcAttr = groupSysProcAttr()
	return cmd
}

// KillGroup sends sig to the entire process group led by cmd. The child's pid
// (the group leader pid under setpgid) is negated to address the whole group.
// A nil/finished cmd is a no-op. ESRCH (group already gone) is not an error.
func KillGroup(cmd *exec.Cmd, sig Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pgid := cmd.Process.Pid
	if err := syscall.Kill(-pgid, syscall.Signal(sig)); err != nil {
		if err == syscall.ESRCH {
			return nil
		}
		return err
	}
	return nil
}
