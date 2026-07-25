# REQUIREMENTS — Minimal Reliable Run (`forge run`)

> **Status of this document:** specification only. It defines what a second
> agent must implement and a third agent must review. It is **not** an
> implementation. Every requirement below is initially **NOT IMPLEMENTED**
> (see `ACCEPTANCE_MATRIX.md`).
>
> **Source of truth:** [`../../spec/NEUROFORGE_SPEC.md`](../../spec/NEUROFORGE_SPEC.md)
> (the spec wins on any disagreement). This document freezes a *subset* of the
> spec — the single vertical slice `forge run` — and does not relax any spec
> rule. Where the current code already implements a piece, that is noted as
> "exists today"; where it does not, the requirement is mandatory for this
> stabilization track.

## 0. Scope of this track

### 0.1 In scope (the "minimal run")

The single user-facing scenario this track must make reliable:

```
forge run "Description of the task"
   ↓
resolve the current Git repository
   ↓
safe daemon autostart
   ↓
create task + isolated worktree
   ↓
run one production adapter (OpenCode) against the prompt/model
   ↓
actual repository changes OR an actual local commit
   ↓
verify the real Git state inside the worktree
   ↓
transition workspace + task to a terminal state
   ↓
report a clear result + correct exit code
```

Concretely in scope:

1. `forge run "..."` (new top-level command) and its `--file`, `--engine`,
   `--model`, `--json`, `--verbose` forms.
2. Daemon autostart (find / spawn / readiness / stale-PID reclaim / no dual).
3. Task creation from a free-form description.
4. Isolated Git worktree creation (already implemented; reused).
5. Launching exactly one production coding-agent adapter (default `opencode`).
6. Transport of `prompt` and `model` all the way to the adapter argv/env.
7. Waiting for a single terminal adapter event
   (`run.completed` / `run.failed` / `run.cancelled`).
8. Post-run Git inspection of the worktree
   (`git rev-parse HEAD`, `git status --porcelain`, `git diff --stat`).
9. Outcome classification (see `OUTCOME_CONTRACT.md`).
10. Terminal, atomic persistence of workspace + task state.
11. Local-only result reference under `refs/heads/forge/result/<task-id>`.
12. Cancellation (`run.cancelled`) with process-tree termination and
    precedence over a late `run.failed`.
13. A clear CLI result (human + `--json`) and a correct exit code.
14. Automated regression + reliability + (optional) real-model smoke tests.

### 0.2 Out of scope (explicit non-goals — do **not** build these here)

These are deliberately deferred. Existing subsystems that implement them **must
not be mass-deleted**; the minimal run may simply bypass them.

- A new TUI; any UI/UX redesign.
- Autonomous backlog / queue / scheduler-driven concurrency.
- Multi-agent routing, model-tier escalation, provider failover chains.
- Parallel execution of work packages.
- AI review, security/architecture review, repair loops.
- Auto-merge, push, PR/MR creation, remote merge, ChangeRequestProvider calls.
- Post-merge sentinel, auto-revert.
- Usage/Cost/Quota dashboards or UI.
- Campaign / multi-task orchestration.
- Context Pack / repoinfo prompt enrichment (a plain prompt is enough here).
- Schema changes beyond what is strictly required to persist the new terminal
  fields (and even then, only via a new forward-only migration).

### 0.3 Hard constraints inherited from the spec / AGENTS.md

These are non-negotiable and apply to every slice:

- Spec §36.13 / AC-7: **no Git network operation** in the local path.
- Spec §17.1 / §36.14: **the user's primary checkout is never modified**.
- Spec §36.18: **no silent privilege escalation**; no silent install.
- Spec §29.2 / AC-28: agent process receives an **allowlisted environment**
  only (no merge tokens, no daemon token, no unrelated API keys).
- Spec §36.22: **every AC / requirement has an automated test**.
- Spec §36.25: **unimplemented behaviour is explicitly marked, never disguised
  as a finished stub.**
