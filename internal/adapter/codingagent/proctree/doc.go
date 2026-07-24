// Package proctree provides helpers to spawn and terminate an entire process
// group (spec: cancellation ends the whole process group).
//
// On unix, children are started as a new process group leader (setpgid) so a
// single negative-pgid signal reaches every descendant. On Windows, a new
// process group is created and terminated via taskkill. Callers use
// [NewGroupCommand] to build the exec.Cmd, [KillGroup] to terminate it, and
// [IsGroupLeader] for diagnostics.
package proctree
