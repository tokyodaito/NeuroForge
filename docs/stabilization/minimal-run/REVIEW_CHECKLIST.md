# REVIEW_CHECKLIST.md — Independent Reviewer (Gate E)

This is the checklist the **independent reviewer** (a third agent, not the
implementer) uses to decide whether the `forge run` stabilization track is
done. The reviewer does **not** trust the implementer's self-reported PASS.

The reviewer's mandate:
1. Reproduce each claim by running the cited command, not by reading the
   implementer's report.
2. Actively look for **false PASS** — tests that pass for the wrong reason,
   missing assertions, mocked-out reality, scope creep, or reverted checks.
3. Refuse to flip an `ACCEPTANCE_MATRIX.md` row to `PASS` without seeing the
   green run and reading the test body.
4. Confirm no production-code changes leaked outside the slice's allowed
   paths, and no scope was silently expanded (REQUIREMENTS.md §0.2).

> Tools the reviewer is expected to use: `git`, `go test`, `go vet`,
> `gofmt`, `make`, a shell, an actual `opencode` install (for Gate D only).
> All commands below are run from the repo root on the branch under review.

---

## 0. Pre-review (do this first)

- [ ] Read `REQUIREMENTS.md`, `STATE_MACHINE.md`, `OUTCOME_CONTRACT.md`,
      `TEST_PLAN.md`, `ACCEPTANCE_MATRIX.md`, `KNOWN_FAILURES.md`,
      `IMPLEMENTATION_SLICES.md`.
- [ ] `git log --oneline base..HEAD` — confirm the change is a stack of
      small slice-shaped commits, not one mega-commit.
- [ ] `git diff --stat base..HEAD` — confirm only allowed paths changed per
      `IMPLEMENTATION_SLICES.md`. **Flag** any edit to:
      `internal/adapter/codingagent/protocol/` (frozen),
      `docs/spec/NEUROFORGE_SPEC.md` (out of scope),
      TUI files, scheduler/failover/postmerge/review/merge packages (must be
      bypassed, not rewritten — unless the slice explicitly allows it).
- [ ] `git diff base..HEAD -- docs/spec/NEUROFORGE_SPEC.md` — must be **empty**.
- [ ] Confirm **no** merge commit into `main`, **no** push, **no** PR was
      created (`git branch -r --contains HEAD` should be empty or only show
      the feature branch remote, never `origin/main`).

---

## Gate A — Static hygiene

Run exactly these. All must be clean.

- [ ] `gofmt -l .` → **empty output**.
- [ ] `go vet ./...` → **exit 0, no complaints**.
- [ ] `git diff --check` → **clean** (no whitespace errors).
- [ ] `go build ./...` → **exit 0**.
- [ ] `make build` → produces `./forge`.

---

## Gate B — Tests

- [ ] `go test -count=1 ./...` → **all PASS**.
- [ ] `go test -race -count=1 ./...` → **all PASS, zero DATA RACE reports**.
- [ ] `make check` → **exit 0**.
- [ ] Confirm the **opt-in smoke test is NOT triggered** by the default
      suite: `go test -count=1 -run Smoke ./internal/cli/...` prints SKIP
      unless `NEUROFORGE_SMOKE=opencode` is set.

If any of the above is red, the review stops here: status is **FAIL**.

---

## Gate C — Reliability (the core anti-flake gate)

- [ ] `go test -race -count=1 -run 'Reliability' ./internal/cli/...` →
      **PASS**.
- [ ] Open the test body. Confirm:
  - [ ] it loops **≥ 10** iterations,
  - [ ] each iteration uses a **fresh** temp repo + temp home (no state
        leakage between iterations),
  - [ ] each iteration asserts: exit 0, `outcome=completed-with-commit`,
        `actual_head_sha != base_sha`, `git rev-parse HEAD` (in the worktree)
        equals `actual_head_sha`, workspace is terminal, **zero** `active`
        workspaces remain,
  - [ ] it fails on the **first** mismatch (does not silently count
        pass/fail),
  - [ ] on failure it dumps the artifacts in TEST_PLAN.md §7 and prints
        their paths.
- [ ] Re-run the reliability test **3 times in a row**. All three must be
      green. (One green run is not evidence of reliability.)

---

## Gate D — Real-adapter smoke (only if an `opencode` CLI is available)

> Skip this gate if no real `opencode` is installed; record
> "Gate D: not exercised (no real adapter)" in the verdict. Do **not** mark
> D rows PASS without running it.

- [ ] `NEUROFORGE_SMOKE=opencode go test -count=1 -run Smoke ./internal/cli/...`
      → **PASS**.
- [ ] Open the test body. Confirm it:
  - [ ] runs `forge run "Create RESULT.md ... and make a local git commit"`
        (or equivalent) in a temp repo,
  - [ ] asserts `engine == "opencode"` and the requested model,
  - [ ] asserts a **real** commit exists (`actual_head_sha != base_sha`,
        `RESULT.md` present in the worktree),
  - [ ] asserts the workspace is terminal `completed`,
  - [ ] asserts the **primary checkout is untouched** (HEAD + file set
        identical),
  - [ ] asserts **no push / no PR / no merge / no remote ref** was created
        (`git -C <repo> branch -r` empty; `git -C <repo> for-each-ref refs/remotes/`
        empty).
- [ ] Inspect `git -C <repo> reflog show HEAD` for the primary and confirm
      NeuroForge never rewrote it.

---

## Anti-false-PASS probes (do every one of these)

These target the specific ways this track can *appear* green while being
broken. Each probe must fail on the **old** code and pass on the new code.

### P-01 — The classifier is not a tautology
- [ ] Read `Classify`. Confirm it branches on `actualHEAD != baseSHA` and on
      `git status --porcelain`, **not** on the adapter's terminal event
      alone.
