# M14-03 Independent Review (re-review after remediation)

## Review identity

- reviewer actor/session ID: `M14-03-rereview-session`
- implementation actor (original candidate): `M14-03-impl-session`
- remediation actor: `M14-03-remediation-session`
- independence confirmed: **yes** — fresh session, no shared context with either
  the original implementation or the remediation actor; this reviewer authored no
  production/test code in this task and is not the acceptor.
- review date: 2026-07-27
- review type: **re-review** of the remediated candidate, addressing the two
  MAJOR findings raised by the prior review (commit `9859c08`, verdict
  `CHANGES_REQUESTED`). The prior review report is preserved in git history at
  `9859c08`; this file supersedes it for the current candidate.

## Git baseline

- accepted predecessor SHA: `5ed8b72f97d0ea0dbf5a762f7aec8bd15cf1c2b8`
  (`M14-02: add acceptance evidence`)
- original candidate SHA: `78d1ff170d925de4ce5e319ddf7c272b2d261d37`
  (`M14-03: Task Compiler production API, CLI and restart flow`) — reviewed at
  `9859c08`, verdict `CHANGES_REQUESTED` (MAJOR-1 TOCTOU concurrency, MAJOR-2
  under-tested semantic equality + unreachable "differs" branches).
- **candidate SHA under review (remediated):** `a9869f9d15f2f717abcebd6018abbc61de11a3cf`
  (`M14-03: address review findings`) — current `HEAD` of `main`.
- ancestry verified:
  - `git merge-base --is-ancestor 5ed8b72 a9869f9` → exit 0 (candidate is a
    direct descendant of the accepted M14-02 tip).
  - `git merge-base --is-ancestor 78d1ff1 a9869f9` → exit 0 (remediation is a
    direct descendant of the original candidate).
  - commit chain `5ed8b72 → 78d1ff1 → 9859c08 → a9869f9` (linear on `main`).
- review worktree held at the exact candidate SHA `a9869f9` throughout. The only
  working-tree noise (`docs/reviews/MINIMAL_RUN_*`, `docs/reviews/M12_M13_REVIEW.md`)
  predates this task and was not touched.

## Predecessor gate

- manifest: `docs/reviews/m14/M14-02.manifest.json` (state `ACCEPTED`,
  baseline_version `1`).
- gate commands (compiled candidate binary `/tmp/forge-m14-03-review`):

| Command | Exit | Result |
|---|---:|---|
| `/tmp/forge-m14-03-review gate baseline` | 0 | active baseline v1, schema v1 |
| `/tmp/forge-m14-03-review gate validate --manifest docs/reviews/m14/M14-02.manifest.json` | 0 | M14-02 transition REVIEW_APPROVED → ACCEPTED legal |
| `/tmp/forge-m14-03-review gate next --manifest docs/reviews/m14/M14-02.manifest.json` | 0 | predecessor ACCEPTED; successor may start |

Predecessor gate is **OPEN**.

## Scope reviewed

Full diff `5ed8b72..a9869f9` (18 files, +4519/−246). The remediation delta
(`78d1ff1..a9869f9`, 8 files, +1950/−121) was the focus; the original wiring
diff (`5ed8b72..78d1ff1`) was re-read for context.

Production files reviewed line-by-line at the candidate SHA:
- `internal/task/specification.go` (637 lines) — `compileLocks` keyed per-task
  mutex (lines 232–274), `SaveIfChanged` (lines 380–431),
  `specificationsSemanticallyEqual` + helpers (lines 572–637).
- `internal/daemon/spec_api.go` (261 lines) — simplified `CompileSpec` now
  delegates compare-and-save to `SaveIfChanged` (line 111); the dead "replace in
  place" branch is gone; the dead "differs | locked → mint new version" branch is
  gone (now both collapse into the single "differs → new version" fallback).
- `internal/daemon/daemon.go` (line 136, 212), `internal/daemon/api.go`
  (line 31) — wiring (single `SpecificationStore` constructed in `NewServices`,
  shared by all goroutines/adapters → one keyed-lock registry).
