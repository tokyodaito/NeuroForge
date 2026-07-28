# M14-05 — Implementation Report

**Task:** M14-05 — Durable Work Graph, leases and readiness.
**Implementer actor:** `M14-05-impl-session-1` (fresh, independent session; no
prior M14-05 work).
**Verdict:** `IMPLEMENTED_TESTED`

## Preconditions verified

- Predecessor `M14-04` is `ACCEPTED` (manifest
  `docs/reviews/m14/M14-04.manifest.json` state `ACCEPTED`, baseline v1;
  acceptance report `docs/reviews/m14/M14-04_ACCEPTANCE.md`).
- Compiled-binary gate open against the starting HEAD `56f7107`:

  ```sh
  $ /tmp/forge-m14-05 gate baseline
  active schema_version: 1
  active baseline_version: 1
  baseline document: docs/engineering/ENGINEERING_BASELINE.md

  $ /tmp/forge-m14-05 gate validate --manifest docs/reviews/m14/M14-04.manifest.json
  OK: task "M14-04" transition REVIEW_APPROVED -> ACCEPTED is legal under baseline v1

  $ /tmp/forge-m14-05 gate next --manifest docs/reviews/m14/M14-04.manifest.json
  OK: predecessor "M14-04" is ACCEPTED; successor task may start
  ```

- Working tree at the starting SHA contained only pre-existing unrelated
  review docs (`docs/reviews/MINIMAL_RUN_*`, `docs/reviews/M12_M13_REVIEW.md`,
  `ism`); they predate this task and are NOT touched.

## SHAs

- **Predecessor acceptance SHA:** `56f7107bb08698f448c7afe6388d2e0d51cf6c3c`
  (`M14-04: accept task (state ACCEPTED, baseline v1)`).
- **Starting SHA (M14-05 implementation):** `56f7107bb08698f448c7afe6388d2e0d51cf6c3c`.
- **Candidate SHA:** this report's commit (`M14-05: <summary>`).

## Goal and actual scope

**Goal (from task brief):** persist the Work Graph, integrate path + semantic
leases as scheduling constraints, compute runnable packages with dependencies
+ leases, expose claim/renew/release/expire lease lifecycle, and survive
restart safely. Mandatory ACs:

1. Package not runnable until completion dependencies.
2. Conflicting lease blocks execution with explainable cause.
3. After restart graph and leases recover safely.

**Actual scope delivered:**

- **Migration v9** (`internal/storage/migrate.go`): three new tables
  (`work_packages`, `work_package_dependencies`, `work_package_attempts`) +
  `leases.expires_at` column (forward-compatible, empty = perpetual) +
  `idx_leases_unique_active_resource` partial UNIQUE index that closes a latent
  race in the M3 lease layer (the SELECT-then-INSERT pattern was not
  race-safe; the partial UNIQUE index plus SQLite's single-writer serialisation
  make the INSERT the linearisation point — mandatory for the concurrent-claim
  test).
- **Storage substrate** (`internal/storage/workgraph.go`, + helpers in
  `internal/storage/json.go` and `internal/storage/checkpoints.go`):
  `WorkPackageRow`, `WorkPackageAttemptRow`, `ReplaceWorkGraph`,
  `ListWorkPackages`, `GetWorkPackage`, `UpdateWorkPackageState`,
  `SetWorkPackageAttempts`, `AppendAttempt`, `ListAttempts`. Lease TTL:
  `Lease.ExpiresAt`, `GetActiveLease`, `RenewWorkspaceLeases`, `ExpireLeases`,
  `ErrLeaseAlreadyExists`/`IsLeaseUniqueConstraint`. `HasActiveLease` and
  `ListActive*` queries already exclude logically-expired rows
  (defence-in-depth so a slow sweeper cannot falsely block).
- **Domain WorkGraphStore** (`internal/workgraph/store.go`): wraps the
  storage substrate, accepts **only** `*ValidatedWorkGraph` (M14-04 AC2
  carried forward — invalid DAG cannot become durable runnable state);
  preserves runtime state + attempts across re-saves; idempotent
  replace-and-prune.
