# M14-03 Independent Review

## Review identity
- reviewer actor/session ID: `M14-03-review-session`
- implementation actor/session ID: `M14-03-impl-session` (per
  `docs/reviews/m14/M14-03_IMPLEMENTATION.md:4`)
- independence confirmed: **yes** — fresh session, no shared context with the
  implementation actor; this reviewer authored no production/test code in this
  task and is not the acceptor.
- review date: 2026-07-27

## Git baseline
- accepted predecessor SHA: `5ed8b72f97d0ea0dbf5a762f7aec8bd15cf1c2b8`
  (`M14-02: add acceptance evidence`)
- implementation candidate SHA: `78d1ff170d925de4ce5e319ddf7c272b2d261d37`
  (`M14-03: Task Compiler production API, CLI and restart flow`)
- review report commit SHA: recorded by `git log -1` after the commit at the
  end of this review (see Commit section).
- ancestry verified:
  `git merge-base --is-ancestor 5ed8b72… 78d1ff1…` → exit 0 (candidate is a
  direct descendant of the accepted M14-02 tip; the candidate is exactly one
  commit ahead of the predecessor on `main`).
- review worktree was kept at the exact candidate SHA throughout; the only
  working-tree noise was pre-existing unrelated review docs
  (`docs/reviews/MINIMAL_RUN_*`, `docs/reviews/M12_M13_REVIEW.md`) that predate
  this task and were not touched.

## Predecessor gate
- manifest: `docs/reviews/m14/M14-02.manifest.json` (state `ACCEPTED`,
  baseline_version `1`, accepted 2026-07-27).
- state: **ACCEPTED**
- gate commands (compiled candidate binary `/tmp/forge-m14-03-review`):

| Command | Exit | Result |
|---|---:|---|
| `/tmp/forge-m14-03-review gate baseline` | 0 | active baseline v1, schema v1 |
| `/tmp/forge-m14-03-review gate next --manifest docs/reviews/m14/M14-02.manifest.json` | 0 | `OK: predecessor "M14-02" is ACCEPTED; successor task may start` |

Predecessor gate is **OPEN**. M14-03 was allowed to start.

## Scope reviewed

Full diff `5ed8b72..78d1ff1` (14 files, +2690/−246). All claims in
`docs/reviews/m14/M14-03_IMPLEMENTATION.md` were cross-checked against the code.

Production files reviewed line-by-line:
- `internal/daemon/spec_api.go` (NEW, 341 lines) — daemon `SpecAPI` adapter,
  idempotency, semantic equality, DTO mappers.
- `internal/transport/spec_api.go` (NEW, 233 lines) — DTOs, `SpecAPI`
  interface, route registration, handlers, `parseIntStrict`.
- `internal/transport/spec_client.go` (NEW, 59 lines) — client methods.
- `internal/cli/spec_cmd.go` (+311) — `spec save/show/lock/versions` +
  unchanged offline `spec compile`.
- `internal/daemon/daemon.go` (+6), `internal/daemon/api.go` (+2) — wiring.
- `internal/transport/server.go` (+4), `internal/transport/api.go` (+7) —
  `Config.SpecAPI`, `registerSpecRoutes`, `"is locked" → 409`.
- `internal/cli/help.go` (+7) — help text.
- `internal/task/backlog.go` (+12) — `Backlog.Get` `sql.ErrNoRows → ErrNotFound`
  defect fix.

Test files reviewed:
- `internal/cli/spec_save_blackbox_test.go` (NEW, 518 lines) — compiled-binary
  black-box, 5 tests.
- `internal/daemon/spec_api_test.go` (NEW, 483 lines) — in-process integration,
  4 tests.
- `internal/transport/spec_api_test.go` (NEW, 431 lines) — transport contract,
  12 tests.

Out-of-scope verification:
- No changes to `docs/spec/`, `docs/engineering/`, gate/baseline enforcement.
- No changes to `internal/workgraph/`, `internal/scheduler/`, `internal/merge/`
  (no M14-04 / Work Graph scope creep).
- No new external dependencies (`go.mod`/`go.sum` unchanged).
- No `TODO`/`FIXME`/`panic("unimplemented")`/stub in new production files.
- No fixture/demo data in production code (the `fakeSpecAPI` lives only in the
  transport contract test, mirroring the existing `fakeProjectAPI`/`fakeTaskAPI`
  pattern, and is NOT the only evidence — daemon integration + compiled-binary
  black-box tests exercise the real adapter against real SQLite).

## Acceptance matrix