- `internal/task/backlog.go` (lines 226–239) — `Backlog.Get` `sql.ErrNoRows →
  ErrNotFound` defect fix.
- `internal/transport/api.go` (line 320) — `"is locked" → 409`.
- `internal/transport/server.go`, `internal/transport/spec_api.go`,
  `internal/transport/spec_client.go`, `internal/cli/spec_cmd.go`,
  `internal/cli/help.go` — unchanged from the original candidate (additive
  surfaces).

Test files reviewed:
- `internal/task/specification_semantic_equal_test.go` (319 lines, 30 table cases).
- `internal/task/specification_store_test.go` (+263 lines: 5 `SaveIfChanged_*`
  tests incl. the 40-goroutine concurrency regression).
- `internal/daemon/spec_api_test.go` (+321 lines: 4 concurrency tests through the
  real transport, incl. the 30-goroutine regression and the audit-count test).
- `internal/cli/spec_save_blackbox_test.go` (+156 lines: compiled-binary
  20-process concurrency regression with restart).

Out-of-scope verification:
- No changes to `docs/spec/`, `docs/engineering/`, gate/baseline enforcement.
- No M14-04 / workgraph / scheduler / merge scope creep.
- No new external dependencies (`go.mod`/`go.sum` unchanged).
- No `TODO`/`FIXME`/`panic("unimplemented")`/stub in new production files.

## Resolution of the prior MAJOR findings

### MAJOR-1 (TOCTOU concurrency) — RESOLVED

**Original defect:** `CompileSpec` performed `GetLatest → compare → Save` as
three non-atomic steps. 20 concurrent identical compiles minted up to 7 versions.

**Fix at the candidate:** the compare-and-mint critical section was moved into
`task.SpecificationStore.SaveIfChanged` (`internal/task/specification.go:409`),
which acquires a **per-task keyed mutex** (`compileLocks`, lines 232–274) around
`GetLatest → specificationsSemanticallyEqual → Save`. The compile step
(`task.Compile`, pure CPU) runs OUTSIDE the critical section so unrelated tasks
are not blocked. `CompileSpec` now only calls `SaveIfChanged`
(`internal/daemon/spec_api.go:111`); the only production caller of the spec
store on the compile path.

**Correctness of the keyed mutex** (`compileLocks.acquire`/`release`):
- `acquire` (line 249) increments the entry's waiter count under the coordinator
  mutex *before* blocking on the per-task mutex, so the entry is not removed
  while a new caller waits on it.
- `release` (line 265) decrements under the coordinator mutex and deletes the
  entry when the count reaches zero; the per-task mutex is unlocked after the
  coordinator mutex is released, operating on the local entry pointer (a
  concurrent `acquire` that misses the entry creates a fresh one — independent
  mutex, no deadlock, no race). Verified by reading the code.
- Entries are reference-counted → no unbounded growth with historical task count.
- Production composition constructs exactly one `SpecificationStore`
  (`internal/daemon/api.go:31`) and thus one lock registry shared by all
  goroutines. Confirmed: `grep Specs\.\(Save\|SaveIfChanged\)` → only
  `SaveIfChanged` at `spec_api.go:111`; no production path calls `Save` directly
  and bypasses the lock.

**Independent verification performed:**

| Probe | What | Result |
|---|---|---|
| Shipped store test ×20 with `-race` | `TestSaveIfChanged_ConcurrentIdenticalCreatesSingleVersion` (40 goroutines, production store) | 20/20 PASS, no duplicate versions |
| Shipped daemon test ×5 with `-race` | `TestSpecAdapter_ConcurrentIdenticalCompileCreatesSingleVersion` (30 goroutines through real HTTP transport) | 5/5 PASS |
| Independent reviewer probe ×10 with `-race` | 60 goroutines on one task (`internal/task/spec_review_probe_test.go`, temporary, deleted after) | 10/10 PASS, `created` count == 1, versions == `[1]` |
| Compiled-binary black-box | `TestSpecSave_BlackBox_ConcurrentIdenticalSaveCreatesSingleVersion` (20 separate `forge` processes + restart + repeat) | PASS, versions == `[1]` |
| Independent manual black-box | 20 concurrent `forge spec save` processes against one task | versions == `[1]` |
| **Sensitivity check (mutation)** | Disable `s.locks.acquire/release` in `SaveIfChanged`, re-run probes | **CAUGHT** — reproduced 12 duplicate versions `[1 2 3 4 5 6 7 8 9 10 11 12]`; both the shipped test and the independent probe failed |

