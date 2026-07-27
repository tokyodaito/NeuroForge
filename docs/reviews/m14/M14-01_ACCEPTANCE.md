# M14-01 Acceptance

## Acceptance identity

- acceptance actor/session ID: `M14-01-accept-session` (fresh, independent session)
- implementation actor/session ID: `M14-01-impl-session` (per `M14-01_IMPLEMENTATION.md`)
- review actor/session ID: `M14-01-review-session` (per `M14-01_REVIEW.md`)
- pairwise independence confirmed: yes — the three role-bound ids are pairwise
  distinct. The acceptor did not implement or review M14-01 and re-checked every
  implementation/review claim against the checked-out code/tests rather than
  trusting either report.
- acceptance date: 2026-07-27

## Git baseline

- implementation starting SHA: `7499d2ea97ca349e05977fa1806055226bf69360`
  (accepted M14-00 tip; the implementation report records a single-digit
  transcription `…0550226…`, already flagged as MINOR-3 by the reviewer — the
  real commit is `…0555226…`; ancestry verified)
- implementation candidate SHA: `48b059ccb82d146e25961fdc0402149751f4df8b`
- independent review commit SHA: `eec8276ba57f4add274f848991fd457b5b293537`
  (sole added file `docs/reviews/m14/M14-01_REVIEW.md`, sole parent the
  candidate — `git merge-base --is-ancestor 48b059cc eec8276` = ANCESTOR_OK)
- administrative starting HEAD: `94c88381e9c9d29ef73f8c80b494d2384a422ec9`
  (adds **only** `docs/reviews/m14/M14-02_IMPLEMENTATION.md`, a BLOCKED report;
  verified by `git diff --stat eec8276..94c8838` = 1 file, 209 insertions, 0
  production-code changes, 0 M14-01 evidence changes)
- acceptance commit SHA: recorded below after the commit is created.

> Choice of acceptance environment: **Variant A** (accept on top of the current
> administrative HEAD `94c8838`). Variant A is permitted because the
> administrative commit is verified to contain *exclusively* the M14-02 BLOCKED
> report — it touches no M14-01 production code, no M14-01 evidence, and no gate
> rule. This is the same acceptance model used by M14-00. The M14-01 production
> state under acceptance is byte-identical to the reviewed candidate `48b059cc`
> (the two intervening commits are doc-only). The administrative commit is **not**
> the M14-01 implementation candidate and is not labelled as such.

## Predecessor gate

- M14-00 manifest: `docs/reviews/m14/M14-00.manifest.json`
- M14-00 state: `ACCEPTED` (candidate `c7f8f57…`; acceptance report
  `docs/reviews/m14/M14-00_ACCEPTANCE.md`)
- gate command: `forge gate next --manifest docs/reviews/m14/M14-00.manifest.json`
- result: **exit 0** — `OK: predecessor "M14-00" is ACCEPTED; successor task may
  start`. The sequential gate is satisfied; M14-01 was allowed to start.

## Review prerequisite

- review verdict: `REVIEW_APPROVED` (`docs/reviews/m14/M14-01_REVIEW.md`)
- blocker findings: **0**
- major findings: **0**
- minor findings: 3
  - MINOR-1 — idempotent re-Lock emits a duplicate `task.specification.locked`
    audit event whose `by` payload disagrees with the durable `locked_by`
    (provenance-preserving). Audit-fidelity issue for a no-op; append-only trail
    intact; no corruption/security impact.
  - MINOR-2 — no negative audit regression tests (rejected-op-produces-zero-
    audit-events; audit-write-failure-rolls-back-storage-mutation). Behaviour is
    correct on inspection and code-read; currently unguarded by an automated
    check.
  - MINOR-3 — implementation report metadata: starting SHA transcription typo
    (`…0550226…` vs `…0555226…`); candidate SHA not stated explicitly.
