# TEST_PLAN.md — Minimal Reliable Run

This is the **authoritative** test plan for the `forge run` track. Every
requirement in `REQUIREMENTS.md`, every outcome in `OUTCOME_CONTRACT.md`, and
every transition in `STATE_MACHINE.md` must have at least one test listed
here. Tests are referenced from `ACCEPTANCE_MATRIX.md`.

**Rules that apply to every test in this plan:**
- No paid model, no network (rule §36.5). The default suite uses the in-process
  fake adapter or a fake executable fixture.
- Every test names the requirement id(s) it proves in a comment.
- A test that cannot be made deterministic is a missing requirement, not a
  skipped test (rule §36.25).
- All tests must pass under `go test -race -count=1 ./...`.

---

## 1. Test layers (and what each is allowed to do)

| Layer          | Drives                          | Real Git | Real daemon | Real binary | Network | Paid model |
|----------------|---------------------------------|----------|-------------|-------------|---------|------------|
| **Unit**       | pure functions in-process       | no       | no          | no          | no      | no         |
| **Integration**| composed services in-process    | yes (temp)| no         | no          | no      | no         |
| **Black-box**  | compiled `forge` binary         | yes (temp)| yes        | yes         | no      | no         |
| **Reliability**| compiled `forge`, 10 iterations | yes (temp)| yes        | yes         | no      | no         |
| **Smoke (opt)**| compiled `forge` + real OpenCode| yes (temp)| yes        | yes         | **yes** | **yes**    |

The smoke layer is **opt-in** (§6) and excluded from `go test ./...`.

---

## 2. Unit + integration tests (in-process)

These live next to the code they test (e.g. `internal/workspace/`,
`internal/cli/`, a new `internal/runapp/` application-service package).

### U-01 — Post-run Git inspection
- **Proves:** FR-9, FR-10, I.2.
- **What:** Given a temp worktree with (a) a new commit, (b) uncommitted
  edits, (c) nothing, the inspector returns the right `(actualHEAD, status,
  diffStat)` triple. The cached `head_sha` field is demonstrably ignored.
- **Pass:** all three branches return the expected triple; `actualHEAD` is
  read from `git rev-parse HEAD`.

### U-02 — Outcome classifier (table-driven)
- **Proves:** FR-11, OUTCOME_CONTRACT.md §1.2, I.1, I.3.
- **What:** Every cell of the §1.1 table is exercised. `classify()` is a pure
  function.
- **Pass:** exact outcome string for each input combination; the "process
  completed but no changes" case is `completed-no-changes`, never a success.

### U-03 — Terminal state atomicity
- **Proves:** STATE_MACHINE.md §3.4, FR-12, FR-13.
- **What:** Simulate a crash (rollback the finalize tx) and assert neither the
  workspace state nor the audit event is persisted.
- **Pass:** atomic — all-or-nothing.

### U-04 — Idempotent finalize
- **Proves:** OUTCOME_CONTRACT.md §6, FR-14.
- **What:** Call finalize twice on the same workspace; assert the second call
  returns the recorded outcome, creates no second result ref, and emits at
  most one `run.outcome_decided` (plus a dedup notice).
- **Pass:** no duplicate ref; same outcome.

### U-05 — Result ref is under `refs/heads/...` and idempotent
- **Proves:** FR-14, I.7.
- **What:** After finalize, `git for-each-ref refs/heads/forge/result/<task>`
  resolves; the ref is `refs/heads/forge/result/<task-id>` (full form). A
  second finalize moves it to the new HEAD without error.
- **Pass:** exact ref path; no `refs/` duplicate outside `refs/heads/`.

### U-06 — Cancellation precedence (live)
- **Proves:** FR-15, I.9, STATE_MACHINE.md §1.3.
- **What:** Start a fake run; concurrently call Cancel and emit `run.failed`.
  Assert the recorded terminal is `CANCELLED`.
- **Pass:** `cancelled`, never `failed`.

### U-07 — Timeout is not cancellation
- **Proves:** FR-15, OUTCOME_CONTRACT.md §4.1.
- **What:** A run whose hard deadline fires (no user cancel) records
  `timed-out`, not `cancelled`.
- **Pass:** outcome `timed-out`, exit code `124`.

