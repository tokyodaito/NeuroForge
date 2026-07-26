# KNOWN_FAILURES.md — Minimal Reliable Run

Confirmed defects blocking the minimal reliable run. Each entry is grounded
in the **current** code (file:line references are to the base SHA
`4816039ec7b0debef14bad995fbdd3462584ee5c`). No fabricated root causes: where
the mechanism is uncertain, the entry says so.

> The "confirmed example" referenced throughout is the run the operator
> reported:
> - workspace `ws-neuroforge-6-main-1`
> - run `run-1784985118480774000`
> - engine `opencode`, model `zai-coding-plan/glm-5.2`
> - daemon terminal `run.completed`, 109 normalized events
> - actual HEAD == base SHA, worktree clean, workspace left `active`.

---

## KF-01 — Process success is mistaken for task success

- **Severity:** BLOCKER (core invariant I.1).
- **Confirmed mechanism:** `internal/daemon/workspace_api.go:113-117`
  (`WorkspaceService.RunWorkspace`):

  ```go
  if !result.Failed && !result.Cancelled {
      if _, cpErr := s.wm.Checkpoint(ctx, ws.ID, workspace.MomentFirstDiff, "agent run completed"); cpErr != nil {
          s.logger.Warn("checkpoint after run failed", "err", cpErr)
      }
  }
  ```

  A "completed process" (`!Failed && !Cancelled`) is treated as a completed
  **task**: a checkpoint is recorded with the message "agent run completed"
  regardless of whether the agent changed anything.
- **Reproduction:** run any production adapter that returns `run.completed`
  but writes no files (matches the confirmed example).
- **Expected:** the run is a failure (`completed-no-changes`); no
  "completed" checkpoint.
- **Actual:** a checkpoint labelled "agent run completed" is created.
- **Suspected ownership:** `internal/daemon/workspace_api.go` (RunWorkspace)
  and, by the same logic, the absence of a classifier in
  `internal/scheduler/dispatch.go` (`scheduler.Dispatch` derives `outcome`
  purely from the adapter terminal at dispatch.go:171).
- **Fix slice:** S1 + S2 + S7 (`IMPLEMENTATION_SLICES.md`).
- **Regression test:** U-02, B-02.

## KF-02 — No post-run Git inspection

- **Severity:** BLOCKER (core invariant I.2).
- **Confirmed mechanism:** Neither `WorkspaceService.RunWorkspace`
  (`workspace_api.go:70-124`) nor `scheduler.Dispatch`
  (`internal/scheduler/dispatch.go:132-227`) executes
  `git rev-parse HEAD` / `git status --porcelain` / `git diff --stat` after
  the run. The cached `ws.HeadSHA` (set at worktree creation in
  `internal/workspace/manager.go:232`) is the only "head" value used.
- **Reproduction:** run the confirmed example; observe `head_sha == base_sha`
  while the worktree is clean.
- **Expected:** NeuroForge reads the actual worktree HEAD and porcelain
  status after the terminal event and uses them as the source of truth.
- **Actual:** no git inspection; cached `head_sha` is trusted.
- **Suspected ownership:** missing helper in `internal/workspace/`; missing
  call site in `internal/daemon/workspace_api.go` and
  `internal/scheduler/dispatch.go`.
- **Fix slice:** S1.
- **Regression test:** U-01.

## KF-03 — Workspace left `active` after a run

- **Severity:** BLOCKER (core invariants I.8, FR-12).
- **Confirmed mechanism:** `workspace_api.go:119-123`:

  ```go
  updated, _ := s.wm.Get(ctx, id)
  if result.Failed {
      updated.State = workspace.StateFailed
  }
  return workspaceToDTO(updated), nil
  ```

  Two problems:
  1. On a non-failed run the state is left as whatever `Get` returned
     (`active`) — there is no transition to `completed`.
  2. Even the `failed` branch only mutates the **in-memory** `updated.State`;
     it never calls `wm.SetState` / `updateState` to persist `failed` to the
     DB. So the DB row stays `active` for both success and failure.
- **Reproduction:** after the confirmed run, `GET /workspaces/{id}` reports
  `state=active`.