The sensitivity check proves the keyed mutex is load-bearing and the regression
tests genuinely catch the TOCTOU. MAJOR-1 is **CLOSED**.

### MAJOR-2 (semantic equality under-tested + dead branches) — RESOLVED

**Original defect:** (a) `specificationsSemanticallyEqual` was only exercised on
the "equal" path; dropping the `Objective` comparison did not fail any test.
(b) The decision-table "differs | unlocked → replace in place" and "differs |
locked → mint new version" branches were unreachable aspirational dead code.

**Fix at the candidate:**
- (a) `specificationsSemanticallyEqual` was moved to the `task` package
  (`internal/task/specification.go:589`) and given `TestSpecificationsSemanticallyEqual`
  (`internal/task/specification_semantic_equal_test.go`) — a 30-case table-driven
  test covering every compared field (Objective, Risk, Complexity, NonGoals,
  Assumptions, Constraints, ProposedScope, each AC ID + statement, AC count +
  order, VisualRequirements.Required/Viewport/Theme/Locale/Density/References
  count + content) and every persistence-only field that must NOT break equality
  (Version, CreatedAt, Locked, LockedBy, LockedAt, CreatedBy, TaskID).
- (b) Chose remediation **B1**: the unreachable "replace in place" branch was
  deleted from `SaveIfChanged`. A genuine difference now defensively mints a new
  version (`spec.Version = 0`, line 421) — safe, no history loss. The
  implementation report's decision table was rewritten to describe the honest
  contract; the "differs" row is now genuinely exercised by
  `TestSaveIfChanged_ChangedInputMintsNewVersion` (unit, direct) and documented
  as a defensive fallback (FU-M14-03-5 tracks a future public recompile API).

**Independent verification performed:**

| Probe | What | Result |
|---|---|---|
| Shipped equality test | `TestSpecificationsSemanticallyEqual` (30 cases) | PASS |
| Shipped "differs" test | `TestSaveIfChanged_ChangedInputMintsNewVersion` (changed objective → v2, both durable) | PASS |
| Shipped locked+equal test | `TestSaveIfChanged_IdempotentAfterLock` (equal content after lock → created=false, locked v1 unchanged) | PASS |
| **Sensitivity check (mutation)** | Drop `Objective` comparison from `specificationsSemanticallyEqual`, re-run | **CAUGHT** — `TestSpecificationsSemanticallyEqual/different_Objective_is_NOT_equal` failed (equality returned `true`) |

The sensitivity check proves the equality test now catches field-comparison
mutations. The "differs" branch is no longer dead — it is directly exercised by
`TestSaveIfChanged_ChangedInputMintsNewVersion`. MAJOR-2 is **CLOSED**.

## Acceptance matrix