### U-08 — Prompt reaches the adapter
- **Proves:** FR-6.
- **What:** Inject a fake adapter that records its `AgentRunRequest.Prompt`;
  assert it equals the input byte-for-byte (including `--file` contents).
- **Pass:** exact equality.

### U-09 — Model reaches the adapter
- **Proves:** FR-7.
- **What:** A fake/recorded opencode-style adapter asserts its argv contains
  `--model <id>` with the exact id.
- **Pass:** exact equality.

### U-10 — Worktree isolation
- **Proves:** FR-4, I.12.
- **What:** Snapshot the primary checkout (HEAD + file set) before and after a
  run; assert unchanged. (Extends the existing M3 test invariant.)
- **Pass:** identical snapshot.

### U-11 — Terminal absorbing states
- **Proves:** STATE_MACHINE.md §3.3, I.8.
- **What:** Attempt every illegal transition (`completed→active`,
  `cancelled→active`, `deleted→anything`, …) and assert a typed error.
- **Pass:** all illegal transitions rejected.

### U-12 — Reconciler keeps terminal; marks stale active failed
- **Proves:** FR-17, STATE_MACHINE.md §5.
- **What:** Seed a `completed` workspace and an `active` workspace with a dead
  pid; run the reconciler.
- **Pass:** `completed` unchanged; `active → failed`.

### U-13 — LOCAL_REVIEW network lock
- **Proves:** FR-19, I.10, spec AC-7.
- **What:** Attempt every git subcommand not on the allowlist through the run
  path; assert rejection.
- **Pass:** none reachable; no remote refs created.

### U-14 — JSON validity for every outcome
- **Proves:** OUTCOME_CONTRACT.md §3, I.11.
- **What:** Marshal the result JSON for each outcome and validate the fixed
  field set; assert non-stdout text never appears.
- **Pass:** schema-valid for all outcomes.

### U-15 — Gemini cancellation conformance under -race (repeated)
- **Proves:** KF-09, FR-15.
- **What:** Run the gemini cancellation path N times (N ≥ 20) under
  `-race`; assert every terminal is `cancelled`.
- **Pass:** 0 races, 0 misclassifications.

---

## 3. Black-box tests (compiled `forge` binary)

These build the real `forge` binary, drive it through a temp `NEUROFORGE_HOME`
and a temp Git repo, and assert only on observable outputs (HTTP/JSON,
filesystem, Git state). They reuse the harness from `internal/cli/m3_scenario_test.go`.

### B-01 — `forge run` happy path (fake adapter, with-commit)
- **Proves:** FR-1..FR-14, FR-18, OUTCOME_CONTRACT.md §2.1/§3.
- **What:** temp repo → `forge run "add RESULT.md and commit"` with a fake
  adapter that creates + commits a file.
- **Pass:** exit 0; JSON `outcome=completed-with-commit`; `actual_head_sha !=
  base_sha`; result ref exists under `refs/heads/...`; workspace `completed`;
  task `COMPLETED`.

### B-02 — No-change run is a failure
- **Proves:** FR-11/FR-13, I.4, OUTCOME_CONTRACT.md §1.1 / §4.
- **What:** fake adapter that emits `run.completed` and writes nothing.
- **Pass:** exit 1; JSON `outcome=completed-no-changes`; workspace `failed`;
  task `FAILED`; human message contains "without producing repository
  changes".

### B-03 — Uncommitted-changes run
- **Proves:** OUTCOME_CONTRACT.md §1.1 / §2.2, I.6.
- **What:** fake adapter that writes a file but does not commit.
- **Pass:** exit 0; `outcome=completed-with-uncommitted-changes`; `changed_files`
  non-empty; `commit_sha` is null; workspace path reported; result ref points
  at base SHA.

### B-04 — Adapter failure
- **Proves:** OUTCOME_CONTRACT.md §2.4.
- **What:** fake adapter `run.failed` scenario.
- **Pass:** exit 1; `outcome=failed`; workspace `failed`; task `FAILED`.

### B-05 — Cancellation via SIGINT
- **Proves:** FR-15, FR-16, OUTCOME_CONTRACT.md §2.5.
- **What:** start `forge run`, send SIGINT mid-run.
- **Pass:** exit 130; `outcome=cancelled`; whole process group gone (no
  orphan child of the fake adapter).

