# M14-01 Independent Review

## Review identity

- reviewer actor/session ID: `M14-01-review-session` (independent; fresh session, no
  access to the implementation session's state)
- implementation actor/session ID: `M14-01-impl-session` (per
  `docs/reviews/m14/M14-01_IMPLEMENTATION.md`)
- independence confirmed: yes — this review was performed against the checked-out
  candidate only; no implementation artefacts were reused. The implementation
  report was read as a *claim document* and every claim was re-checked against the
  code/tests.
- review date: 2026-07-27

## Git baseline

- starting SHA (actual, accepted M14-00 tip): `7499d2ea97ca349e05977fa1806055226bf69360`
- reviewed candidate SHA: `48b059ccb82d146e25961fdc0402149751f4df8b`
- review report commit SHA: self-referential — the commit with subject
  `M14-01: add independent review` whose sole added file is this report and whose
  sole parent is the reviewed candidate `48b059ccb82d146e25961fdc0402149751f4df8b`
  (resolve with `git log --format=%H -G '^M14-01: add independent review'`).
- ancestry verified: `git merge-base --is-ancestor` of the M14-00 SHA against the
  candidate returns `ANCESTOR_OK`; the candidate is exactly one commit ahead of
  M14-00.
- M14-00 gate: `docs/reviews/m14/M14-00_ACCEPTANCE.md` carries `Verdict: ACCEPTED`
  (candidate `c7f8f57...`). Sequential gate satisfied — M14-01 was allowed to start.

> Note on SHAs: the implementation report (and the task brief) record the starting
> SHA as `7499d2ea97ca349e05977fa1806050226bf69360` (`…0550226…`). The real commit
> is `7499d2ea97ca349e05977fa1806055226bf69360` (`…0555226…`): a single-digit
> transcription error. Ancestry is otherwise correct. See MINOR-3.

## Scope reviewed

Touched files (diff `7499d2ea…0555226..48b059cc…`, 8 files, +2296 lines) match the
report's claimed changed-file list exactly:

```
internal/storage/migrate.go                          (+51, migration v8)
internal/storage/specifications.go                   (+317, repository API)
internal/storage/specifications_test.go              (+548)
internal/task/specification.go                       (+453, domain model + store)
internal/task/specification_test.go                  (+201)
internal/task/specification_store_test.go            (+397)
internal/cli/spec_migration_blackbox_test.go         (+160)
docs/reviews/m14/M14-01_IMPLEMENTATION.md            (+169)
```

No edits outside the declared scope. No changes to `docs/spec/`, no daemon wiring
of `SpecificationStore`, no compiler, no `forge spec` CLI, no transport endpoints.
The only non-M14-01 working-tree entries (`docs/reviews/MINIMAL_RUN_*`,
`docs/reviews/M12_M13_REVIEW.md`) are pre-existing untracked/modified review docs,
untouched by this commit and by this review.

## Acceptance matrix

| Criterion | Implementation | Test evidence | Independent result | Status |
|---|---|---|---|---|
| AC1 durable round trip | `task.SpecificationStore.Save/Get`; `storage.SaveSpecification/GetSpecification`; migration v8 | `task.TestSpecificationStore_SaveGetRoundTrip`, `task.TestSpecificationStore_RestartRecovery` (DB closed+reopened+re-migrated), `storage.TestSaveAndGetSpecification_RoundTrip` | Every structured field (objective, ACs+ids, non-goals, assumptions, constraints, risk, complexity, proposed scope, visual requirements, version, lock state, provenance) round-trips and survives restart. Re-verified green. | MET |
| AC2 stable AC IDs | `AcceptanceCriterion.ID`; `task_acceptance_criteria.ac_id` column + PK `(task_id,version,ac_id)`; deterministic `ORDER BY ordinal, ac_id` read-back | `TestSaveAndGetSpecification_RoundTrip` (AC-2/AC-1 preserved), `TestSpecificationStore_RestartRecovery` (ids survive restart), `TestSaveSpecification_IdempotentResave` (no orphans) | IDs stored in a dedicated column (not positional JSON), uniqueness enforced by PK, preserved across save/restart/re-save. List order is normalised to sorted-by-id (documented in tests); IDs themselves are stable. Duplicate IDs rejected by `ValidateSpecification` and by the PK. | MET |
| AC3 locked immutability | `SaveSpecification` reads `locked` inside the same tx that writes; `LockSpecification` is idempotent + preserves original provenance (`CASE WHEN COALESCE(locked_at,'')…`) | `storage.TestSaveSpecification_LockedVersionRejected`, `storage.TestSaveSpecification_ConcurrentLockedGuard` (20 concurrent savers), `storage.TestLockSpecification_IdempotentAndNotFound`, `task.TestSpecificationStore_LockedVersionCannotBeMutated`, `task.TestSpecificationStore_RestartRecovery` (lock survives restart + still rejects mutation) | Lock check and mutation share one SQLite tx; idempotent re-lock preserves first reviewer; restart keeps lock. Sensitivity check (disabled the `locked != 0` branch) made 3 regression tests fail — proving the tests genuinely guard the invariant. | MET |
| AC4 migration compatibility | migration v8 is purely additive (`CREATE TABLE IF NOT EXISTS` ×3 + index), appended to the forward-only `migrations` slice; applied by production `daemon.Run → db.Migrate` (`internal/daemon/daemon.go:84`) | `storage.TestMigrate_FromV7_AddsSpecificationTables` (seeds a real v7 DB via migrations 1..7, full `Migrate`, seeded rows survive, new tables usable), `storage.TestMigrate_IdempotentOnV8Schema`, `cli.TestSpecMigration_BlackBox_DaemonAppliesV8` (compiled `forge` daemon start/stop/restart, `forge doctor` reports schema, tables physically present in home DB) | Migration is additive + idempotent; v7 data survives; new DB migrates to v8; daemon restart on v8 is a no-op. Each migration runs in its own tx and only records `schema_migrations` after `Up` succeeds, so a failed migration cannot leave a false version marker. | MET |
| AC5 atomic version allocation | `Tx.NextSpecificationVersion` = atomic `INSERT … ON CONFLICT DO UPDATE SET next_version = next_version + 1` then `SELECT` in the same tx; mirrors the accepted `Tx.NextTaskSeq` pattern; no process-local mutex | `storage.TestNextSpecificationVersion_MonotonicAndRaceFree`, `storage.TestNextSpecificationVersion_ConcurrentNoCollisions` (25 goroutines behind a `start` barrier), `task.TestSpecificationStore_ConcurrentNewVersionsNoCollisions` (20 concurrent `Save` with `Version=0`) | Allocation lives in durable storage; multiple goroutines receive distinct, gap-free, monotonically-increasing versions. Sensitivity check (`+1 → +0`) made all three tests fail with `duplicate version 1`. Mechanism is SQLite single-writer serialisation + `busy_timeout(5000)`, so it holds for multiple store instances on the same DB file too. | MET |
| AC6 storage + audit consistency | `SpecificationStore.Save`/`Lock` open a `storage.Tx`, perform the mutation and `audit.RecordTx(ctx, tx, …)` (shares the same tx), then `Commit` — genuinely atomic | `task.TestSpecificationStore_AuditRecorded` (save + lock events present) | Atomicity is real: `Tx.AppendAuditEvent` writes into `t.tx`, so the audit row and the spec row commit together or roll back together. Positive audit coverage is present. Two gaps in *negative* audit coverage and one audit-fidelity nuance are recorded as MINOR findings; the core atomicity claim is proven. | MET (with MINOR findings) |

## Commands executed

| Command | Exit code | Result |
|---|---:|---|
| `make check` | 0 | fmt-check + vet + full suite green; every package `ok` (incl. `internal/task`, `internal/storage`, `internal/cli`); FAIL_COUNT 0. No M0–M13 regression. |
| `go test -race -count=1 ./internal/task/...` | 0 | `ok neuroforge/internal/task 2.785s` |
| `go test -race -count=1 ./internal/storage/...` | 0 | `ok neuroforge/internal/storage 2.995s` |
| `go test -race -run TestSpecMigration_BlackBox -count=1 -v ./internal/cli/` | 0 | `--- PASS: TestSpecMigration_BlackBox_DaemonAppliesV8 (7.16s)` |
| `go test -race -count=1 ./...` | 0 | every package `ok`; no FAIL, **no race detected** |
| `gofmt -l <7 touched files>` | 0 | clean (GOFMT_CLEAN) |

Toolchain: `go version go1.26.5 darwin/arm64`. Race detector clean across the whole
module (including the version allocator, the concurrent locked-guard, and the
20-goroutine concurrent-new-version path).

## Black-box verification

`internal/cli.TestSpecMigration_BlackBox_DaemonAppliesV8` is genuine black-box
evidence, not an internal-API call dressed up:

- `forgeBinary(t)` builds `./cmd/forge` via the real `go build` toolchain.
- `runForge(... "daemon","start")` against a fresh `t.TempDir()` home drives the
  production path `daemon.Run → storage.Open → db.Migrate`
  (`internal/daemon/daemon.go:84`), which applies migration v8.
- `runForge(... "doctor")` output is parsed for the literal token `schema vN`
  (emitted at `internal/cli/doctor_cmd.go:162`); the test asserts `N >= 8`
  (forward-compatible).
- `assertSpecTablesExist` opens the home's DB file read-only through the **real**
  `storage.Driver` and confirms `task_specifications`,
  `task_acceptance_criteria`, `task_specification_sequences` exist in
  `sqlite_master` — a structural check of the production DB file, not a `doctor`
  re-print.
- `daemon stop` then `daemon start` exercises restart recovery: re-`Migrate` is a
  no-op, schema version unchanged, tables still present.

The `if testing.Short() { t.Skip(...) }` guard is the standard opt-out; the test
runs under both `make check` and `go test -race ./...` (confirmed: 7.16s PASS).
This is not a skipped/manual/opt-in test masquerading as mandatory evidence.

No spec-CRUD black-box exists, which is correct for M14-01: there is no CLI /
transport surface for specifications yet (compiler lands later). Spec durability
itself is proven by real-SQLite integration tests (real driver, real DB file,
restart), and the migration is proven black-box through the compiled daemon.

## Race and concurrency verification

- `TestNextSpecificationVersion_ConcurrentNoCollisions`: 25 goroutines behind a
  `start` channel barrier call `db.NextSpecificationVersion`. The barrier
  releases them simultaneously, so the test creates genuine Go-level concurrency;
  SQLite serialises them via the single-writer lock + `busy_timeout(5000)`. The
  test asserts the 25 returned values are exactly a permutation of `1..25` (no
  duplicates, no gaps).
- `TestSpecificationStore_ConcurrentNewVersionsNoCollisions`: 20 goroutines call
  the domain `Save` with `Version=0` concurrently; each must reserve a distinct
  version AND the row must be durable afterwards.
- `TestSaveSpecification_ConcurrentLockedGuard`: 20 concurrent savers against an
  already-locked version; every one must receive `ErrSpecificationLocked` and the
  stored content must be the original.
- All three passed under `-race` with no detector trips.

Race is real (not serialised before the repository): the `start` barrier holds the
goroutines until release, and the work happens inside the store/storage calls.
Sensitivity check (below) proves the tests detect a broken allocator.

## Migration verification

- Forward-only, append-only: migration v8 is appended to `migrations` as entry 8;
  entries 1..7 are byte-identical to the M14-00 tip.
- Additive: three `CREATE TABLE IF NOT EXISTS` + one `CREATE INDEX IF NOT EXISTS`;
  no `ALTER`, no `DROP`, no data rewrite. Existing v7 rows are untouched.
- Backward-compatible: `TestMigrate_FromV7_AddsSpecificationTables` seeds a v7-era
  DB using the real migrations 1..7 (project + task + audit event), runs the full
  production `Migrate`, and asserts (a) `CurrentVersion` advanced to 8, (b) the
  seeded task + audit row survive unchanged, (c) the new tables are usable
  (insert + read a specification round-trips).
- Idempotent: `TestMigrate_IdempotentOnV8Schema` re-runs `Migrate` on a v8 DB and
  asserts the version does not change and a seeded spec survives.
- No false version marker: `applyMigration` runs `Up` and the
  `INSERT INTO schema_migrations` in one tx; a failing `Up` rolls back both, so a
  partial/failed migration cannot advance the recorded schema version.
- New empty DB → v8, and daemon stop/start restart idempotency are both proven by
  the black-box test above.

The `task_specification_sequences` table is intentionally **not** backfilled in v8:
there are no pre-existing specification versions (this is a brand-new substrate),
so the counter starts empty and `NextSpecificationVersion` initialises it. This is
correct, not a gap.

## Lock invariant verification

- The lock check and the mutation run inside the **same** `*storage.Tx`
  (`Tx.SaveSpecification`): `SELECT locked …` then `UPDATE/INSERT` on `t.tx`. A
  concurrent locker that commits first is observed by a subsequent saver's read;
  SQLite single-writer serialisation prevents a lost update. The check is not done
  "before" the transaction — it is inside it.
- Idempotent lock preserves original provenance:
  `SET locked=1, locked_at = CASE WHEN COALESCE(locked_at,'')='' THEN ? ELSE locked_at END, locked_by = …`
  (`specifications.go:251-257`); verified by
  `TestLockSpecification_IdempotentAndNotFound` (re-lock by a second reviewer keeps
  `locked_by = first-reviewer`).
- Restart keeps the lock and the rejection:
  `TestSpecificationStore_RestartRecovery` reopens the DB and asserts a post-
  restart `Save` on the locked version still returns `ErrSpecificationLocked`.
- Versioning a locked spec creates a **new** version (`Version=0` → new row), it
  does not mutate the locked row (`TestSpecificationStore_LockedVersionCannotBeMutated`).
- Sensitivity check: setting the locked-rejection branch to a constant `false`
  made `TestSaveSpecification_LockedVersionRejected`,
  `TestSaveSpecification_ConcurrentLockedGuard`, and
  `TestSpecificationStore_LockedVersionCannotBeMutated` all fail — the invariant is
  genuinely guarded by tests, not just by code comments.

Observation (not a finding): the concurrent-guard test locks the version *before*
spawning the savers, so it covers the "lock already held" interleaving rather than
a save racing *with* the lock acquisition. The architecture still protects the
latter: with WAL deferred transactions, a saver that read `locked=0` and then sees
a concurrent lock commit before its own write is rejected by SQLite
(`SQLITE_BUSY`/snapshot conflict) — it cannot silently land a mutation. No bypass
path exists.

## Audit atomicity verification

The atomicity claim is **architecturally real**, not nominal:

- `SpecificationStore.Save`/`Lock` begin a `storage.Tx`; the storage mutation and
  `audit.RecordTx(ctx, tx, …)` both execute against `t.tx`.
- `audit.RecordTx` → `Tx.AppendAuditEvent` → `appendAuditEvent(ctx, t.tx, …)`
  (`internal/storage/audit.go:45-47`): the audit `INSERT` shares the caller's tx.
- `Commit` is the single durability point. If the audit write fails, `Save`/`Lock`
  return the error without committing; the deferred `tx.Rollback()` discards the
  storage mutation too. Conversely, a mutation that fails never reaches the audit
  write.
- A rejected operation (validation error, `ErrSpecificationLocked`) returns before
  `audit.RecordTx`, so no audit row is produced for a rejected/rolled-back op.

The shared-transaction mechanism is the same one already accepted across the
codebase (e.g. tasks, workspaces, finalize intents). It is sound.

Two coverage gaps and one fidelity nuance are recorded as MINOR findings below;
none of them invalidate the atomicity claim itself.

## Sensitivity checks

Performed in a disposable `git worktree add -d` off the candidate SHA. All
mutations were reverted and the worktree was removed; the candidate commit was not
modified.

1. **Disable the locked-version guard.** Changed
   `internal/storage/specifications.go` `if locked != 0` → `if false && locked != 0`
   inside `Tx.SaveSpecification`. Result:
   - `TestSaveSpecification_LockedVersionRejected` **FAIL** (`expected ErrSpecificationLocked, got <nil>`)
   - `TestSaveSpecification_ConcurrentLockedGuard` **FAIL** (goroutine 0 not rejected)
   - `TestSpecificationStore_LockedVersionCannotBeMutated` **FAIL**
   
   Conclusion: the lock invariant is genuinely test-guarded.

2. **Break the version allocator.** Changed the sequence upsert
   `next_version = next_version + 1` → `next_version + 0` in
   `Tx.NextSpecificationVersion`. Result:
   - `TestNextSpecificationVersion_MonotonicAndRaceFree` **FAIL** (`second version = 1, want 2`)
   - `TestNextSpecificationVersion_ConcurrentNoCollisions` **FAIL** (`duplicate version 1 (race condition)`)
   - `TestSpecificationStore_ConcurrentNewVersionsNoCollisions` **FAIL** (`duplicate version 1 (collision)`)
   
   Conclusion: the race-free allocator is genuinely test-guarded; the concurrency
   tests are not no-ops.

Both temporary mutations were fully reverted (`git checkout --`); `git diff` was
empty before worktree removal.

## Findings

### [MINOR] Idempotent re-Lock emits a duplicate audit event whose `by` field disagrees with durable `locked_by`

Location:
`internal/task/specification.go:343-372` (`SpecificationStore.Lock`, specifically the
unconditional `audit.RecordTx` at lines 354-364); interaction with
`internal/storage/specifications.go:247-266` (`Tx.LockSpecification`, which preserves
the original `locked_by`).

Requirement:
AC6 — "duplicate/idempotent operation does not create misleading duplicate audit
events."

Observed:
`Lock` always appends a `task.specification.locked` event with payload
`by: <lockedBy>`, regardless of whether the version was already locked. The storage
layer, by design, keeps the **first** reviewer's `locked_by` on a re-lock
(`CASE WHEN COALESCE(locked_by,'')='' …`). So an idempotent re-lock by
`second-reviewer` writes an audit row saying `by: second-reviewer` while the
durable `locked_by` remains `first-reviewer`.

Evidence:
- Code path inspection (above).
- `TestLockSpecification_IdempotentAndNotFound` proves `locked_by` is preserved as
  the first reviewer.
- No test asserts what the audit trail records on an idempotent re-lock.

Impact:
An auditor reconstructing "who locked version N" from the audit log alone would
name a different actor than the durable `locked_by`. The append-only trail is
intact (no corruption, no security impact); the issue is audit fidelity / a
misleading duplicate event for a no-op.

Required fix:
Either (a) skip the `task.specification.locked` audit event when the version is
already locked (truly idempotent — no new durable state, no new event), or (b)
record the event with a payload that distinguishes a re-lock attempt (e.g.
`by: <original>`, `attempted_by: <caller>`). Add a regression test asserting the
audit-trail content of an idempotent re-lock (and that it does not mis-attribute
the lock).

### [MINOR] No negative audit regression tests

Location:
`internal/task/specification_store_test.go` (only `TestSpecificationStore_AuditRecorded`
exists, covering the positive path).

Requirement:
AC6 — "audit not recorded on rejected or rolled-back operation"; "storage mutation
does not stay committed if the mandatory audit write fails."

Observed:
The behaviour is correct on inspection — a rejected `Save` (validation error or
`ErrSpecificationLocked`) returns before `audit.RecordTx`, and a failed audit write
aborts the shared tx (rolling back the mutation). But no test asserts either of
these: there is no "rejected op produces zero new audit events" test and no test
that injects an audit-write failure to prove the storage mutation rolls back.

Evidence:
- `grep` of the test file shows a single audit-related test, positive-only.
- AC6 asks the reviewer to verify these behaviours, not just assume them.

Impact:
The correct behaviour is currently unguarded by an automated check. A future
refactor (e.g. moving the `audit.RecordTx` call before the storage mutation, or
swallowing the audit error) could regress silently. This does not indicate a
present defect.

Required fix:
Add two tests: (1) perform a rejected `Save` (invalid spec, and a save on a locked
version) and assert the audit-event count for that scope does not increase; (2)
inject a failing `AuditAppender` (e.g. a wrapper that returns an error) and assert
the storage mutation is not committed (no spec row, `ListVersions` empty).

### [MINOR] Implementation report metadata: incorrect starting SHA and no explicit candidate SHA

Location:
`docs/reviews/m14/M14-01_IMPLEMENTATION.md:10-14`.

Requirement:
Review checklist §3 — the implementation report must contain the correct starting
SHA, candidate SHA, scope, and changed files.

Observed:
- The starting SHA is recorded as `7499d2ea…0550226…`; the real M14-00 tip is
  `7499d2ea…0555226…` (single-digit transcription error; the same typo appears in
  the review task brief).
- The candidate SHA is not stated; the report defers resolution to
  `git log --format=%H -G '^M14-01:'`.

Evidence:
`git rev-parse HEAD~1` (parent of the candidate) =
`7499d2ea97ca349e05977fa1806055226bf69360`; `git log -1 7499d2e` confirms the
M14-00 ACCEPTED commit. `git show --stat 48b059cc…` confirms the candidate.

Impact:
No product impact. The ancestry is correct and the candidate is unambiguous in the
repository; the defects are purely in report metadata precision. Acceptance is not
obstructed.

Required fix:
Correct the starting SHA to `7499d2ea97ca349e05977fa1806055226bf69360` and state
the candidate SHA explicitly (`48b059ccb82d146e25961fdc0402149751f4df8b`).

## Scope and documentation assessment

- No scope creep. No compiler, no `forge spec` CLI, no transport CRUD, no Work
  Graph, no attachment-role mapping, no daemon wiring of `SpecificationStore`.
  These absences are correct for M14-01 (the report lists them as explicit
  follow-ups) and are not findings.
- No disguised stubs: no production code is registered/wired as if a feature were
  ready when it is not. `SpecificationStore` is a real, tested domain service that
  simply has no consumer yet — instantiating it in the daemon now would itself be
  a disguised stub (rule §36.25), so leaving it unwired is correct.
- No weakening of audit/security invariants: the append-only `audit_events`
  triggers are untouched; the new tables are additive; `task_specifications` FK→
  `tasks(id) ON DELETE CASCADE` is verified by
  `TestSpecification_CascadesOnTaskDelete`.
- No TODO/FIXME/panic in the new production code; `gofmt -l` clean; `go vet` clean.
- Layering is consistent with the existing `task.Task`/`storage.Task` split:
  domain types + validation + service in `internal/task`, data-only rows +
  SQL in `internal/storage`. The report flags an ADR for this as a follow-up,
  which is reasonable.

## Verdict

REVIEW_APPROVED

All six mandatory acceptance criteria (AC1 durable round trip, AC2 stable AC IDs,
AC3 locked immutability, AC4 migration compatibility, AC5 atomic version
allocation, AC6 storage+audit consistency) are met and proven by automated
evidence. `make check`, the targeted `-race` runs, and `go test -race ./...` are
green with no race detected. The migration is proven black-box through the compiled
`forge` daemon. The lock invariant and the race-free allocator are each proven
genuine by sensitivity checks (the regression tests fail when the invariant is
broken). The storage+audit atomicity claim is real (shared `*storage.Tx`). There
are **no BLOCKER or MAJOR findings**. The three MINOR findings (audit fidelity on
idempotent re-lock; missing negative audit tests; report SHA metadata) are
follow-up improvements and do not obstruct acceptance.

An independent acceptance session is **permitted** to proceed on candidate
`48b059ccb82d146e25961fdc0402149751f4df8b`. The MINOR findings should be tracked
as follow-ups (the negative audit tests are the most valuable to add, since they
convert currently-correct-but-unguarded behaviour into regression-guarded
behaviour).
