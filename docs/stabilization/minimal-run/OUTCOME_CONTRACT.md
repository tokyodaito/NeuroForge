# OUTCOME_CONTRACT.md — Minimal Reliable Run

This is the normative contract for the result of a single `forge run`. It is
consumed by the CLI (human + `--json`), by tests, and by audit. The outcome
set is **disjoint and total** (invariant I.3): every terminal run maps to
exactly one outcome, and no two outcomes overlap.

---

## 1. Outcome classification

The classifier is a **pure function** of four inputs (NFR-2):

```
outcome = classify(
    runTerminal,     // COMPLETED | FAILED | CANCELLED  (from the adapter event stream)
    baseSHA,         // from the workspace record
    actualHEAD,      // from `git rev-parse HEAD` inside the worktree  (FR-9)
    gitStatus,       // from `git status --porcelain`                 (FR-9)
)
```

A hard timeout is reflected as `runTerminal = FAILED` with class `TIMEOUT`
(the supervisor synthesizes this), and maps to `timed-out` below.

### 1.1 The outcome set

| Outcome                             | runTerminal | actualHEAD == baseSHA | gitStatus empty | Meaning                                         |
|-------------------------------------|-------------|-----------------------|-----------------|-------------------------------------------------|
| `completed-with-commit`             | COMPLETED   | **no** (HEAD advanced) | (any)          | Agent committed; result ref points at HEAD.     |
| `completed-with-uncommitted-changes`| COMPLETED   | **yes**               | **no**          | Agent modified files but did not commit.        |
| `completed-no-changes`              | COMPLETED   | **yes**               | **yes**         | Agent ended cleanly but changed nothing.        |
| `failed`                            | FAILED      | (any)                 | (any)           | Adapter reported failure (non-timeout).         |
| `cancelled`                         | CANCELLED   | (any)                 | (any)           | User cancellation was the accepted terminal.    |
| `timed-out`                         | FAILED+TIMEOUT | (any)              | (any)           | Hard wall-clock deadline fired.                 |
| `interrupted`                       | (none)      | (any)                 | (any)           | Daemon died mid-run; reconciler marked failed.  |

> The `interrupted` outcome is produced **only** by the restart reconciler
> (STATE_MACHINE.md §5.1), never by a live `forge run`.

### 1.2 Classification rules (deterministic)

Applied in order; the first match wins:

1. `runTerminal == CANCELLED` ⇒ `cancelled`. *(I.9: cancellation precedence.)*
2. `runTerminal == FAILED` with class `TIMEOUT` ⇒ `timed-out`.
3. `runTerminal == FAILED` (any other class) ⇒ `failed`.
4. `runTerminal == COMPLETED`:
   - 4a. `actualHEAD != baseSHA` ⇒ `completed-with-commit`.
   - 4b. `actualHEAD == baseSHA` **and** `gitStatus` non-empty ⇒
     `completed-with-uncommitted-changes`.
   - 4c. `actualHEAD == baseSHA` **and** `gitStatus` empty ⇒
     `completed-no-changes`.

> **Note on `completed-with-commit` with a dirty tree:** if the agent
> committed *and* left further uncommitted edits, the outcome is still
> `completed-with-commit` (HEAD advanced). The uncommitted edits are reported
> in the result (changed files, workspace path) so nothing is hidden, but the
> presence of a commit takes precedence. This preserves invariant I.5 (a
> committed result carries the real commit SHA).

### 1.3 Per-outcome derived state

| Outcome                             | Workspace state   | Task state   | Result ref created? | Retry allowed? |
|-------------------------------------|-------------------|--------------|---------------------|----------------|
| `completed-with-commit`             | `completed`       | `COMPLETED`  | **yes** → HEAD      | yes (new run)  |
| `completed-with-uncommitted-changes`| `completed`       | `COMPLETED`  | **yes** → HEAD*     | yes (new run)  |
| `completed-no-changes`              | `failed`          | `FAILED`     | no                  | yes (new run)  |
| `failed`                            | `failed`          | `FAILED`     | no                  | yes (new run)  |
| `cancelled`                         | `cancelled`       | `CANCELLED`  | no                  | yes (new run)  |
| `timed-out`                         | `timed_out`       | `FAILED`     | no                  | yes (new run)  |
| `interrupted`                       | `failed`          | `FAILED`     | no                  | yes (new run)  |