### B-06 — Timeout
- **Proves:** FR-08, OUTCOME_CONTRACT.md §2.6 / §4.
- **What:** `forge run --timeout 1s` against a fake adapter that blocks.
- **Pass:** exit 124; `outcome=timed-out`.

### B-07 — `--json` is one document
- **Proves:** I.11, OUTCOME_CONTRACT.md §3.
- **What:** capture stdout of `forge run --json ...`; parse it.
- **Pass:** exactly one JSON object followed by a single newline; no stray
  text; all fixed fields present.

### B-08 — Validation errors create no state (exit 2)
- **Proves:** REQUIREMENTS.md §1.2, OUTCOME_CONTRACT.md §4.
- **What:** `forge run` (no prompt); `forge run a --file b`; `forge run` outside
  a repo; `forge run --engine bogus x`.
- **Pass:** exit 2; snapshot of `~/.neuroforge` DB shows no task/workspace
  rows created by the failed invocation.

### B-09 — Daemon autostart (cold) and reuse (warm)
- **Proves:** FR-2, §3 of REQUIREMENTS.md.
- **What:** (a) no daemon → `forge run` starts one and succeeds; (b) running
  daemon → `forge run` reuses it (no second pid in the pidfile / no second
  process).
- **Pass:** exactly one daemon pid; readiness achieved; exit 0.

### B-10 — Stale-PID recovery
- **Proves:** FR-2, R-2.4.
- **What:** write a pidfile with a dead pid, then `forge run`.
- **Pass:** stale reclaimed; daemon starts; run succeeds.

### B-11 — Two-CLIs-spawn race (no dual daemon)
- **Proves:** FR-2, R-2.3.
- **What:** launch two `forge run` concurrently from the same cold home.
- **Pass:** exactly one daemon process owns the pidfile; both runs either
  reuse it or one starts and one reuses.

### B-12 — Primary checkout untouched + no network
- **Proves:** I.10, I.12, FR-19.
- **What:** snapshot primary HEAD + file set; configure a (fake) remote;
  run; check remote refs.
- **Pass:** primary unchanged; zero remote refs; zero network git ops
  (asserted by allowlist + a probe).

### B-13 — Reliability loop (10×) — see §5.
- **Proves:** Gates C, I.8, all FR.

### B-14 — Daemon restart mid-run → `interrupted`
- **Proves:** FR-17, STATE_MACHINE.md §5.1.
- **What:** start `forge run`, kill -9 the daemon, restart, inspect the
  workspace.
- **Pass:** workspace `failed` with reason `interrupted by daemon restart`;
  task `FAILED`; no `active` workspace left.

### B-15 — Daemon restart after success preserves terminal
- **Proves:** FR-17, STATE_MACHINE.md §5.2.
- **What:** after B-01, stop+start the daemon.
- **Pass:** workspace still `completed`; result ref + SHA intact.

---

## 4. Outcome & state-machine coverage matrix (tests → requirement)

Every outcome and every legal/illegal transition has at least one test. The
implementing agent fills `IMPLEMENTATION_SLICES.md` with the test names.

| Outcome / Transition                    | Test(s)            |
|-----------------------------------------|--------------------|
| `completed-with-commit`                 | U-02, B-01, B-13   |
| `completed-with-uncommitted-changes`    | U-02, B-03, B-13   |
| `completed-no-changes`                  | U-02, B-02, B-13   |
| `failed`                                | U-02, B-04, B-13   |
| `cancelled`                             | U-06, U-15, B-05   |
| `timed-out`                             | U-07, B-06         |
| `interrupted`                           | U-12, B-14         |
| terminal → active (rejected)            | U-11               |
| restart keeps terminal                  | U-12, B-15         |
| restart marks stale active failed       | U-12, B-14         |
| idempotent finalize                     | U-04, U-05         |

---

## 5. Reliability loop (Gate C)

`TestForgeRun_Reliability_10x` (black-box, opt-in via a short `-count` loop or
a `for` inside the test):