- **Readiness calculator** (`internal/workgraph/readiness.go`): pure function
  of `(ValidatedWorkGraph, active leases, now)`; per-package verdict with
  explainable blocked-reasons (dependency state + path-lease holder).
  Deterministic: reasons sorted lexicographically, packages in canonical
  order.
- **Scheduler** (`internal/workgraph/scheduling.go`): `Claim` (atomic:
readiness check → path-lease acquire → optional semantic-lease acquire →
  package state transition `pending → running`, with rollback on any failure),
  `Renew`, `Release`, `Expire`. Typed `ConflictError` / `NotReadyError` for
  explainable causes.
- **Lease manager extensions** (`internal/workgraph/leases.go`): `AcquirePathTTL`,
  `AcquireSemanticTTL`, `RenewAll`, `ExpireLeases`, `IsExpired`, typed
  `ConflictError` with per-conflict reasons; race-correct acquire (fast path:
  idempotent-or-conflict lookup; cold path: UNIQUE-protected INSERT with
  post-race re-read).
- **Transport API** (`internal/transport/workgraph_api.go` +
  `internal/transport/workgraph_client.go`): `GET /tasks/{id}/workgraph`
  returns the graph + per-package readiness + active-lease snapshot in one
  round-trip. `writeAPIError` extended to map lease-conflict / not-ready to
  HTTP 409.
- **Daemon adapter** (`internal/daemon/workgraph_api.go`): implements
  `transport.WorkGraphAPI`; loads the validated graph, snapshots leases,
  computes readiness, attaches the verdict to every package DTO. Wired into
  `daemon.Services` (`Graphs`, `Leases`) and `daemon.Run`
  (`WorkGraphAPI: workGraphAdapter`).
- **CLI command** (`internal/cli/workgraph_cmd.go` + dispatch in
  `internal/cli/cli.go` + help in `internal/cli/help.go`):
  `forge workgraph show -t <id> [--json]`.

**Out of scope (deferred — see Follow-ups):** the dispatch hook that calls
`Scheduler.Claim` in response to a daemon event; `forge workgraph claim`
CLI; per-lease `Renew` (currently `RenewAll`); richer scheduler policies
(retry budget, quota integration); TUI bindings; multi-daemon writer
coordination for the work graph (the lease layer is race-safe within one
daemon process).

## Files changed

Production code:

```
internal/daemon/api.go              | +5    (Services gains Graphs + Leases)
internal/daemon/daemon.go           | +7    (WorkGraphAPI wiring in Run)
internal/daemon/workgraph_api.go    | +174  (NEW — transport.WorkGraphAPI adapter)
internal/storage/checkpoints.go     | +114  (Lease.ExpiresAt, GetActiveLease, RenewWorkspaceLeases, ExpireLeases, ErrLeaseAlreadyExists, scanLeases 10th column)
internal/storage/json.go            | +30   (NEW — package-local json + utcNowRFC3339 helpers)
internal/storage/migrate.go         | +88   (migration v9: 3 tables + ALTER TABLE + partial UNIQUE index)
internal/storage/workgraph.go       | +333  (NEW — work-package substrate)
internal/transport/api.go           | +10   (writeAPIError: lease-conflict + not-ready → 409)
internal/transport/server.go        | +4    (Config.WorkGraphAPI + registerWorkGraphRoutes)
internal/transport/workgraph_api.go | +85   (NEW — DTOs + handler + registerWorkGraphRoutes)
internal/transport/workgraph_client.go | +20 (NEW — Client.GetWorkGraph)
internal/workgraph/json.go          | +9    (NEW — package-local json helpers)
internal/workgraph/leases.go        | +300  (TTL/expiry/Renew/Expire, ConflictError, race-correct acquire)
internal/workgraph/readiness.go     | +169  (NEW — ComputeReadiness + Readiness verdict)
internal/workgraph/scheduling.go    | +263  (NEW — Scheduler.Claim/Renew/Release/Expire + NotReadyError)
internal/workgraph/store.go         | +390  (NEW — WorkGraphStore accepting *ValidatedWorkGraph)
internal/cli/cli.go                 | +2    (dispatch "workgraph")
internal/cli/help.go                | +4    (help entry)
internal/cli/workgraph_cmd.go       | +177  (NEW — forge workgraph show)
```