| Criterion | Production implementation | Automated evidence | Independent result | Status |
|---|---|---|---|---|
| **AC-1** compiled spec via real CLI/API | `specAPIAdapter.CompileSpec` (`internal/daemon/spec_api.go:69`) → `task.Compile` → `SaveIfChanged`; transport `POST /tasks/{id}/specification/compile`; CLI `forge spec save`/`show` | `TestSpecSave_BlackBox_CreateCompileShowLockRestart`, transport contract suite (12), daemon integration suite | Reproduced via compiled `/tmp/forge-m14-03-review`: project add → task add → `spec save` (created=true, v1) → `spec show` (v1). Real path `CLI → HTTP → adapter → task.Compile → store → SQLite`. | **MET** |
| **AC-2** survives daemon restart | All spec state in SQLite (migration v8, applied by `daemon.Run`); no in-memory read cache | `TestSpecAdapter_PersistsAcrossRestart`, `TestSpecSave_BlackBox_CreateCompileShowLockRestart` steps 9–11 | Reproduced: `daemon stop` → `daemon start` → `spec show` returned same v1, same objective, Locked=true, LockedBy=alice. Lock provenance survives. | **MET** |
| **AC-3** idempotent (single-threaded) | `SaveIfChanged` semantic equality → return latest unchanged (created=false, no Save, no audit) | `TestSaveIfChanged_IdempotentReuse`, `TestSpecAdapter_CompileAndGetAndLock` step 2, black-box step 6 | Reproduced: second `spec save` → created=false, v1; `spec versions` == `[1]`. After lock: still created=false, locked v1. After restart: still created=false. | **MET** |
| **AC-3** idempotent (concurrent) | `SaveIfChanged` keyed per-task mutex serialises compare-and-mint | `TestSaveIfChanged_ConcurrentIdenticalCreatesSingleVersion` (40 goroutines), `TestSpecAdapter_ConcurrentIdenticalCompileCreatesSingleVersion` (30), `TestSpecSave_BlackBox_ConcurrentIdenticalSaveCreatesSingleVersion` (20 procs), `TestSpecAdapter_ConcurrentIdempotentReuseAudit` (audit count==1) | Reproduced: 60-goroutine independent probe (10× -race), 30-goroutine transport test (5× -race), 20-process black-box — all yield exactly `[1]`, created count == 1. Sensitivity check (disable lock) reproduced 12 duplicates. | **MET** |
| **AC-4** black-box evidence (not unit-only) | real compiled `forge` binary driving real daemon against isolated HOME | 6 compiled-binary tests (lifecycle, invalid task, locked update, text output, flag validation, concurrent) | Independently reproduced the full lifecycle and the concurrent case via `/tmp/forge-m14-03-review` against `$(mktemp -d)` HOMEs. | **MET** |
| **AC-5** HTTP/CLI error semantics | `writeAPIError` "not found"→404, "is locked"→409 (`internal/transport/api.go:320`); `parseIntStrict` rejects bad/overflow `?version`→400; `Backlog.Get` `sql.ErrNoRows→ErrNotFound` (`internal/task/backlog.go:234`) | `TestSpecAPI_*NotFound*`, `TestSpecAPI_LockedErrorMaps409`, `TestSpecAPI_Get_InvalidVersionParam`, `TestSpecSave_BlackBox_InvalidTask`, `TestSpecSave_BlackBox_FlagValidation` | Reproduced: missing task → 404 `{"error":"task not found"}` / CLI exit 1; missing spec → 404; `spec versions` on missing task → `[]` exit 0. `Backlog.Get` fix is load-bearing (sensitivity: removing it regresses the missing-task 404). | **MET** |
| **AC-6** locked-version invariant | `SpecificationStore.Save` rejects locked-version mutation with `ErrSpecificationLocked` (storage tx); `SaveIfChanged` equal+locked → return latest unchanged | `TestSpecificationStore_LockedVersionCannotBeMutated`, `TestSpecAdapter_LockedUpdateRejected`, `TestSpecSave_BlackBox_LockedSpecNoNewVersion`, `TestSaveIfChanged_IdempotentAfterLock` | Reproduced: lock v1 → `spec save` returns created=false, locked v1; direct `Save` on locked v1 → `ErrSpecificationLocked`; restart preserves lock. | **MET** |
| **AC-7** M14-02 compatibility (`forge spec compile` offline) | `specCompile` unchanged; new subcommands additive | `TestSpecCompile_BlackBox_*` (9), `TestCompile_Deterministic` ×10 | Reproduced via compiled binary: offline compile + determinism unaffected. | **MET** |
| **AC-8** honest wiring (no fake/fallback) | real compiler, real store, real SQLite, real transport; nil `Specs` → explicit error | shipped suites + sensitivity (skip `SaveIfChanged` → tests fail) | Verified: `CompileSpec` always calls `SaveIfChanged`; no fake-store fallback. | **MET** |