- **Expected:** the workspace is persisted in a terminal state matching the
  outcome.
- **Actual:** DB row remains `active`.
- **Suspected ownership:** `internal/daemon/workspace_api.go`. (The state
  machine itself in `internal/workspace/recovery.go` is permissive:
  `allowedRecoveryTransition` does not even list `StateCompleted`.)
- **Fix slice:** S3 + S7.
- **Regression test:** U-03, U-11, B-01, B-13.

## KF-04 — `head_sha` stays equal to `base_sha`

- **Severity:** BLOCKER (invariant I.2, FR-10).
- **Confirmed mechanism:** consequence of KF-02. `head_sha` is written once
  at workspace creation (`manager.go:232`) and only ever refreshed by
  `Checkpoint` (`checkpoint.go:104-108`) or `CreateResult`
  (`result.go:60`). A run path that does neither (the current `RunWorkspace`
  behaviour when the agent does not change files) leaves `head_sha == base`.
- **Reproduction:** the confirmed example: actual HEAD == base SHA.
- **Expected:** `head_sha == git rev-parse HEAD` after finalize.
- **Actual:** `head_sha == base_sha`.
- **Suspected ownership:** missing refresh call (depends on S1/S3).
- **Fix slice:** S1 + S3.
- **Regression test:** U-01, B-01.

## KF-05 — Checkpoint points at the base commit

- **Severity:** MAJOR (invariant I.1).
- **Confirmed mechanism:** `internal/workspace/checkpoint.go:63-77`:

  ```go
  commitSHA := ws.HeadSHA
  if hasChanges {
      ... commit ...
      commitSHA = rev-parse HEAD
  }
  ```

  When there are no staged/unstaged changes, `commitSHA` stays `ws.HeadSHA`
  (the base), and a checkpoint **record** is still inserted at lines 95-103
  with that base SHA. Combined with KF-01 (Checkpoint is unconditionally
  called after a non-failed run), a no-change run produces a checkpoint that
  points at the base commit.
- **Reproduction:** run an adapter that emits `run.completed` and writes
  nothing; `forge workspace checkpoints <id>` shows a `first-diff` checkpoint
  whose `commit_sha == base`.
- **Expected:** no `first-diff` checkpoint is created when there is no diff,
  and the outcome is `completed-no-changes`.
- **Actual:** a checkpoint at the base SHA is recorded as if work happened.
- **Suspected ownership:** `internal/workspace/checkpoint.go` (do not create
  the checkpoint record when `!hasChanges` for the `first-diff` moment) and
  `internal/daemon/workspace_api.go` (do not call Checkpoint unconditionally).
- **Fix slice:** S2 + S7.
- **Regression test:** U-02.

## KF-06 — Task stays `NEW`

- **Severity:** MAJOR (FR-13, STATE_MACHINE.md §4).
- **Confirmed mechanism:** neither `WorkspaceService.RunWorkspace` nor
  `scheduler.Dispatch` calls `task.Backlog.Transition`. The M1 task machine
  (`internal/task/state.go`) only reaches `CANCELLED` as terminal; the
  runtime terminal states (`COMPLETED`, `FAILED`) exist as constants
  (`state.go:23-29`) but have **no** transition into them
  (`validTransitions`, `state.go:54-65`, defines none). So after any run the
  task remains in its pre-run state (`NEW`).
- **Reproduction:** after the confirmed run, `forge task show <id>` reports
  `state=NEW`.
- **Expected:** the task is `COMPLETED`/`FAILED`/`CANCELLED` per the outcome.
- **Actual:** `NEW`.
- **Suspected ownership:** `internal/task/state.go` (add transitions) and
  the run path (call `Transition`).
- **Fix slice:** S3 + S7.
- **Regression test:** B-01, B-02.

## KF-07 — Result ref is not created automatically on run

- **Severity:** MAJOR (FR-14, I.5).
- **Confirmed mechanism:** `WorkspaceService.RunWorkspace` never calls
  `wm.CreateResult`. The result ref `forge/result/<task>` is only created by
  an explicit `POST /workspaces/{id}/result` → `CreateResult`
  (`internal/workspace/result.go:19`). The user must run
  `forge workspace result <id>` as a separate step.