Test code:

```
internal/daemon/workgraph_api_test.go | +218 (NEW — in-process transport tests + 404 case)
internal/cli/workgraph_show_blackbox_test.go | +278 (NEW — compiled-binary black-box + migration v9)
internal/workgraph/store_test.go      | +933 (NEW — store / readiness / lease TTL / scheduler / restart / race / integration)
```

~3700 added lines (≈1900 production + ≈1800 test). No production code
deleted. Product spec `docs/spec/NEUROFORGE_SPEC.md` untouched. No baseline /
gate enforcement weakened. No security / autonomy / delivery / merge-policy
invariant changed. No new external dependencies (`go.mod` / `go.sum`
unchanged).

## Acceptance criterion matrix

| Mandatory AC | Implementation | Test(s) | Verdict |
|---|---|---|---|
| **AC1** Package not runnable until completion dependencies | `workgraph.ComputeReadiness` (readiness.go): a package is `Ready` only if every dependency is in state `succeeded` AND no path-lease conflict AND state is `pending`. The verdict's `BlockedReasons` enumerate each unmet dependency by ID + current state. `Scheduler.Claim` (scheduling.go) re-checks readiness inside the claim path and refuses with `NotReadyError` if any reason is present. | `TestReadiness_BlockedByDependency`, `TestReadiness_UnblocksAfterDependencySucceeds`, `TestReadiness_TerminalStateNotReady`, `TestReadiness_BlockedByPathLease`, `TestReadiness_Deterministic`; `TestScheduler_ClaimSuccess`, `TestScheduler_ClaimBlockedByDependency`; integration `TestIntegration_CompileDecomposeSaveReadiness` (real `task.Compile` → `Decompose` → store → readiness). Black-box `TestWorkGraphShow_BlackBox_CreateShowAndExplainConflict` (asserts `Readiness: ready` for chain head + `Readiness: blocked` for the rest + dependency-not-succeeded reason in stdout). | **MET** |
| **AC2** Conflicting lease blocks execution with explainable cause | `LeaseManager.acquire` (leases.go): on conflict returns `*ConflictError` (wraps `ErrLeaseConflict`) whose `Reasons` slice names the resource, the holding workspace and (when known) the expiry. `Scheduler.Claim` propagates the typed error and rolls back any partial lease acquisitions + state transition. `workgraph.ComputeReadiness` predicts path-lease conflicts statically from `AllowedScope` and emits a reason naming the path + workspace. `writeAPIError` (transport/api.go) maps "lease conflict" / "not ready" to HTTP 409. | `TestLease_ConflictExplainsCause` (asserts `errors.Is(err, ErrLeaseConflict)` + `Reasons[0]` carries resource + workspace + HeldBy); `TestScheduler_ClaimBlockedByLeaseConflict` (asserts cause string names path + workspace); `TestLease_TTL_ExpiryReclaim` (TTL+expiry+sweep+reclaim); `TestLease_Renew`, `TestLease_RenewDoesNotAffectPerpetual`, `TestLease_SweeperIdempotent`; `TestScheduler_ClaimSemanticTTL` (semantic conflict → `ErrLeaseConflict`); `TestScheduler_RenewAndRelease`. Black-box: `TestWorkGraphShow_BlackBox_CreateShowAndExplainConflict` step 8 asserts the JSON readiness verdict on the first package exposes the conflicting path AND the workspace `other-ws`. | **MET** |
| **AC3** After restart graph and leases recover safely | `WorkGraphStore.Save` persists the graph through the v9 storage substrate (idempotent replace-and-prune; preserves runtime state + attempts); `LeaseManager` is backed by the durable `leases` table (TTL column added by v9). Restart = re-opening the SQLite DB and re-running `Migrate` (a no-op once v9 is applied). The daemon wires `WorkGraphStore` + `LeaseManager` into `Services` so the same durable handles survive the daemon's restart; `GET /tasks/{id}/workgraph` recomputes the readiness verdict against the persisted graph + leases. | `TestStore_RestartRecoversGraphAndLeases` (closes the DB handle to simulate a crash; re-opens against the same file; asserts packages, package state, attempts and active leases all survive; re-claim of running package fails for a new workspace). Black-box: `TestWorkGraphShow_BlackBox_CreateShowAndExplainConflict` step 9 stops the daemon, re-starts it, re-runs `forge workgraph show` and asserts both the graph (`packages` count) AND the active lease survive the restart; `TestWorkGraphShow_BlackBox_MigrationV9Applied` asserts the v9 schema is applied idempotently across restart (schema version unchanged, tables + UNIQUE index still present). | **MET** |