1. Build `forge`.
2. For `i := 0; i < 10; i++` (a fresh temp repo + temp home each iteration):
   - `forge run "add RESULT.md and commit"` with a fake adapter fixture that
     creates the file and commits it.
   - Assert exit 0.
   - Assert JSON `outcome == "completed-with-commit"`.
   - Assert `actual_head_sha != base_sha`.
   - Assert `git rev-parse HEAD` in the worktree equals `actual_head_sha`.
   - Assert workspace state is terminal (`completed`).
   - Assert zero `active` workspaces remain for this home.
   - Assert result ref resolves under `refs/heads/forge/result/<task-id>`.
   - Assert primary checkout HEAD unchanged.
3. The test **fails on the first mismatch** and dumps artifacts (§7).

**Pass criteria for Gate C:**
- 10/10 iterations green,
- zero stale `active` workspaces across all iterations,
- zero mismatched head SHA,
- zero duplicate daemon processes,
- zero network Git operations (probed).

The loop runs under `go test -race -count=1`.

---

## 6. Real-model smoke test (Gate D — opt-in)

A single, separate, opt-in test that exercises the **real** OpenCode adapter
once, against a real (paid) model. It is **skipped** unless the env var
`NEUROFORGE_SMOKE=opencode` is set, so it never runs in `go test ./...` nor in
CI.

- **File:** `internal/cli/run_smoke_test.go` with
  `if os.Getenv("NEUROFORGE_SMOKE") != "opencode" { t.Skip(...) }`.
- **Scenario:** `forge run "Create RESULT.md with the text 'hello' and make a
  local git commit"` in a temp repo.
- **Asserts:**
  - engine is `opencode`,
  - model is `zai-coding-plan/glm-5.2` (or the `--model` provided),
  - a real commit exists (`actual_head_sha != base_sha`),
  - `RESULT.md` exists in the worktree,
  - workspace is terminal `completed`,
  - **primary checkout untouched**,
  - **no push / PR / merge / remote ref** (LOCAL_REVIEW wall).
- **Does not** run under `-race` necessarily, runs once (`-count=1`), and is
  the only test permitted to use the network + a paid model.

---

## 7. Failure-artifact collection

When any test in §2–§5 fails, it dumps (under the test's temp dir, and prints
the paths to the test log):

| Artifact                 | Source                                          |
|--------------------------|-------------------------------------------------|
| `forge-stdout.txt`       | captured CLI stdout                              |
| `forge-stderr.txt`       | captured CLI stderr                              |
| `forge-exit-code.txt`    | the exit code                                    |
| `daemon.log`             | `~/.neuroforge/daemon.log` of the run           |
| `db.sqlite`              | a copy of the SQLite state db                    |
| `worktree/`              | the managed worktree directory (for git forensics) |
| `git-status.txt`         | `git -C <worktree> status --porcelain`           |
| `git-log.txt`            | `git -C <worktree> log --oneline -20 base..HEAD` |
| `primary-head-before/after.txt` | primary checkout HEAD around the run        |

The reliability loop dumps one set per failed iteration. This is what makes a
flaky failure diagnosable instead of mystical.

---

## 8. Review-gate tests (Gate E)

`REVIEW_CHECKLIST.md` enumerates the exact commands the independent reviewer
runs. They map onto this plan as follows:

| Gate | Command                                                 | Maps to |
|------|---------------------------------------------------------|---------|
| A    | `gofmt -l .`                                            | NFR-5   |
| A    | `go vet ./...`                                          | NFR-5   |
| A    | `git diff --check`                                      | NFR-5   |
| B    | `go test -count=1 ./...`                                | §2–§4   |
| B    | `go test -race -count=1 ./...`                          | §2–§5   |
| B    | `make check`                                            | NFR-5   |
| C    | `go test -race -count=1 -run Reliability ./internal/cli`| §5     |
| D    | `NEUROFORGE_SMOKE=opencode go test -count=1 -run Smoke ./internal/cli` | §6 |
| E    | (human review per REVIEW_CHECKLIST.md)                  | —       |

---

## 9. What is explicitly NOT tested here (and why)

- Autonomous scheduling, failover, post-merge, review, merge, image providers,
  visual harness, TUI — out of scope (REQUIREMENTS.md §0.2). Existing tests
  for those subsystems are not touched.
- A real OpenCode run in the default suite — by design (rule §36.5); covered
  by the opt-in smoke test only.
