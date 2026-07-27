# M14-01 — Implementation Report

**Task:** M14-01 — Domain model of compiled task specification + SQLite migration
(durable, versioned specification substrate, **without** the compiler itself).
**Implementer actor:** `M14-01-impl-session`.
**Verdict:** `IMPLEMENTED_TESTED`

## SHAs

- **Starting SHA:** `7499d2ea97ca349e05977fa1806050226bf69360` (branch `main`,
  the accepted tip of M14-00).
- **Candidate SHA:** produced by the local commit `M14-01: ...` at the end of
  this task (resolve with
  `git log --format=%H -G '^M14-01:'`).

## Preconditions verified

- Previous M14 task (`M14-00`) verdict is `ACCEPTED` (acceptance report present
  at `docs/reviews/m14/M14-00_ACCEPTANCE.md`, candidate SHA
  `c7f8f57...`). ✓ — successor task may start.
- Working tree had no uncommitted M14-01 production changes at the starting SHA.
  The only unstaged/untracked entries (`docs/reviews/MINIMAL_RUN_*`,
  `docs/reviews/M12_M13_REVIEW.md`) are pre-existing review docs unrelated to
  M14-01 (identical situation to M14-00); they are **not** touched by this task
  and **not** committed.

## Goal and actual scope

Add the durable, versioned substrate for a structured (compiled) task
specification, **without** implementing the compiler (spec §18.1, §9, §26, §27,
§28). The compiler + `forge spec` CLI land in a later milestone; M14-01 delivers
the data model, the SQLite schema + forward-only migration, and the repository
API (idempotent create/read/version/lock + restart recovery).

**Scope delivered:**

1. **Domain model** (`internal/task/specification.go`): types `Risk` (R0..R4,
   §26), `Complexity` (C0..C3), `AcceptanceCriterion` (stable `ID` +
   `Statement`), `VisualRequirements` (structured: required/viewport/theme/
   locale/density/references), and `Specification` carrying objective, ACs,
   non-goals, assumptions, constraints, risk, complexity, proposed scope, visual
   requirements + version + lock status. Pure `ValidateSpecification` enforces
   all structural invariants and normalises whitespace; returns every violation
   at once via `errors.Join`.
2. **SQLite schema (migration v8)** in `internal/storage/migrate.go`:
   - `task_specifications` (one row per `(task_id, version)`; FK→tasks,
     cascade);
   - `task_acceptance_criteria` (stable `ac_id` in its own column, scoped to a
     version);
   - `task_specification_sequences` (per-task monotonic version counter —
     atomic UPSERT-increment, mirrors `task_sequences`).
3. **Repository API** (`internal/storage/specifications.go`): data-only row
   types + `DB`/`Tx` methods — `SaveSpecification` (idempotent resave of an
   unlocked version; rejects a locked version with `ErrSpecificationLocked`),
   `GetSpecification`, `GetLatestSpecification`, `ListSpecificationVersions`,
   `LockSpecification` (idempotent, preserves original provenance),
   `NextSpecificationVersion` (race-free atomic counter). The locked-flag
   check and the write share one SQLite transaction (single-writer
   serialisation → race-free).
4. **Domain service** (`internal/task.SpecificationStore`): wraps storage +
   audit; runs `ValidateSpecification` before any write; reserves the version
   and persists the row + audit event in one transaction (§11.4, §29.4).

**Out of scope (explicit follow-ups, not started):** the task compiler itself;
attachment-role mapping in the spec (spec §18.1 lists it, but it is absent from
the M14-01 task scope); cross-version AC id stability tracking (compiler
concern); daemon transport endpoint + `forge spec` CLI for spec CRUD; wiring
`SpecificationStore` into the daemon service composition (no consumer exists
yet — instantiating an unused service would itself be a disguised stub, rule
§36.25). The product spec was **not** modified (baseline rule 3).

## Changed files

```
internal/storage/migrate.go                          | modified (migration v8)
internal/storage/specifications.go                   | new (repository API: rows + Save/Get/List/Lock/NextVersion)
internal/storage/specifications_test.go              | new (migration + CRUD + lock + idempotency + race + cascade)
internal/task/specification.go                       | new (domain types + ValidateSpecification + SpecificationStore)
internal/task/specification_test.go                  | new (table-driven validation)
internal/task/specification_store_test.go            | new (domain store: round-trip + lock + restart recovery + audit + concurrency)
internal/cli/spec_migration_blackbox_test.go         | new (black-box: forge daemon applies v8, doctor reports schema, restart idempotent)
docs/reviews/m14/M14-01_IMPLEMENTATION.md            | new (this report)
```