- [ ] Force a fake run that emits `run.completed` and writes nothing.
      Confirm the outcome is `completed-no-changes` with a **non-zero**
      exit. (Reproduces KF-01/KF-05.)

### P-02 — `head_sha` is really read from Git
- [ ] In a live run, corrupt the workspace's `head_sha` DB column to a fake
      value **after** the run but **before** finalize (or use a test seam).
      Confirm the reported `actual_head_sha` is `git rev-parse HEAD`, not
      the DB value. (Reproduces KF-02/KF-04.)

### P-03 — Workspace is really terminal
- [ ] After a successful `forge run`, hit `GET /workspaces/{id}` and confirm
      `state` ∈ {`completed`,`failed`,`cancelled`,`timed_out`} — **never**
      `active`. (Reproduces KF-03.)
- [ ] List all workspaces for the home: assert **zero** `active` rows after
      any terminal run.

### P-04 — Task is really terminal
- [ ] After a successful run, the task is `COMPLETED`; after a no-change
      run, the task is `FAILED` (not `NEW`). (Reproduces KF-06.)

### P-05 — Result ref is the full form
- [ ] `git -C <repo> for-each-ref refs/heads/forge/result/<task>` resolves.
- [ ] Confirm the implementation calls `git update-ref` with the literal
      `refs/heads/forge/result/<task-id>` argument (grep the diff). (Reproduces
      KF-07/KF-08.)
- [ ] Run `forge run` twice for the same task; confirm a **single** ref
      (idempotent), no duplicate.

### P-06 — Cancellation really wins
- [ ] Read the supervisor's terminal decision path. Confirm it is owned by
      one goroutine / one `once` per run id.
- [ ] Run the gemini cancellation test under `-race` **20+ times**
      (`go test -race -count=20 -run GeminiCancel ./...` or the dedicated
      U-15). Zero races, zero `run.failed`-after-cancel. (Reproduces KF-09.)
- [ ] Audit the **other five** adapter `supervise` loops for the same race
      pattern; if any shares it, confirm it was fixed, not just gemini.

### P-07 — Timeout is not cancellation
- [ ] `forge run --timeout 1s` against a blocking fake → exit **124**,
      `outcome=timed-out` (not `cancelled`).

### P-08 — `--json` is one document
- [ ] `forge run --json ... 2>/dev/null | python -m json.tool` (or `jq .`)
      parses to exactly one object. Stderr is where the progress text went.
- [ ] Confirm the JSON has the **full fixed field set** from
      OUTCOME_CONTRACT.md §3 (no field omitted; null where not applicable).

### P-09 — LOCAL_REVIEW wall is real
- [ ] `git -C <repo> branch -r` empty; `git -C <repo> remote` empty (or
      unchanged) after a run.
- [ ] Inspect the agent process env in a test: confirm no
      `*TOKEN*`, `*SECRET*`, `FORGE_DAEMON_TOKEN` is forwarded (AC-28).
- [ ] Grep the diff for any new `git push|fetch|pull|clone|ls-remote|
      send-pack|fetch-pack` — expect **zero** in the run path.

### P-10 — Primary checkout is untouched
- [ ] Snapshot `git -C <repo> rev-parse HEAD` and the file set **before**
      and **after** a run; they must be byte-identical.

### P-11 — Validation errors create no state
- [ ] `forge run` (no prompt) in a fresh home; then inspect the DB: zero
      task rows, zero workspace rows. Exit code 2.

### P-12 — Daemon autostart is race-clean
- [ ] From a cold home, run **two** `forge run` concurrently. After they
      finish, assert exactly **one** daemon pid owns the pidfile and there
      is exactly one daemon process (`pgrep -f "forge daemon run"`).

### P-13 — No scope creep / no silent deletion
- [ ] `git diff --stat base..HEAD` does **not** delete
      `internal/scheduler/`, `internal/supervisor/failover*`,
      `internal/postmerge/`, `internal/review/`, `internal/merge/` packages.
- [ ] The `forge workspace ...` and `forge task dispatch` commands still
      build and their existing tests still pass (they are bypassed by
      `forge run`, not removed).

---

## Verdict form (the reviewer fills this)

| | |
|--|--|
| Gate A (static)        | PASS / FAIL |
| Gate B (tests + race)  | PASS / FAIL |
| Gate C (reliability ×3)| PASS / FAIL |
| Gate D (real smoke)    | PASS / FAIL / NOT EXERCISED |
| Anti-false-PASS P-01..P-13 | (list any FAIL) |
| Scope/safety audit     | PASS / FAIL |

**Overall verdict:** `ACCEPTED` / `REJECTED` / `ACCEPTED WITH FOLLOW-UPS`.

For each `ACCEPTANCE_MATRIX.md` row the reviewer either leaves `NOT IMPLEMENTED`
/ `PARTIAL` / sets `PASS`, or sets `FAIL` with a one-line reason. A row the
reviewer did not personally observe green **stays not-PASS**.

The reviewer signs the report with: base SHA reviewed, head SHA reviewed,
date, and the exact commands they ran (pasted output optional but
encouraged for any FAIL).

---

## What the reviewer must never accept

- "The test passes on my machine" without a reproducible command.
- A green test whose body does not assert the invariant (e.g. a reliability
  test that loops 10× but does not check `head_sha` or terminal state).
- A `forge run` that reports success while the worktree is clean.
- A workspace left `active` after the run, for any reason.
- A cancellation that ends as `failed`.
- A `--json` output mixed with progress text on stdout.
- Any push, PR, merge into `main`, or change to `docs/spec/NEUROFORGE_SPEC.md`.
- Any silent removal of an existing subsystem.
