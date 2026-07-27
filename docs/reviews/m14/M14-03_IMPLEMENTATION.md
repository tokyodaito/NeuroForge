# M14-03 — Implementation Report

**Task:** M14-03 — Task Compiler production API, CLI and restart flow.
**Implementer actor:** `M14-03-impl-session`.
**Verdict:** `BLOCKED`

## SHAs

- **Starting SHA:** `02726b89949fbe160cb35b0c70a2669bb3c0ed18` (branch `main`, current
  tip — the M14-02 re-review commit `M14-02: re-review remediated task compiler`).
- **Candidate SHA:** this report's commit (no production code change; the task is
  blocked at the precondition gate — see below). Resolve with
  `git log --format=%H -G '^M14-03: BLOCKED'`.

## Goal and actual scope

**Goal (from task brief):** wire the Task Compiler (produced by M14-02) into the
daemon application service (compile / get / lock / version), the TUI ↔ daemon
transport, the user-facing CLI (`compile` / `show` / `lock`), task-creation
integration, audit events, and the daemon-restart persistence path. Mandatory
acceptance criteria require that the user can obtain a compiled specification
through the real CLI/API, that the result survives a daemon restart, that a
repeat request is idempotent, and that the evidence is black-box (unit-only
wiring does not count).

**Actual scope delivered:** **none.** No production code, no tests, no CLI
surface, no transport endpoint, no daemon wiring, no migration, no audit
plumbing. The task was stopped at the precondition check because the declared
predecessor (M14-02) is not in the `ACCEPTED` state that the engineering
baseline and the task brief require before a successor may start. The brief is
unambiguous:

> *"Убедись, что предыдущая задача имеет verdict `ACCEPTED`. Если нет — поставь
> `BLOCKED`, ничего не реализуй."*

That condition fails on `main`; nothing was implemented.

## Precondition check — predecessor gate

Engineering baseline
([`docs/engineering/ENGINEERING_BASELINE.md`](../../engineering/ENGINEERING_BASELINE.md))
is mandatory per `AGENTS.md`. It establishes the gate:

- *"… and no successor task may start until its predecessor is `ACCEPTED`."*
- *"A successor task may start **only** when its declared predecessor is
  `ACCEPTED`."*
- *"`forge gate next` … exit 0 only if the predecessor task is genuinely
  `ACCEPTED`."*

The task brief restates the same gate as the first working step.

### State of M14-02 (the declared predecessor of M14-03)

| Artifact                                          | Present on `main` (HEAD `02726b8`)? | Verdict / state                                                                                             |
|---------------------------------------------------|-------------------------------------|-------------------------------------------------------------------------------------------------------------|
| `docs/reviews/m14/M14-02_IMPLEMENTATION.md`       | yes                                 | `IMPLEMENTED_TESTED` (header line 5)                                                                        |
| `docs/reviews/m14/M14-02_REVIEW.md`               | yes                                 | `CHANGES_REQUESTED` (original review); `REVIEW_APPROVED` (re-review after remediation, line 377)            |
| `docs/reviews/m14/M14-02_ACCEPTANCE.md`           | **no**                              | —                                                                                                           |
| `docs/reviews/m14/M14-02.manifest.json`           | **no**                              | —                                                                                                           |

M14-02 is therefore at `REVIEW_APPROVED` on `main`. It has **not** transitioned
to `ACCEPTED`. The lifecycle mandated by the baseline is
`STARTED → IMPLEMENTED_TESTED → REVIEW_APPROVED → ACCEPTED`; the final
`REVIEW_APPROVED → ACCEPTED` transition requires an independent acceptor and a
manifest that passes `forge gate validate`. Neither exists on `main`.

### Why a worktree branch does not unblock this task

A separate acceptance session is in progress in a distinct git worktree:

```
$ git worktree list
/Users/bogdan/Projects/neuroforge                                                   02726b8 [main]
/private/var/folders/…/opencode/m14-02-accept-wt                                    bf714b6 [m14-02-accept]
…
```

That branch (`m14-02-accept` at `bf714b6`, *"M14-02: add acceptance evidence"*)
does add `M14-02.manifest.json` and `M14-02_ACCEPTANCE.md` — but:

- `bf714b6` is **not** an ancestor of `main`:

  ```
  $ git merge-base --is-ancestor bf714b6 main && echo yes || echo no
  no
  ```

- The acceptance worktree's own re-review report explicitly reserves the
  acceptance step for a separate session: *"A separate acceptance session
  (distinct from this re-review) may now move M14-02 from `REVIEW_APPROVED` to
  `ACCEPTED`. This re-review session does not perform acceptance."*