Required test classes (task brief):

| Required class | Covered | Evidence |
|---|---|---|
| Transport contract tests | yes | `internal/transport/spec_api_test.go` (12 tests) |
| Black-box create→compile→show→lock→restart→show | yes | `TestSpecSave_BlackBox_CreateCompileShowLockRestart` (43 assertions) + independent manual reproduction |
| Invalid task / locked update / duplicate request | yes | `TestSpecSave_BlackBox_InvalidTask`, `TestSpecAdapter_LockedUpdateRejected` / `TestSpecSave_BlackBox_LockedSpecNoNewVersion`, `TestSaveIfChanged_IdempotentReuse` / concurrent variants |
| `make check` + race tests | yes | both green (see Commands) |

## Commands executed (independent run)

| Command | Exit | Result |
|---|---:|---|
| `go build -o /tmp/forge-m14-03-review ./cmd/forge` | 0 | builds cleanly |
| `/tmp/forge-m14-03-review gate baseline` | 0 | baseline v1 |
| `/tmp/forge-m14-03-review gate validate --manifest docs/reviews/m14/M14-02.manifest.json` | 0 | transition legal |
| `/tmp/forge-m14-03-review gate next --manifest docs/reviews/m14/M14-02.manifest.json` | 0 | predecessor ACCEPTED |
| `go test -count=1 -run 'TestSpecAPI|TestSpecAdapter|TestSpecSave|TestSpecificationsSemanticallyEqual|TestSaveIfChanged|TestSpecificationStore' ./internal/transport/ ./internal/daemon/ ./internal/cli/ ./internal/task/` | 0 | all PASS (transport, daemon, cli, task) |
| `go test -race -count=20 -run 'TestSaveIfChanged_ConcurrentIdenticalCreatesSingleVersion' ./internal/task/` | 0 | 20× PASS, no duplicates |
| `go test -race -count=5 -run 'TestSpecAdapter_ConcurrentIdenticalCompileCreatesSingleVersion' ./internal/daemon/` | 0 | 5× PASS |
| `go test -race -count=10 -run 'TestReviewProbe_ConcurrentIdenticalSaveIfChanged' ./internal/task/` (temporary probe, 60 goroutines) | 0 | 10× PASS, created==1, `[1]` |
| `go test -count=15 -run 'TestSpecAdapter_ConcurrentIdempotentReuseAudit' ./internal/daemon/` | 0 | 15× PASS (audit count==1) |
| `go test -count=1 -run 'TestSpecSave_BlackBox' -timeout 240s ./internal/cli/` | 0 | 6/6 compiled-binary black-box tests PASS |
| `go test -count=1 -run 'TestSpecCompile_BlackBox' ./internal/cli/` | 0 | M14-02 offline regression PASS |
| `go test -count=10 -run 'TestCompile_Deterministic$' ./internal/task/` | 0 | 10× PASS |
| `make check` | 0 | gofmt clean; `go vet ./...` clean; FAIL_COUNT 0 |
| `go test -race ./...` | 0 | every package ok; no race detected |
| Manual black-box lifecycle (compiled binary, isolated HOME) | 0 | create→save#1(created,v1)→save#2(idempotent)→show→lock(by alice)→restart→show(v1,locked,alice)→save#3(idempotent,locked); 20 concurrent saves→`[1]`; missing task→404 | 

Sensitivity checks (mutations applied to a local copy, measured, then fully
reverted; working tree verified clean — `git diff HEAD -- internal/` empty —
before writing this report):

| Mutation | Expected regression | Independent result |
|---|---|---|
| Disable `s.locks.acquire/release` in `SaveIfChanged` | TOCTOU duplicates return | **CAUGHT** — 12 duplicate versions `[1..12]`; shipped test + independent probe both failed |
| Drop `Objective` comparison from `specificationsSemanticallyEqual` | changed-objective spec treated as idempotent | **CAUGHT** — `TestSpecificationsSemanticallyEqual/different_Objective_is_NOT_equal` failed |