## Acceptance criterion → code → test

| AC | Where implemented | Test(s) | Why the test verifies observable behaviour |
|----|-------------------|---------|--------------------------------------------|
| **AC1** Spec is saved and fully restored after restart | `task.SpecificationStore.Save/Get` (`internal/task/specification.go`); persistence in `storage.SaveSpecification`/`GetSpecification` (`internal/storage/specifications.go`); migration v8 (`internal/storage/migrate.go`) | `internal/task.TestSpecificationStore_SaveGetRoundTrip` (every field incl. AC ids, list fields, visual requirements round-trips); `internal/task.TestSpecificationStore_RestartRecovery` (DB closed + reopened + re-migrated; all fields + lock state restored); `internal/storage.TestSaveAndGetSpecification_RoundTrip` | Round-trip asserts every structured field equals what was saved. Restart test closes the DB, reopens the **same file**, re-runs `Migrate` (the production daemon's recovery path), and asserts the full spec is restored — directly observable durability, not a fake. |
| **AC2** Each acceptance criterion has a stable ID | `AcceptanceCriterion.ID` (`internal/task/specification.go`); `task_acceptance_criteria.ac_id` column + PK `(task_id, version, ac_id)` (migration v8); `storage.AcceptanceCriterionRow.AcID` (`internal/storage/specifications.go`) | `TestSaveAndGetSpecification_RoundTrip` (AC-1/AC-2 ids preserved exactly); `TestSpecificationStore_RestartRecovery` (ids survive restart); `TestSaveSpecification_IdempotentResave` (AC set is replaced, ids remain durable, no orphans) | The id is stored in its own dedicated column (not positional JSON), so re-reading yields the identical id even after reorder/resave/restart. Asserted at the storage row level and the domain level. |
| **AC3** Locked version cannot be silently changed | `SaveSpecification` reads `locked` and returns `ErrSpecificationLocked` before writing (`internal/storage/specifications.go`); `LockSpecification` sets `locked=1` and preserves original `locked_at`/`locked_by` (`internal/storage/specifications.go`); `SpecificationStore.Lock` + `Save` enforce through the domain service | `internal/storage.TestSaveSpecification_LockedVersionRejected` (save on locked version → `ErrSpecificationLocked`, stored content is the original); `internal/storage.TestSaveSpecification_ConcurrentLockedGuard` (20 concurrent savers all rejected, content intact); `internal/storage.TestLockSpecification_IdempotentAndNotFound` (re-lock preserves first reviewer); `internal/task.TestSpecificationStore_LockedVersionCannotBeMutated` (locked save rejected; a NEW version is still allowed); `internal/task.TestSpecificationStore_RestartRecovery` (lock survives restart and still rejects mutation) | The check and the write share one SQLite transaction, so the invariant holds under concurrency. Tests prove both the happy path (new version allowed) and the negative (locked mutation rejected, original content intact, provenance durable across restart). |
| **AC4** Migrations do not break existing databases | Migration v8 is purely additive (`CREATE TABLE IF NOT EXISTS` + index + sequence table) appended to the forward-only `migrations` slice (`internal/storage/migrate.go`); applied by the production `db.Migrate()` in `daemon.Run` | `internal/storage.TestMigrate_FromV7_AddsSpecificationTables` (seeds a real v7-era DB with project/task/audit via the real migrations 1..7, runs full `Migrate`, asserts version advanced to 8, seeded rows survive unchanged, new tables are usable); `internal/storage.TestMigrate_IdempotentOnV8Schema` (re-migrate is a no-op); `internal/cli.TestSpecMigration_BlackBox_DaemonAppliesV8` (compiled `forge daemon start` applies v8; `forge doctor` reports schema; stop+start restart is idempotent; tables physically present in the home DB) | The v7→v8 test seeds a database exactly as an older daemon would have left it (using the real migrations 1..7), then runs the production migration path and proves existing data survives and the new schema is usable. The black-box test drives the **compiled binary** (not internal objects) and observes the schema version through `forge doctor` — production wiring evidence (engineering baseline §2). |

## Exact commands and results

```sh
gofmt -w <new files>                                    # applied; gofmt -l . is clean
go vet ./...                                            # clean
go test -race -count=1 ./internal/task/                 # PASS  (2.4s)
go test -race -count=1 ./internal/storage/              # PASS  (2.6s)
go test -run TestSpecMigration_BlackBox -count=1 -v ./internal/cli/  # PASS (3.4s; daemon started+stopped, schema v8 observed)
go test -race -run 'Specification|Validate|Risk_|Complexity_|Migrate_FromV7|Migrate_IdempotentOnV8|SpecMigration' -count=1 ./internal/task/ ./internal/storage/ ./internal/cli/  # PASS
make check                                              # exit 0 (fmt-check + vet + full test suite; FAIL_COUNT=0)
go test -race ./...                                     # no FAIL, no race detected
```

All gates green. `make check` exit 0 across every package (no regression in
M0–M13 + M14-00 suites). `-race` clean for the touched concurrency paths
(version allocator, locked-guard, concurrent new-version saves).

## Black-box evidence (compiled `forge` binary, observable)

`internal/cli.TestSpecMigration_BlackBox_DaemonAppliesV8` drives the production
binary, not internal Go objects:

1. `forge daemon start` (fresh home) → the daemon's `daemon.Run → db.Migrate`
   applies migration v8.
2. `forge doctor` prints `... (schema v8, WAL)` — the schema-version check reads
   `storage.CurrentVersion`. The test parses `schema vN` and asserts `N >= 8`
   (forward-compatible).
3. The home's DB file is opened read-only through the **real SQLite driver**
   (`storage.Driver`) and `task_specifications`, `task_acceptance_criteria`,
   `task_specification_sequences` are confirmed present in `sqlite_master`.
4. `forge daemon stop` then `forge daemon start` again → restart recovery:
   re-`Migrate` is a no-op, schema version unchanged, tables still present.

This is genuine black-box evidence that the migration is wired into the
production daemon path and is backward-compatible/idempotent on restart.

## Known limitations

- **Spec CRUD has no CLI/transport surface yet.** M14-01 deliberately does not
  implement the compiler or a `forge spec` command (out of scope: "without
  implementing the compiler itself"). Spec create/read/version/lock is therefore
  proven by real-SQLite integration tests (real `modernc.org/sqlite` driver,
  real migrations, real DB file, restart recovery) rather than by a binary-driven
  CRUD scenario. The migration itself IS proven black-box through the compiled
  daemon (above). A spec-CRUD black-box is a follow-up tied to the compiler/
  transport milestone that exposes a user surface.
- **Cross-version AC id stability** (the same logical "AC-1" tracked across
  versions) is not modelled; within a version each AC's id is stable and durable
  (AC2). Cross-version identity is a compiler concern.
- **Attachment-role mapping** (spec §18.1 lists it) is omitted: it is absent
  from the M14-01 task scope ("objective, acceptance criteria..., proposed scope
  and visual requirements"). Recorded as a follow-up.
- `SpecificationStore` is not yet instantiated by the daemon service composition
  (`internal/daemon`): there is no consumer today, and instantiating an unused
  service would be a disguised stub (rule §36.25). The compiler/transport
  milestone will wire it.

## Follow-up issues

1. Task compiler (free-form text → `Specification`), spec §18.1/§9.
2. Daemon transport endpoint + `forge spec {create,get,lock,versions}` CLI and a
   binary-driven spec-CRUD black-box test.
3. Cross-version acceptance-criterion id stability (logical AC identity).
4. Attachment-role mapping in the compiled specification.
5. Wire `SpecificationStore` into `daemon.NewServices` once a consumer exists.
6. ADR for the package-boundary decision (spec types live in `internal/task`,
   data-only rows in `internal/storage` — consistent with the existing
   `task.Task`/`storage.Task` split).

## Verdict

`IMPLEMENTED_TESTED` — all four mandatory acceptance criteria are proven by
automated tests (table-driven domain validation, migration from empty + previous
v7 schema, create/read/version/lock/idempotency, concurrent race, restart
recovery) plus a black-box test through the compiled `forge` binary; `make check`
and `go test -race ./...` are green.