- AGENTS.md: **no push, no PR, no merge into `main`** during this work.

---

## 1. User contract — `forge run`

### 1.1 Command surface

```
forge run "Description of the task"
forge run --file task.md
forge run --engine opencode --model zai-coding-plan/glm-5.2 "Description"
forge run --json "Description"
forge run --verbose "Description"
```

| Flag         | Default                  | Meaning                                                                 |
|--------------|--------------------------|-------------------------------------------------------------------------|
| (positional) | — (required)             | Free-form task description. Mutually exclusive with `--file`.           |
| `--file`     | —                        | Read the description from a file. Mutually exclusive with positional.   |
| `--engine`   | `opencode`               | Coding-agent engine id. Must be a registered adapter.                   |
| `--model`    | `zai-coding-plan/glm-5.2`| Model id forwarded to the engine (engine ≠ model, spec §12.1).          |
| `--json`     | false                    | Emit a single machine-readable JSON document (see §4 / OUTCOME_CONTRACT).|
| `--verbose`  | false                    | Show internal ids (task id, workspace id, run id) in human output.      |
| `--base`     | current branch           | Base branch/commit for the worktree. Default: current checked-out branch.|
| `--timeout`  | 10m                      | Hard wall-clock timeout for the agent run.                              |

Defaults are fixed and documented (no hidden config required for the happy
path):

- **repository:** the current Git repository (resolved from `$PWD`).
- **profile:** `LOCAL_REVIEW` (no push / no PR / no merge — always).
- **engine:** `opencode`.
- **model:** `zai-coding-plan/glm-5.2`.
- **daemon:** autostart (see §3).
- **base branch:** the repository's current branch.
- **delivery:** local result only (a `refs/heads/forge/result/<task-id>` ref).

### 1.2 Input validation

- `forge run` with no description and no `--file` ⇒ exit code `2`, message:
  `forge: a task description (or --file) is required`.
- Both a positional description and `--file` ⇒ exit code `2`, message:
  `forge: --file and a positional description are mutually exclusive`.
- Not inside a Git repository ⇒ exit code `2`, message:
  `forge: not inside a git repository`.
- Unknown `--engine` (not a registered adapter) ⇒ exit code `2`, message:
  `forge: unknown engine %q`.
- `--file` pointing at a missing/unreadable file ⇒ exit code `2`.

### 1.3 Default human output (success-with-commit)

```
Preparing workspace...
Running OpenCode...
Finalizing repository state...
Completed

Workspace: /home/user/.neuroforge/workspaces/.../attempt-1
Commit:    def4567
Changed:   3 files
Next:      forge task show <task-id>   (or)   forge workspace diff <ws-id>
```

Internal ids (task id, workspace id, run id) are shown **only** with
`--verbose` or `--json`.

### 1.4 Default human output (no-change run)

```
Preparing workspace...
Running OpenCode...
Finalizing repository state...
Agent finished without producing repository changes.

Workspace: /home/user/.neuroforge/workspaces/.../attempt-1
Next:      rephrase the task and run again
```

Exit code **non-zero** (see `OUTCOME_CONTRACT.md`).

### 1.5 No-double-stdout guarantee

When `--json` is set, **nothing else** may be written to stdout — not progress
dots, not logs, not the daemon's own stderr. Progress text must go to stderr
(or be suppressed). The stdout stream must be exactly one valid JSON document
terminated by a single newline.

---

## 2. Functional requirements

Each requirement has a stable id (`FR-*`) reused by `ACCEPTANCE_MATRIX.md`,
`TEST_PLAN.md`, and `IMPLEMENTATION_SLICES.md`.

### FR-1 — Repository resolution
`forge run` resolves the current Git repository from `$PWD` by walking up to
the first `.git` (worktree or common dir). Failure ⇒ exit code `2`.

### FR-2 — Daemon autostart
`forge run` connects to a running daemon if healthy; otherwise spawns one,
waits for readiness, and never spawns a second. Stale PID files are reclaimed.
Startup failure is an actionable error (exit code `3`). See §3.

