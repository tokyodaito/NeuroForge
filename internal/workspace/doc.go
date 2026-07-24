// Package workspace manages Git worktrees and task branches.
//
// STATUS: implemented (milestone M3).
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §17): create isolated worktrees under
// ~/.neuroforge/workspaces, enforce branch naming
// (forge/<task-id>/<work-package-id>/attempt-<n> and forge/result/<task-id>),
// create local checkpoint commits, and never touch the user's primary checkout.
// See ADR-0007.
//
// Security invariants (AC-7, AC-8, §17.1, §36.13, §36.14):
//   - The user's primary checkout is NEVER modified. All writes happen inside
//     managed worktrees.
//   - Zero Git network operations: the git runner enforces an allowlist that
//     excludes push/fetch/pull/clone/ls-remote/etc. LOCAL_REVIEW is safe by
//     construction.
//   - Task branches are local-only and never sent to a remote.
//
// Boundaries: must never perform push/PR/MR/merge (those belong to adapter/vcs
// and are gated by policy and the Merge Governor).
package workspace