Toolchain: `go version go1.26.5 darwin/arm64`. No skipped/manual tests in the
targeted evidence set (the black-box tests skip only under `-short`, which is not
the default; they all ran).

## Findings

### No BLOCKER findings.

### No MAJOR findings.

The two MAJOR findings from the prior review (`9859c08`) are resolved with
regression tests that this reviewer independently confirmed catch the defects
(via sensitivity checks). No new MAJOR findings were identified.

### MINOR findings

#### MINOR-1 — Unnecessary defensive `time.Sleep` in the audit-count test
- **Location:** `internal/daemon/spec_api_test.go`, in
  `TestSpecAdapter_ConcurrentIdempotentReuseAudit` (`time.Sleep(100 *
  time.Millisecond)` after `wg.Wait()`).
- **Observation:** the comment justifies the sleep as "the test reads from a
  separate connection". But the audit append uses `audit.RecordTx`
  (`internal/audit/audit.go:77`), which joins the caller's SQLite transaction;
  the audit event commits atomically with the spec save inside `Save`. After
  `wg.Wait()` every winner transaction is committed, and a fresh read
  connection sees committed rows immediately (no WAL required). The sleep is
  therefore unnecessary.
- **Proof it is not masking a real race:** the test passed 15/15 consecutive
  runs in this review. The transactional audit write is verified by reading
  `audit.RecordTx` and by `TestRecordTx_JoinsCallerTransaction`.
- **Required fix (optional):** remove the `time.Sleep` and rely on the
  transactional commit guarantee; or, if a read-visibility concern is ever
  proven, document the concrete cause. Non-blocking — the test is reliable as
  shipped. (Engineering baseline: "Не маскируй flaky tests sleeps без доказанной
  причины" — no flakiness is being masked here, but the sleep has no proven
  reason either.)

#### MINOR-2 — `spec show` on a missing task returns "specification not found", not "task not found" (pre-existing, tracked)
- **Location:** `internal/daemon/spec_api.go:GetSpecification` (read path does
  not pre-check task existence; it goes straight to `SpecificationStore.GetLatest`,
  which returns `ErrSpecificationNotFound`).
- **Observation:** for a missing task, `spec save` (compile) returns
  `{"error":"task not found"}` (it loads the task first), but `spec show`
  returns `{"error":"specification not found"}`. Both are HTTP 404, so the CLI
  contract (exit 1, readable message) holds.
- **Status:** already tracked as FU-M14-03-4 (typed GET errors). Confirmed
  unchanged by the remediation. Non-blocking.

#### MINOR-3 — `compileLocks.release` would panic on a double-release (latent)
- **Location:** `internal/task/specification.go:265` — `e := c.items[taskID]` is
  dereferenced without a nil check.
- **Observation:** under correct usage `release` is only ever invoked via
  `defer` after a successful `acquire`, so the entry always exists. A future
  misuse (double release, or release-without-acquire) would panic with a nil
  pointer dereference rather than a clear error.
- **Status:** latent-only; not reachable by any current caller. Noting for
  completeness; no fix required for M14-03.

## Scope and documentation assessment

- Scope is bounded to M14-03. No M14-04 / workgraph / scheduler / merge changes.
  No product-spec change. No baseline/gate weakening. No new dependencies.
- Code is honest: real compiler, real store, real SQLite, real daemon transport;
  no fixture data in production code, no fake-store fallback, no
  success-without-`SaveIfChanged` handler, no second compiler implementation.
  The `fakeSpecAPI` lives only in the transport contract test (mirrors the
  established `fakeProjectAPI`/`fakeTaskAPI` pattern) and is not the only
  evidence.
- `Backlog.Get` defect fix is minimal, correct, and regression-tested; no other
  caller relied on the old wrapping (the two `Tasks.Get` callers —
  `internal/daemon/api.go:171` and `internal/daemon/spec_api.go:79` — both
  propagate to the transport, which needs the clean "not found").
