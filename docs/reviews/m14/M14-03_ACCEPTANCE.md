# M14-03 Acceptance

## Acceptance identity

- acceptance actor/session ID: `M14-03-accept-session` (fresh, independent
  session; performed no implementation, no remediation, no review, no re-review
  of M14-03, and is not the M14-02 accept actor).
- implementation actor/session ID: `M14-03-impl-session`
  (per `M14-03_IMPLEMENTATION.md`).
- remediation actor/session ID: `M14-03-remediation-session`
  (per `M14-03_IMPLEMENTATION.md` "Review remediation"; produced `a9869f9`).
- review actor/session ID (original): `M14-03-review-session`
  (verdict `CHANGES_REQUESTED`; commit `9859c08`, prior review report
  superseded by the re-review).
- re-review actor/session ID: `M14-03-rereview-session`
  (verdict `REVIEW_APPROVED` on `a9869f9`; per `M14-03_REVIEW.md`).
- independence confirmed: **yes** — all five role-bound ids are pairwise
  distinct. The acceptor re-checked every implementation/review claim against
  the checked-out code, tests, and the compiled `forge` binary rather than
  trusting any report. This session authored no production code, no tests,
  performed no remediation/review, and does not perform M14-04.
- **Authorization note:** acceptance was opened at the explicit instruction of
  the human project owner, who is the meta-independent authority above all
  agent actors and who confirmed M14-03 acceptance had been authorised. The
  acceptor actor itself remains role-distinct from the implementer and the
  reviewer (baseline §3 rule 3 / §6 actor-independence rule). No mandatory
  criterion was waived.
- acceptance date: 2026-07-27.

## Git baseline

- accepted predecessor SHA (M14-02 acceptance): `5ed8b72f97d0ea0dbf5a762f7aec8bd15cf1c2b8`
  (M14-03 starting SHA per `M14-03_IMPLEMENTATION.md`; M14-02 manifest
  `state = ACCEPTED`).
- original candidate SHA: `78d1ff170d925de4ce5e319ddf7c272b2d261d37`
  (`M14-03: Task Compiler production API, CLI and restart flow`).
- remediated candidate SHA (the candidate being accepted):
  `a9869f9d15f2f717abcebd6018abbc61de11a3cf`
  (`M14-03: address review findings`).
- re-review report commit SHA: `3c26aa0b4351f0ac5869290cb242190d5eb7de7d`
  (`M14-03: re-review after remediation (REVIEW_APPROVED)` — sole added file
  `docs/reviews/m14/M14-03_REVIEW.md`).
- acceptance starting HEAD: `086748a56619dede8a64a4cee2c8cb1103d0a71f`.
- acceptance commit SHA: recorded in the manifest after the commit is created.

Ancestry verified (all `git merge-base --is-ancestor` → exit 0):

- `5ed8b72` (M14-02 accept) is an ancestor of `78d1ff1` (original candidate).
- `78d1ff1` is an ancestor of `a9869f9` (remediation is a direct descendant).
- `a9869f9` is an ancestor of `3c26aa0` (re-review report) and of the current
  HEAD `086748a`.

Production/test code identity: `git diff --name-only a9869f9 HEAD` lists only
`docs/reviews/m14/M14-03_REVIEW.md` and `docs/reviews/m14/M14-04_IMPLEMENTATION.md`
— i.e. the production and test code exercised in this acceptance session is
byte-identical to the reviewed candidate `a9869f9`. The review's evidence
applies unchanged.

## Predecessor gate

- M14-02 manifest: `docs/reviews/m14/M14-02.manifest.json` (state `ACCEPTED`,
  baseline_version `1`).
- command (compiled `/tmp/forge-m14-03-accept`): `forge gate next --manifest
  docs/reviews/m14/M14-02.manifest.json` → exit 0 —
  `OK: predecessor "M14-02" is ACCEPTED; successor task may start`.
- `forge gate baseline` → exit 0 (active schema_version 1, baseline_version 1,
  doc `docs/engineering/ENGINEERING_BASELINE.md`).

Predecessor gate is **OPEN**; M14-03 was lawfully allowed to start.

## Review prerequisite

- reviewed candidate (re-review): `a9869f9d15f2f717abcebd6018abbc61de11a3cf`
  — **matches** the remediated candidate being accepted exactly.
- verdict: `REVIEW_APPROVED` (`M14-03_REVIEW.md`, "Verdict").
- blocker findings: **0**.
- major findings: **0** (MAJOR-1 TOCTOU concurrency and MAJOR-2 under-tested
  equality + unreachable branches are both **RESOLVED** in the re-review and
  re-verified independently below).