- Per engineering-baseline actor separation, the acceptor must be independent
  of the implementer, reviewer, and re-reviewer. An implementation agent
  starting M14-03 is not the acceptor of M14-02 and may not self-serve the
  acceptance transition.

- Baseline rule 14 forbids the implementation agent from performing the merge
  even if it were ready: *"Не выполняй push, PR, MR или merge, если это прямо
  не требуется текущей подзадачей."* M14-03's job description does not include
  merging M14-02.

- Baseline rule 15 forbids touching another session's worktree/branch:
  *"Не удаляй чужие branches/worktrees/artifacts."*

Therefore the gate remains closed from the perspective of the only history that
matters for this task: `main` at the starting SHA.

### Compiled-binary gate enforcement

`forge gate next` checks the predecessor by reading `predecessor_task_id` from
the successor's manifest. No `M14-03.manifest.json` exists (it cannot exist
until M14-03 is implemented and reviewed). The only available manifest for the
predecessor chain is `M14-01.manifest.json`, which proves that M14-01's
predecessor (M14-00) is `ACCEPTED` — that is the wrong check for M14-03:

```
$ ./forge gate baseline
active schema_version: 1
active baseline_version: 1
baseline document: docs/engineering/ENGINEERING_BASELINE.md

$ ./forge gate next --manifest docs/reviews/m14/M14-01.manifest.json
OK: predecessor "M14-01" is ACCEPTED; successor task may start   # exit 0
```

This exit-0 result is irrelevant: it is the gate for M14-02 starting, not for
M14-03. There is no manifest on `main` whose `predecessor_task_id` is `M14-02`,
because M14-02 has not been accepted on `main`. The structural absence of
`docs/reviews/m14/M14-02.manifest.json` on `main` is itself the gate proof: the
successor cannot start.

## What was verified (read-only) before stopping

To confirm this is a true gate failure and not a naming artefact, the following
read-only checks were performed against the repository at the starting SHA:

1. `git rev-parse --abbrev-ref HEAD` → `main`.
2. `git rev-parse HEAD` → `02726b89949fbe160cb35b0c70a2669bb3c0ed18`.
3. `git status --short` → only pre-existing unrelated review docs
   (`docs/reviews/MINIMAL_RUN_*`, `docs/reviews/M12_M13_REVIEW.md`); they
   predate this task and are untouched, identical to the situation in M14-00,
   M14-01, and M14-02.
4. `ls docs/reviews/m14/M14-02*` → only `M14-02_IMPLEMENTATION.md` and
   `M14-02_REVIEW.md` exist; **no** `M14-02_ACCEPTANCE.md`, **no**
   `M14-02.manifest.json`.
5. `grep -n '^\*\*Verdict' docs/reviews/m14/M14-02_*.md` →
   `IMPLEMENTATION` is `IMPLEMENTED_TESTED`; `REVIEW` is `CHANGES_REQUESTED`
   (original) and `REVIEW_APPROVED` (re-review). Neither is `ACCEPTED`.
6. `git log --oneline --all -- docs/reviews/m14/M14-02.manifest.json` → the
   only commit that adds the manifest (`bf714b6`) is on branch
   `m14-02-accept` and is **not** an ancestor of `main`.
7. `git merge-base --is-ancestor bf714b6 main` → exit non-zero (`no`).
8. `./forge gate baseline` → active baseline version 1 (the validator under
   which M14-02's missing `REVIEW_APPROVED → ACCEPTED` transition would be
   judged).
9. `./forge gate next --manifest docs/reviews/m14/M14-01.manifest.json`
   → exit 0 (proves only that M14-01 → M14-02 is gated correctly; does **not**
   speak to M14-02 → M14-03).

## Acceptance criterion matrix (M14-03)

| Mandatory AC | Status | Reason |
|---|---|---|
| User can obtain a compiled specification via real CLI/API | **not pursued** | Predecessor gate closed; no production code may be written this session. |
| Result survives daemon restart | **not pursued** | Same. |
| Repeat request is idempotent | **not pursued** | Same. |
| Evidence is black-box (unit-only wiring insufficient) | **not pursued** | Same. |

No mapping to code or tests is possible because no code was written.

## Required test plan

| Required test class | Status |
|---|---|
| Transport contract tests | not pursued |
| Black-box compiled binary: create → compile → show → lock → daemon restart → show | not pursued |
| Invalid task, locked update, duplicate request cases | not pursued |
| `make check` and race tests | not pursued (no change made; baseline remains as it was at the starting SHA) |

## Files changed by this task

None in production code, tests, CLI, transport, daemon, or migrations. This
report is the only artifact added:

