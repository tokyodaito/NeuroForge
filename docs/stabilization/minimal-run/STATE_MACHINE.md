# STATE_MACHINE.md — Minimal Reliable Run

This document defines the **authoritative** state machines for the `forge run`
track, the legal transitions, the terminal (absorbing) states, and the
restart/reconciliation rules. It overrides the looser behaviour currently in
the code wherever they disagree (the current code allows workspaces to stay
`active` after a run — that is a defect this track removes).

> Notation: `→` is a legal transition. `(terminal)` marks an absorbing state.
> Every transition is persisted atomically with its audit event inside one
> SQLite transaction (spec §11.4, ADR-0003).

---

## 1. Run state machine (the adapter process lifecycle)

This is the lifecycle of a single agent run as observed by the supervisor. It
is derived from the normalized event stream (spec §12.4).

```
            Start()
              │
              ▼
          ┌────────┐  run.started / run.resumed   ┌──────────┐
          │ STARTING │────────────────────────────▶│ RUNNING  │
          └────────┘                                └──────────┘
              │                                          │
              │ spawn error                              │ terminal event
              ▼                                          ▼
          ┌────────┐   ┌────────────┐   ┌────────────────────────┐
          | FAILED  │   | CANCELLED  │   | COMPLETED (process ok) |
          └────────┘   └────────────┘   └────────────────────────┘
            (terminal)   (terminal)            (terminal)
                                                 │
                                                 │ Git inspection + classifier
                                                 ▼
                                  (workspace/task outcome — see §3, §4)
```

### 1.1 Run states

| State        | Meaning                                                      |
|--------------|--------------------------------------------------------------|
| `STARTING`   | Adapter `Start()` has been called; no `run.started` yet.    |
| `RUNNING`    | `run.started`/`run.resumed` observed; awaiting a terminal.   |
| `COMPLETED`  | Terminal `run.completed` observed (process ended cleanly).   |
| `FAILED`     | Terminal `run.failed` observed, or spawn error, or timeout.  |
| `CANCELLED`  | Cancellation accepted; the terminal **must** be `run.cancelled`. |

### 1.2 Legal run transitions

- `STARTING → RUNNING` on `run.started` / `run.resumed`.
- `STARTING → FAILED` on spawn error.
- `RUNNING → COMPLETED` on `run.completed`.
- `RUNNING → FAILED` on `run.failed` or hard timeout (classified
  `timed-out` at the outcome layer).
- `RUNNING → CANCELLED` on accepted cancellation.

### 1.3 Run invariants (invariant I.9 — cancellation precedence)

- **Exactly one** terminal transition is ever recorded per run id. The first
  terminal wins; subsequent events for the same run id are ignored and
  logged-but-not-applied.
- **Cancellation beats failure.** If a cancellation is accepted (the caller
  invoked `Cancel` and the run context is cancelled), then even if a
  `run.failed` event is in flight from the process exit, the recorded terminal
  is `CANCELLED`. This is the root cause of `KF-09` (Gemini race) and is
  fixed by serializing the terminal decision through a single channel/once
  per run.
- **Timeout beats cancellation.** If the hard deadline fired
  (`context.DeadlineExceeded`), the terminal is `FAILED` with class
  `TIMEOUT` (outcome `timed-out`), **not** `CANCELLED`, even if a cancel was
  also requested. (A timeout is a failure mode, not a user action.)
- The terminal decision is made by exactly one goroutine owning the run.

---

## 2. Attempt state machine (one try inside a workspace)

An *attempt* is one execution of the agent against a worktree. In the minimal
run there is exactly one attempt per `forge run`.

```
   ┌─────────┐  worktree created   ┌──────────┐  terminal event + Git inspect
   │ PENDING  │───────────────────▶│ ACTIVE   │──────────────────────────────┐
   └─────────┘                      └──────────┘                               │
                                                                               │
              ┌────────────────────────────────────────────────────────────────┤
              │                                                                │
              ▼                                                                ▼
   ┌──────────────────┐  ┌─────────────────────┐  ┌───────────────────────────┐
   │ COMPLETED        │  │ FAILED              │  │ CANCELLED / TIMED-OUT     │
   │ (with-commit /   │  │ (process / no-change│  │ (user cancel / hard       │
   │  uncommitted)    │  │  / classifier)      │  │  deadline)                │
   └──────────────────┘  └─────────────────────┘  └───────────────────────────┘
        (terminal)              (terminal)                 (terminal)
```