| Criterion | Production implementation | Automated evidence | Independent result | Status |
|---|---|---|---|---|
| **AC-1** real CLI/API production path (project → task → save → show → versions → lock) | `specAPIAdapter.CompileSpec/GetSpecification/ListSpecificationVersions/LockSpecification` wired via `transport.Config.SpecAPI` → `registerSpecRoutes` → `Client.*Spec*` → CLI `specSave/Show/Lock/Versions` | `TestSpecSave_BlackBox_CreateCompileShowLockRestart` (compiled binary, isolated HOME, 43 assertions), `TestSpecAdapter_CompileAndGetAndLock`, transport contract suite | Reproduced independently via compiled `/tmp/forge-m14-03-bb` against isolated HOME: project add → task add → spec save (v1) → spec show → spec versions → spec lock all exit 0 with correct DTOs. Full black-box path `forge CLI → daemon transport → daemon adapter → task.Compile → SpecificationStore → SQLite` is real. | **MET** |
| **AC-2** durable restart recovery | All spec state in SQLite (migration v8 applied at `daemon.Run` step 1, line 84, before `NewServices`). No in-memory caches on the read path. | `TestSpecAdapter_PersistsAcrossRestart` (in-process stop/start), `TestSpecSave_BlackBox_CreateCompileShowLockRestart` steps 9–11 (binary-level `daemon stop`/`start`) | Reproduced independently: after `daemon stop` + `daemon start`, `spec show` returned same Version=1, same Objective, same AC IDs, Locked=true, LockedBy=alice, same LockedAt timestamp. Idempotent re-save after restart stayed at v1. | **MET** |
| **AC-3** idempotent recompile/save (single-threaded) | `specificationsSemanticallyEqual` compares Objective, Risk, Complexity, NonGoals, Assumptions, Constraints, ProposedScope, VisualRequirements, ordered AcceptanceCriteria; ignores Version/lock state/timestamps/CreatedBy. Equal → return latest unchanged (no Save). | `TestSpecAdapter_CompileAndGetAndLock` step 2 + step 8, `TestSpecSave_BlackBox_CreateCompileShowLockRestart` steps 6 + 12 | Reproduced independently: second `spec save` returned Created=false, Version=1; `spec versions` = [1] only. Single-threaded idempotency holds. | **MET (single-threaded)** |
| **AC-3** idempotent recompile/save (concurrent) | Idempotency check is `GetLatest` → compare → `Save`, NOT atomic. | No concurrency test exists for the compile path. | **FAILS under concurrency** — see MAJOR-1. 20 concurrent identical compiles minted up to 7 versions (observed [1,2,3,4,5,6,7]). | **NOT MET (concurrent)** |
| **AC-3** changed meaningful input → new version or predictable policy error | Decision table in report claims `differs | unlocked → replace in place`, `differs | locked → mint new version`. | **No test exercises the "differs" branch.** The branch is unreachable in production (no API to mutate a task description after `task add`). | Sensitivity Mutation 2 (drop `Objective` from equality) was NOT caught by any test. See MAJOR-2. | **NOT MET (untested/unreachable)** |
| **AC-4** locked version invariant | `SpecificationStore.Save` rejects mutation of a locked version with `ErrSpecificationLocked` (storage tx). Compile adapter: equal+locked → return latest; the "differs+locked → new version" branch is code but unreachable. | `TestSpecAdapter_LockedUpdateRejected`, `TestSpecSave_BlackBox_LockedSpecNoNewVersion`, `TestSpecAPI_LockedErrorMaps409`. | Reproduced: lock v1 → `spec save` returns Created=false, v1 unchanged, Locked=true. Direct `SpecificationStore.Save` on locked v1 returns `ErrSpecificationLocked` (verified by test). Restart preserves lock + provenance. Repeated lock idempotent. | **MET for the reachable cases** (locked-content-equal path). "differs+locked → new version" is untested/unreachable. |
| **AC-5** HTTP/CLI error semantics | `writeAPIError` cases: "not found"→404, "is locked"→409, "is required"/"invalid"→400, "invalid transition"/"already registered"→409. `parseIntStrict` rejects non-digit/sign/overflow → 400. `decodeJSON` rejects empty/invalid body → 400. | `TestSpecAPI_Get_NotFoundMaps404`, `TestSpecAPI_Lock_NotFoundMaps404`, `TestSpecAPI_LockedErrorMaps409`, `TestSpecAPI_Get_InvalidVersionParam`, `TestSpecSave_BlackBox_InvalidTask`, `TestSpecSave_BlackBox_FlagValidation`. | Reproduced independently via curl against the daemon: missing task→404, missing version→404, invalid JSON→400, `?version=abc`→400, `?version=99999999999999999999`→400 (overflow), PUT→404 (ServeMux default). Missing task via CLI exits 1 with "task not found". | **MET** (FU-M14-03-4 GET error typing remains a documented non-blocking follow-up). |
| **AC-6** M14-02 compatibility (`forge spec compile` offline) | `specCompile` unchanged; new `save/show/lock/versions` are additive switch cases. | `TestSpecCompile_BlackBox_*` (9 tests, unchanged), `TestCompile_Deterministic` ×10. | Reproduced via compiled binary: text output, `--json`, invalid priority→exit 1, invalid attach role→exit 1, empty desc→exit 1, determinism (SHA-256 identical across runs). No regression. | **MET** |
| **AC-7** honest production wiring | Real `task.Compile`, real `SpecificationStore`, real SQLite, real daemon transport. No second compiler, no fake store fallback, no success-without-persistence handler. | Sensitivity Mutation 3 (skip `Save`, return compiled directly) → 6 tests fail. | Verified: handler always calls `SpecificationStore.Save` on the create path; nil `Specs` yields an explicit error, not a fake success. | **MET** |
| **Backlog.Get defect fix** regression-tested | `errors.Is(err, sql.ErrNoRows) → ErrNotFound`; minimal, only translates the not-found sentinel. | Sensitivity Mutation 1 (remove mapping) → `TestSpecSave_BlackBox_InvalidTask` + `TestSpecAdapter_CompileAndGetAndLock` step 9 fail (raw `sql: no rows` surfaces, no "not found"). | All other callers (`runapp/finalize.go:309`, `daemon/api.go:171`) propagate the error; none relied on the old wrapping. Fix is correct and minimal. | **MET** |