- The remediation correctly narrowed the implementation report's decision table
  to the honest contract (the "differs → replace in place" row was removed;
  FU-M14-03-5 tracks a future public recompile API). No claim exceeds proven
  scope.
- FU-M14-03-1 (TUI bindings), FU-M14-03-2 (compile-on-create flag), FU-M14-03-3
  (order-independent AC equality), FU-M14-03-4 (typed GET errors), FU-M14-03-5
  (public recompile API), FU-M14-03-6 (multi-daemon transactional
  compare-and-create) remain reasonable non-blocking follow-ups. FU-M14-03-6 in
  particular is correctly scoped: the keyed mutex is process-local and NeuroForge
  runs a single daemon per database, so it is not a practical limitation today.

## Counterexample search

The reviewer actively sought a state in which the tests are green but a mandatory
requirement is unmet:

1. **Concurrency counterexample:** wrote an independent 60-goroutine probe (not a
   copy of the shipped test) and ran it 10× with `-race`. Could not reproduce a
   duplicate. Disabling the lock *did* reproduce 12 duplicates, proving the lock
   is the effective guard and the tests are sensitive to it.
2. **"Differs" branch counterexample:** the original dead branch is removed; the
   remaining "differs → new version" fallback is directly exercised by
   `TestSaveIfChanged_ChangedInputMintsNewVersion`. No silent content loss path
   remains.
3. **Equality-mutation counterexample:** dropping any single field comparison
   fails the equality test (proven for `Objective`; the 30-case table exercises
   every field analogously).
4. **Restart counterexample:** could not bypass restart persistence — every read
   goes through `SpecificationStore` → SQLite; no in-memory cache exists on the
   read path.
5. **Lock-bypass counterexample:** a concurrent compile cannot corrupt a locked
   snapshot — `Lock` targets an explicit `(taskID, version)` transactionally,
   and `SaveIfChanged`'s worst case is minting an unlocked version, which never
   overwrites a locked row.

No counterexample succeeded.

## Verdict

**REVIEW_APPROVED**

Rationale:

- The predecessor gate is open (M14-02 ACCEPTED, baseline v1) and the candidate
  is the exact reviewed SHA `a9869f9…`, a direct descendant of the accepted
  M14-02 tip and of the original candidate `78d1ff1…`.
- Actor independence holds (this reviewer = `M14-03-rereview-session`, distinct
  from both the implementation and remediation actors).
- **Both MAJOR findings from the prior review (`9859c08`) are resolved:**
  - MAJOR-1 (TOCTOU): `SaveIfChanged` now serialises compare-and-mint per task;
    verified by shipped tests (×20/×5 with `-race`), an independent 60-goroutine
    probe (×10), the compiled-binary black-box, and a sensitivity check that
    reproduced 12 duplicates when the lock is disabled.
  - MAJOR-2 (under-tested equality + dead branches): the equality function has a
    30-case table test (sensitivity-confirmed) and the dead "replace in place"
    branch is removed; the honest "differs → new version" contract is directly
    exercised.
- All eight mandatory acceptance criteria are proven by automated evidence,
  including the black-box requirement (compiled-binary lifecycle, restart
  recovery, concurrency, error mapping, M14-02 regression). Every claim was
  independently reproduced through the real `forge` binary against isolated
  `NEUROFORGE_HOME`s.
- `make check` is green (FAIL_COUNT 0); `go test -race ./...` is green (no race);
  gofmt + `go vet` clean.
- No BLOCKER or MAJOR findings remain. The three MINOR findings are non-blocking
  (one unnecessary defensive sleep that masks nothing; one pre-existing tracked
  GET-error-typing follow-up; one latent double-release guard).

`REVIEW_APPROVED` is permitted: every mandatory acceptance criterion is backed
by passing automated evidence, independently reproduced, with no unproven
mandatory criterion remaining. The candidate is eligible for a separate
acceptance session.