`*` For `completed-with-uncommitted-changes`, the result ref points at
`actualHEAD` (== baseSHA) **only if** the worktree's index/working-tree state
is preserved; the result is the **working-tree diff**, honestly reported as
uncommitted. The ref is still created so the user has a stable handle, but the
CLI and JSON make clear the result is *not* a commit (see §2, §3).

### 1.4 Audit events per outcome

Each finalize appends **one** audit event `run.outcome_decided` carrying:

```json
{
  "outcome": "<one of §1.1>",
  "run_terminal": "COMPLETED|FAILED|CANCELLED",
  "base_sha": "...",
  "actual_head_sha": "...",
  "git_status_empty": true,
  "commit_sha": "..." ,
  "result_branch": "refs/heads/forge/result/<task-id>",
  "engine": "opencode",
  "model": "zai-coding-plan/glm-5.2",
  "run_id": "..."
}
```

For `cancelled` / `timed-out` / `failed`, an additional `run.<outcome>` event
records the failure class/reason when present.

---

## 2. Human output contract

Written to **stdout** for non-`--json` runs. Progress lines go to **stderr**
so they never corrupt JSON. All outcomes share the prefix:

```
Preparing workspace...
Running OpenCode...
Finalizing repository state...
```

Then one of the following terminal blocks.

### 2.1 `completed-with-commit`
```
Completed

Workspace: <path>
Commit:    <short sha>
Changed:   <N> file(s)
Next:      forge task show <task-id>
```

### 2.2 `completed-with-uncommitted-changes`
```
Completed (uncommitted changes — nothing was committed by the agent)

Workspace:   <path>
Changed:     <N> file(s): path/a, path/b, ...
Result ref:  refs/heads/forge/result/<task-id> (at base; changes are in the worktree)
Next:        cd <path> && git diff     (or)     forge workspace diff <ws-id>
```

### 2.3 `completed-no-changes`
```
Agent finished without producing repository changes.

Workspace: <path>
Next:      rephrase the task and run again
```

### 2.4 `failed`
```
Failed: <short reason>

Workspace: <path>
Run:       <run-id>   (--verbose only)
Next:      check the agent output; re-run with a clearer task description
```

### 2.5 `cancelled`
```
Cancelled.

Workspace: <path>
Next:      re-run when ready
```

### 2.6 `timed-out`
```
Timed out after <duration>.

Workspace: <path>
Run:       <run-id>   (--verbose only)
Next:      raise --timeout, or split the task into smaller steps
```

With `--verbose`, every block also prints `Task:`, `Workspace id:`, and
`Run:` internal ids.

---

## 3. JSON output contract (`--json`)

`forge run --json` writes **exactly one** JSON object to stdout, followed by a
single newline, and nothing else on stdout (invariant I.11). The field set is
**fixed** — every field is always present; empty/unset values are `null`,
empty arrays are `[]`, empty strings are `""`.

```json
{
  "outcome": "completed-with-commit",
  "task_id": "neuroforge-7",
  "workspace_id": "ws-neuroforge-7-main-1",
  "run_id": "run-1784985118480774000",
  "workspace_path": "/home/u/.neuroforge/workspaces/neuroforge/neuroforge-7/main/attempt-1",
  "base_sha": "abc123...",
  "actual_head_sha": "def456...",
  "engine": "opencode",
  "model": "zai-coding-plan/glm-5.2",
  "changed_files": ["src/a.go", "src/b.go"],
  "commit_sha": "def456...",
  "result_branch": "refs/heads/forge/result/neuroforge-7",
  "next_action": "forge task show neuroforge-7",
  "error": null,
  "error_class": null
}
```

### 3.1 Field semantics