## Commands executed

| Command | Exit code | Result |
|---|---:|---|
| `go build -o /tmp/forge-m14-03-review ./cmd/forge` | 0 | builds cleanly |
| `/tmp/forge-m14-03-review gate baseline` | 0 | baseline v1 |
| `/tmp/forge-m14-03-review gate next --manifest docs/reviews/m14/M14-02.manifest.json` | 0 | predecessor ACCEPTED |
| `go test -count=1 -run 'TestSpecAPI\|TestSpecAdapter\|TestSpecSave' ./internal/transport/ ./internal/daemon/ ./internal/cli/` | 0 | 21/21 PASS (transport 12, daemon 4, cli 5) |
| `go test -race -count=1 -run 'TestSpecAPI\|TestSpecAdapter\|TestSpecSave' ./internal/transport/ ./internal/daemon/ ./internal/cli/` | 0 | targeted race tests PASS |
| `go test -count=1 -run 'TestSpecCompile_BlackBox' ./internal/cli/` | 0 | M14-02 offline regression PASS |
| `go test -count=10 -run 'TestCompile_Deterministic$' ./internal/task/` | 0 | determinism 10× PASS |
| `go test -count=1 ./internal/task/... ./internal/storage/... ./internal/transport/... ./internal/daemon/... ./internal/cli/...` | 0 | all packages ok |
| `go vet ./...` | 0 | clean |
| `gofmt -l .` | 0 | no affected Go files flagged |
| `make check` | 0 | FAIL_COUNT 0; gofmt + vet + tests green |
| `go test -race ./...` | 0 | every package ok; no race detected by the existing suite |

Toolchain: `go version go1.26.5 darwin/arm64`. No skipped/manual tests in the
targeted evidence set (the black-box tests use `testing.Short()` to skip only
under `-short`, which is not the default; they all ran).

## Transport verification

- Route patterns use Go 1.22 method-prefixed ServeMux patterns:
  `POST /tasks/{id}/specification/compile`, `GET /tasks/{id}/specification`,
  `GET /tasks/{id}/specification/versions`, `POST /tasks/{id}/specification/lock`.
  No conflict with the existing `GET /tasks/{id}`, `POST /tasks/{id}/pause`,
  `POST /tasks/{id}/cancel`, `POST /tasks/{id}/dispatch`, etc. (distinct path
  segments; longest-prefix match is unambiguous).
- Path-value `id` is taken from the URL and overwrites any body `TaskID` in
  `CompileSpec`/`LockSpecRequest` (client and server both enforce this), so a
  body cannot address a different task. Verified by
  `TestSpecAPI_Compile_HappyPath` (`fake.lastCompileReq.TaskID == "p-1"`).
