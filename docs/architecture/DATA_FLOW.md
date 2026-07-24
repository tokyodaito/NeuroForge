# Data flow

How information moves through NeuroForge (spec §3, §9–§22). Companion to
[`COMPONENTS.md`](COMPONENTS.md). The spec is authoritative.

## 1. Control flow: command → daemon → adapters

```
User
 │  TUI (internal/tui) ── or ── CLI (internal/cli)
 ▼
loopback HTTP+JSON / SSE  (internal/transport)        ← random local token, loopback only
 │
 ▼
Forge Daemon (internal/daemon)
 ├─ project/task/workgraph  (planning)
 ├─ scheduler ──► router ──► quota/budget            (selection)
 ├─ workspace (git worktree)                         (isolation)
 ├─ supervisor ──► adapter/codingagent               (execution)
 ├─ testengine / visual / review                     (verification)
 └─ merge ──► adapter/vcs                            (delivery, gated)
Durable state: internal/storage (SQLite WAL)         (every step persisted)
Audit: internal/audit                                 (append-only)
```

Two hard invariants:

- The daemon **persists state before every external action** (spec §11.4), so it
  can resume or safely restart any attempt.
- Adapter (agent) processes **never** receive merge credentials or the daemon auth
  token (spec §28, §29.2). Only `adapter/vcs` performs network delivery, and only
  when the Merge Governor authorizes it.

## 2. Task lifecycle data flow (spec §9, §15, §18)

```
free-form text + attachments
   │  (§9.5 content-addressed artifacts; §9.6 redaction before any provider upload)
   ▼
Task Compiler (internal/task)
   │  objective · ACs · non-goals · assumptions · risk · complexity · proposed scope
   ▼
Work Graph (internal/workgraph)  ── DAG of work packages + semantic leases (§18.3–§18.4)
   │
   ▼ per work package
Scheduler (internal/scheduler)
   │  picks next runnable package, respects leases/parallelism/budget
   ▼
Router (internal/router)
   │  route = engine + model_tier + account + runtime   (never hard-coded names, §19)
   ▼
Workspace (internal/workspace)
   │  forge/<task>/<wp>/attempt-<n>   (user's main checkout untouched, §17)
   ▼
Supervisor (internal/supervisor) ──► adapter/codingagent
   │  normalized events (§12.4): run.started…file.changed…usage.updated…run.completed
   ▼
Checkpoints (§21.3) stored in storage; continuation pack on provider switch (§21.2)
   │
   ▼
Verification pipeline (progressive, §24.3)
   syntax → compile changed module → targeted tests → module tests → full verification
   (+ design/visual verification for UI tasks, §15–§16)
   ▼
Review Engine (§25) + Risk (§26) ──► findings → Merge Governor (§28)
   ▼
Merge Governor (internal/merge)  ── deterministic ALLOW_* / POLICY_BLOCKED / QUARANTINE
   ▼
adapter/vcs (push / PR / MR / merge) — only when authorized
   ▼
Post-Merge Sentinel (M12) ──► auto-revert on regression
```

## 3. Token / context flow (spec §22)

- **Never** dump the whole repository into a prompt (§22.1).
- `internal/repoinfo` builds a repo index (git, file tree, symbols, imports, build
  & test graph, SQLite FTS, related-change history).
- A **Context Pack** (§22.3) carries: specification, allowed scope, repo map,
  relevant files, architecture rules, commands, recent failures, artifact refs.
- Logs are **sliced** (§22.4): exit code, failing command, first error, relevant
  stack trace, summary of the rest, path to the full log.
- Repair is **delta** (§22.5): finding + current diff + failing test + needed
  files — never the full research history again.

## 4. Quota & budget feedback loop (spec §20, §23)

```
adapter ── quota snapshot (confidence EXACT…UNKNOWN) ──► quota (internal/quota)
                                                            │ circuit breaker CLOSED/OPEN/HALF_OPEN
                                                            ▼
                                             router avoids exhausted accounts
usage events ──► budget (internal/budget)
                   │ soft limit → cheaper route / fewer design variants
                   │ hard limit → block new paid runs → task BUDGET_EXCEEDED
                   ▼
                dashboard (exact vs ~estimated vs unknown, §20.1, AC-18)
```

Rate limits use `retry-after` with jitter and do **not** exhaust the account;
auth failures stop automatic retry (§20.3).

## 5. Local-review isolation (spec §3.2, AC-7, AC-8)

In `LOCAL_REVIEW` the entire `adapter/vcs` network path is unreachable by policy:

```
work result ──► forge/result/<task-id> (local branch only)
   • push: denied   • PR/MR: denied   • merge: denied
   user reviews diff/worktree → accept | reject | ask-for-changes
```

No code, branch, patch or task description leaves the machine; only the context
explicitly allowed by the project's attachment policy may reach a chosen model
(spec §3.2, §9.6).

## 6. Events → TUI

```
supervisor/visual/etc. ── normalized events ──► daemon ──► transport (SSE) ──► TUI
```

The TUI never polls adapters directly; it consumes the daemon's event stream over
the loopback transport. Image previews degrade gracefully when the terminal lacks
image support (§6.7).
