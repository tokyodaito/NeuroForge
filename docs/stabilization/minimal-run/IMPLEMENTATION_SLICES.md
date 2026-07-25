# IMPLEMENTATION_SLICES.md — Minimal Reliable Run

This is the **sequencing plan** for the implementing agent. Each slice is
small, independently reviewable, and lands green (`make check` clean, new
tests green, no regressions). The slices are ordered so that later slices
depend only on already-reviewed earlier ones.

> **Hard rules for every slice (from AGENTS.md / REQUIREMENTS.md):**
> - No production-code changes outside the slice's **Allowed paths**.
> - No deletion of existing subsystems (NFR-7). Bypass, don't remove.
> - No schema downgrade; new storage is a forward-only idempotent migration.
> - No push, no PR, no merge into `main`. Local commits only.
> - `make check` green after every slice; `gofmt`/`vet`/`git diff --check`
>   clean.
> - A slice is **done** only when its Acceptance rows move to `PASS` **and**
>   an independent reviewer signs off (Gate E).
>
> The implementing agent must read `REQUIREMENTS.md`, `STATE_MACHINE.md`,
> `OUTCOME_CONTRACT.md`, `TEST_PLAN.md`, `ACCEPTANCE_MATRIX.md`, and
> `KNOWN_FAILURES.md` before starting slice 1.

## Slice dependency graph

```
S1 Git inspection ─┐
                   ├─▶ S2 Classifier ─▶ S3 Atomic persist ─▶ S4 Idempotent finalize ─▶ S7 RunApp ─▶ S8 forge run ─▶┐
S5 Result refs ────┘─────────────────────────────────────────────────────────────────────▶ S7 RunApp ─▶ S8 ─▶├─▶ S11 Reliability ─▶ S12 Smoke
S6 Cancel precedence ───────────────────────────────────────────────────────────────────────────────▶ S8 ─▶──┘
S9 Daemon autostart ─────────────────────────────────────────────────────────────────────────────────▶ S8 ─▶
S10 Black-box fixture ───────────────────────────────────────────────────────────────────────────────▶ S11 ─▶
```

Slices S1–S6 are the correctness core. S7 composes them. S8 is the user
surface. S9–S12 are verification.

---

## S1 — Post-run Git inspection

- **Goal:** a pure, tested helper that reads the *actual* worktree state
  after a run (FR-9, FR-10, I.2). Fixes KF-02, KF-04.
- **Entry conditions:** none (first slice).
- **Exact result:**
  - A function (e.g. `workspace.InspectWorktree(ctx, ws) (Inspection, error)`)
    that returns `{ ActualHEAD, StatusPorcelain, DiffStat, ChangedFiles }`
    read via the allowlisted git runner from the worktree path.
  - It reads `git rev-parse HEAD`, `git status --porcelain`,
    `git diff --stat <base>...HEAD`, and `git diff --stat`. It **never**
    trusts the cached `ws.HeadSHA`.
- **Allowed paths:** `internal/workspace/` (+ its `_test.go`). May add a
  forward-only migration **only if** a new column is needed (prefer reusing
  existing columns).
- **Forbidden:** CLI, daemon, transport, adapters, scheduler, any other pkg.
- **Tests:** U-01 (table-driven over commit / uncommitted / clean).
- **Review criteria:** reviewer confirms (a) the cached `head_sha` is
  demonstrably ignored, (b) only allowlisted git subcommands are used, (c)
  error paths (missing worktree) are handled and surface a classified error.

## S2 — Outcome classifier

- **Goal:** a pure function implementing OUTCOME_CONTRACT.md §1.2 (FR-11,
  I.1, I.3). Fixes KF-01, KF-05.
- **Entry conditions:** S1 landed (provides the inspection shape the
  classifier consumes).
- **Exact result:**
  - A new pure type+function, e.g. `runapp.Classify(terminal, baseSHA, ins Inspection) Outcome`.
  - Lives in a **new** package `internal/runapp/` (the thin application
    service that will compose S3–S7). Pure domain logic only; no daemon/CLI
    imports.
- **Allowed paths:** `internal/runapp/` (new package).
- **Forbidden:** touching existing packages; I/O; time; randomness (must be
  deterministic — NFR-2).