- `?version=N` parsing: `parseIntStrict` rejects empty, non-digit, sign, and
  overflow (`n < 0` after multiply) → HTTP 400 with a clear message. `version=0`
  and absent param both mean "latest". Verified independently via curl
  (`?version=abc`→400, `?version=99999999999999999999`→400).
- Empty body tolerated on compile/lock (`r.ContentLength > 0` guard before
  `decodeJSON`); invalid JSON → 400 with the parse error. Verified via curl.
- Content-Type not strictly enforced (consistent with existing endpoints); body
  size capped at 4 MiB by `decodeJSON`'s `io.LimitReader`.
- Response DTOs are consistent (`SpecificationDTO` everywhere; `Created` flag on
  the compile result). Time fields rendered as RFC3339Nano.
- Unsupported method (PUT/PATCH/DELETE on spec routes) falls through to ServeMux
  `handleRoot` → 404 "not found". The brief allows "405 или принятый transport
  convention"; 404 is the accepted transport convention here (matches the rest
  of the API). Not a finding.
- No panic paths observed on malformed requests.

## Daemon wiring verification

- `newSpecAPIAdapter(services)` constructed at `internal/daemon/daemon.go:136`,
  after `db.Migrate` (line 84) and `NewServices` (which builds the real
  `task.SpecificationStore` from the same `*storage.DB`).
- Wired into `transport.Config.SpecAPI` at `daemon.go:212`. Production daemon
  startup path; not a test-only composition root.
- Uses the SAME `task.Compile` accepted in M14-02 (no second compiler).
- Uses the SAME `task.SpecificationStore` accepted in M14-01 (atomic tx + audit,
  migration v8). Audit recorder dependency connected via `NewServices`.
- No optional nil dependency that panics: the adapter guards `a.svc.Specs == nil`
  at the top of every method; `Services.Specs` is always constructed in
  `NewServices`, so nil only happens in synthetic test configs (which then get
  the documented 503 from the transport layer).

## Semantic equality verification

`specificationsSemanticallyEqual` (`internal/daemon/spec_api.go:293-318`):

**Compared (correct):** Objective, Risk, Complexity, NonGoals, Assumptions,
Constraints, ProposedScope, VisualRequirements (all sub-fields), and
AcceptanceCriteria (ordered, element-wise `!=` on the struct → both ID and
Statement).

**Intentionally ignored (correct):** Version, Locked, LockedAt, LockedBy,
CreatedAt, CreatedBy (durability/provenance metadata, not content).

**Defect (MAJOR-2):** the function is exercised ONLY on the "equal" path. No
test ever feeds it a semantically different specification, so:

1. Removing the `Objective` comparison (Sensitivity Mutation 2) did NOT break
   any test — the entire targeted suite stayed green.
2. The "differs | unlocked → replace in place" and "differs | locked → mint new
   version" branches of the `CompileSpec` decision table are unreachable through
   any documented API (there is no `UpdateTask`/task-edit endpoint, so a task's
   description — and therefore its compiled content — is immutable after
   `task add`). The implementation report presents these branches as live
   behaviour (decision table at `M14-03_IMPLEMENTATION.md:204-210`); they are in
   fact aspirational dead code.

The ordered AC comparison is correct under the current deterministic compiler
(appends ACs in fixed input order), so FU-M14-03-3 (order-independent equality)
remains a valid non-blocking follow-up.

## Idempotency and concurrency verification

**Single-threaded idempotency: PASS.** Two sequential identical saves return the
same version with `Created=false`; no new version minted; invariant holds before
lock, after lock, after restart, and across separate CLI processes. Verified by
existing tests and reproduced independently.

**Concurrent identical compile: FAIL (MAJOR-1).** The idempotency check in
`CompileSpec` (`internal/daemon/spec_api.go:110-130`) is a classic TOCTOU:

```
latest, latestErr := a.svc.Specs.GetLatest(ctx, req.TaskID)   // READ
if latestErr == nil && specificationsSemanticallyEqual(...) { return ... } // CHECK
// ...
saved, err := a.svc.Specs.Save(ctx, compiled)                 // WRITE (Version=0)
```

Between the `GetLatest` and the `Save`, another goroutine can observe the same
"no latest" state and both proceed to `Save(Version=0)`. `Save` allocates the
next version race-free *inside* its own transaction, but each concurrent winner
gets its own version. The idempotency invariant is enforced by an interleaved
read+write sequence, not by a transactional/unique mechanism.

