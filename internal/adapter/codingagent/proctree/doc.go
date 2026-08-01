// Package proctree provides helpers to spawn and terminate an entire process
// group (spec: cancellation ends the whole process group).
//
// Children are started as a new process group leader (setpgid) so a single
// negative-pgid signal reaches every descendant. Callers use [NewGroupCommand]
// to build the exec.Cmd, [KillGroup] to terminate it, and [IsGroupLeader] for
// diagnostics.
package proctree
