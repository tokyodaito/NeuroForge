// Package workspace manages Git worktrees and task branches.
//
// STATUS: scaffold — not implemented (planned for milestone M3).
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §17): create isolated worktrees under
// ~/.neuroforge/workspaces, enforce branch naming
// (forge/<task-id>/<work-package-id>/attempt-<n> and forge/result/<task-id>),
// create local checkpoint commits, and never touch the user's primary checkout.
// See ADR-0007.
//
// Boundaries: must never perform push/PR/MR/merge (those belong to adapter/vcs and
// are gated by policy and the Merge Governor).
package workspace
