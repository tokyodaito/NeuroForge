# M3 — Workspaces: Closure Report

**Milestone:** M3 — Workspaces (spec §17, §18.4, §21.2, §21.3, §29.2)
**Status:** COMPLETE
**Spec ACs satisfied:** AC-7, AC-8, AC-9, AC-10, AC-28
**Related ADRs:** 0007 (worktree isolation), 0008 (LOCAL_REVIEW security)

## What was implemented

### 1. Git worktree manager (`internal/workspace`)

Creates isolated worktrees under `~/.neuroforge/workspaces/` with branch naming
per §17.3 (`forge/<task-id>/<work-package-id>/attempt-<n>` and
`forge/result/<task-id>`). The user's primary checkout is never modified.

**Safe git runner (AC-7):** every git invocation is validated against a positive
allowlist that structurally excludes `push`, `fetch`, `pull`, `clone`,
`ls-remote`, `send-pack`, `fetch-pack`, and `archive`. LOCAL_REVIEW performs
zero Git network operations by construction.

### 2. Checkpoints (`internal/workspace`, §5.2, §21.3)

Checkpoint commits created inside the attempt branch at the defined moments
(plan, first-diff, compile, tests, screenshot, pre-quota-switch, pre-repair,
pre-integration, manual). Checkpoint commits never auto-merge into the user's
main branch. Records are durable (survive restart).

### 3. Local result branch + review lifecycle (AC-8, AC-9, AC-10)

`forge/result/<task-id>` is created locally pointing at the workspace HEAD. The
CLI/API supports diff, patch export, and review (keep / reject / ask-for-changes).
Reject deletes only the managed worktree and its branches — never user data
outside the managed path.

### 4. Semantic + path leases (`internal/workgraph`, §18.4)

Advisory locks on file paths and the five §18.4 semantic resources
(database_schema, navigation_graph, subscription_contract, design_system,
build_configuration). Conflicts block concurrent work packages.

### 5. Process supervisor (`internal/supervisor`, AC-28)

Runs coding agents with a **positive-allowlist environment** that strips merge
tokens, production credentials, API keys, and the daemon auth token. Enforces
turn limits and a hard timeout. Streams normalized events.

### 6. Continuation packs (§21.2)

Durable artifacts for provider switching and crash recovery.

### 7. Daemon wiring + restart recovery

Workspace service wired to the loopback API. A workspace reconciler verifies
worktree integrity at startup (stale worktrees are marked, never silently
resumed). The fake coding agent is registered by default.

### 8. Doctor orphan detection

`forge doctor` scans the managed workspaces directory for worktrees with no
matching workspace record.

### 9. CLI commands

`forge workspace {create, list, show, run, checkpoint, result, review, diff,
patch, delete, checkpoints}` with `--json`.

## Critical security guarantees verified

| Guarantee | How | Test |
|-----------|-----|------|
| Primary checkout never modified | Worktree isolation + HEAD/file comparison | M3 scenario steps 11-13 |
| No Git network ops (AC-7) | Allowlist-enforced git runner | M3 scenario steps 14a-14b + NoNetwork test |
| Task branch never pushed | No remote configured; allowlist blocks push | NoNetwork test |
| Agent env has no merge creds (AC-28) | Positive-allowlist EnvAllowlist | supervisor_test |
| Crash doesn't lose checkpoint | Durable SQLite records | M3 scenario step 19 (after restart) |
| Restart recovers workspace metadata | Workspace reconciler + durable state | M3 scenario steps 15-17 |
| Reject doesn't delete user data | Only managed worktree path touched | M3 scenario steps 20a-20b |
| All destructive actions audited | Every mutation goes through audit recorder | Audit events verified |

## Tests

- **Unit tests**: workspace manager (12 tests), workgraph leases (5 tests),
  supervisor env + fake agent run (5 tests).
- **Integration tests**: M3 demonstrable scenario (30+ steps through the real
  `forge` binary), AC-7 no-network-ops security test.
- All pass under `go test -race` and `make check`.

## What remains for later milestones

- **Full AC-27** (agent-attempt resume via continuation packs) — M7.
- **TUI result review screen** (interactive diff viewer, accept/reject buttons)
  — the API/CLI surface is complete; the TUI screen is a later enhancement.
- **Accept-into-current-branch** (§17.5 merge/squash/cherry-pick/patch) — M11.