### 2.1 Attempt states

| State        | Meaning                                                       |
|--------------|---------------------------------------------------------------|
| `PENDING`    | Worktree record persisted; `git worktree add` not yet done.  |
| `ACTIVE`     | Worktree exists; agent may run / is running / has finished.  |
| `COMPLETED`  | Terminal adapter event **and** a verified repository result. |
| `FAILED`     | Process failed, or no-change run, or classifier failure.     |
| `CANCELLED`  | User-initiated cancel was the accepted terminal.             |
| `TIMED_OUT`  | Hard deadline fired.                                          |

### 2.2 Attempt invariants

- An attempt is the unit that receives an **outcome** from
  `OUTCOME_CONTRACT.md §1`.
- An attempt never moves from a terminal state back to `ACTIVE` (I.8).
- A restart finds `ACTIVE` attempts with no live process and reconciles them
  to `FAILED` (§5) — it does **not** silently resume them.

---

## 3. Workspace state machine (the durable record)

The workspace is the durable row that owns the worktree path, branch, base
SHA, head SHA and result ref. **The minimal run tightens this machine**: a
workspace may not remain `active` after its run reaches a terminal.

### 3.1 States

| State           | Terminal? | Meaning                                                    |
|-----------------|-----------|------------------------------------------------------------|
| `pending`       | no        | Row persisted, worktree not yet created (pre-creation).    |
| `active`        | no        | Worktree ready / run in progress / run done, not finalized.|
| `completed`     | **yes**   | Run finished with a real, verified repository result.      |
| `failed`        | **yes**   | Run failed, no-change, or interrupted by restart.          |
| `cancelled`     | **yes**   | User cancellation was the accepted terminal.               |
| `timed_out`     | **yes**   | Hard deadline fired.                                       |
| `kept`          | **yes**   | User retained a completed result (review lifecycle).       |
| `rejected`      | **yes**   | User rejected; managed worktree removed.                   |
| `waiting_quota` | no        | Parked (recovery-only; not used by the minimal run).       |
| `quarantined`   | **yes***  | Unrecoverable; human un-quarantines (not used here).       |
| `deleted`       | **yes**   | Worktree removed.                                          |

`*` quarantined is absorbing from the minimal run's point of view.

### 3.2 Legal workspace transitions (minimal run)

```
pending ──worktree add ok──▶ active
pending ──worktree add fail─▶ failed        (terminal)

active ──finalize: outcome completed-*──▶ completed   (terminal)
active ──finalize: outcome failed ──────▶ failed      (terminal)
active ──finalize: outcome cancelled ───▶ cancelled   (terminal)
active ──finalize: outcome timed-out ───▶ timed_out   (terminal)
active ──restart finds dead process────▶ failed       (terminal, §5)

completed ──review keep──▶ kept        (terminal)
completed ──review reject▶ rejected    (terminal)
```

### 3.3 Forbidden transitions (must be rejected)

- **Any terminal → `active`.** A late adapter event, a duplicate `forge run`
  on the same workspace, or a daemon restart must not revive a finalized
  workspace. The API returns `409 Conflict` / a typed error.
- **`deleted` / `rejected` → anything.** These are gone.
- **`pending` → `completed`/`cancelled`/`timed_out` directly** (you must pass
  through `active` — the worktree must exist before a result can be verified).
- **`completed` → `failed`** by a late event. Once `completed`, the workspace
  stays `completed` (or moves to `kept`/`rejected` via review).

> The current code's `allowedRecoveryTransition` (recovery.go) permits
> `* → failed` and `* → active` for recovery. This track **constrains** it:
> terminal states are absorbing; only `waiting_quota`/`quarantined` may
> re-enter `active`, and only via an explicit un-park (out of scope here).

### 3.4 Atomicity

Every transition persists, in one SQLite transaction:
1. the new `state` + `head_sha` + `result_sha` + `result_branch` + timestamps,
2. an audit event describing `{from, to, outcome, run_id, commit_sha}`.

A failure of either aborts the whole transition (spec §11.4, ADR-0003). There
is no "state written, audit forgotten" path.