| Field             | Type     | Notes                                                              |
|-------------------|----------|--------------------------------------------------------------------|
| `outcome`         | string   | One of §1.1. Always present.                                       |
| `task_id`         | string   | The created/used task id.                                          |
| `workspace_id`    | string   | The workspace id.                                                  |
| `run_id`          | string\|null | The adapter run id; `null` if the run never started.          |
| `workspace_path`  | string   | The worktree path (so the user can `cd` in).                      |
| `base_sha`        | string   | The base commit the worktree was branched from.                   |
| `actual_head_sha` | string\|null | `git rev-parse HEAD` after the run (FR-9); `null` if unreadable.|
| `engine`          | string   | The adapter engine id used.                                        |
| `model`           | string   | The model id forwarded to the adapter.                             |
| `changed_files`   | []string | Files changed vs base (`git diff --name-only base...HEAD`) plus   |
|                   |          | uncommitted files from `git status --porcelain`; de-duplicated.   |
| `commit_sha`      | string\|null | The actual HEAD when `outcome` is `*-with-commit`; else `null`.|
| `result_branch`   | string\|null | `refs/heads/forge/result/<task-id>` when created; else `null`. |
| `next_action`     | string   | A concrete suggested command (non-empty).                         |
| `error`           | string\|null | Human-readable error string for `failed`/`timed-out`/etc.      |
| `error_class`     | string\|null | Stable error class: `EMPTY_PROMPT`, `UNKNOWN_ENGINE`,           |
|                   |          | `NOT_A_REPO`, `DAEMON_START_FAILED`, `ADAPTER_FAILED`,            |
|                   |          | `GIT_INSPECT_FAILED`, `TIMEOUT`, `CANCELLED`, `NO_CHANGES`.       |

### 3.2 `--json` on input/validation errors

Validation errors (§1.2 of REQUIREMENTS.md) still emit a JSON object (so
scripts can parse) with `outcome` omitted-only-if-impossible; in practice the
object always carries `outcome: "failed"` and a populated `error`/`error_class`,
plus an `exit_code` mirror field is **not** added (exit code is the process's;
see §4). Example:

```json
{"outcome":"failed","task_id":null,"workspace_id":null,"run_id":null,
 "workspace_path":null,"base_sha":null,"actual_head_sha":null,
 "engine":"opencode","model":"zai-coding-plan/glm-5.2","changed_files":[],
 "commit_sha":null,"result_branch":null,"next_action":null,
 "error":"not inside a git repository","error_class":"NOT_A_REPO"}
```

---

## 4. Exit codes

Exit codes are part of the contract; scripts depend on them.

| Exit code | Outcome / situation                                            |
|-----------|----------------------------------------------------------------|
| `0`       | `completed-with-commit` **or** `completed-with-uncommitted-changes`. |
| `1`       | `failed` (adapter failure, git inspect failure, internal error).|
| `2`       | Usage / validation error (missing prompt, unknown engine, not a repo, mutually-exclusive flags, unreadable `--file`). **No workspace is created.** |
| `3`       | Infrastructure error (daemon autostart failed, daemon unhealthy). |
| `130`     | `cancelled` (SIGINT-like) **or** `interrupted`.                |
| `124`     | `timed-out` (matches `timeout(1)` convention).                 |

Notes:
- `completed-no-changes` is a **non-zero** exit (code `1`): the task did not
  succeed. This is invariant I.4. The CLI message makes the reason clear, so
  it is not confused with an adapter crash.
- Only code `0` means "there is a real result".
- A validation error (code `2`) must **not** create any durable state (no
  task, no workspace). This is verified by a test that snapshots the DB.

### 4.1 Exit-code precedence
When multiple conditions apply, the order is:
`validation(2) > infrastructure(3) > cancelled(130) > timed-out(124) > failed(1) > success(0)`.
(E.g. a cancelled run that also hit the deadline reports `130`, not `124` —
cancellation precedence, I.9.)

---

## 5. Retry semantics

- A retry is **always** a new run: a new workspace/attempt and a new `run_id`.
  The minimal run does not mutate an existing terminal workspace on retry.
- The previous result ref is overwritten to the new HEAD by the new run's
  finalize (idempotent `update-ref`, FR-14).
- A task left in `FAILED` / `CANCELLED` by a prior run may be retried by
  running `forge run` again with the same description (this creates a new task
  in the minimal run). Reusing the *same* task id across retries is a future
  concern (autonomous backlog); out of scope here.

---

## 6. Idempotency of finalize

Calling finalize twice on the same workspace (e.g. a late duplicate event, or
a double-clicked API call) yields the **same** persisted outcome. The second
call:
- does not create a second result ref,
- does not append a second `run.outcome_decided` audit event beyond a single
  dedup `run.finalize_idempotent` notice,
- returns the already-recorded outcome to the caller.

This is verified by a dedicated test (TEST_PLAN.md §2).