- accepted minor follow-ups: **3** (MINOR-1 unnecessary defensive sleep that
  masks nothing, MINOR-2 pre-existing `spec show` error message wording tracked
  as FU-M14-03-4, MINOR-3 latent double-release guard not reachable by any
  current caller).

Acceptance is permitted: the re-review examined the exact remediated candidate
`a9869f9…` and returned `REVIEW_APPROVED` with no BLOCKER or MAJOR remaining.

## Previous findings status

Each finding was reproduced against the checked-out code at `a9869f9`
(byte-identical to HEAD for production/test code) and the compiled binary.

| Finding | Fix | Acceptance evidence | Status |
|---|---|---|---|
| **MAJOR-1** Concurrent identical compile minted duplicate versions (TOCTOU) | Compare-and-mint critical section moved into `task.SpecificationStore.SaveIfChanged` (`internal/task/specification.go`), serialised by a per-task keyed mutex `compileLocks`; `task.Compile` runs outside the lock so unrelated tasks are not blocked. The dead "differs → replace in place" branch was removed. | Shipped tests: `TestSaveIfChanged_ConcurrentIdenticalCreatesSingleVersion` (40 goroutines, production store, asserts `[1]`), `TestSpecAdapter_ConcurrentIdenticalCompileCreatesSingleVersion` (30 goroutines through the real transport), `TestSpecSave_BlackBox_ConcurrentIdenticalSaveCreatesSingleVersion` (compiled binary, 20 concurrent `forge spec save` + restart). **Independent black-box (this session):** 20 concurrent `forge spec save` against an isolated HOME → `forge spec versions` returns `[1]` only. Sensitivity check (re-review): disabling the lock reproduced 12 duplicates. | **CLOSED** |
| **MAJOR-2** Semantic equality under-tested; dead "differs" branches unreachable | `specificationsSemanticallyEqual` moved to the `task` package and covered by a 30-case table test; dead "replace in place" branch deleted; honest "differs → new version" fallback directly exercised. | `TestSpecificationsSemanticallyEqual` (30 cases, ran PASS this session); `TestSaveIfChanged_IdempotentReuse`, `TestSaveIfChanged_ChangedInputMintsNewVersion`, `TestSaveIfChanged_IdempotentAfterLock` (all PASS this session); `TestSpecAdapter_ConcurrentChangedInputCreatesDistinctVersions` (different tasks concurrently, no content lost). | **CLOSED** |
| **MINOR-1** Unnecessary defensive `time.Sleep` in audit-count test | Not fixed (non-blocking). | `TestSpecAdapter_ConcurrentIdempotentReuseAudit` ran 15/15 in the re-review and PASS in this session; the transactional audit write is verified by `audit.RecordTx` + `TestRecordTx_JoinsCallerTransaction`. The sleep masks nothing. | **ACCEPTED_FOLLOW_UP** (no FU id; cosmetic test hygiene) |
| **MINOR-2** `spec show` on missing task says "specification not found" not "task not found" | Not fixed (pre-existing). | Already tracked as FU-M14-03-4. Both messages are HTTP 404; CLI contract (exit 1, readable message) holds. | **ACCEPTED_FOLLOW_UP** (`FU-M14-03-4`) |
| **MINOR-3** `compileLocks.release` would panic on double-release | Not fixed (latent). | Latent only; not reachable by any current caller (`release` is only invoked via `defer` after a successful `acquire`). | **ACCEPTED_FOLLOW_UP** (latent) |

MAJOR-1 and MAJOR-2 are **CLOSED**. The three MINOR findings are
non-blocking and accepted as follow-ups; none violates a mandatory AC.

## Acceptance matrix

Mandatory acceptance criteria (per the M14-03 task brief and
`M14-03_IMPLEMENTATION.md`):

