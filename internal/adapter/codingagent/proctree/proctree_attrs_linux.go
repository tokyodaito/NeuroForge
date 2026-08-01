//go:build linux

package proctree

import "syscall"

// groupSysProcAttr returns the process attributes for a [NewGroupCommand]
// child: a dedicated process group plus PR_SET_PDEATHSIG=SIGKILL, so the child
// (and, because they share its fate, its process group) cannot outlive the
// daemon. Without this, a kill -9'd daemon leaves the agent process group
// running, and the orphaned agent can keep writing the worktree while restart
// recovery re-drives the same run in it (security review H1).
//
// Residual race (documented, accepted): Go applies Pdeathsig in the child
// between fork and exec. If the parent dies inside that tiny window the signal
// is lost because the kernel only arms the death-signal at prctl time; the
// child also does not re-check its parent's liveness. The window is
// microseconds and the standard kill -9 case (parent exits while children are
// already running) is fully covered.
func groupSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}
