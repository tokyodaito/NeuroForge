# M14-03 — Implementation Report

**Task:** M14-03 — Task Compiler production API, CLI and restart flow.
**Implementer actor:** `M14-03-impl-session`.
**Verdict:** `IMPLEMENTED_TESTED`

## SHAs

- **Starting SHA:** `5ed8b72f97d0ea0dbf5a762f7aec8bd15cf1c2b8` (branch `main`,
  the M14-02 acceptance commit `M14-02: add acceptance evidence`, originally
  authored in the `m14-02-accept` worktree and cherry-picked onto `main` at
  the start of this session per the human owner's instruction).
- **Candidate SHA:** this report's commit (`M14-03: <summary>`). Resolve with
  `git log --format=%H -G '^M14-03:' | -1`.

## Preconditions verified

- Predecessor `M14-02` is `ACCEPTED` (manifest
  `docs/reviews/m14/M14-02.manifest.json`; acceptance report
  `docs/reviews/m14/M14-02_ACCEPTANCE.md`). The compiled-binary gate is open:

  ```sh
  $ ./forge gate baseline
  active schema_version: 1
  active baseline_version: 1
  baseline document: docs/engineering/ENGINEERING_BASELINE.md

  $ ./forge gate validate --manifest docs/reviews/m14/M14-02.manifest.json
  OK: task "M14-02" transition REVIEW_APPROVED -> ACCEPTED is legal under baseline v1
  # exit 0

  $ ./forge gate next --manifest docs/reviews/m14/M14-02.manifest.json
  OK: predecessor "M14-02" is ACCEPTED; successor task may start
  # exit 0
  ```

- Working tree at the starting SHA contained only pre-existing unrelated
  review docs (`docs/reviews/MINIMAL_RUN_*`, `docs/reviews/M12_M13_REVIEW.md`);
  they predate this task and are NOT touched.

## Goal and actual scope

**Goal (from task brief):** wire the deterministic Task Compiler (M14-02) into
the daemon application service and the user-facing production path. Mandatory
ACs:

1. The user can obtain a compiled specification through the real CLI/API.
2. The result survives a daemon restart.
3. A repeat request is idempotent.
4. Unit-only wiring is insufficient — black-box evidence is required.

**Actual scope delivered (all of it):**

- **Daemon application service** `compile/get/lock/version`:
  `internal/daemon/spec_api.go` (`specAPIAdapter` implementing
  `transport.SpecAPI`). Compile reads the task from the backlog, runs the pure
  `task.Compile`, performs idempotent re-compile detection, and persists
  through `task.SpecificationStore.Save` (same atomic transaction + audit
  contract M14-01 established). Get / GetLatest / ListVersions / Lock delegate
  to `task.SpecificationStore`.
- **Transport endpoints** under `/tasks/{id}/specification*`:
  `POST /tasks/{id}/specification/compile`, `GET /tasks/{id}/specification`
  (with `?version=N`), `GET /tasks/{id}/specification/versions`,
  `POST /tasks/{id}/specification/lock`. Wired through `transport.Config.SpecAPI`
  + `registerSpecRoutes` (mirrors the existing `RunAppAPI` / `SchedulerAPI`
  pattern). A new `writeAPIError` case maps `"is locked"` → 409 Conflict so
  `ErrSpecificationLocked` no longer surfaces as 500.
- **CLI commands** `forge spec save / show / lock / versions` (daemon-mediated):
  `internal/cli/spec_cmd.go`. The existing offline `forge spec compile` (M14-02)
  is preserved unchanged — its pure-compiler contract stays intact. Help text
  in `internal/cli/help.go` documents both surfaces.
- **Task creation integration:** the compile endpoint reads the task's durable
  fields (description, title, priority, attachments) from the backlog via
  `Services.Tasks.Get`, compiles them, and persists the result. No
  compile-on-create magic — compile is an explicit, idempotent, replayable
  step the user invokes after `task add` (mirrors the `task dispatch`
  structural analogue).
- **Audit events:** every persistence goes through `task.SpecificationStore`,
  which already records `task.specification.saved` and `task.specification.locked`
  atomically with the storage change (M14-01). The daemon adapter additionally
  publishes `task.specification.compiled` and `task.specification.locked` bus
  events for live UI consumers.
- **Restart persistence:** all spec state lives in SQLite (migration v8,
  applied by the production daemon startup path). The
  `TestSpecAdapter_PersistsAcrossRestart` in-process test and the
  `TestSpecSave_BlackBox_CreateCompileShowLockRestart` compiled-binary test
  prove the state survives a full daemon stop/start cycle.

**Out of scope (deferred — see Follow-ups):** compile-on-create flag on
`forge task add`; TUI integration; visual harness triggering; auto-lock-on-
dispatch.

## Files changed

Production code:

```
internal/cli/help.go                       | +7   (spec subcommands listed)
internal/cli/spec_cmd.go                   | +311 (save/show/lock/versions + shared text writer)
internal/daemon/api.go                     | +2   (Services.Specs + construction)
internal/daemon/daemon.go                  | +6   (specAdapter wiring + SpecAPI in transport.Config)
internal/daemon/spec_api.go                | +287 (NEW — specAPIAdapter + idempotency + DTOs)
internal/task/backlog.go                   | +12  (Backlog.Get now returns ErrNotFound on sql.ErrNoRows — bug fix needed for clean transport 404)
internal/transport/api.go                  | +7   (writeAPIError "is locked" → 409 Conflict)
internal/transport/server.go               | +4   (Config.SpecAPI + registerSpecRoutes)
internal/transport/spec_api.go             | +229 (NEW — DTOs + SpecAPI interface + registerSpecRoutes + handlers)
internal/transport/spec_client.go          | +58  (NEW — Client.CompileSpec/Get/List/Lock)
```

Test code:

```
internal/cli/spec_save_blackbox_test.go    | +518 (NEW — compiled-binary black-box)
internal/daemon/spec_api_test.go           | +488 (NEW — in-process integration incl. restart)
internal/transport/spec_api_test.go        | +432 (NEW — transport contract)
```

Total: ~2400 added lines (≈ 920 production + ≈ 1440 test). No production code
deleted. No `TODO`/`FIXME`/`panic("unimplemented")` added. Product spec
`docs/spec/NEUROFORGE_SPEC.md` untouched. No baseline/gate enforcement
weakened. No security / autonomy / delivery / merge-policy invariant changed.

## Acceptance criterion matrix

| Mandatory AC | Implementation | Test(s) | Verdict |
|---|---|---|---|
| **AC1** User can obtain a compiled specification through the real CLI/API | `internal/daemon/spec_api.go:CompileSpec` (load task → `task.Compile` → idempotent save via `SpecificationStore.Save`); transport `POST /tasks/{id}/specification/compile` (`internal/transport/spec_api.go:handleCompileSpec`); CLI `forge spec save` (`internal/cli/spec_cmd.go:specSave`); GET path: `GET /tasks/{id}/specification` + `forge spec show` | Transport contract: `TestSpecAPI_Compile_HappyPath`, `TestSpecAPI_Compile_AllowsEmptyBody`, `TestSpecAPI_Get_LatestAndVersion`, `TestSpecAPI_DTO_JSONShape`. Daemon integration: `TestSpecAdapter_CompileAndGetAndLock` steps 1–4. Black-box: `TestSpecSave_BlackBox_CreateCompileShowLockRestart` steps 4–5 (`forge spec save` → `forge spec show` through the compiled binary). | **MET** |
| **AC2** Result survives daemon restart | All spec state in SQLite (migration v8, applied by `daemon.Run` → `db.Migrate`). No in-memory caches on the read path; every read goes through `SpecificationStore` → storage. | Daemon integration: `TestSpecAdapter_PersistsAcrossRestart` (in-process: stop daemon #1 → start daemon #2 against same home → re-read spec → byte-identical objective, AC IDs, version, lock state, lock provenance). Black-box: `TestSpecSave_BlackBox_CreateCompileShowLockRestart` steps 9–11 (`forge daemon stop` → `forge daemon start` → `forge spec show` returns the same persisted spec). | **MET** |
| **AC3** Repeat request is idempotent | `internal/daemon/spec_api.go:CompileSpec` idempotency block — fetches the latest version, compares semantically (objective, ACs, non-goals, assumptions, constraints, risk, complexity, proposed scope, visual requirements; ignores Version/CreatedAt/Locked*/CreatedBy). Equal → return latest unchanged with `Created=false`; differ + unlocked → replace in place; differ + locked → mint new version. | Daemon integration: `TestSpecAdapter_CompileAndGetAndLock` step 2 (idempotent re-compile, same version, Created=false) + step 8 (idempotent after lock). Black-box: `TestSpecSave_BlackBox_CreateCompileShowLockRestart` step 6 (second `spec save` returns same version, Created=false) + step 12 (idempotent after restart). Negative: `TestSpecAdapter_LockedUpdateRejected` (mutating a locked v1 via `SpecificationStore.Save` returns `ErrSpecificationLocked`); `TestSpecSave_BlackBox_LockedSpecNoNewVersion` (binary-level: locked spec + re-save does not mint v2). | **MET** |
| **AC4** Black-box evidence (unit-only wiring insufficient) | (all of the above) | The headline evidence is the compiled-binary scenario `TestSpecSave_BlackBox_CreateCompileShowLockRestart` (43 step-by-step assertions through the real `forge` binary driving the real daemon against an isolated `NEUROFORGE_HOME`). Also `TestSpecSave_BlackBox_InvalidTask`, `TestSpecSave_BlackBox_LockedSpecNoNewVersion`, `TestSpecSave_BlackBox_TextOutput`, `TestSpecSave_BlackBox_FlagValidation`. The in-process daemon integration (`internal/daemon/spec_api_test.go`) provides the restart-persistence proof at the transport level. | **MET** |

Required test classes (task brief):

| Required test class | Covered | Evidence |
|---|---|---|
| Transport contract tests | yes | `internal/transport/spec_api_test.go` (12 tests: happy paths, not-found → 404, locked → 409, empty list → `[]`, 401 without token, nil adapter → 503, invalid `?version` → 400, DTO JSON shape pin) |
| Black-box compiled binary: create → compile → show → lock → daemon restart → show | yes | `TestSpecSave_BlackBox_CreateCompileShowLockRestart` — exactly this scenario, 43 sub-assertions, isolated HOME |
| Invalid task, locked update, duplicate request cases | yes | Invalid task: `TestSpecSave_BlackBox_InvalidTask` (save / show / lock on missing task → exit 1 + "not found"); daemon integration: `TestSpecAdapter_CompileAndGetAndLock` steps 9–11. Locked update: `TestSpecAdapter_LockedUpdateRejected` (in-process: direct `SpecificationStore.Save` on a locked v1 → `ErrSpecificationLocked`); `TestSpecSave_BlackBox_LockedSpecNoNewVersion` (binary-level). Duplicate request: see AC3 row. |
| `make check` and race tests | yes | `make check` exit 0; `go test -race ./...` clean (see Commands). |

## Commands executed and results

| Command | Exit | Result |
|---|---:|---|
| `make build` | 0 | `./forge` builds cleanly from the candidate SHA |
| `./forge gate baseline` | 0 | active baseline v1 |
| `./forge gate validate --manifest docs/reviews/m14/M14-02.manifest.json` | 0 | M14-02 transition REVIEW_APPROVED → ACCEPTED legal |
| `./forge gate next --manifest docs/reviews/m14/M14-02.manifest.json` | 0 | predecessor ACCEPTED; successor may start |
| `go test -count=1 -run 'TestSpecAPI' ./internal/transport/` | 0 | 12/12 transport contract tests PASS |
| `go test -count=1 -run 'TestSpecAdapter' ./internal/daemon/` | 0 | 4/4 daemon integration tests PASS (incl. restart) |
| `go test -count=1 -run 'TestSpecSave_BlackBox' -timeout 180s ./internal/cli/` | 0 | 5/5 compiled-binary black-box tests PASS |
| `go test -count=1 -run 'TestSpecCompile_BlackBox' ./internal/cli/` | 0 | M14-02 offline `forge spec compile` regression: still PASS |
| `go test -count=5 -run 'TestCompile_Deterministic$' ./internal/task/` | 0 | determinism regression: 5× PASS |
| `make check` | 0 | gofmt clean; `go vet ./...` clean; every package ok; FAIL_COUNT 0 |
| `go test -race -count=1 -run 'TestSpecAPI\|TestSpecAdapter\|TestSpecSave' ./internal/transport/ ./internal/daemon/ ./internal/cli/` | 0 | targeted race tests PASS, no race detected |
| `go test -race ./...` | 0 | every package ok; no FAIL; no race detected |

Toolchain: `go version go1.26.5 darwin/arm64`.

## Black-box evidence

Each scenario drives the **real compiled `forge` binary** against an isolated
`NEUROFORGE_HOME` (never the user's real home). The binary is the same
`./cmd/forge` M14-02 built; only the daemon's transport + service composition
changed.

1. **Create → compile → show → lock → restart → show**
   (`TestSpecSave_BlackBox_CreateCompileShowLockRestart`, 3.7s): 43 assertions.
   `forge daemon start` → `forge project add` → `forge task add` →
   `forge spec save` (returns version 1, Created=true, HIGH confidence, 2 ACs)
   → `forge spec show` (byte-identical objective + AC IDs) → `forge spec save`
   again (Created=false, same version — idempotent) → `forge spec lock -v 1`
   (Locked=true, LockedBy=alice) → `forge spec versions` → `[1]` →
   `forge daemon stop` → `forge daemon start` → `forge spec show` returns
   the SAME spec (version 1, same objective, same AC IDs, Locked=true,
   LockedBy=alice — full provenance survives restart) → `forge spec save`
   after restart is still idempotent.
2. **Invalid task** (`TestSpecSave_BlackBox_InvalidTask`): `forge spec save`,
   `forge spec show`, `forge spec lock` on a missing task all exit 1 with
   `not found` in stderr. `forge spec versions` on a missing task returns
   `[]` (empty list, exit 0).
3. **Locked update** (`TestSpecSave_BlackBox_LockedSpecNoNewVersion`): after
   locking v1, `forge spec save` again returns Created=false with the locked
   v1 unchanged (no v2 minted) — locked content matches the compile output,
   so the idempotent path returns the locked snapshot. Combined with the
   daemon-level `TestSpecAdapter_LockedUpdateRejected` (direct
   `SpecificationStore.Save` on a locked v1 returns `ErrSpecificationLocked`),
   the full locked-update contract is proven.
4. **Text output** (`TestSpecSave_BlackBox_TextOutput`): default (no `--json`)
   output of all four subcommands contains the headline fields
   (`TaskID:`, `Version:`, `Objective:`, `Risk:`, `Complexity:`, `Locked:`,
   `v1`, …) and is NOT JSON.
5. **Flag validation** (`TestSpecSave_BlackBox_FlagValidation`):
   `forge spec save` without `--task` exits 1 with `--task` in stderr;
   `forge spec lock` without `--version` exits 1; unknown spec subcommand
   exits 1. No daemon round-trip for the rejected cases (edge validation).

## Idempotency design (for the reviewer)

The compile-and-save idempotency rule is implemented in
`task.SpecificationStore.SaveIfChanged` (called by the daemon adapter's
`CompileSpec`). The decision table below reflects the **honest, proven
contract** after MAJOR-2 remediation (the unreachable "differs → replace in
place" branch has been removed; see Review remediation):

| Compiled content vs latest | Action |
|---|---|
| (no latest exists) | `Save(Version=0)` → mints v1; `Created=true` |
| semantically equal (any lock state) | return latest unchanged; `Created=false` (no Save, no audit event) |
| content differs | `Save(Version=0)` → mints a new version; `Created=true` (defensive fallback; **not reachable via any current public API** — see FU-M14-03-5) |

The "content differs" row is a safe defensive fallback: if the semantic
comparison detects a difference, a new version is allocated (the old version is
preserved, never overwritten in place). It is **not** reachable through the
current daemon CLI/API because a task's description — and therefore its
compiled content — is immutable after `task add` (no `UpdateTask`/task-edit
endpoint exists). The original report's "differs | unlocked → replace in place"
and "differs | locked → mint new version" branches were aspirational dead
code; the replace-in-place variant has been removed. A future public API for
recompiling a task from revised inputs is tracked as FU-M14-03-5.

Semantic equality (`task.specificationsSemanticallyEqual`) compares Objective,
AcceptanceCriteria (ordered, element-wise ID + Statement; count + order),
NonGoals, Assumptions, Constraints, Risk, Complexity, ProposedScope, and
VisualRequirements (Required, Viewport, Theme, Locale, Density, References).
It deliberately ignores Version, lock state, timestamps and provenance — those
are durability metadata, not content. The function is exhaustively unit-tested
(`TestSpecificationsSemanticallyEqual`, 30 cases — one per meaningful field)
so a mutation to any single comparison is caught.

The compare-and-save critical section is serialised **per task** via a keyed
mutex inside `SpecificationStore` (`compileLocks`). The compile step
(`task.Compile`, pure CPU, no I/O) runs OUTSIDE the critical section; only
GetLatest → semantic compare → Save is inside. Unrelated tasks proceed in
parallel. See MAJOR-1 remediation for details and concurrency proof.

The compiler is deterministic (proven by M14-02's 20× unit + 10× black-box
byte-identity tests), so for any fixed task state the compiled content is
byte-identical and the idempotent path is taken.

## Pre-existing defect fixed (regression-tested)

`task.Backlog.Get` previously returned the storage-layer wrapping
`fmt.Errorf("storage: get task %q: %w", id, sql.ErrNoRows)` for a missing
task — i.e. `"storage: get task \"X\": sql: no rows in result set"`. The
documented sentinel `ErrNotFound` ("task not found") was never returned by
`Get`, so the transport layer's `writeAPIError` `"not found"` case never
matched and the missing-task case surfaced as HTTP 500. This is a hard
blocker for the compile endpoint's missing-task UX.

Fix (`internal/task/backlog.go`): `Get` now translates `errors.Is(err,
sql.ErrNoRows)` → `ErrNotFound`. The change is one conditional plus a
`database/sql` import; the function's documented sentinel is now actually
returned. Regression coverage: `TestSpecAdapter_CompileAndGetAndLock` step 9,
`TestSpecSave_BlackBox_InvalidTask`, and the daemon-level "negative" cases
all assert the clean "not found" surfaces.

The change is in-scope: the compile endpoint's not-found semantics depend on
it, and no other `Backlog.Get` caller relied on the old wrapping (verified
with `grep -rn "Tasks.Get\|Backlog.Get"` — every call site either propagates
the error to the transport (which needs the clean "not found") or treats any
error as a not-found case).

## Backward compatibility

- The M14-02 offline `forge spec compile` is unchanged. Its existing
  black-box tests (`TestSpecCompile_BlackBox_*`, 9 tests) pass without
  modification, and `TestCompile_Deterministic` ×5 still passes.
- The new endpoints are additive: `SpecAPI` is a new `transport.Config`
  field defaulting to nil (every existing embedder / test server is
  unaffected; nil yields HTTP 503, matching the established pattern).
- The new CLI subcommands are additive: `forge spec compile` still works
  exactly as before; `save/show/lock/versions` are new subcommands in the
  `forge spec` switch.
- `Backlog.Get` behavioural change is a strict improvement: it now returns
  the documented sentinel instead of a raw sql wrapping. No caller relied on
  the old wrapping.

## Fake / stub / demo leakage

- No fakes, stubs, or demo data in production code.
- The transport contract tests use a `fakeSpecAPI` (in
  `internal/transport/spec_api_test.go`) — a test double for the
  `transport.SpecAPI` interface, used to prove the wire contract without
  exercising storage. This is consistent with `fakeProjectAPI` / `fakeTaskAPI`
  in the existing `internal/transport/api_test.go` and is NOT the only
  evidence: the daemon integration tests and the compiled-binary black-box
  tests exercise the production adapter against real SQLite.
- The risk classifier reuses the accepted `internal/risk.Classify` (M6-3).
- No `TODO` / `FIXME` / `panic("unimplemented")` in the new files.

## Policy / security

- The compile endpoint reuses the existing transport auth (`withToken`); every
  spec endpoint requires the bearer token. The new `TestSpecAPI_RequiresToken`
  proves it.
- No agent process is spawned; no environment is forwarded; no security
  policy, autonomy profile, merge policy, or quota/budget arithmetic is
  touched. The compile step is a pure transformation + storage write.
- The locked-update rejection path is preserved end-to-end
  (`TestSpecAdapter_LockedUpdateRejected` proves `ErrSpecificationLocked`
  surfaces from the daemon-internal `SpecificationStore.Save`).

## Known limitations

1. **Compile-on-create is not implemented.** The user must explicitly call
   `forge spec save` (or `POST /tasks/{id}/specification/compile`) after
   `task add`. This is a deliberate scope decision (the brief lists
   `compile/show/lock` as separate CLI commands; auto-compile would make
   `task add` non-idempotent across re-submissions). See FU-M14-03-2.
2. **Whitespace-only description** at the CLI surface still bypasses the
   empty-description guard (pre-existing M14-02 FU-M14-02-9, not aggravated
   here — the daemon compile path uses the durable task description verbatim,
   so the CLI guard is irrelevant on the daemon-mediated path).
3. **Semantic equality compares objective + ACs at the field level.** A
   compiler change that produced the "same" spec with reordered ACs would
   mint a new version. The compiler is deterministic and appends ACs in fixed
   input order, so this is theoretical; tracked as FU-M14-03-3.

## Follow-up problems

- **FU-M14-03-1:** Add TUI bindings for the spec endpoints (live view of the
  compiled spec, lock/unlock controls). Out of M14-03 scope (TUI is a
  separate milestone surface).
- **FU-M14-03-2:** Add an opt-in `--compile` flag to `forge task add` that
  triggers compile-and-save right after task creation. Disabled by default to
  preserve `task add` idempotency.
- **FU-M14-03-3:** Consider AC-set equality (order-independent) for
  `specificationsSemanticallyEqual` if the compiler ever learns to reorder
  ACs by ID. Currently the compiler appends in input order so ordered
  equality is correct.
- **FU-M14-03-4:** The transport contract currently asserts GET-not-found via
  error-message substring (`getJSON` returns a plain error, not `*APIError`).
  If the codebase later unifies GET/POST error typing, tighten the assertion
  to a type assertion + status-code check.
- **FU-M14-03-5:** Public API for recompiling a task from revised inputs.
  Today the task description is immutable after `task add`, so the
  `SaveIfChanged` "content differs → new version" branch is a defensive
  fallback that is not reachable via any current public CLI/API endpoint.
  When a task-mutation or explicit re-compile-from-revised-input surface is
  added, it must be backed by a black-box test proving version 2 is created
  and the decision-table "differs" row is genuinely exercised.
- **FU-M14-03-6:** Multi-daemon / multi-process idempotency. The per-task
  keyed mutex in `SpecificationStore` serialises the compare-and-save critical
  section **within a single daemon process** (and across goroutines sharing the
  same store). It does NOT protect against two separate daemon processes
  writing to the same SQLite database. NeuroForge currently runs a single
  daemon per database, so this is not a practical limitation; if multi-daemon
  deployment is ever supported, the compare-and-mint must be pushed into a
  storage-level `BEGIN IMMEDIATE` transaction (Variant A) so the serialization
  is cross-process.

## Verdict

`IMPLEMENTED_TESTED`

Rationale:

- All four mandatory ACs are proven by automated evidence I ran at unit,
  daemon-integration, race, and compiled-binary black-box levels.
- The headline acceptance criterion (black-box create → compile → show →
  lock → restart → show) is proven through the real `forge` binary against
  an isolated HOME in `TestSpecSave_BlackBox_CreateCompileShowLockRestart`
  (43 step-by-step assertions, including restart persistence of objective,
  AC IDs, version, lock state, and lock provenance).
- Idempotency is proven at three levels (unit decision table implicit in
  `specificationsSemanticallyEqual`; daemon integration step 2; black-box
  step 6 + 12) plus the locked-update negative case
  (`TestSpecAdapter_LockedUpdateRejected` + `TestSpecSave_BlackBox_LockedSpecNoNewVersion`).
- `make check` is green (FAIL_COUNT 0); `go test -race ./...` is green (no
  race detected).
- Scope is bounded to M14-03; no M14-04 work; no baseline/gate enforcement
  weakened; no product-spec change; no security/autonomy/delivery/merge-policy
  invariant weakened.
- The M14-02 offline `forge spec compile` and its 9 black-box tests are
  preserved unchanged (regression-clean).

`IMPLEMENTED_TESTED` is permitted: every mandatory AC is backed by passing
automated evidence, including the black-box requirement.

---

## Review remediation

This section documents the fixes for the two MAJOR findings raised by the
independent review (`M14-03_REVIEW.md`, verdict `CHANGES_REQUESTED`).

### Remediation identity

- remediation actor/session ID: `M14-03-remediation-session` (fresh, independent
  session; performed no implementation, no review, no acceptance of the
  original candidate `78d1ff1`).
- original candidate SHA: `78d1ff170d925de4ce5e319ddf7c272b2d261d37`
- review report commit SHA: `9859c0860801e4f55cf814ecccba57e3caf0368f`
- new candidate SHA: this report's commit.

### Finding → Root cause → Fix → Regression test → Result

| Finding | Root cause | Fix | Regression test | Result |
| ------- | ---------- | --- | --------------- | ------ |
| **MAJOR-1** — Concurrent identical compile mints duplicate versions (TOCTOU) | `CompileSpec` performed `GetLatest → semantic compare → Save` as three non-atomic steps. Between `GetLatest` and `Save`, N goroutines could observe the same "no latest" (or same latest) state and each call `Save(Version=0)`, each allocating its own version. 20 concurrent identical compiles reproduced up to 7 duplicate versions. | Moved the compare-and-save critical section into `task.SpecificationStore.SaveIfChanged` (`internal/task/specification.go`), which acquires a **per-task keyed mutex** (`compileLocks`) around `GetLatest → compare → Save`. The compile step (`task.Compile`, pure CPU) runs OUTSIDE the lock so unrelated tasks are not blocked. Variant B (process-local keyed serialization) was chosen; multi-daemon protection is tracked as FU-M14-03-6. The unreachable "differs → replace in place" branch was also removed (see MAJOR-2). | `TestSaveIfChanged_ConcurrentIdenticalCreatesSingleVersion` (task, 40 goroutines, production `SpecificationStore.SaveIfChanged`, asserts `[1]` + created==1); `TestSpecAdapter_ConcurrentIdenticalCompileCreatesSingleVersion` (daemon, 30 goroutines through the REAL transport); `TestSpecSave_BlackBox_ConcurrentIdenticalSaveCreatesSingleVersion` (compiled binary, 20 concurrent `forge spec save` + restart + repeat). | **CLOSED** — all concurrency tests pass ×20 (task) and ×5 (daemon) with `-race`; black-box lifecycle green. Sensitivity check (disable the lock) reproduced 6 duplicate versions. |
| **MAJOR-2** — Semantic equality under-tested; "differs" decision-table branches unreachable | (a) `specificationsSemanticallyEqual` was only exercised on the "equal" path; removing the `Objective` comparison did not break any test. (b) The report's "differs | unlocked → replace in place" and "differs | locked → mint new version" branches were unreachable via any public API (no task-mutation endpoint exists). | (a) Moved `specificationsSemanticallyEqual` to the `task` package (`internal/task/specification.go`) and added `TestSpecificationsSemanticallyEqual` — a table-driven unit test with 30 cases covering every meaningful field (Objective, Risk, Complexity, NonGoals, Assumptions, Constraints, ProposedScope, AC ID, AC statement, AC count, AC order, VisualRequirements.Required/Viewport/Theme/Locale/Density/References) plus every persistence-only field that must NOT break equality (Version, CreatedAt, Locked, LockedBy, LockedAt, CreatedBy). (b) Chose **B1** (delete unreachable branch): the `differs → replace in place` logic was removed from `SaveIfChanged`; a genuine difference now defensively mints a new version (safe, no history loss). The decision table was updated to reflect the honest contract; FU-M14-03-5 tracks a future public recompile API. | `TestSpecificationsSemanticallyEqual` (task, 30 cases); `TestSaveIfChanged_IdempotentReuse` / `TestSaveIfChanged_ChangedInputMintsNewVersion` / `TestSaveIfChanged_IdempotentAfterLock` (task); `TestSpecAdapter_ConcurrentChangedInputCreatesDistinctVersions` (daemon, different tasks compiled concurrently — no content lost). | **CLOSED** — sensitivity checks (remove Objective comparison → FAIL; remove VisualRequirements.Required comparison → FAIL) confirm the test catches mutations. |

### Concurrency solution: Variant B (keyed per-task serialization)

Chosen over Variant A (transactional compare-and-create) because:
- The `SpecificationStore.Save` already manages its own transaction with audit
  atomicity; refactoring it to accept an externally-provided transaction (or to
  use `BEGIN IMMEDIATE` for the compare-and-create) would be a larger change
  than the remediation scope warrants.
- The keyed mutex is minimal, correct, and handles the production composition
  (one daemon → one `SpecificationStore` → one lock registry shared by all
  goroutines and adapter instances).
- Multi-process protection is not needed today (single daemon per database);
  FU-M14-03-6 records the future requirement.

**Proof of no global serialization:**
- `compileLocks` is keyed by task ID: `acquire(taskID)` creates/looks-up a
  per-task `sync.Mutex`; `release(taskID)` decrements a waiter count and
  deletes the entry when it reaches zero (no unbounded growth).
- Unrelated task IDs use independent mutexes.
- `TestSpecAdapter_ConcurrentDifferentTasksDoNotBlockEachOther` and
  `TestSaveIfChanged_ConcurrentDifferentTasksDoNotBlock` exercise two tasks
  concurrently; both complete without error and each persists exactly `[1]`.
- The compile step (`task.Compile`) is explicitly OUTSIDE the critical section
  (called by the adapter before `SaveIfChanged`), so deterministic CPU work
  does not block unrelated tasks.

### Audit semantics verification

After MAJOR-1 fix, concurrent identical compiles produce exactly ONE
`task.specification.saved` audit event (for the winner). The losers take the
idempotent-reuse path (`SaveIfChanged` returns `created=false`, no `Save`, no
audit event). Verified by `TestSpecAdapter_ConcurrentIdempotentReuseAudit`
(20 concurrent compiles → exactly 1 `task.specification.saved` event).

### Sensitivity checks performed

All mutations were applied inline, the failing test was confirmed, then the
mutation was fully reverted. The working tree was verified clean before
committing.

| Mutation | Expected failing test | Result |
|---|---|---|
| Disable `compileLocks` in `SaveIfChanged` (revert to TOCTOU) | `TestSaveIfChanged_ConcurrentIdenticalCreatesSingleVersion` | **CAUGHT** — reproduced 6 duplicate versions (`[1 2 3 4 5 6]`). |
| Remove `Objective` comparison from `specificationsSemanticallyEqual` | `TestSpecificationsSemanticallyEqual/different_Objective_is_NOT_equal` | **CAUGHT** — equality returned `true` for different objectives. |
| Remove `VisualRequirements.Required` comparison from `visualRequirementsEqual` | `TestSpecificationsSemanticallyEqual/different_VisualRequirements.Required_is_NOT_equal` | **CAUGHT** — equality returned `true` for different `Required`. |

### Commands executed and results

| Command | Exit | Result |
|---|---:|---|
| `go test -count=20 -run 'TestSpecAdapter_ConcurrentIdenticalCompileCreatesSingleVersion' ./internal/daemon/` | 0 | 20× PASS, no duplicate versions |
| `go test -race -count=10 -run 'TestSpecAdapter_Concurrent\|TestSpecificationsSemanticallyEqual' ./internal/daemon/ ./internal/task/` | 0 | 10× PASS, race-clean |
| `go test -count=1 -run 'TestSpecAPI\|TestSpecAdapter\|TestSpecSave\|TestSpecificationsSemanticallyEqual' ./internal/transport/ ./internal/daemon/ ./internal/cli/` | 0 | all PASS |
| `go test -race -count=1 -run 'TestSpecAPI\|TestSpecAdapter\|TestSpecSave\|TestSpecificationsSemanticallyEqual' ./internal/transport/ ./internal/daemon/ ./internal/cli/` | 0 | all PASS, race-clean |
| `go test -count=1 -run 'TestSpecCompile_BlackBox' ./internal/cli/` | 0 | M14-02 offline regression PASS |
| `go test -count=10 -run 'TestCompile_Deterministic$' ./internal/task/` | 0 | 10× PASS |
| `go test -count=1 ./internal/task/... ./internal/storage/... ./internal/transport/... ./internal/daemon/... ./internal/cli/...` | 0 | all packages ok |
| `go vet ./...` | 0 | clean |
| `gofmt -l .` | 0 | no affected Go files |
| `make check` | 0 | FAIL_COUNT 0; gofmt + vet + tests green |
| `go test -race ./...` | 0 | every package ok; no race detected |
| Black-box: 20 concurrent `forge spec save` (compiled binary, isolated HOME) → `forge spec versions` | 0 | `[1]` only; after restart + repeat → still `[1]` |

### Files changed in remediation

Production code:
- `internal/task/specification.go` — added `compileLocks` (keyed per-task
  mutex), `SaveIfChanged` method, `specificationsSemanticallyEqual` (moved
  from daemon), `stringSliceEqual`, `visualRequirementsEqual`.
- `internal/daemon/spec_api.go` — simplified `CompileSpec` to call
  `SaveIfChanged`; removed the moved equality functions; removed the
  unreachable "differs → replace in place" branch (B1).

Test code:
- `internal/task/specification_semantic_equal_test.go` (NEW) —
  `TestSpecificationsSemanticallyEqual` (30 table-driven cases).
- `internal/task/specification_store_test.go` — `TestSaveIfChanged_*` (5 tests:
  idempotent reuse, concurrent identical, concurrent different tasks, changed
  input, idempotent after lock).
- `internal/daemon/spec_api_test.go` —
  `TestSpecAdapter_ConcurrentIdenticalCompileCreatesSingleVersion`,
  `TestSpecAdapter_ConcurrentDifferentTasksDoNotBlockEachOther`,
  `TestSpecAdapter_ConcurrentChangedInputCreatesDistinctVersions`,
  `TestSpecAdapter_ConcurrentIdempotentReuseAudit`.
- `internal/cli/spec_save_blackbox_test.go` —
  `TestSpecSave_BlackBox_ConcurrentIdenticalSaveCreatesSingleVersion`.

Documentation:
- `docs/reviews/m14/M14-03_IMPLEMENTATION.md` — this section + updated
  idempotency decision table + FU-M14-03-5/FU-M14-03-6.

No changes to: `docs/reviews/m14/M14-03_REVIEW.md`,
`docs/reviews/m14/M14-02.manifest.json`, `docs/spec/**`,
`docs/engineering/**`, gate/baseline enforcement, or any adapter/core package
boundary.