### FR-3 — Task creation
A task is created in the backlog from the description (spec §9 / AC-3). The
task id is persisted and returned in `--json`. The task starts in `NEW`.

### FR-4 — Isolated worktree
An isolated worktree is created under `~/.neuroforge/workspaces/...` branched
off the resolved base (spec §17 / ADR-0007). The primary checkout is not
touched (verified by test). The worktree's attempt branch is
`forge/<task>/<wp>/attempt-<n>`.

### FR-5 — Adapter launch (production OpenCode)
Exactly one production adapter is launched inside the worktree. The default
engine is `opencode`; the default model is `zai-coding-plan/glm-5.2`. The
adapter receives:
- the **prompt** verbatim (FR-6),
- the **model** id verbatim (FR-7),
- the **workspace path** as its working directory,
- an **allowlisted environment** (no merge creds, no daemon token — AC-28).

### FR-6 — Prompt reaches the adapter
The free-form description (or `--file` contents) is delivered to the adapter
as the run prompt. A test asserts the prompt seen by a fake adapter equals the
input byte-for-byte.

### FR-7 — Model reaches the adapter
The resolved model id is delivered to the adapter (e.g. opencode
`--model <id>`). A test asserts the argv contains the model id.

### FR-8 — Wait for a single terminal event
The run blocks until exactly one terminal normalized event arrives
(`run.completed` / `run.failed` / `run.cancelled`) or the hard timeout fires
(classified as `timed-out`). A late terminal event after the first one is
ignored (see FR-15).

### FR-9 — Post-run Git inspection (the core fix)
After the terminal adapter event, NeuroForge **must** inspect the worktree
using the equivalent of:

```
git rev-parse HEAD
git status --porcelain
git diff --stat <base>...HEAD
git diff --stat
```

All four are read from the **actual** worktree, inside the allowlisted git
runner (no network). The cached `head_sha` field is **never** trusted as the
source of truth for the outcome (invariant §I.2).

### FR-10 — Actual HEAD is persisted
The workspace's `head_sha` is set to the value returned by
`git rev-parse HEAD` inside the worktree, not to a cached or pre-run value.

### FR-11 — Outcome classification
The run is classified into exactly one outcome from the set in
`OUTCOME_CONTRACT.md` §1, computed from (a) the terminal adapter event and
(b) the actual Git inspection from FR-9. Process success alone never yields a
"completed-*" outcome (invariant §I.1).

### FR-12 — Terminal workspace state
After classification the workspace is persisted in a **terminal** state
matching the outcome (`completed` / `failed` / …). It may **not** remain
`active`. Once terminal, no late event or daemon restart may move it back to
`active` (invariant §I.8, `STATE_MACHINE.md`).

### FR-13 — Terminal task state
The task is moved to a terminal state matching the outcome. A no-change run
must not leave the task looking done; it is `FAILED` (or equivalent) with a
clear reason. See `STATE_MACHINE.md`.

### FR-14 — Result reference
When the outcome is `completed-with-commit` or `completed-with-uncommitted-changes`,
a local result ref is created/updated at exactly:

```
refs/heads/forge/result/<task-id>
```

The operation is:
- **local** (no push, ever),
- **idempotent** (re-running finalization moves the ref to the current HEAD;
  a second call does not error and does not create a duplicate),
- does **not** change the user's currently checked-out branch,
- does **not** delete pre-existing non-standard refs silently.

The ref name passed to git must be the fully-qualified `refs/heads/...` form
(not a short name that relies on ref-resolution rules).

### FR-15 — Cancellation precedence
Once a cancellation has been accepted (terminal `run.cancelled`), a later
process exit / `run.failed` event must **not** overwrite the state. A timeout
must classify as `timed-out`, **not** as `cancelled`. Cancellation is
idempotent. The race detector must pass for repeated cancellations
(invariant §I.9).

