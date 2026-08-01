//go:build unix && !linux

package proctree

import "syscall"

// groupSysProcAttr returns the process attributes for a [NewGroupCommand]
// child: a dedicated process group only. Pdeathsig is Linux-specific; other
// unix platforms (darwin, *BSD) have no equivalent knob in syscall.SysProcAttr,
// so orphan containment relies on [KillGroup] plus the daemon's cancellation
// paths there.
func groupSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