Independent harness (`internal/daemon/spec_concurrency_review_test.go`,
temporary, removed after the review) ran 20 goroutines firing identical
`CompileSpec` against one freshly-created task, then asserted
`ListSpecificationVersions` == `[1]`. Result over 5 `-race` runs:

| Run | Versions created | Created=true count |
|---:|---|---:|
| 1 | `[1 2 3 4]` | 4 / 20 |
| 2 | `[1 2 3 4]` | 4 / 20 |
| 3 | `[1 2 3 4 5 6 7]` | 7 / 20 |
| 4 | `[1]` | 1 / 20 |
| 5 | `[1]` | 1 / 20 |

3 of 5 runs violated the invariant (up to 7 duplicate versions for one logical
operation). The race detector did NOT flag it (the data race is logical, not a
memory race — the storage layer is properly transactional).

**Concurrent different compile (distinct tasks): PASS.** The same harness ran 6
goroutines each compiling a distinct task; every task ended up with exactly
`[1]`. Version allocation across tasks is race-free.

**Save/lock race:** Lock targets an explicit `(taskID, version)` and is fully
transactional inside `SpecificationStore.Lock`; a concurrent compile cannot
bypass the lock (the worst case is MAJOR-1 minting an extra unlocked version,
which does not corrupt the locked snapshot). No separate finding.

**Audit impact of MAJOR-1:** each spurious version also writes a
`task.specification.saved` audit event, so the audit trail records N "created"
events for one logical idempotent operation — misleading for audit consumers.

## Lock and restart verification

- Lock latest (`-v 1`): Locked=true, LockedBy set, LockedAt set. ✓
- Lock explicit version: same. ✓
- Lock missing version → `specification not found` → 404 / CLI exit 1. ✓
- Repeated lock (same actor): idempotent, no error, state unchanged. ✓
  (`TestSpecAdapter_CompileAndGetAndLock` step 7).
- Repeated lock (different actor): NOT explicitly tested. The storage
  `LockSpecification` is idempotent on already-locked rows, but whether
  `LockedBy` is overwritten on a second lock by a different actor is not
  asserted anywhere. Low-severity gap (the production UX is single-actor lock);
  noted here, not promoted to a finding.
- Save same content after lock: Created=false, locked v1 returned unchanged. ✓
- Save changed content after lock: UNREACHABLE (no task mutation API) — part of
  MAJOR-2.
- Restart preserves Locked, LockedBy, LockedAt. ✓ (Verified independently via
  compiled binary + by `TestSpecAdapter_PersistsAcrossRestart`).
- Sensitivity Mutation 4 (drop `LockedBy` from `specificationToDTO`) was caught
  immediately by both the black-box restart test and the in-process restart
  test → lock-provenance regression coverage is adequate.

## Error mapping verification

| Scenario | Expected | Observed (independent) | Status |
|---|---|---|---|
| missing task (compile/show/lock) | 404 / CLI non-zero | 404 `{"error":"task not found"}`; CLI exit 1 `task not found` | ✓ |
| missing specification (show) | 404 | 404 `{"error":"specification not found"}`; CLI exit 1 | ✓ |
| missing version (show/lock) | 404 | 404 `specification not found`; CLI exit 1 | ✓ |
| locked mutation | 409 | `TestSpecAPI_LockedErrorMaps409` → 409; `TestSpecAdapter_LockedUpdateRejected` → `ErrSpecificationLocked` | ✓ |
| malformed task id/path | 400 or 404, not 500 | unknown route → 404 `not found` (no 500) | ✓ |
| invalid compile body | 400, no panic | invalid JSON → 400 `invalid JSON: …` | ✓ |
| `?version=abc` / overflow | 400 | 400 `version query param must be a non-negative integer` | ✓ |
| unsupported method | 405 or convention | 404 (ServeMux default; accepted convention) | ✓ |
| daemon unavailable | CLI non-zero | CLI auto-starts the daemon via `ensureDaemon`; if start/connect truly fails, `errf("connect to daemon: %v", err)` → exit 1 | ✓ |
| `spec versions` on missing task | empty list, exit 0 | `[]`, exit 0 (documented list-endpoint behaviour) | ✓ |

`Backlog.Get` not-found mapping is the load-bearing fix for the missing-task 404
path; Sensitivity Mutation 1 proved the regression tests catch its removal.

## M14-02 regression verification

Reproduced via the compiled candidate binary (`/tmp/forge-m14-03-bb`):

- `forge spec compile -p demo "<desc>"` → default text output with `TaskID:`,
  `Objective:`, `Risk:`, `Complexity:`, `Confidence:`, `AC-1:` lines. ✓