### FR-16 — Process-tree termination
Cancelling a run terminates the **whole** agent process group (no orphaned
children). Verified cross-platform (unix `setpgid` / Windows
`CREATE_NEW_PROCESS_GROUP`).

### FR-17 — Daemon restart preserves terminal state
A daemon restart (or the startup reconciler) must not move a terminal
workspace back to `active`, and must not lose the result ref / result SHA.
An `active` workspace whose run was interrupted is reconciled to `failed`
(already partly implemented; this track verifies it for the minimal run).

### FR-18 — Clear CLI result + exit code
Human output shows the outcome, the workspace path, the commit SHA (if any),
the changed-file count, and a concrete "Next:" action. `--json` emits exactly
one JSON document with the full contract (OUTCOME_CONTRACT.md §3). Exit codes
follow OUTCOME_CONTRACT.md §4.

### FR-19 — LOCAL_REVIEW safety
For the minimal run the profile is **always** `LOCAL_REVIEW`. No path in
`forge run` may perform: push, fetch, pull, clone, ls-remote, send-pack,
fetch-pack, remote merge, remote-ref mutation, or pass merge credentials to
the agent process. Verified by an allowlist test + a network-op probe test.

### FR-20 — No paid model in automated tests
The automated test suite (unit / integration / black-box / reliability) must
not call any paid model. It drives a managed fake adapter fixture or a fake
executable (rule §36.5). The real OpenCode path is exercised only by the
optional, manually-triggered smoke test (TEST_PLAN.md §6).

---

## 3. Daemon autostart requirements (FR-2 detail)

- **R-2.1** If a daemon is already running and healthy on the resolved
  `NEUROFORGE_HOME`, `forge run` reuses it.
- **R-2.2** If no daemon is running, `forge run` spawns one
  (`forge daemon run`, detached) and waits for readiness.
- **R-2.3** `forge run` never creates a second daemon process when one is
  already healthy. (A guard — e.g. file lock or health re-check after spawn —
  must close the two-CLIs-race window.)
- **R-2.4** A stale PID file (PID present but process dead) is reclaimed and
  the daemon is started cleanly.
- **R-2.5** A corrupted PID/addr/token file is treated as "not running";
  runtime files are cleaned and the daemon is started.
- **R-2.6** If the daemon does not become healthy within the readiness
  timeout, `forge run` aborts with exit code `3` and an actionable message
  (pointing at `forge daemon logs`).
- **R-2.7** The autostart path performs **no** network operation and **no**
  privileged install.

> Note: most of this already exists today
> (`internal/cli/daemon_connect.go`, `internal/daemon/lifecycle.go`). The
> gap this track closes is (a) wiring it under `forge run`, (b) closing the
> two-CLIs-spawn race, and (c) asserting each behaviour with a test.

---

## 4. Machine-readable contract (summary — full shape in OUTCOME_CONTRACT.md)

`forge run --json` emits exactly one JSON object (and nothing else on stdout):

```json
{
  "outcome": "completed-with-commit",
  "task_id": "...",
  "workspace_id": "...",
  "run_id": "...",
  "workspace_path": "...",
  "base_sha": "...",
  "actual_head_sha": "...",
  "engine": "opencode",
  "model": "zai-coding-plan/glm-5.2",
  "changed_files": ["src/a.go", "src/b.go"],
  "commit_sha": "def4567...",
  "result_branch": "refs/heads/forge/result/<task-id>",
  "next_action": "forge task show <task-id>",
  "error": null,
  "error_class": null
}
```

The field set is fixed in `OUTCOME_CONTRACT.md` and must not vary by outcome
(empty / null where not applicable). Human progress output (stderr) must not
corrupt the JSON stream.

---

## 5. Invariants (numbered for cross-reference)

These are the non-negotiable properties every slice must preserve. Each is
expanded in the referenced document.

- **I.1** Process success ≠ task success. `run.completed` only means the
  adapter process ended cleanly. Task success requires a verified Git result
  (FR-9 / FR-11).