- unresolved mandatory findings: **0** (all findings are MINOR, none obstruct any
  mandatory AC; MINOR-1/MINOR-2 are accepted as follow-ups, see "Known
  limitations").

The reviewer performed the mandatory sensitivity checks for this
concurrency/recovery/storage task: (1) disabling the `locked != 0` guard inside
`Tx.SaveSpecification` made `TestSaveSpecification_LockedVersionRejected`,
`TestSaveSpecification_ConcurrentLockedGuard`, and
`TestSpecificationStore_LockedVersionCannotBeMutated` all FAIL; (2) breaking the
version allocator (`next_version + 1` → `+ 0`) made
`TestNextSpecificationVersion_MonotonicAndRaceFree`,
`TestNextSpecificationVersion_ConcurrentNoCollisions`, and
`TestSpecificationStore_ConcurrentNewVersionsNoCollisions` all FAIL. Both
mutations were reverted in a disposable worktree (the candidate was not modified).
This satisfies the baseline's sensitivity-check expectation for storage/concurrency
invariants.

## Acceptance matrix

| Criterion | Production implementation | Test evidence | Acceptance result | Status |
|---|---|---|---|---|
| AC1 durable round trip | `task.SpecificationStore.Save/Get` (`internal/task/specification.go:246,318`); `storage.SaveSpecification/GetSpecification` (`internal/storage/specifications.go:63,144`); migration v8 tables (`internal/storage/migrate.go:313`); payload JSON + dedicated AC table | `task.TestSpecificationStore_SaveGetRoundTrip`, `task.TestSpecificationStore_RestartRecovery` (DB closed+reopened+re-migrated), `storage.TestSaveAndGetSpecification_RoundTrip` | Every structured field (objective, ACs + ids, non-goals, assumptions, constraints, risk, complexity, proposed scope, visual requirements, version, lock state, provenance/timestamps) round-trips and survives restart. Re-verified PASS. | MET |
| AC2 stable AC IDs | `AcceptanceCriterion.ID` (`specification.go:72`); `task_acceptance_criteria.ac_id` column + PK `(task_id,version,ac_id)` (`migrate.go:343`); deterministic `ORDER BY ordinal, ac_id` read-back (`specifications.go:171`); duplicate-id rejection in `ValidateSpecification` (`specification.go:175`) and by PK | `storage.TestSaveAndGetSpecification_RoundTrip`, `task.TestSpecificationStore_RestartRecovery`, `storage.TestSaveSpecification_IdempotentResave` | IDs stored in a dedicated column (not positional JSON); uniqueness enforced by PK and by domain validation; preserved across save/restart/idempotent re-save. Duplicate IDs rejected (`TestValidateSpecification_TableDriven/duplicate_acceptance_criterion_id`). | MET |
| AC3 locked immutability | `Tx.SaveSpecification` reads `locked` inside the same tx that writes (`specifications.go:89-118`); `Tx.LockSpecification` is idempotent + preserves original provenance via `CASE WHEN COALESCE(locked_at,'')='' …` (`specifications.go:251-257`); `SpecificationStore.Lock`+`Save` enforce via the domain service | `storage.TestSaveSpecification_LockedVersionRejected`, `storage.TestSaveSpecification_ConcurrentLockedGuard` (20 concurrent savers), `storage.TestLockSpecification_IdempotentAndNotFound`, `task.TestSpecificationStore_LockedVersionCannotBeMutated`, `task.TestSpecificationStore_RestartRecovery` (lock survives restart + still rejects mutation) | Lock check and mutation share one SQLite tx; idempotent re-lock preserves first reviewer; restart keeps lock; a NEW version remains allowed. Reviewer sensitivity check (disable guard) failed the 3 guarding tests. | MET |
| AC4 migration compatibility | migration v8 is purely additive (`CREATE TABLE IF NOT EXISTS` ×3 + index), appended to the forward-only `migrations` slice (`migrate.go:313`); applied by production `daemon.Run → db.Migrate` (`internal/daemon/daemon.go:84`); `applyMigration` runs `Up` + `INSERT INTO schema_migrations` in one tx (`migrate.go:430`) | `storage.TestMigrate_FromV7_AddsSpecificationTables` (seeds a real v7 DB via migrations 1..7, full `Migrate`, seeded rows survive, new tables usable), `storage.TestMigrate_IdempotentOnV8Schema`, `cli.TestSpecMigration_BlackBox_DaemonAppliesV8` (compiled `forge` daemon start/doctor/stop/restart) | Migration additive + idempotent; v7 data survives; new DB migrates to v8; daemon restart on v8 is a no-op; schema_migrations only written after `Up` commits, so a failed migration cannot leave a false version marker. Black-box daemon evidence re-verified PASS. | MET |
| AC5 atomic version allocation | `Tx.NextSpecificationVersion` = atomic `INSERT … ON CONFLICT DO UPDATE SET next_version = next_version + 1` then `SELECT` in the same tx (`specifications.go:303-316`); mirrors the accepted `Tx.NextTaskSeq` pattern; no process-local mutex | `storage.TestNextSpecificationVersion_MonotonicAndRaceFree`, `storage.TestNextSpecificationVersion_ConcurrentNoCollisions` (25 goroutines behind a start barrier), `task.TestSpecificationStore_ConcurrentNewVersionsNoCollisions` (20 concurrent `Save` with `Version=0`) | Distinct, gap-free, monotonically-increasing versions under genuine Go-level concurrency (barrier-held goroutines) serialised by SQLite's single-writer lock + `busy_timeout`. Holds for multiple store instances on the same DB file (mechanism is durable storage, not process-local). Reviewer sensitivity check (`+1`→`+0`) failed all 3 tests. | MET |
| AC6 storage + audit consistency | `SpecificationStore.Save`/`Lock` open a `storage.Tx`, perform the mutation and `audit.RecordTx(ctx, tx, …)` against the same tx, then `Commit` (`specification.go:255-307, 344-368`); `audit.RecordTx`→`Tx.AppendAuditEvent` shares `t.tx` (`internal/storage/audit.go`); rejected ops return before `audit.RecordTx` | `task.TestSpecificationStore_AuditRecorded` (save + lock events present) | Atomicity is architecturally real (shared tx; single durability point at `Commit`). Positive audit coverage proven. Two negative-coverage gaps (MINOR-1 audit fidelity on idempotent re-lock; MINOR-2 no negative audit regression tests) are accepted follow-ups — the core atomicity claim is proven by code-read and the shared-transaction mechanism already accepted across the codebase. | MET (with accepted MINOR follow-ups) |

## Commands executed

| Command | Exit code | Result |
|---|---:|---|
| `git status --short` (baseline) | 0 | HEAD `main` @ `94c8838`; only pre-existing untracked/modified review docs (`MINIMAL_RUN_*`, `M12_M13_REVIEW.md`), unrelated to M14-01 and untouched |
| `git merge-base --is-ancestor 48b059cc eec8276` | 0 | ANCESTOR_OK (review is exactly one commit ahead of candidate) |
| `git diff --stat eec8276..94c8838` | 0 | 1 file: `docs/reviews/m14/M14-02_IMPLEMENTATION.md` (+209) — administrative only |
| `git diff --stat 7499d2e..48b059cc` | 0 | 8 files, +2296 (matches report; no scope creep) |
| `go test -count=1 ./internal/task/...` | 0 | `ok neuroforge/internal/task 1.111s` |
| `go test -count=1 ./internal/storage/...` | 0 | `ok neuroforge/internal/storage 0.725s` |
| `go test -count=1 -v ./internal/cli/... -run TestSpecMigration_BlackBox_DaemonAppliesV8` | 0 | `--- PASS: TestSpecMigration_BlackBox_DaemonAppliesV8 (4.19s)` |
| `make check` | 0 | gofmt clean + `go vet ./...` clean + full suite green (FAIL_COUNT 0; every package `ok`, no regression in M0–M13 + M14-00) |
| `go test -race ./...` | 0 | every package `ok`; **no FAIL, no race detected** |
| `go test -race -count=1 -run 'Specification|Validate|Risk_|Complexity_|Migrate_FromV7|Migrate_IdempotentOnV8|SpecMigration' ./internal/task/ ./internal/storage/ ./internal/cli/` | 0 | all targeted concurrency/migration paths PASS under `-race`; no detector trip |
| `go build -o /tmp/forge-m14-01-accept ./cmd/forge` | 0 | compiled binary produced |
| `/tmp/forge-m14-01-accept gate baseline` | 0 | active schema_version 1, baseline_version 1, doc `docs/engineering/ENGINEERING_BASELINE.md` |
| `/tmp/forge-m14-01-accept gate next --manifest docs/reviews/m14/M14-00.manifest.json` | 0 | `OK: predecessor "M14-00" is ACCEPTED; successor task may start` |
| `/tmp/forge-m14-01-gate gate validate --manifest docs/reviews/m14/M14-01.manifest.json` | 0 | REVIEW_APPROVED → ACCEPTED legal under baseline v1 |
| `/tmp/forge-m14-01-gate gate next --manifest docs/reviews/m14/M14-01.manifest.json` | 0 | `OK: predecessor "M14-01" is ACCEPTED; successor task may start` |

Toolchain: `go version go1.26.5 darwin/arm64`.

## Durable storage verification

Independent re-run of `task.TestSpecificationStore_SaveGetRoundTrip`,
`task.TestSpecificationStore_RestartRecovery`, and
`storage.TestSaveAndGetSpecification_RoundTrip` all PASS. The restart test closes
the DB, reopens the **same file**, re-runs the production `Migrate` path, and
asserts every structured field (objective, ACs + ids + order, non-goals,
assumptions, constraints, risk, complexity, proposed scope, visual requirements,
version, lock state, locked_at/locked_by, created_at/created_by) is restored.
This is real-SQLite durability (real `modernc.org/sqlite` driver, real DB file),
not a fake. AC1 MET.

## Stable AC ID verification

`task_acceptance_criteria.ac_id` is a first-class column under PK
`(task_id, version, ac_id)` (`migrate.go:343-350`), so an AC's identifier survives
reorder, re-save, and restart (verified by `TestSaveSpecification_IdempotentResave`
— no orphans, ids durable — and `TestSpecificationStore_RestartRecovery`).
Duplicate IDs are rejected both by domain validation
(`TestValidateSpecification_TableDriven/duplicate_acceptance_criterion_id`) and by
the PK. Read-back ordering is deterministic (`ORDER BY ordinal, ac_id`,
`specifications.go:171`). Cross-version AC identity tracking is an explicit
out-of-scope follow-up (compiler concern), correctly not claimed. AC2 MET.

## Lock immutability verification

`Tx.SaveSpecification` performs `SELECT locked … FOR the (task_id, version)` and
the write inside the **same** `*storage.Tx` (`specifications.go:89-118`); SQLite's
single-writer serialisation makes the read-then-write race-free w.r.t. concurrent
lockers/savers. Verified by `TestSaveSpecification_LockedVersionRejected`,
`TestSaveSpecification_ConcurrentLockedGuard` (20 concurrent savers all rejected,
content intact), `TestSpecificationStore_LockedVersionCannotBeMutated` (locked
save rejected, a NEW version still allowed), and `TestSpecificationStore_RestartRecovery`
(lock survives restart and still rejects mutation). Re-lock is idempotent and
preserves original provenance (`CASE WHEN COALESCE(locked_at,'')='' …`,
`specifications.go:251-257`; `TestLockSpecification_IdempotentAndNotFound`).
Reviewer sensitivity check (disable the guard) failed the three guarding tests,
proving the invariant is genuinely test-guarded. AC3 MET.

## Migration compatibility verification

Migration v8 (`migrate.go:313-363`) is purely additive: three
`CREATE TABLE IF NOT EXISTS` + one index, appended to the forward-only
`migrations` slice. No `ALTER`, no `DROP`, no data rewrite. v7 rows are untouched.
`TestMigrate_FromV7_AddsSpecificationTables` seeds a real v7-era DB using the real
migrations 1..7, runs the full production `Migrate`, and asserts (a)
`CurrentVersion` advanced to 8, (b) seeded task + audit rows survive unchanged,
(c) the new tables are usable (insert + read round-trips).
`TestMigrate_IdempotentOnV8Schema` re-runs `Migrate` on a v8 DB and asserts the
version does not change and a seeded spec survives. `applyMigration` runs `Up` and
`INSERT INTO schema_migrations` in one tx (`migrate.go:430-447`); a failing `Up`
rolls back both, so a partial/failed migration cannot advance the recorded schema
version. The `task_specification_sequences` table is intentionally not backfilled
(there are no pre-existing specification versions — this is a brand-new substrate);
`NextSpecificationVersion` initialises it lazily. This is correct, not a gap. AC4
MET, including black-box daemon evidence (below).

## Concurrency and race verification

`-race` is clean across the whole module and across the targeted concurrency paths:
- `TestNextSpecificationVersion_ConcurrentNoCollisions`: 25 goroutines behind a
  `start` channel barrier call `db.NextSpecificationVersion`; assertions confirm
  the 25 returned values are exactly a permutation of `1..25` (no duplicates, no
  gaps).
- `TestSpecificationStore_ConcurrentNewVersionsNoCollisions`: 20 goroutines call
  the domain `Save` with `Version=0` concurrently; each reserves a distinct
  version AND the row is durable afterwards.
- `TestSaveSpecification_ConcurrentLockedGuard`: 20 concurrent savers against an
  already-locked version; every one receives `ErrSpecificationLocked` and the
  stored content is the original.

Race is genuine (goroutines are barrier-held until simultaneous release; the work
happens inside the store/storage calls, not serialised before the repository). The
allocator mechanism lives in durable storage (`INSERT … ON CONFLICT DO UPDATE SET
next_version = next_version + 1`), not a process-local mutex, so it holds for
multiple store/DB instances on the same DB file. AC5 MET.

## Storage and audit consistency

The atomicity claim is architecturally real, not nominal:
- `SpecificationStore.Save`/`Lock` begin a `storage.Tx`; the storage mutation and
  `audit.RecordTx(ctx, tx, …)` both execute against `t.tx`.
- `audit.RecordTx` → `Tx.AppendAuditEvent` → `appendAuditEvent(ctx, t.tx, …)`
  (`internal/storage/audit.go`): the audit `INSERT` shares the caller's tx.
- `Commit` is the single durability point. If the audit write fails, `Save`/`Lock`
  return the error without committing; the deferred `tx.Rollback()` discards the
  storage mutation too. Conversely, a mutation that fails never reaches the audit
  write.
- A rejected operation (validation error, `ErrSpecificationLocked`) returns before
  `audit.RecordTx`, so no audit row is produced for a rejected op.

The shared-transaction mechanism is the same one already accepted across the
codebase. `TestSpecificationStore_AuditRecorded` proves positive coverage. The
two coverage gaps (MINOR-1 idempotent re-lock audit fidelity; MINOR-2 no negative
audit regression tests) are correct-behaviour-on-inspection that is currently
unguarded by an automated check; they are accepted as follow-ups (see Known
limitations) and do not invalidate the atomicity claim. AC6 MET (with accepted
MINOR follow-ups).

## Black-box daemon migration evidence

`internal/cli.TestSpecMigration_BlackBox_DaemonAppliesV8` is genuine black-box
evidence, re-verified PASS (4.19s) in this session:
- `forgeBinary(t)` builds `./cmd/forge` via the real `go build` toolchain.
- `runForge(... "daemon","start")` against a fresh `t.TempDir()` home drives the
  production path `daemon.Run → storage.Open → db.Migrate`
  (`internal/daemon/daemon.go:84`), which applies migration v8.
- `runForge(... "doctor")` output is parsed for `schema vN`; the test asserts
  `N >= 8` (forward-compatible).
- `assertSpecTablesExist` opens the home's DB file read-only through the **real**
  `storage.Driver` and confirms `task_specifications`,
  `task_acceptance_criteria`, `task_specification_sequences` exist in
  `sqlite_master` — a structural check of the production DB file.
- `daemon stop` then `daemon start` exercises restart recovery: re-`Migrate` is a
  no-op, schema version unchanged, tables still present.

The `if testing.Short() { t.Skip(...) }` guard is the standard opt-out; the test
runs under both `make check` and `go test -race ./...`. It is not a
skipped/manual/opt-in test masquerading as mandatory evidence. No spec-CRUD
black-box exists, which is correct for M14-01 (no CLI/transport surface yet — the
compiler lands later); spec durability itself is proven by real-SQLite integration
tests and the migration is proven black-box through the compiled daemon.

## Regression assessment

- `make check` is green across every M0–M13 + M14-00 package; no regression.
- `go test -race ./...` is clean.
- The predecessor gate (`forge gate next` on M14-00) returns exit 0.
- No existing command, doc, or product-spec invariant is weakened. The new tables
  are additive; `task_specifications` FK→`tasks(id) ON DELETE CASCADE` is verified
  by `TestSpecification_CascadesOnTaskDelete`. The append-only `audit_events`
  triggers are untouched.
- No TODO/FIXME/panic in the new production code; `gofmt -l` clean; `go vet`
  clean.

## Known limitations and accepted follow-ups

1. **MINOR-1 (accepted follow-up):** idempotent re-Lock appends a
   `task.specification.locked` audit event with `by: <caller>` while the durable
   `locked_by` keeps the first reviewer. Audit fidelity issue for a no-op;
   append-only trail intact; no corruption/security impact. Not blocking.
2. **MINOR-2 (accepted follow-up):** no negative audit regression tests (rejected
   op → zero new audit events; audit-write failure → storage mutation rolls back).
   Behaviour is correct on inspection and code-read; currently unguarded. The most
   valuable follow-up (converts correct-but-unguarded behaviour into
   regression-guarded behaviour).
3. **MINOR-3 (documentation only):** implementation-report SHA metadata typo;
   candidate SHA not stated explicitly. Ancestry is correct and the candidate is
   unambiguous in the repo. No product impact.
4. **Out-of-scope by design (not findings):** the task compiler; `forge spec`
   CLI/transport CRUD; cross-version AC identity tracking; attachment-role mapping;
   daemon wiring of `SpecificationStore` (no consumer exists yet — wiring now would
   be a disguised stub, rule §36.25). These are explicit follow-ups tied to later
   milestones.

None of the above obstructs any mandatory acceptance criterion or the sequential
gate.

## Verdict

**ACCEPTED**

Every mandatory acceptance criterion (AC1 durable round trip, AC2 stable AC IDs,
AC3 locked immutability, AC4 migration compatibility, AC5 atomic version
allocation, AC6 storage + audit consistency) is met and proven by passing
automated evidence at unit, integration, and black-box levels. `make check` is
green; `go test -race ./...` is clean (no race detected); the targeted concurrency
and migration paths PASS under `-race -count=1`. The migration is proven black-box
through the compiled `forge` daemon. The lock invariant and the race-free
allocator are each proven genuine by the reviewer's sensitivity checks. Actor
separation is pairwise distinct across implementation/review/acceptance. There are
no BLOCKER or MAJOR findings; the three MINOR findings are accepted follow-ups.
The manifest passes `forge gate validate` and `forge gate next` returns exit 0.

The successor task **M14-02 may now start** (`forge gate next --manifest
docs/reviews/m14/M14-01.manifest.json` returns exit 0).