- **Tests:** U-02 — exhaustive over the §1.1 table.
- **Review criteria:** every cell of OUTCOME_CONTRACT.md §1.1 covered; the
  "process completed, no changes" case is `completed-no-changes`, not a
  success.

## S3 — Atomic persistence of terminal states

- **Goal:** the finalize step writes workspace+task terminal state and the
  audit event in **one** SQLite tx (FR-12, FR-13, STATE_MACHINE.md §3.4,
  §4.2). Fixes KF-03, KF-06.
- **Entry conditions:** S1, S2.
- **Exact result:**
  - A method (e.g. `runapp.Finalize(ctx, wsID, decision) (Outcome, error)`)
    that opens a `storage.Tx`, writes workspace state (incl. `head_sha` from
    S1, `result_sha`/`result_branch` when applicable), writes task state, and
    appends the `run.outcome_decided` audit event — all in the same tx.
  - Enforces the STATE_MACHINE.md transition table: an illegal transition
    (e.g. terminal→active) returns a typed error and the tx is rolled back.
- **Allowed paths:** `internal/runapp/`, `internal/workspace/recovery.go`
  (constrain `allowedRecoveryTransition`), `internal/task/state.go` (add
  runtime terminal states + transitions). A forward-only migration only if a
  column is missing.
- **Forbidden:** CLI, transport, adapters, daemon composition root.
- **Tests:** U-03 (atomicity/rollback), U-11 (illegal transitions rejected),
  U-12 (reconciler keeps terminal).
- **Review criteria:** reviewer confirms the tx spans state+audit+task;
  `allowedRecoveryTransition` no longer permits terminal→active; no
  "state written, audit forgotten" path exists.

## S4 — Idempotent finalization

- **Goal:** calling finalize twice is a no-op that returns the recorded
  outcome (OUTCOME_CONTRACT.md §6, FR-14). Fixes duplicate-event bugs.
- **Entry conditions:** S3.
- **Exact result:**
  - `Finalize` detects an already-terminal workspace and short-circuits,
    returning the stored outcome. It does not create a second result ref or a
    second `run.outcome_decided` event (at most one dedup notice).
- **Allowed paths:** `internal/runapp/`.
- **Forbidden:** changing the public transport/CLI surface.
- **Tests:** U-04 (double finalize is a no-op).
- **Review criteria:** reviewer confirms no duplicate ref, no duplicate audit
  decision, identical return value.

## S5 — Correct result refs

- **Goal:** result ref is always created at the fully-qualified
  `refs/heads/forge/result/<task-id>` and is idempotent (FR-14, I.7, I.5).
  Fixes KF-07, KF-08.
- **Entry conditions:** S3.
- **Exact result:**
  - Update/extract a helper that uses `git update-ref refs/heads/forge/result/<task-id> <sha>`
    (full ref form) — never a short name. Idempotent: re-running moves the
    ref to the current HEAD. No deletion of pre-existing non-standard refs.
  - The ref is created during finalize for `completed-with-commit` and
    `completed-with-uncommitted-changes`.
- **Allowed paths:** `internal/workspace/result.go`, `internal/runapp/`.
- **Forbidden:** any push/fetch path; any code that resolves short ref names.
- **Tests:** U-05 (full ref form + idempotent), and a check that
  `git for-each-ref refs/heads/forge/result/<task>` resolves.
- **Review criteria:** reviewer confirms the full ref form is used in the
  actual git invocation; no remote mutation; existing `forge/result/<task>`
  short-name behaviour is migrated, not silently deleted.

## S6 — Cancellation precedence

- **Goal:** once `run.cancelled`, a late `run.failed` cannot overwrite it;
  timeout classifies as `timed-out`, not `cancelled`; race-clean (FR-15, I.9,
  STATE_MACHINE.md §1.3). Fixes KF-09.
- **Entry conditions:** none (independent of S1–S5; can be done in parallel).
- **Exact result:**
  - The supervisor's terminal decision is serialized through a single
    owner/once per run id. The Gemini `supervise` select is fixed so a
    concurrent process-EOF does not produce `run.completed`/`run.failed`
    when cancellation was accepted. Timeout
    (`context.DeadlineExceeded`) ⇒ `run.failed`+TIMEOUT ⇒ outcome
    `timed-out`.
  - `Cancel` is idempotent.