- **I.2** Git is the source of truth after a run. `head_sha` is read from
  `git rev-parse HEAD` inside the worktree; the cached value is never trusted
  (FR-9 / FR-10).
- **I.3** Outcomes are disjoint and total (OUTCOME_CONTRACT.md §1).
- **I.4** A no-change run is a failure with a non-zero exit code (FR-13 /
  OUTCOME_CONTRACT.md).
- **I.5** A committed result carries the real commit SHA and the result ref
  points at it (FR-14).
- **I.6** An uncommitted result is preserved, reported honestly, and never
  disguised as a commit (FR-11 / OUTCOME_CONTRACT.md).
- **I.7** Result refs live only under `refs/heads/forge/result/<task-id>`,
  created locally and idempotently (FR-14).
- **I.8** Terminal states are absorbing: no late event or restart revives an
  `active` workspace (FR-12 / FR-17 / STATE_MACHINE.md).
- **I.9** Cancellation precedence: once `run.cancelled`, a late `run.failed`
  cannot overwrite it; a timeout is `timed-out`, not `cancelled`
  (FR-15 / STATE_MACHINE.md).
- **I.10** LOCAL_REVIEW is a hard wall: no Git network op, no remote delivery,
  no merge credentials to the agent (FR-19 / spec AC-7, §36.13, §29.2).
- **I.11** `--json` is one valid document (FR-18 / OUTCOME_CONTRACT.md §3).
- **I.12** The primary checkout is never modified (FR-4 / spec §17.1, §36.14).

---

## 6. Non-functional requirements

- **NFR-1 Performance.** A no-op `forge run` (fake adapter, empty repo) from
  CLI to terminal state completes in < 5s on a warm daemon, < 12s on a cold
  autostart. Asserted by a benchmark-ish test with an upper bound.
- **NFR-2 Determinism.** The outcome classification is a pure function of
  (terminal event, base SHA, actual HEAD, `git status --porcelain`,
  `git diff --stat`). Same inputs ⇒ same outcome, always.
- **NFR-3 Concurrency safety.** The supervisor's cancel map, the scheduler's
  usage sinks and the workspace state writes are safe under
  `go test -race -count=1 ./...`. No data race is acceptable.
- **NFR-4 Offline CI.** No test in the default suite touches the network or a
  paid model (rule §36.5). The real-model smoke test is opt-in.
- **NFR-5 Build hygiene.** `gofmt` clean, `go vet ./...` clean,
  `git diff --check` clean, `make check` green, after every slice.
- **NFR-6 No silent fallback.** If a step cannot be performed (e.g. git not
  installed, daemon would need sudo), the run fails loudly with a classified
  error — never silently degrades to a fake success.
- **NFR-7 No mass deletion.** Existing subsystems (scheduler, failover,
  postmerge, review, merge, …) are not removed. The minimal run may bypass
  them via a thin application service, but their packages and tests stay.
- **NFR-8 Forward-only schema.** Any storage change is a new idempotent
  forward migration. No down migration is required by this track.

---

## 7. Traceability

| Concept       | Defined in                | Tested in              | Tracked in              |
|---------------|---------------------------|------------------------|-------------------------|
| Outcomes      | OUTCOME_CONTRACT.md       | TEST_PLAN.md §2–§5     | ACCEPTANCE_MATRIX.md    |
| States        | STATE_MACHINE.md          | TEST_PLAN.md §2        | ACCEPTANCE_MATRIX.md    |
| Each FR/INV   | this file                 | TEST_PLAN.md           | ACCEPTANCE_MATRIX.md    |
| Slice → FR    | IMPLEMENTATION_SLICES.md  | TEST_PLAN.md           | ACCEPTANCE_MATRIX.md    |
| Known bugs    | KNOWN_FAILURES.md         | TEST_PLAN.md §7        | ACCEPTANCE_MATRIX.md    |
| Review gates  | REVIEW_CHECKLIST.md       | TEST_PLAN.md §8        | ACCEPTANCE_MATRIX.md    |
