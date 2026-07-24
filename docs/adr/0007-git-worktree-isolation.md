# ADR-0007: Git worktree isolation for agent runs

- **Status:** Accepted
- **Date:** 2026-07-24
- **Spec refs:** §17 (workspace & git), §5.2 (checkpoint commits), AC-8 (result
  branch), AC-9 (diff/worktree access), §36.14 (never modify primary checkout)

## Context

Agents must never modify the user's primary checkout (rule §36.14, §17.1), yet
they must be able to make checkpoint commits even when push is disabled (§5.2),
and the user must be able to inspect a diff/worktree and accept or reject results
(AC-8, AC-9). Concurrent tasks on the same project need independent branches.

## Decision

Each attempt runs inside a dedicated **Git worktree** created under
`~/.neuroforge/workspaces/<project>/<task>/<work-package>/attempt-<n>/`
(`internal/workspace`), with branch names:

- attempt branch: `forge/<task-id>/<work-package-id>/attempt-<n>`
- final local result: `forge/result/<task-id>`

Properties:

- The user's main checkout is read-only to NeuroForge (repo intelligence may read
  it; nothing writes to it).
- Checkpoint commits are made inside the worktree and **never** land in the user's
  main branch automatically (§5.2).
- "Accept into current branch" (§17.5) is an explicit, user-confirmed, guarded
  operation that checks for conflicting uncommitted changes and offers
  merge/squash/cherry-pick/patch with a backup reference.
- Branch SHA, base SHA and worktree path are persisted (ADR-0003) so the daemon
  can resume and the TUI can reopen them after restart (AC-9, AC-27).

## Consequences

**Positive**

- Strong isolation; parallel tasks on one project don't collide.
- Honest `LOCAL_REVIEW` result (AC-8): code exists only in `forge/result/<task>`.

**Negative / trade-offs**

- Worktrees consume disk; mitigated by `forge cleanup` and explicit lifecycle.

## Alternatives considered

- **Edit the main checkout on a stash/branch.** Rejected: violates §17.1/§36.14
  and risks the user's working tree.
- **Copy-on-write full clones.** Rejected: `git worktree` already shares object
  storage efficiently; clones would be heavier and slower.