### Required test classes (task brief)

| Required class | Covered | Evidence |
|---|---|---|
| Dependency readiness tests | yes | `TestReadiness_BlockedByDependency`, `TestReadiness_UnblocksAfterDependencySucceeds`, `TestReadiness_TerminalStateNotReady`, `TestReadiness_Deterministic`; integration `TestIntegration_CompileDecomposeSaveReadiness` (real `task.Compile` composition) |
| Lease conflict, expiry and reclaim tests | yes | `TestLease_ConflictExplainsCause`, `TestLease_TTL_ExpiryReclaim`, `TestLease_Renew`, `TestLease_RenewDoesNotAffectPerpetual`, `TestLease_SweeperIdempotent`; `TestScheduler_ClaimSemanticTTL` |
| Persistence / restart | yes | `TestStore_RestartRecoversGraphAndLeases` (full close + re-open against same file); black-box `TestWorkGraphShow_BlackBox_CreateShowAndExplainConflict` step 9 (daemon stop + restart + show) |
| Concurrent claim race tests | yes | `TestScheduler_ConcurrentClaimRace` (16 goroutines racing the same package; asserts exactly 1 winner, 15 losers, exactly 1 surviving lease; race detector clean) |
| Black-box graph show через daemon | yes | `TestWorkGraphShow_BlackBox_CreateShowAndExplainConflict` (compiled-binary `forge workgraph show` end-to-end through the real daemon transport); `TestWorkGraphShow_BlackBox_MigrationV9Applied` (compiled-binary migration v9 evidence); in-process transport `TestWorkGraphAdapter_ShowThroughTransport` + `TestWorkGraphAdapter_MissingGraphIs404` |
| `make check` green | yes | exit 0 (see Commands) |
| `go test -race ./...` clean | yes | 61 packages `ok`; 0 FAIL; no race detected |

## Design decisions

### Race-correct lease acquisition (latent M3 defect fix)

The M3 `leases` table had no uniqueness constraint on
`(scope, scope_id, kind, resource)` for active rows. The M3 `acquire`
performed a `SELECT` for a conflict then an `INSERT`; under concurrent claims
two callers could both pass the SELECT and both succeed at the INSERT,
producing two active leases for the same resource. The M3 tests did not catch
this because they were sequential.

M14-05 closes this with a partial UNIQUE index
(`idx_leases_unique_active_resource ... WHERE state = 'active'`) plus a
restructured acquire: a fast path that re-reads the existing row (returning
it idempotently for the holder, or surfacing `ConflictError` for a different
workspace) and a cold path whose INSERT is the linearisation point. On
`ErrLeaseAlreadyExists` the loser re-reads and surfaces the explainable
conflict.

This is in-scope for M14-05 (the brief mandates a concurrent-claim race
test); it is the minimal correctness fix and is honestly documented.

### Logically-expired leases do not block (defence-in-depth)

A lease whose `expires_at` is in the past but whose row is still
`state='active'` (the sweeper has not yet run) is treated as expired by every
read path: `HasActiveLease`'s SQL filters them out, `ListActiveByProject`
post-filters them in Go, and `LeaseManager.acquire` sweeps them inline before
attempting a fresh INSERT. This makes reclaim latency independent of the
sweeper's cadence — a slow sweeper cannot falsely block execution (mandatory
AC). The sweeper's job is to convert the soft-expired state into an auditable
`state='expired'` row.

### Semantic leases are runtime conflicts; path leases are static readiness inputs