---

## 4. Task state machine (the backlog entry)

The current M1 task machine only reaches `CANCELLED` as terminal. The minimal
run introduces the runtime terminal states the spec already names
(`COMPLETED`, `FAILED`) and fixes the legal transitions.

### 4.1 States (minimal-run-relevant subset)

| State        | Terminal? | Meaning                                                  |
|--------------|-----------|----------------------------------------------------------|
| `NEW`        | no        | Created, not yet dispatched (current default).           |
| `INGESTED`   | no        | Accepted into the backlog.                               |
| `RUNNING`    | no        | A workspace is active for this task.                     |
| `PAUSED`     | no        | User paused.                                             |
| `COMPLETED`  | **yes**   | The run produced a verified result.                      |
| `FAILED`     | **yes**   | The run failed, no-change, or was interrupted.           |
| `CANCELLED`  | **yes**   | User cancelled the task/run.                             |

### 4.2 Legal task transitions (minimal run)

```
NEW ──forge run dispatch──▶ RUNNING
INGESTED ──forge run────▶ RUNNING
RUNNING ──outcome completed-*──▶ COMPLETED   (terminal)
RUNNING ──outcome failed ──────▶ FAILED      (terminal)
RUNNING ──outcome timed-out ───▶ FAILED      (terminal; reason=timed_out)
RUNNING ──user cancel──────────▶ CANCELLED   (terminal)
NEW / INGESTED / RUNNING / PAUSED ──user cancel──▶ CANCELLED   (terminal)
```

### 4.3 Task invariants

- A task whose workspace is terminal is itself terminal (same outcome).
- `NEW` is **not** terminal — but a `NEW` task whose run ended with no changes
  moves to `FAILED`, not back to `NEW` (this fixes `KF-06`).
- A terminal task cannot be revived by a late event; only by an explicit
  user retry (a *new* workspace / new run id).

---

## 5. Restart / reconciliation rules (AC-27)

The startup reconciler runs before the daemon binds its listener
(already the case in `internal/daemon/daemon.go`). For the minimal run:

### 5.1 `active` workspace on restart
- The recorded `run_id` is stale (the process died with the daemon).
- Default decision: transition `active → failed` with reason
  `"interrupted by daemon restart"`. **Never** silently resume.
- If a continuation pack + checkpoint exist, the workspace is still marked
  `failed` (not revived) but the durable pack survives for an explicit
  re-run. (The minimal run does not auto-resume.)
- This decision is audited and idempotent (re-reconciling the same workspace
  is a no-op because it is now terminal).

### 5.2 Terminal workspace on restart
- A `completed` / `failed` / `cancelled` / `timed_out` workspace is **left
  alone**. The reconciler must not change its state.
- The result ref (`refs/heads/forge/result/<task-id>`) and `result_sha` must
  still be readable.

### 5.3 Worktree integrity
- If a terminal workspace's worktree directory has vanished, the workspace
  row stays terminal and an audit warning is recorded. It is **not** silently
  recreated.
- If an `active` workspace's worktree has vanished, it transitions to
  `failed` with reason `"worktree missing after restart"`.

### 5.4 Idempotency
- Running the reconciler twice yields the same durable state. A reconciler
  decision is recorded as an audit event and not re-applied.

---

## 6. Cross-machine contract

A single `forge run` drives the three machines together:

```
task:        NEW ──run──▶ RUNNING ──finalize──▶ COMPLETED | FAILED | CANCELLED
                                │
workspace:   (create) pending─▶active ──finalize──▶ completed|failed|cancelled|timed_out
                                          │
attempt/run:           STARTING─▶RUNNING ──terminal──▶ COMPLETED|FAILED|CANCELLED
```

The **finalize** step is the single chokepoint where:
1. the run terminal is observed,
2. Git is inspected (FR-9),
3. the outcome is classified (OUTCOME_CONTRACT.md §1),
4. workspace + task states are persisted atomically (§3.4, §4),
5. the result ref is created/updated when applicable (FR-14),
6. the CLI result + exit code are derived.

`finalize` is **idempotent**: a second finalize on the same workspace is a
no-op that returns the already-recorded outcome (this is tested explicitly —
`TEST_PLAN.md §2`).