| Criterion | Production implementation | Automated evidence (re-run this session) | Independent black-box result | Status |
|---|---|---|---|---|
| **AC1** User can obtain a compiled specification through the real CLI/API | `internal/daemon/spec_api.go:CompileSpec` (load task → `task.Compile` → `SaveIfChanged`); transport `POST /tasks/{id}/specification/compile` (`internal/transport/spec_api.go`); CLI `forge spec save` (`internal/cli/spec_cmd.go`); GET path `GET /tasks/{id}/specification` + `forge spec show` | Transport: `TestSpecAPI_Compile_HappyPath/AllowsEmptyBody/Get_LatestAndVersion/DTO_JSONShape` (PASS). Daemon: `TestSpecAdapter_CompileAndGetAndLock` (PASS). Black-box: `TestSpecSave_BlackBox_CreateCompileShowLockRestart` (PASS). | Manual black-box: `forge spec save -t … --by alice --json` → version 1, Created=true, Objective="Add a retry button to the login form.", AC-1, R1, C1, VisualRequirements.Required=false. `forge spec show` returns the same persisted spec. | **MET** |
| **AC2** The result survives a daemon restart | All spec state in SQLite (migration v8, applied by `daemon.Run` → `db.Migrate`); no in-memory cache on the read path (every read goes through `SpecificationStore`). | Daemon: `TestSpecAdapter_PersistsAcrossRestart` (in-process stop/start, byte-identical) PASS. Black-box: `TestSpecSave_BlackBox_CreateCompileShowLockRestart` steps 9–11 PASS. | Manual black-box: `forge daemon stop` → `forge daemon start` → `forge spec show` returns version 1, same objective, AC-1, Locked=true, LockedBy=alice (full provenance survives restart). | **MET** |
| **AC3** A repeat request is idempotent | `CompileSpec` delegates to `SaveIfChanged`: semantically equal → return latest unchanged (Created=false, no Save, no audit event); differs → mint new version (defensive fallback, unreachable via current API). Per-task keyed mutex serialises compare-and-mint. | Daemon: `TestSpecAdapter_CompileAndGetAndLock` steps 2 & 8 (idempotent reuse + after lock) PASS. Black-box: `TestSpecSave_BlackBox_CreateCompileShowLockRestart` steps 6 & 12 PASS. Negatives: `TestSpecAdapter_LockedUpdateRejected`, `TestSpecSave_BlackBox_LockedSpecNoNewVersion` PASS. | Manual black-box: second `forge spec save` → Created=false, version 1; third `forge spec save` after restart → Created=false, version 1; 20 concurrent `forge spec save` → `forge spec versions` still `[1]`. | **MET** |
| **AC4** Unit-only wiring is insufficient — black-box evidence is required | (all of the above) | `TestSpecSave_BlackBox_CreateCompileShowLockRestart/InvalidTask/LockedSpecNoNewVersion/TextOutput/FlagValidation/ConcurrentIdenticalSaveCreatesSingleVersion` — 6 compiled-binary tests, all PASS this session. | The full lifecycle scenario above was independently reproduced through the real `forge` binary against an isolated `NEUROFORGE_HOME` (never the user's real home), exercising the real daemon transport and real SQLite. | **MET** |

All four mandatory acceptance criteria are proven by automated evidence
independently re-run at unit, daemon-integration, race, and compiled-binary
black-box levels in this session.

## Commands executed

All commands ran from the primary checkout at HEAD `086748a` with a fresh
build. Toolchain: `go version go1.26.5 darwin/arm64`.

| Command | Exit code | Result |
|---|---:|---|
| `go build -o /tmp/forge-m14-03-accept ./cmd/forge` | 0 | compiled binary produced |
| `/tmp/forge-m14-03-accept gate baseline` | 0 | schema 1, baseline 1, doc path correct |
| `/tmp/forge-m14-03-accept gate next --manifest docs/reviews/m14/M14-02.manifest.json` | 0 | predecessor M14-02 ACCEPTED; successor may start |
| `go vet ./...` | 0 | clean |
| `gofmt -l .` | 0 | clean (no files listed) |
| `go test -count=1 -run 'TestSpecAPI' ./internal/transport/` | 0 | transport contract PASS |
| `go test -count=1 -run 'TestSpecAdapter' ./internal/daemon/` | 0 | daemon integration PASS (incl. restart, 15.9s) |
| `go test -count=1 -run 'TestSpecificationsSemanticallyEqual' ./internal/task/` | 0 | 30-case equality table PASS |
| `go test -count=1 -run 'TestSaveIfChanged' ./internal/task/` | 0 | SaveIfChanged concurrency tests PASS |
| `go test -count=1 -run 'TestSpecSave_BlackBox' -timeout 180s ./internal/cli/` | 0 | 6 compiled-binary black-box tests PASS (11.7s) |
| `go test -count=1 -run 'TestSpecCompile_BlackBox' ./internal/cli/` | 0 | M14-02 offline regression PASS |
| `go test -count=20 -run 'TestCompile_Deterministic$' ./internal/task/` | 0 | determinism 20× PASS |
| `go test -race -count=1 -run 'TestSpecAPI\|TestSpecAdapter\|TestSpecSave\|TestSpecificationsSemanticallyEqual\|TestSaveIfChanged' ./internal/transport/ ./internal/daemon/ ./internal/cli/ ./internal/task/` (with `\|` → `\|` RE2 alternation) | 0 | targeted race PASS, no race |
| `make check` | 0 | gofmt clean + `go vet ./...` clean + full suite green (FAIL_COUNT 0; every package `ok`) |
| `go test -race ./...` | 0 | every package `ok`; **no FAIL, no race detected** |
| Manual black-box (compiled `/tmp/forge-m14-03-accept`, isolated HOME) | 0 | create → compile → show → lock → restart → show → idempotent re-save → 20 concurrent saves → all consistent (`[1]`) |

No skipped/manual/opt-in test is the sole evidence for a mandatory criterion.
The compiled-binary black-box tests run under both `make check` and
`go test -race ./...` (verified: full suite green, race detector clean).

## Independent black-box reproduction (manual, this session)

Driven through the real compiled `forge` binary against an isolated
`NEUROFORGE_HOME` (`/tmp/forge-accept-home.T2CSpF`, never the user's real home).
Project backed by a real throwaway Git repo (the daemon rejects non-Git paths).

| Step | Command | Result |
|---|---|---|
| 1 | `forge daemon start` | `daemon started (pid 6457) at http://127.0.0.1:56587` |
| 2 | `forge project add --json <git-repo>` | id `acceptproj-repo2-my2p17`, state DISABLED, profile LOCAL_REVIEW |
| 3 | `forge task add -p <pid> --json "Add a retry button…"` | task id `acceptproj-repo2-my2p17-1`, state NEW |
| 4 | `forge spec save -t <tid> --by alice --json` | version 1, Created=true, Objective="Add a retry button to the login form.", AC-1, R1, C1, VisualRequirements.Required=false, Confidence=MEDIUM, 1 uncertainty reason (synthesised AC), 1 risk reason ("keyword hint: button") |
| 5 | `forge spec show -t <tid> --json` | version 1, same objective, AC-1, locked=false |
| 6 | `forge spec save -t <tid> --json` (repeat) | Created=false, version 1 — **idempotent** |
| 7 | `forge spec lock -t <tid> -v 1 --by alice --json` | locked=true, locked_by=alice |
| 8 | `forge spec versions -t <tid> --json` | `[1]` |
| 9 | `forge daemon stop` | `daemon stopped` |
| 10 | `forge daemon start` | `daemon started (pid 6589) at http://127.0.0.1:56627` |
| 11 | `forge spec show -t <tid> --json` (after restart) | version 1, **same** objective, AC-1, **locked=true, locked_by=alice** — provenance survives restart |
| 12 | `forge spec save -t <tid> --json` (after restart) | Created=false, version 1 — **still idempotent** |
| 13 | `forge spec versions -t <tid> --json` | `[1]` |
| 14 | 20× concurrent `forge spec save -t <tid> --json` | `forge spec versions` → `[1]` — **no duplicate versions** |
| 15 | `forge daemon stop` | cleanup OK |

This independently proves the create → compile → show → lock → restart → show
lifecycle, restart persistence of (objective, AC IDs, version, lock state,
lock provenance), idempotent repeat (steps 6, 12, 14), and the concurrency
invariant (step 14).

## Concurrency and determinism verification

- **Concurrency:** 20 concurrent `forge spec save` (manual, step 14) → `[1]`;
  shipped `TestSaveIfChanged_ConcurrentIdenticalCreatesSingleVersion` (40
  goroutines, production store) and `TestSpecAdapter_ConcurrentIdenticalCompileCreatesSingleVersion`
  (30 goroutines, real transport) both PASS; targeted `-race` clean. The
  per-task keyed mutex is the effective guard (re-review sensitivity check:
  disabling it reproduced 12 duplicates).
- **Determinism:** `TestCompile_Deterministic` ×20 PASS; the compiler is pure
  (no I/O, no clock, no randomness; ordered outputs appended in fixed input
  sequence; `AttachmentRoles` JSON-serialised with sorted keys).
- **Actor separation:** implementer / remediation / review / re-review /
  acceptance are five pairwise-distinct session ids.

## Scope and regression assessment

- `git diff --name-only 5ed8b72 a9869f9` touches only: implementation report,
  review report, daemon `spec_api.go` / `daemon.go` / `api.go`, transport
  `api.go` / `server.go` / `spec_api.go` / `spec_client.go`, CLI `spec_cmd.go` /
  `help.go`, task `backlog.go` (defect fix) / `specification.go` (concurrency),
  and the corresponding test files. **No** changes to
  `docs/spec/NEUROFORGE_SPEC.md`, `docs/engineering/ENGINEERING_BASELINE.md`,
  `internal/enggate`, `internal/policy`, `internal/merge`, gate/baseline
  enforcement, or any adapter/core package boundary.
- No scope creep; no M14-04 / workgraph / scheduler / merge work in this delta.
- No new external dependencies (`go.mod` / `go.sum` unchanged).
- No `TODO`/`FIXME`/`panic("unimplemented")`/stub in the new production files;
  `gofmt -l .` clean; `go vet ./...` clean.
- The `Backlog.Get` `sql.ErrNoRows → ErrNotFound` defect fix is minimal,
  correct, regression-tested, and no other caller relied on the old wrapping
  (verified: the two `Tasks.Get` callers — `internal/daemon/api.go:171` and
  `internal/daemon/spec_api.go:79` — both propagate to the transport, which
  needs the clean "not found").
- M14-02 regression: `TestSpecCompile_BlackBox_*` (9 tests) and
  `TestCompile_Deterministic` ×20 PASS unchanged.
- `make check` green across every M0–M13 + M14-00/M14-01/M14-02 package; no
  regression. `go test -race ./...` clean.

## Known limitations and accepted follow-ups

1. **FU-M14-03-1:** TUI bindings for the spec endpoints — out of scope.
2. **FU-M14-03-2:** opt-in `--compile` flag on `forge task add` — out of scope.
3. **FU-M14-03-3:** order-independent AC-set equality — theoretical (compiler
   appends ACs in fixed input order); tracked.
4. **FU-M14-03-4 (MINOR-2):** typed GET errors / `spec show` error wording for
   missing task — pre-existing, non-blocking.
5. **FU-M14-03-5:** public API for recompiling a task from revised inputs.
   Today task description is immutable after `task add`, so the
   `SaveIfChanged` "content differs → new version" branch is a defensive
   fallback not reachable via any current public CLI/API endpoint. When a
   task-mutation surface is added, it must be backed by a black-box test
   proving version 2 is created and the "differs" row is genuinely exercised.
6. **FU-M14-03-6:** multi-daemon / multi-process idempotency. The per-task
   keyed mutex is process-local; NeuroForge runs a single daemon per database
   today, so this is not a practical limitation. If multi-daemon deployment is
   ever supported, the compare-and-mint must be pushed into a storage-level
   `BEGIN IMMEDIATE` transaction.
7. **MINOR-1 (cosmetic):** unnecessary defensive `time.Sleep` in
   `TestSpecAdapter_ConcurrentIdempotentReuseAudit` — masks nothing, can be
   removed in a future test-hygiene pass.
8. **MINOR-3 (latent):** `compileLocks.release` would panic on a double-release
   — not reachable by any current caller.

None of the above obstructs any mandatory acceptance criterion or the
sequential gate.

## Verdict

**ACCEPTED**

Every mandatory acceptance criterion (AC1 compiled spec via real CLI/API,
AC2 restart persistence, AC3 idempotent repeat, AC4 black-box evidence) is met
and proven by passing automated evidence independently re-run at unit,
daemon-integration, black-box, and race levels in this session. `make check`
is green (FAIL_COUNT 0); `go test -race ./...` is clean (no race detected);
`go vet ./...` and `gofmt -l .` are clean.

- M14-02 is `ACCEPTED` (predecessor gate exit 0).
- The re-review examined the exact remediated candidate `a9869f9…` and returned
  `REVIEW_APPROVED`; production/test code is byte-identical at HEAD.
- MAJOR-1 and MAJOR-2 are genuinely **CLOSED**; each fix is guarded by named
  regression tests re-run here.
- The three MINOR findings are non-blocking and accepted as follow-ups.
- Determinism is proven by 20× unit + manual binary reproduction.
- Actor separation is pairwise distinct across implementation / remediation /
  review / re-review / acceptance.
- No scope creep; product spec, baseline, and gate enforcement untouched.
- The manifest passes `forge gate validate` and `forge gate next` returns
  exit 0.

The successor task **M14-04 may now start** (`forge gate next --manifest
docs/reviews/m14/M14-03.manifest.json` returns exit 0).