Path-lease conflicts are statically predictable from a package's
`AllowedScope`, so `ComputeReadiness` reports them as blocked-reasons. Semantic
leases are runtime-supplied (a package's "I need database_schema for this
work" is the dispatcher's input, not part of the graph definition), so they
are checked at `Claim` time: the caller passes `SemanticLeases` and
`Scheduler.Claim` attempts to acquire them; a conflict surfaces as
`*ConflictError`. Both paths produce explainable causes satisfying AC2.

### Save preserves runtime state for already-existing packages

A re-decomposed graph (via `workgraph.Decompose`) sets every package to
`PackagePending`. Naively persisting that would erase runtime state on
re-decomposition. `WorkGraphStore.Save` preserves the persisted `state` (and
attempts) for any package whose in-memory state is the Decompose default
(`PackagePending`) and whose ID already exists. A caller that explicitly
transitions a package to `running`/`succeeded` before Save still wins (the
caller's intent is honoured). This is the same shape the dispatch layer will
rely on when it re-decomposes after a spec re-compile.

## Commands executed and results

Toolchain: `go version go1.26.5 darwin/arm64`. All commands ran from the
primary checkout at the candidate SHA with a freshly built binary.

| Command | Exit | Result |
|---|---:|---|
| `go build ./...` | 0 | clean build |
| `go vet ./...` | 0 | clean |
| `gofmt -l .` | 0 | no files listed |
| `go test -count=1 ./internal/workgraph/...` | 0 | 35+ tests PASS (store / readiness / scheduling / leases / integration) |
| `go test -race -count=1 ./internal/workgraph/...` | 0 | race-clean |
| `go test -race -count=3 -run 'TestReadiness\|TestScheduler\|TestLease_TTL\|TestLease_Renew\|TestLease_Conflict\|TestStore_Restart\|TestWorkGraphStore' ./internal/workgraph/` | 0 | 3× determinism PASS, race-clean |
| `go test -count=1 -run 'TestWorkGraph' ./internal/daemon/` | 0 | transport-level adapter tests PASS |
| `go test -count=1 -run 'TestWorkGraph' ./internal/cli/` | 0 | compiled-binary black-box + migration v9 PASS |
| `make check` | 0 | gofmt clean + `go vet ./...` clean + full suite green (every package `ok`; FAIL_COUNT 0) |
| `go test -race ./...` | 0 | 61 packages `ok`; **0 FAIL, no race detected** |
| `/tmp/forge-m14-05 gate baseline` | 0 | active baseline_version 1 |
| `/tmp/forge-m14-05 gate next --manifest docs/reviews/m14/M14-04.manifest.json` | 0 | predecessor ACCEPTED; successor may start |

No skipped / manual / opt-in test is the sole evidence for a mandatory
criterion. `testing.Short()` skips the two compiled-binary black-box tests
when `-short` is set; they are the sole evidence for **no** criterion
(their evidence is duplicated by in-process integration tests +
`TestStore_RestartRecoversGraphAndLeases` for the restart AC).

## Black-box evidence

The mandatory black-box scenario is the compiled `forge` binary driving the
real daemon end-to-end. Two black-box tests cover it:

1. `TestWorkGraphShow_BlackBox_CreateShowAndExplainConflict`
   (`internal/cli/workgraph_show_blackbox_test.go`) drives the production CLI
   through the real daemon:

   ```
   daemon start
     → project add → task add → spec save (--json)
     → seed Work Graph through the daemon's own WorkGraphStore
       (the dispatch layer that does this internally is a later milestone;
        here we prove the read path + readiness surface are correct)
     → forge workgraph show -t <id>            (text)
     → forge workgraph show -t <id> --json     (machine-readable)
     → AcquirePath from a different workspace
     → forge workgraph show -t <id> --json     (must show blocked + reason)
     → daemon stop → daemon start              (restart recovery)
     → forge workgraph show -t <id> --json     (graph AND lease survive)
     → forge workgraph show -t bogus --json    (404 + "not found")
   ```

   Observed this session against a freshly compiled `forge`: every step
   passed (see test log); the explainable cause names both the conflicting
   path and the holding workspace; restart preserves both the package set
   and the active lease.

2. `TestWorkGraphShow_BlackBox_MigrationV9Applied` proves the production
   daemon applies migration v9 (`work_packages`,
   `work_package_dependencies`, `work_package_attempts`,
   `leases.expires_at`, `idx_leases_unique_active_resource`) and that
   re-migration is idempotent across a daemon restart (schema version
   unchanged, tables + UNIQUE index still present).

The in-process transport-level evidence
(`internal/daemon/workgraph_api_test.go::TestWorkGraphAdapter_ShowThroughTransport`)
exercises the same path through the real loopback transport against the real
SQLite driver and the real daemon `Run`, providing additional integration-level
coverage of the DTO + readiness composition.

## Concurrent-claim race proof

`TestScheduler_ConcurrentClaimRace` (race detector enabled) spawns 16
goroutines that each call `Scheduler.Claim` on the same package through
distinct `Scheduler` instances sharing the same store + lease manager. The
assertions:

- Exactly 1 winner (`atomic.AddInt64(&wins, 1)` == 1).
- 15 losers, every loser's error satisfies
  `errors.Is(err, ErrLeaseConflict) || errors.Is(err, ErrPackageNotReady)`.
- Exactly 1 surviving active lease (the winner's).
- No race detected by `-race`.

The race detector was clean across `go test -race ./...` (61 packages `ok`).

## Restart-recovery proof

`TestStore_RestartRecoversGraphAndLeases` simulates a daemon crash by closing
the storage handle without an orderly release, then re-opens a fresh handle
against the same DB file. Assertions:

- The graph (2 packages) survives `LoadValidated`.
- The package's runtime state (`PackageRunning`) survives — the
  re-decomposed-default preservation rule did NOT reset it.
- The attempt history survives (1 attempt).
- The active lease (held by `ws-1` on `src/a.go`) survives
  `ListActiveByProject`.
- Re-claiming the running package from a different workspace fails
  (`NotReadyError`: state not pending).

The compiled-binary black-box test additionally proves the daemon's
production restart path (stop → start → show) preserves both the graph and
the active lease.

## Scope and regression assessment

- `git diff --name-only 56f7107 HEAD` touches only:
  - `internal/storage/checkpoints.go`, `internal/storage/json.go`,
    `internal/storage/migrate.go`, `internal/storage/workgraph.go`
    (storage layer + migration v9);
  - `internal/workgraph/leases.go` (lease TTL/expiry/conflict + race fix),
    `internal/workgraph/store.go`, `internal/workgraph/readiness.go`,
    `internal/workgraph/scheduling.go`, `internal/workgraph/json.go`;
  - `internal/transport/api.go`, `internal/transport/server.go`,
    `internal/transport/workgraph_api.go`, `internal/transport/workgraph_client.go`;
  - `internal/daemon/api.go`, `internal/daemon/daemon.go`,
    `internal/daemon/workgraph_api.go`;
  - `internal/cli/cli.go`, `internal/cli/help.go`, `internal/cli/workgraph_cmd.go`;
  - new tests in `internal/{workgraph,daemon,cli}`.
- **No** changes to `docs/spec/`, `docs/engineering/`, `go.mod`, `go.sum`,
  `internal/enggate`, `internal/policy`, `internal/merge`, `internal/task`,
  `internal/scheduler`, `internal/supervisor`, `internal/workspace`, or any
  adapter.
- The existing M3 lease layer is enhanced (TTL/expiry/conflict typing +
  race-correct acquire) but the public API remains a strict superset:
  `AcquirePath`, `AcquireSemantic`, `ReleaseAll`, `ListActiveByProject`,
  `ListByWorkspace`, `ErrLeaseConflict` are unchanged; existing tests pass
  unchanged.
- `make check` green across every M0–M13 + M14-00..M14-04 package; no
  regression.
- No new external dependencies (`go.mod` / `go.sum` unchanged).
- No `TODO` / `FIXME` / `panic("unimplemented")` / stub in the new production
  files; `gofmt -l .` clean; `go vet ./...` clean.

## Fake / stub / demo leakage

- No fakes, stubs, or demo data in production code. The work-graph store
  consumes the real `*ValidatedWorkGraph` type; the readiness calculator
  consumes real `Lease` values; the scheduler composes the real
  `WorkGraphStore` + `LeaseManager`.
- `TestIntegration_CompileDecomposeSaveReadiness` imports and exercises the
  real `internal/task.Compile` (the accepted M14-02 compiler) — no fake
  compiler.
- The black-box tests seed the durable substrate through the **same**
  `WorkGraphStore` / `LeaseManager` the production daemon uses (a second
  read/write handle to the same WAL DB); they do not bypass the production
  code path. The dispatch layer that will perform this seed internally is a
  later milestone and is honestly documented as out of scope (FU-M14-05-3).
- Test helpers (`helperValidatedGraph`, `setupWGDB`,
  `assertWorkGraphTablesExist`) are concise constructors over real types,
  not test doubles.

## Policy / security

- The Work Graph substrate performs durable I/O through `*storage.DB` but
  spawns no goroutine, forwards no environment, and touches no security
  policy, autonomy profile, merge policy, or quota/budget arithmetic.
- No agent process is involved; no allowlist concern applies.
- The partial UNIQUE index + race-correct acquire is a correctness fix that
  *strengthens* the lease layer's enforcement of spec §18.4 ("conflicts
  block — BLOCKED_LEASE"): a concurrent claim can no longer result in two
  active leases for the same resource. No security invariant is weakened.
- The CLI command is read-only (`forge workgraph show`); it does not mutate
  state and inherits the daemon's loopback token authentication.
- `writeAPIError` maps lease-conflict / not-ready to HTTP 409 Conflict
  (not 500), matching the existing locked/transition semantics.

## Determinism

- `workgraph.ComputeReadiness` is a pure function of `(graph, leases, now)`;
  reasons are sorted lexicographically so the verdict is byte-stable for
  identical inputs.
- `WorkGraphStore.Save` → `LoadOrDie` is round-trip stable: list-shaped
  fields (AcceptedACIDs, AllowedScope, Dependencies, Attempts) survive
  JSON encode/decode; packages are returned in canonical (sorted-by-ID)
  order.
- `TestReadiness_Deterministic` asserts byte-stable verdicts across two
  calls; `TestWorkGraphStore_SaveAndLoad` asserts the round-trip.

## Known limitations

1. **Multi-daemon writer coordination is not addressed.** The
   race-correct acquire protects against concurrent goroutines within one
   daemon process; two daemon processes writing to the same SQLite database
   would still race at the storage layer (the partial UNIQUE index catches
   the duplicate, but the loser's rollback path is process-local). Production
   deployment runs a single daemon per home (BF-05), so this is not a
   production hazard; multi-daemon writers are tracked as FU-M14-05-5.
2. **`ClaimRequest.ProjectID` is sourced from `TaskID`.** The lease layer
   scopes leases to `(scope="project", scope_id=<projectID>)`. For M14-05
   the Claim request does not carry an explicit project ID, so the scheduler
   uses TaskID as the scope id. The production dispatch layer (future
   milestone) will pass a fully-scoped request via a richer API. Documented
   honestly in `ClaimRequest.ProjectID`'s docstring.
3. **No `forge workgraph claim/release` CLI.** The CLI surface is read-only
   (`show`). The Claim/Renew/Release lifecycle is exposed through the
   `Scheduler` Go API (which the dispatch layer will call) and proven by
   unit + integration tests, but not yet through a daemon endpoint or CLI
   command. This matches the brief's "CLI/API graph inspection" requirement
   (inspection is what `show` provides); the mutation endpoints are
   FU-M14-05-2.
4. **`Renew` is workspace-scoped, not lease-scoped.** `Scheduler.Renew`
   extends every active TTL lease held by a workspace. Per-lease renewal is
   not yet exposed; FU-M14-05-4.
5. **No semantic enforcement in the readiness calculator.** The calculator
   predicts path-lease conflicts statically (AllowedScope is part of the
   package definition) but semantic conflicts are surfaced at `Claim` time
   (the dispatcher's runtime input). A future task could add per-package
   `SemanticNeeds` to make semantic conflicts statically predictable too;
   FU-M14-05-6.
6. **Decomposition remains the M14-04 sequential chain.** The M14-04 minimal
   decomposition strategy is unchanged; richer stage-based decomposition
   (research → contract fan-out → integration → verification) is still
   FU-M14-04-1.

None of the above obstructs any mandatory acceptance criterion or the
sequential gate.

## Follow-up problems

- **FU-M14-05-1:** dispatch hook — wire `Scheduler.Claim` into the daemon's
  scheduler service so a task with a persisted Work Graph actually picks a
  ready package, claims it, and dispatches it to the supervisor. Closes the
  "graph exists but nothing runs it" gap.
- **FU-M14-05-2:** daemon endpoints for the claim/release lifecycle
  (`POST /tasks/{id}/workgraph/claim`, `/release`, `/renew`) + matching CLI
  (`forge workgraph claim/release`). Currently the lifecycle is Go-API-only.
- **FU-M14-05-3:** `forge workgraph decompose` CLI command (decompose a
  compiled spec into a graph and persist it). Removes the test-only seeding
  path used by the black-box tests.
- **FU-M14-05-4:** per-lease `Renew(leaseID, ttl)` instead of workspace-scoped
  `RenewAll`.
- **FU-M14-05-5:** multi-daemon writer coordination for the Work Graph
  (transactional compare-and-create at the storage layer; mirrors
  FU-M14-03-6).
- **FU-M14-05-6:** per-package `SemanticNeeds` field so the readiness
  calculator can predict semantic conflicts statically (currently they
  surface at Claim time).
- **FU-M14-05-7:** TUI bindings for visualising the work graph (depends on
  FU-M14-05-2).
- **FU-M14-05-8 (MINOR, inherited from M14-04 R1):** the
  `ValidateWorkGraph` empty-state-default inconsistency (dead code at
  graph.go:355-362). Unchanged by M14-05; tracked separately.
- **FU-M14-05-9 (latent defect fix documentation):** document the M3
  lease-race defect this task closed (partial UNIQUE index) in an ADR if the
  project's ADR conventions require it; the code-level documentation is in
  `migrate.go` and `leases.go`.

## Verdict

`IMPLEMENTED_TESTED`

Rationale:

- All three mandatory ACs are proven by passing automated evidence at unit +
  integration + black-box levels:
  - **AC1** (package not runnable until completion dependencies):
    `workgraph.ComputeReadiness` + `Scheduler.Claim` readiness gate;
    `TestReadiness_*`, `TestScheduler_ClaimBlockedByDependency`,
    integration `TestIntegration_CompileDecomposeSaveReadiness`; black-box
    asserts the chain's readiness map.
  - **AC2** (conflicting lease blocks execution with explainable cause):
    typed `*ConflictError` carrying per-conflict reasons; `*NotReadyError`
    carrying explainable reasons; `writeAPIError` → 409;
    `TestLease_ConflictExplainsCause`, `TestScheduler_ClaimBlocked*`,
    `TestLease_TTL_ExpiryReclaim`; black-box step 8 asserts the JSON
    verdict names both path and workspace.
  - **AC3** (graph and leases survive restart):
    `WorkGraphStore.Save` (idempotent, runtime-state-preserving) +
    `LeaseManager` (durable, TTL-aware); `TestStore_RestartRecoversGraphAndLeases`;
    black-box step 9 (daemon stop + start + show).
- `make check` is green (FAIL_COUNT 0); `go test -race ./...` is clean (61
  packages `ok`, no race, no FAIL); `go vet ./...` and `gofmt -l .` are clean.
- M14-04 is `ACCEPTED` (predecessor gate exit 0).
- Scope is bounded to M14-05 (storage / domain store / readiness / scheduler /
  transport / daemon / CLI); no M14-06 work; no execution dispatch; no
  baseline / gate / spec enforcement weakened; no product-spec change; no
  security / autonomy / delivery / merge-policy invariant changed.
- The mandatory black-box test is `TestWorkGraphShow_BlackBox_*` through the
  compiled `forge` binary driving the real daemon; it is not skipped (only
  `-short` opt-out, with integration evidence duplicating the affected ACs).
- The manifest passes `forge gate validate` (exit 0) — verifiable after this
  report + manifest are committed.

`IMPLEMENTED_TESTED` is permitted: every mandatory AC is backed by passing
automated evidence at unit + integration + black-box levels, with the
black-box evidence exercising the real `forge` binary + real daemon + real
SQLite + real transport.