```
docs/reviews/m14/M14-03_IMPLEMENTATION.md   (new — this file)
```

No changes to `docs/spec/NEUROFORGE_SPEC.md`, `internal/daemon`, `internal/cli`,
`internal/transport`, `internal/task`, `internal/storage`, `internal/scheduler`,
`internal/policy`, `internal/merge`, or any baseline/gate enforcement. No
security, autonomy, delivery, or merge-policy invariant was weakened (none was
touched). Product spec untouched.

## Commands executed

All read-only; no mutation of the repository's tracked state occurred.

| Command | Exit | Result |
|---|---:|---|
| `git rev-parse --abbrev-ref HEAD` | 0 | `main` |
| `git rev-parse HEAD` | 0 | `02726b89949fbe160cb35b0c70a2669bb3c0ed18` |
| `git status --short` | 0 | only pre-existing unrelated review docs |
| `ls docs/reviews/m14/M14-02*` | 0 | `IMPLEMENTATION` + `REVIEW` only |
| `ls docs/reviews/m14/M14-02.manifest.json` | 2 | No such file |
| `ls docs/reviews/m14/M14-02_ACCEPTANCE.md` | 2 | No such file |
| `git log --oneline --all -- docs/reviews/m14/M14-02.manifest.json` | 0 | only `bf714b6` (branch `m14-02-accept`, not on `main`) |
| `git merge-base --is-ancestor bf714b6 main` | 1 | not an ancestor |
| `make build` | 0 | `./forge` builds cleanly from the starting SHA |
| `./forge gate baseline` | 0 | baseline version 1 |
| `./forge gate next --manifest docs/reviews/m14/M14-01.manifest.json` | 0 | M14-00 ACCEPTED (wrong gate for M14-03; for completeness only) |

No targeted, race, or black-box M14-03 tests were executed because no
M14-03 code exists. `make check` was not re-run as part of the task work
because no source change was made; the working tree's only staged change is
this document.

## Black-box evidence

None. The task is blocked at the precondition gate; there is no M14-03
production path to drive through the compiled `forge` binary.

## Known limitations

The limitation is the block itself: M14-03 cannot begin until an independent
acceptor moves M14-02 from `REVIEW_APPROVED` to `ACCEPTED` on `main` and
commits `docs/reviews/m14/M14-02.manifest.json` and
`docs/reviews/m14/M14-02_ACCEPTANCE.md` (or until the existing `m14-02-accept`
worktree's acceptance is reviewed and merged by whoever owns that flow).

## Follow-up problems

- **FU-M14-03-1 (blocking):** Accept M14-02 on `main` (independent acceptor;
  commit `M14-02.manifest.json` + `M14-02_ACCEPTANCE.md`). Once
  `forge gate next --manifest docs/reviews/m14/M14-02.manifest.json` returns
  exit 0, M14-03 may restart and the scope described in the task brief becomes
  actionable. This is the only unblock; it is outside this implementation
  agent's actor role and outside this task's allowed scope.
- **FU-M14-03-2 (informational):** When M14-03 starts in earnest, the
  implementation report should be rewritten from this BLOCKED stub into a full
  `IMPLEMENTED_TESTED` report covering the AC → code → test mapping, transport
  contract tests, the create → compile → show → lock → restart → show black-box
  scenario, and the invalid-task / locked-update / duplicate-request cases.

No neighbouring defects were investigated (none may be under baseline rule 4
when the gate is closed).

## Verdict

`BLOCKED`

Rationale:

- The declared predecessor `M14-02` is at `REVIEW_APPROVED` on `main`
  (`docs/reviews/m14/M14-02_REVIEW.md`, re-review section, line 377). The
  engineering-baseline lifecycle requires `ACCEPTED` before any successor task
  may start.
- The M14-02 acceptance artifacts (`M14-02_ACCEPTANCE.md`,
  `M14-02.manifest.json`) do not exist on `main`. They exist only on a separate
  unmerged worktree branch (`m14-02-accept` at `bf714b6`), which this
  implementation agent may neither merge (baseline rule 14) nor disturb
  (baseline rule 15).
- The task brief's first working step is unambiguous: if the predecessor is not
  `ACCEPTED`, set `BLOCKED` and implement nothing. That condition holds.
- No production code, tests, CLI, transport, daemon, migration, or product-spec
  changes were made. No invariant was weakened. The only artifact produced is
  this report.

Per the brief, `IMPLEMENTED_TESTED` is forbidden because every mandatory
acceptance criterion is unproven (none was pursued). `BLOCKED` is the only
honest verdict.