- **Reproduction:** after `forge workspace run <id>` (no explicit
  `workspace result`), `git -C <repo> for-each-ref refs/heads/forge/result`
  is empty.
- **Expected:** finalize creates the result ref for committed/uncommitted
  results.
- **Actual:** no result ref until a manual `workspace result` call.
- **Suspected ownership:** the run path / finalize.
- **Fix slice:** S5 + S7.
- **Regression test:** B-01, U-05.

## KF-08 — Result ref must be the fully-qualified `refs/heads/...` form

- **Severity:** MAJOR (I.7, robustness).
- **Confirmed mechanism:** `internal/workspace/result.go:46` issues

  ```go
  primaryRunner.run(ctx, "update-ref", resultBranch, headSHA)
  ```

  where `resultBranch = ResultBranch(taskID) = "forge/result/<task>"`
  (`internal/workspace/naming.go:19-21`) — a **short** name. `git update-ref`
  resolves it to `refs/heads/forge/result/<task>` for a branch, so the
  *current* behaviour is correct in effect, but it relies on git's ref
  resolution rules rather than pinning the explicit
  `refs/heads/forge/result/<task-id>` form the spec requires. The operator
  report notes the ref was previously created "not under `refs/heads`",
  consistent with an earlier non-explicit form. Using the short form leaves
  the ref vulnerable to future resolution changes (e.g. if something ever
  calls `update-ref` without `<oldvalue>` against a name that ambiguously
  resolves).
- **Reproduction (current):** `git for-each-ref` shows the ref under
  `refs/heads/forge/result/<task>` *today* — but grep the diff: the call
  uses the short name.
- **Expected:** the implementation passes the literal
  `refs/heads/forge/result/<task-id>` to `update-ref`.
- **Actual:** the short name is passed.
- **Suspected ownership:** `internal/workspace/result.go` + `naming.go`.
- **Fix slice:** S5.
- **Regression test:** U-05.

## KF-09 — Gemini cancellation race: ends as `run.failed` instead of `run.cancelled`

- **Severity:** MAJOR (FR-15, I.9). Confirmed intermittent under the race
  detector.
- **Confirmed mechanism:** `internal/adapter/codingagent/gemini/run.go:178-194`
  in `supervise`:

  ```go
  select {
  case <-ctx.Done():
      ... cancelled = true (or timedOut)
  case o := <-readCh:
      raw = o.raw
  }
  ```

  When the process happens to EOF (closing stdout) at the same instant a
  cancellation cancels the context, Go's `select` picks one case
  pseudo-randomly. If `readCh` wins, the code never sets `cancelled`; it
  falls through to `synthesizedTerminal` (`run.go:216-223`), which emits
  `run.completed` (exit 0) or `run.failed` (non-zero) — **not**
  `run.cancelled`. Under the race detector, with concurrent kill + EOF, this
  surfaces as a `run.failed` after a user cancel.
- **Reproduction:** run the gemini cancellation test in a tight loop under
  `-race` (the existing suite does not loop it enough to surface the race).
- **Expected:** once cancellation is accepted, the terminal is always
  `run.cancelled`, regardless of concurrent EOF.
- **Actual:** sometimes `run.failed`.
- **Suspected ownership:** `internal/adapter/codingagent/gemini/run.go`
  (`supervise`). The same select pattern should be audited in the other five
  adapters (`codex`, `claude`, `kimi`, `grok`, `opencode`) — opencode's
  `supervise` (`opencode/run.go:193-255`) handles ctx.Done/timeout/EOF as
  separate cases but `finishRun` can still synthesize a non-cancelled
  terminal on an EOF race; needs the same hardening.
- **Note (do not over-claim):** the exact trigger window is timing-dependent;
  the fix is to make the terminal decision owned by a single path that
  records the *reason* (cancel vs timeout vs EOF) before synthesizing.
- **Fix slice:** S6.
- **Regression test:** U-06, U-15 (loop under `-race`).

## KF-10 — Production-adapter usage not persisted via the workspace-run path