- **Allowed paths:** `internal/supervisor/`, `internal/adapter/codingagent/gemini/run.go`
  (and any sibling adapter with the same race — audit all six). Pure
  classifier tests in `internal/runapp/`.
- **Forbidden:** adapter protocol changes (`internal/adapter/codingagent/protocol/`
  is frozen); CLI.
- **Tests:** U-06 (cancel beats fail), U-07 (timeout ≠ cancel), U-15
  (gemini cancel × 20 under `-race`).
- **Review criteria:** reviewer runs U-15 and confirms 0 races / 0
  misclassifications; reviewer reads the six adapter `supervise` loops and
  confirms none can synthesize a non-cancelled terminal after cancel.

## S7 — Minimal run application service

- **Goal:** compose S1–S6 into a single `runapp.Run(ctx, Request) (Result, error)`
  that drives one attempt end-to-end without touching CLI/transport (FR-5,
  FR-8). This is the **only** place the full sequence lives.
- **Entry conditions:** S1–S6 all landed and reviewed.
- **Exact result:**
  - A service that, given a resolved repo + description + engine + model:
    1. (caller provides workspace + run info) runs the adapter via the
       supervisor,
    2. waits for the single terminal event,
    3. runs S1 inspection,
    4. runs S2 classifier,
    5. runs S3/S4 finalize (creating S5 ref when applicable),
    6. persists usage events for the run (this also fixes KF-10 — usage from
       a production adapter is recorded in the DB, reusing the scheduler's
       `UsageSink` shape),
    7. returns a structured `Result` consumed by the CLI.
  - Reuses existing `workspace.Manager`, `supervisor.Supervisor`,
    `task.Backlog`, `audit.Recorder`. Does **not** import the scheduler,
    failover, postmerge, review or merge packages.
- **Allowed paths:** `internal/runapp/`, `internal/daemon/` (a thin wiring
  helper exposing it on the daemon if needed).
- **Forbidden:** changing adapter protocol; TUI; scheduler/failover/postmerge
  packages (bypass only).
- **Tests:** integration U-08 (prompt), U-09 (model), U-10 (isolation),
  plus an integration test that drives Run end-to-end against the in-process
  fake adapter for each outcome.
- **Review criteria:** reviewer confirms no imports of the out-of-scope
  subsystems; the service is the single owner of the finalize sequence.

## S8 — `forge run` CLI

- **Goal:** the user-facing command (REQUIREMENTS.md §1, FR-1, FR-18).
  Fixes KF-11, KF-12.
- **Entry conditions:** S7, S9.
- **Exact result:**
  - `case "run":` in `internal/cli/cli.go` dispatching to a new
    `run_cmd.go`.
  - Flag parsing per REQUIREMENTS.md §1.1; validation per §1.2 (exit 2, no
    state created).
  - Human output per OUTCOME_CONTRACT.md §2 (progress to **stderr**, result
    to stdout; `--json` = exactly one document to stdout).
  - Exit codes per OUTCOME_CONTRACT.md §4.
- **Allowed paths:** `internal/cli/` (new `run_cmd*.go`), `cmd/forge/main.go`
  only if strictly required for wiring (prefer not).
- **Forbidden:** transport DTO reshapes that break other commands; TUI.
- **Tests:** B-01..B-08 (black-box).
- **Review criteria:** reviewer confirms exit codes; reviewer confirms
  `--json` stdout is a single document (B-07); reviewer confirms a validation
  failure creates no rows (B-08).

## S9 — Daemon autostart under `forge run`

- **Goal:** `forge run` finds/spawns/reuses a daemon safely (FR-2, §3 of
  REQUIREMENTS.md). Mostly exists today; this slice wires it and closes the
  dual-spawn race.
- **Entry conditions:** none (independent; can run in parallel with S1–S6).
- **Exact result:**
  - `forge run` uses `ensureDaemon` before dispatching.
  - A guard closes the two-CLIs-spawn race (e.g. a short file lock around
    `daemon.Start`, or a post-spawn health re-check that aborts the second
    spawn).
  - Actionable error + exit 3 when the daemon does not become ready.