- `--json` → valid JSON, `result.specification` present, no `version` field
  (offline compiler has no storage). ✓
- `--priority BOGUS` → exit 1. ✓
- `--attach hash=BOGUS_ROLE` → exit 1. ✓
- empty description → exit 1. ✓
- determinism: two identical runs → identical SHA-256
  (`415925e9cb66e66f5820619e3400cacb385f63afc09678eb6d200c94e3215b53`). ✓

The offline `specCompile` function is byte-identical to the M14-02 version; the
new `save/show/lock/versions` are additive switch cases and a new
`writeSpecDTOText` helper that does not touch the compile path. No regression.

## Black-box verification

Driven the real compiled `forge` binary against an isolated `NEUROFORGE_HOME`
(`/tmp/nf-m14-03-review-home`, never the user's home) with a throwaway git repo:

**Scenario A — lifecycle (create → save → show → versions → lock → restart →
show → save):** all steps exit 0; first save Created=true Version=1; second
identical save Created=false Version=1; `spec versions` = `[1]`; lock sets
Locked=true LockedBy=alice; after `daemon stop`+`daemon start`, `spec show`
returns same Version/Objective/AC IDs/Locked/LockedBy/LockedAt; idempotent save
after restart stays at v1. ✓

**Scenario B — changed input:** cannot be exercised through the daemon API
(there is no `UpdateTask`/task-edit endpoint; a task's description is immutable
after `task add`). The `CompileSpec` "differs" branches are unreachable in
production. See MAJOR-2. (The brief allows "либо вернуть ожидаемую policy
error"; the implementation neither creates a new version nor returns an error
because the branch is dead code — it is simply never reached.)

**Scenario C — explicit version:** `show` latest (no `-v`) → v1; `show -v 1` →
v1; `show -v 99` → 404 / exit 1; `show -v 0` → latest (v1); `lock -v 99` → 404 /
exit 1. ✓

**Scenario D — missing entities:** `spec save/show/lock` on a missing task →
exit 1 with "not found"; `spec versions` on a missing task → `[]` exit 0
(documented). Daemon-down: CLI auto-starts via `ensureDaemon`; with a truly
unreachable daemon the CLI exits 1 with `connect to daemon: …`. ✓

**Scenario E — malformed input:** invalid JSON body → 400; `?version=abc` → 400;
`?version=99…99` (overflow) → 400; PUT on spec route → 404; compile on missing
task → 404. No 500 observed for any not-found / bad-input case. ✓

**Scenario F — M14-02 offline compile:** see "M14-02 regression verification"
above. ✓

## Sensitivity checks

All mutations were applied to a local copy of the candidate, measured, then
fully reverted. The working tree was confirmed clean (`git diff HEAD -- internal/`
empty) before writing this report.

| Mutation | What changed | Expected regression | Independent result |
|---|---|---|---|
| **M1** — remove `sql.ErrNoRows → ErrNotFound` mapping in `Backlog.Get` | `internal/task/backlog.go` | not-found case regresses to raw `sql: no rows`, surfaces as 500 / no "not found" | **CAUGHT** — `TestSpecSave_BlackBox_InvalidTask` + `TestSpecAdapter_CompileAndGetAndLock` step 9 fail. |
| **M2** — drop `Objective` from `specificationsSemanticallyEqual` | `internal/daemon/spec_api.go` | a changed-objective spec would be treated as idempotent | **NOT CAUGHT** — entire targeted suite green. See MAJOR-2. |
| **M3** — skip `SpecificationStore.Save`, return `compiled` directly | `internal/daemon/spec_api.go` `CompileSpec` | restart/lock/show tests fail on missing persistence | **CAUGHT** — `TestSpecSave_BlackBox_CreateCompileShowLockRestart`, `TestSpecAdapter_CompileAndGetAndLock`, `TestSpecAdapter_PersistsAcrossRestart` fail. |
| **M4** — drop `LockedBy` from `specificationToDTO` | `internal/daemon/spec_api.go` | restart/lock tests fail on missing provenance | **CAUGHT** — black-box + in-process restart tests fail. |

Mutation 2 not being caught is itself a MAJOR finding (engineering baseline:
"Если test suite не ловит mutation, это MAJOR finding").

## Findings

### MAJOR-1 — Concurrent identical compile mints duplicate versions (TOCTOU)

**Location:** `internal/daemon/spec_api.go:110-143` (`CompileSpec` idempotency
block: `GetLatest` → `specificationsSemanticallyEqual` → `Save`).

**Requirement:** AC-3 ("Повторный запрос с семантически тем же input не должен
создавать новую версию"; "одинаковый input при concurrency"); engineering
baseline rule 10 (concurrency under daemon + SQLite + shared state).

**Observed:** 20 concurrent identical `CompileSpec` calls against one
freshly-created task (no existing version) minted up to 7 versions in a single
run (`versions=[1 2 3 4 5 6 7]`). 3 of 5 `-race` runs reproduced the duplicate.
The idempotency check is a non-atomic read (`GetLatest`) + compare + write
(`Save(Version=0)`); each goroutine that observes "no latest" proceeds to
`Save(Version=0)`, and `Save` allocates a new version per call inside its own
transaction. There is no per-task serialisation and no transactional
compare-and-insert.

**Evidence:**
- Independent harness `TestReview_ConcurrentIdenticalCompile` (temporary,
  removed after the review) — 5 `-race` runs: `[1 2 3 4]`, `[1 2 3 4]`,
  `[1 2 3 4 5 6 7]`, `[1]`, `[1]`.
- No existing test covers concurrent compile (the targeted race run of the
  shipped suite passed only because it never exercises this interleaving).
- Audit side-effect: each spurious version writes a `task.specification.saved`
  event, so the audit trail records N "created" events for one logical
  idempotent operation.

**Impact:** Violates the documented idempotent-recompile contract under
concurrency. Produces duplicate durable versions + misleading audit history for
a single user intent. Not data corruption (each version is individually valid)
and not a lock bypass, so MAJOR (not BLOCKER) per the brief's severity rule
("concurrent duplicate versions → MAJOR").

**Required fix:** Make the idempotency invariant race-free. Minimal options:
(a) serialise the compile-and-save critical section per task (per-task `sync.Mutex`
in the adapter, or a single adapter-level mutex covering `CompileSpec`), or
(b) push the compare-and-mint into the storage transaction so
`SpecificationStore.Save` (or a new `SaveIfChanged`) checks for a
semantically-equal latest *inside* the same `BeginTx` and returns it without
allocating a new version. Then add a regression test: N concurrent identical
compiles → `ListSpecificationVersions` must equal `[1]` (or the single existing
version) with high confidence (run with `-count=N` or a synchronised barrier).

---

### MAJOR-2 — Semantic-equality mutation not caught; "changed input → new version" path is untested and unreachable

**Location:** `internal/daemon/spec_api.go:293-318` (`specificationsSemanticallyEqual`)
and `internal/daemon/spec_api.go:118-130` (the `differs | unlocked → replace in
place` and `differs | locked → mint new version` branches).

**Requirement:** AC-3 ("изменённый значимый input обязан создать новую версию
либо вернуть ожидаемую policy error"); engineering baseline sensitivity-check
rule ("Если test suite не ловит mutation, это MAJOR finding").

**Observed:**
1. Removing the `Objective` comparison from `specificationsSemanticallyEqual`
   (Sensitivity Mutation 2) did NOT fail any test in the targeted suite. The
   equality function — the heart of the idempotency rule — is only ever
   exercised on the "equal" path; no test feeds it a semantically different
   specification.
2. The "differs" branches of the `CompileSpec` decision table
   (`M14-03_IMPLEMENTATION.md:204-210`) are unreachable in production: there is
   no `UpdateTask`/task-edit endpoint, so a task's description — and therefore
   its compiled content — is immutable after `task add`. The reported behaviour
   ("differs | unlocked → replace in place", "differs | locked → mint new
   version") is aspirational dead code, not proven behaviour.

**Evidence:**
- Sensitivity Mutation 2: `go test -count=1 -run 'TestSpecAPI|TestSpecAdapter|TestSpecSave' ./internal/transport/ ./internal/daemon/ ./internal/cli/` → all green with `Objective` comparison removed.
- `grep -rn "UpdateTask\|task edit\|task update" internal/cli/ internal/daemon/ internal/transport/` → no task-mutation API exists.

**Impact:** The equality function is undertested. If task mutation is added
later (a natural evolution, and the report's decision table already assumes it),
a dropped field would silently treat changed content as idempotent — no new
version, no error, lost update. The implementation report's claims about the
"differs" rows exceed the actually proven scope (baseline rule: "Documentation
claim [must not] превышать реально доказанный scope").

**Required fix:**
- Add direct unit tests for `specificationsSemanticallyEqual` covering each
  compared field (objective, each AC ID + statement, risk, complexity, non-goals,
  assumptions, constraints, proposed scope, each visual-requirement sub-field,
  references) — a mutation on any one comparison must fail a test.
- Either (a) add an integration test that drives the "differs" branch via a
  test-only seam (e.g. construct two `task.Specification` values that differ and
  call `Save` + `CompileSpec` directly through the adapter), or (b) explicitly
  mark the "differs" branches as unimplemented/unreachable in the report and
  track a follow-up (FU) until a task-mutation API exists. Today the report
  presents them as live, tested behaviour, which is not accurate.

---

(No BLOCKER findings. No additional MINOR findings beyond the already-tracked
FU-M14-03-1..4 follow-ups, which remain reasonable non-blocking items.)

## Scope and documentation assessment

- Scope is bounded to M14-03. No M14-04/Work Graph/scheduler/merge changes. No
  product-spec change. No baseline/gate weakening. No new dependencies.
- Code is honest: real compiler, real store, real SQLite, real daemon
  transport; no fixture data, no fake-store fallback, no success-without-Save
  handler, no second compiler implementation.
- `Backlog.Get` defect fix is minimal, correct, and regression-tested.
- The implementation report is largely accurate, with two exceptions that
  amount to MAJOR-2: (i) the decision table's "differs" rows describe behaviour
  that is neither tested nor reachable, and (ii) the headline idempotency claim
  ("a second call against the same task whose compiled content is byte-identical
  … returns that latest version unchanged with Created=false",
  `M14-03_IMPLEMENTATION.md:75-77`) is silent about the concurrency caveat — it
  holds single-threaded but is violated under concurrent identical compiles
  (MAJOR-1).
- Claimed follow-ups FU-M14-03-1 (TUI bindings), FU-M14-03-2 (compile-on-create
  flag), FU-M14-03-3 (order-independent AC equality), FU-M14-03-4 (typed GET
  errors) are all reasonable and non-blocking. FU-M14-03-4 in particular is
  confirmed: GET-not-found surfaces as a plain `getJSON` error string containing
  "status 404" (not `*APIError`); the CLI contract is still correct (exit 1 +
  readable message), so it stays a MINOR follow-up per the brief.

## Verdict

**CHANGES_REQUESTED**

Rationale:

- The predecessor gate is open (M14-02 ACCEPTED, baseline v1) and the candidate
  is the exact reviewed SHA `78d1ff1…`, a direct descendant of the accepted
  M14-02 tip.
- Actor independence holds (this reviewer = `M14-03-review-session`, distinct
  from `M14-03-impl-session`).
- The single-threaded production path is real and well proven: compiled-binary
  black-box (AC-1), durable restart recovery (AC-2), locked-version invariant
  for the reachable cases (AC-4), correct HTTP/CLI error mapping including the
  `Backlog.Get` defect fix (AC-5), M14-02 offline-compile regression-clean
  (AC-6), and honest wiring (AC-7). `make check`, `go test -race ./...`,
  targeted `-race` runs, and `gofmt`/`go vet` are all green.
- HOWEVER, two MAJOR findings block `REVIEW_APPROVED`:
  1. **MAJOR-1**: concurrent identical compile mints duplicate versions (TOCTOU
     between `GetLatest` and `Save`). The idempotency contract (AC-3, concurrent
     case) is violated; the duplicate-version behaviour is reproducible
     independently and is not guarded by any transactional/unique mechanism or
     regression test.
  2. **MAJOR-2**: the semantic-equality function is undertested (Sensitivity
     Mutation 2 not caught), and the implementation report's "differs" decision
     rows describe behaviour that is neither tested nor reachable through any
     production API. This fails the "changed meaningful input → new version or
     predictable policy error" limb of AC-3 and the "documentation must not
     exceed proven scope" baseline rule.
- Both findings are fixable within M14-03 scope (serialise/transactionalise the
  compile-and-save idempotency check + add a concurrency regression test; add
  direct equality unit tests + either exercise or explicitly mark the "differs"
  branches). They do not require re-architecting the task or touching the spec,
  hence `CHANGES_REQUESTED` rather than `REJECTED`.
- No BLOCKER findings (no corruption, no security violation, no fabricated
  evidence, no predecessor fraud, no irreversible data loss).

A separate acceptance session for M14-03 is **NOT** permitted on this candidate:
the verdict is `CHANGES_REQUESTED`, so the implementation actor must address
MAJOR-1 and MAJOR-2, after which a fresh independent re-review is required
before any acceptance actor may transition M14-03 to `ACCEPTED`.