- **Severity:** MAJOR (data integrity; the operator explicitly flagged
  "usage production adapter not saved in NeuroForge DB").
- **Confirmed mechanism:** `scheduler.Dispatch` records usage events
  (`internal/scheduler/dispatch.go:176-199`) by scanning the event stream
  for `usage.updated` and calling `s.usage.RecordUsage`. The opencode adapter
  emits `usage.updated` correctly (`internal/adapter/codingagent/opencode/run.go:240-243`
  maps usage; the scheduler extracts it). **However**, the
  `forge workspace run` path (`WorkspaceService.RunWorkspace`,
  `workspace_api.go:99-117`) never extracts usage events from
  `supervisor.RunResult.Events` and never calls `RecordUsage`. So a
  production adapter run via that path writes zero `usage_events` rows.
- **Reproduction:** run `forge workspace run --engine opencode ...`; inspect
  `usage_events` (or `forge usage`); observe zero rows for the run.
- **Expected:** usage from any production adapter run is persisted (the
  scheduler path does this; the workspace path must too, or `forge run` must
  use the path that does).
- **Actual:** no usage rows for workspace-run production runs.
- **Suspected ownership:** the run path used by `forge run` (S7) must record
  usage via the same `UsageSink` the scheduler uses, regardless of which
  entry point triggered it.
- **Fix slice:** S7.
- **Regression test:** a usage-persistence test that runs a production-style
  adapter (via the fake, emitting `usage.updated`) through `forge run` and
  asserts a `usage_events` row.

## KF-11 — There is no `forge run` command

- **Severity:** MAJOR (user surface; the minimal scenario is not exposed).
- **Confirmed mechanism:** `internal/cli/cli.go:55-101` dispatch has no
  `case "run"`. The only "run" subcommands are `forge daemon run`
  (`daemon_cmd.go:30`) and `forge workspace run` (`workspace_cmd.go:31`).
  Reaching the minimal scenario today requires chaining `project add → task
  add → workspace create → workspace run → workspace checkpoint → workspace
  result` manually.
- **Reproduction:** `forge run "x"` → `forge: unknown command "run"`.
- **Expected:** a single `forge run "..."` command implementing
  REQUIREMENTS.md §1.
- **Actual:** no such command.
- **Suspected ownership:** `internal/cli/cli.go` + a new `run_cmd.go`.
- **Fix slice:** S8.
- **Regression test:** B-01..B-08.

## KF-12 — CLI exit code misreports success

- **Severity:** MAJOR (FR-18, OUTCOME_CONTRACT.md §4).
- **Confirmed mechanism:** `internal/cli/workspace_cmd.go:281-300`
  (`workspaceRun`) returns `ExitOK` as long as `cli.RunWorkspace` returns no
  HTTP error — even if the workspace ended `active` with `head_sha == base`.
  Because of KF-03, a no-change or even a failed run that did not surface an
  HTTP error returns exit 0.
- **Reproduction:** run an adapter that emits `run.completed` with no
  changes; the CLI exits 0.
- **Expected:** non-zero exit for `completed-no-changes` / `failed` /
  `cancelled` / `timed-out`, per OUTCOME_CONTRACT.md §4.
- **Actual:** exit 0 for any non-HTTP-error run.
- **Suspected ownership:** `internal/cli/workspace_cmd.go` and the new
  `forge run` exit-code mapping.
- **Fix slice:** S8.
- **Regression test:** B-02, B-04.

---

## Suspected-but-not-confirmed (recorded for the implementer, not for Gate E)

These were flagged in the operator report but not pinned to a line in this
review. The implementer should reproduce or dismiss each before claiming the
relevant row PASS.

- **S-1** "Gemini cancellation conformance passes multiple times under race
  detector" — covered by KF-09 / U-15. Confirm the existing suite does not
  already loop enough to hide the race.
- **S-2** Whether `forge workspace run` (the old path) should also be fixed
  or merely superseded by `forge run`. Decision in this track: **supersede,
  do not mass-rewrite** (NFR-7). The old command stays for compatibility;
  its known gaps are documented here.