- **Allowed paths:** `internal/cli/daemon_connect.go`,
  `internal/daemon/lifecycle.go`.
- **Forbidden:** changing the daemon's internal composition root.
- **Tests:** B-09 (cold + warm), B-10 (stale pid), B-11 (no dual).
- **Review criteria:** reviewer reproduces B-11 (two concurrent cold runs →
  one pid); reviewer confirms exit 3 on a forced startup failure.

## S10 — Black-box fixture

- **Goal:** a deterministic, offline fake adapter fixture usable by the
  black-box and reliability tests (FR-20, NFR-4). Reuses
  `cmd/fake-coding-agent` + the fake adapter scenarios.
- **Entry conditions:** S7 (so the fixture maps onto the real run path).
- **Exact result:**
  - A test helper that builds `forge`, registers/uses a fake adapter
    (in-process) or a fake executable, and can script: write+commit,
    write-no-commit, no-change, fail, block-until-cancelled.
  - The fixture is selectable from black-box tests via the existing engine
    id `fake` (no production adapter is invoked).
- **Allowed paths:** `internal/cli/` (test helpers), possibly
  `internal/adapter/codingagent/fake/` for a new scenario if missing.
- **Forbidden:** production code paths that bypass the real supervisor.
- **Tests:** the fixture is exercised by S11; a self-test asserts each
  scenario produces the expected worktree state.
- **Review criteria:** reviewer confirms the fixture drives the **real**
  supervisor + workspace path (not a mock of the result); reviewer confirms
  no paid model is reachable.

## S11 — Ten-run reliability suite

- **Goal:** Gate C — automated reliability (TEST_PLAN.md §5).
- **Entry conditions:** S8, S10.
- **Exact result:**
  - `TestForgeRun_Reliability_10x` in `internal/cli/` (black-box, `-race`).
  - 10 sequential green iterations; dumps §7 artifacts on failure.
- **Allowed paths:** `internal/cli/` (test file).
- **Forbidden:** production code changes (test-only slice).
- **Tests:** itself + all of §2–§4 green.
- **Review criteria:** reviewer runs it locally; confirms 0 stale active
  workspaces, 0 head-sha mismatches, 0 dual daemons, 0 network ops.

## S12 — Real OpenCode smoke validation

- **Goal:** Gate D — one real, opt-in run (TEST_PLAN.md §6).
- **Entry conditions:** S8 (and a real `opencode` CLI installed locally).
- **Exact result:**
  - `internal/cli/run_smoke_test.go` guarded by
    `NEUROFORGE_SMOKE=opencode` (skipped otherwise).
  - Runs `forge run "Create RESULT.md ... and make a local git commit"`
    against a real model, asserts the real-adapter invariants.
- **Allowed paths:** `internal/cli/` (test file only).
- **Forbidden:** running in the default suite; CI enabling it.
- **Tests:** itself (opt-in).
- **Review criteria:** reviewer runs it once manually; confirms real commit,
  terminal workspace, clean primary, no push/PR/merge.

---

## Rollback strategy

- Every slice is a small, focused commit (or short stack). If a slice breaks
  `make check` and cannot be quickly fixed, it is reverted as a unit; the
  previous reviewed slice is the restore point.
- Because S1–S6 are additive (new `internal/runapp/` package, new helpers in
  `internal/workspace/`), reverting a slice does not destabilize existing
  milestones — the existing `forge workspace run` / `forge task dispatch`
  paths keep working until S8 supersedes them for the `forge run` scenario.
- Schema changes are forward-only; a slice that adds a migration must also
  keep older code paths functional against the migrated DB (no "broken on
  rollback" columns).

## Review after each slice

After **every** slice the implementing agent updates `ACCEPTANCE_MATRIX.md`
rows from `NOT IMPLEMENTED` → `PARTIAL`/`PASS`, and the independent reviewer
(Gate E, per-slice) either accepts or rejects. No slice may be considered
done by its author; only the reviewer signs off (rule: do not trust the
coding-agent's own PASS).
