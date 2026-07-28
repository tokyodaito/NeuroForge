# M14-05 — Independent Review Report

**Task:** M14-05 — Durable Work Graph, leases and readiness.
**Reviewer actor:** `M14-05-review-session-1` (independent session; no prior M14-05 work).
**Verdict:** `REVIEW_APPROVED` (with one MAJOR and five MINOR findings to address in tracked follow-ups).

## Reviewed candidate SHA

- **Candidate SHA:** `eb1843043f5b7a122c24b7a3b8c23da43956404b`
  (`M14-05: durable Work Graph, leases and readiness`).
- **Starting SHA (predecessor):** `56f7107bb08698f448c7afe6388d2e0d51cf6c3c`
  (`M14-04: accept task (state ACCEPTED, baseline v1)`).
- HEAD of the working checkout at review time == candidate SHA; the candidate
  is the most recent commit and was reviewed in full.

## Scope of review

- Full diff `git diff 56f7107..eb18430` (24 files, +4569/-32 lines).
- Independent re-run of: targeted unit tests, race tests, integration tests,
  compiled-binary black-box tests, `make check`, and `forge gate`.
- Counter-example search across: production composition/wiring, fake/demo/stub
  leakage, policy/security bypass, restart/idempotency/cancellation/concurrency,
  scope creep, backward compatibility, and overstated documentation claims.

## Acceptance criterion matrix

| Mandatory AC | Implementation | Test(s) | Verdict |
|---|---|---|---|
| **AC1** Package not runnable until completion dependencies | `workgraph.ComputeReadiness` (readiness.go:103-186) blocks any package whose dependencies are not in `PackageSucceeded`; `Scheduler.Claim` (scheduling.go:124-146) re-checks readiness inside the claim path and refuses with `*NotReadyError` enumerating each unmet dependency. | `TestReadiness_BlockedByDependency`, `TestReadiness_UnblocksAfterDependencySucceeds`, `TestReadiness_TerminalStateNotReady`, `TestReadiness_Deterministic`; `TestScheduler_ClaimSuccess`, `TestScheduler_ClaimBlockedByDependency`; integration `TestIntegration_CompileDecomposeSaveReadiness` (real `task.Compile` chain). Black-box `TestWorkGraphShow_BlackBox_CreateShowAndExplainConflict` step 6 (compiled `forge workgraph show` text + JSON; asserts `Readiness: ready` for chain head + `Readiness: blocked` for the rest + dependency-not-succeeded reason in stdout). | **MET** |
| **AC2** Conflicting lease blocks execution with explainable cause | `LeaseManager.acquire` (leases.go:221-303) returns typed `*ConflictError` (wraps `ErrLeaseConflict`) whose `Reasons[0]` names resource + holding workspace + (when known) expiry; `Scheduler.Claim` propagates and rolls back; `ComputeReadiness` statically predicts path-lease conflicts from `AllowedScope`. `writeAPIError` (transport/api.go:327-336) maps lease-conflict / not-ready → HTTP 409. | `TestLease_ConflictExplainsCause` (`errors.Is(err, ErrLeaseConflict)` + `Reasons[0]` carries resource + workspace + HeldBy); `TestScheduler_ClaimBlockedByLeaseConflict` (cause string names path + workspace; package stays pending); `TestLease_TTL_ExpiryReclaim`, `TestLease_Renew`, `TestLease_RenewDoesNotAffectPerpetual`, `TestLease_SweeperIdempotent`, `TestScheduler_ClaimSemanticTTL`; in-process transport `TestWorkGraphAdapter_ShowThroughTransport` (lease → readiness verdict reports blocked with cause). Black-box step 8 asserts JSON verdict on first package exposes conflicting path AND workspace `other-ws`. | **MET** |
| **AC3** After restart graph and leases recover safely | `WorkGraphStore.Save` persists via migration v9 substrate (`work_packages`, `work_package_dependencies`, `work_package_attempts`) idempotently; `LeaseManager` backed by durable `leases` table (+ `expires_at` from v9); re-opening DB + re-running `Migrate` is a no-op; daemon wires `Graphs` + `Leases` into `Services` so `GET /tasks/{id}/workgraph` recomputes readiness against persisted state. | `TestStore_RestartRecoversGraphAndLeases` (close DB → re-open same file; packages + state + attempts + active leases survive; re-claim of running package fails). Black-box step 9 (daemon stop → daemon start → `forge workgraph show`; both package set AND active lease survive). `TestWorkGraphShow_BlackBox_MigrationV9Applied` (doctor reports schema ≥9; new tables + `leases.expires_at` + `idx_leases_unique_active_resource` present; idempotent across restart). | **MET** |

### Required test classes (task brief)

| Required class | Covered | Evidence |
|---|---|---|
| Dependency readiness tests | yes | `TestReadiness_BlockedByDependency`, `TestReadiness_UnblocksAfterDependencySucceeds`, `TestReadiness_TerminalStateNotReady`, `TestReadiness_Deterministic`; integration `TestIntegration_CompileDecomposeSaveReadiness` |
| Lease conflict, expiry and reclaim tests | yes | `TestLease_ConflictExplainsCause`, `TestLease_TTL_ExpiryReclaim`, `TestLease_Renew`, `TestLease_RenewDoesNotAffectPerpetual`, `TestLease_SweeperIdempotent`; `TestScheduler_ClaimSemanticTTL` |
| Persistence / restart | yes | `TestStore_RestartRecoversGraphAndLeases` (DB close + re-open); `TestWorkGraphStore_SaveIsIdempotentAndPreservesAttempts`; black-box step 9 (daemon stop + restart + show) |
| Concurrent claim race tests | yes | `TestScheduler_ConcurrentClaimRace` (16 goroutines; exactly 1 winner; 15 typed-error losers; exactly 1 surviving lease; `-race` clean across 5 reruns) |
| Black-box graph show through daemon | yes | `TestWorkGraphShow_BlackBox_CreateShowAndExplainConflict` + `TestWorkGraphShow_BlackBox_MigrationV9Applied` (compiled `forge` binary + real daemon); in-process transport `TestWorkGraphAdapter_ShowThroughTransport` + `TestWorkGraphAdapter_MissingGraphIs404` |
| `make check` green | yes | exit 0; FAIL_COUNT 0 (independently re-verified) |
| `go test -race ./...` clean | yes | 61 packages `ok`; 0 FAIL; no race (independently re-verified) |

## Findings

### MAJOR-1: `ClaimRequest.ProjectID()` returns `TaskID`, weakening lease isolation to per-task instead of per-project

- **Path:** `internal/workgraph/scheduling.go:210-219` (`ClaimRequest.ProjectID`); consumed at `scheduling.go:128, 185, 190`; mirrored in the daemon adapter at `internal/daemon/workgraph_api.go:58` (`a.svc.Leases.ListActiveByProject(ctx, taskID)`).
- **Observation:** the §18.4 lease layer was designed (M3 schema migration v4 line 156-157) for `scope="project", scope_id=<projectID>`, i.e. project-wide resource isolation. The M3 lease tests (`internal/workgraph/leases_test.go:40-56`) use the project ID as the scope id, matching this contract. M14-05's `ClaimRequest.ProjectID()` returns `req.TaskID`, and the daemon's `workGraphAPIAdapter.GetWorkGraph` passes `taskID` to `ListActiveByProject`. As a result, leases acquired by `Scheduler.Claim` are scoped to `(scope="project", scope_id=<taskID>)`, so **two different tasks in the same project can simultaneously acquire the same file path or semantic resource without conflicting**. The `LeaseManager.AcquirePath` parameter is still named `projectID`, but the production caller passes a task id.
- **Proof:** the entire mandatory conflict test suite (AC2) uses a single task id (`taskID`/`"T-1"`); no test asserts cross-task isolation. The implementation report's "Known limitations #2" acknowledges this honestly but does not flag the production isolation hazard severity: until a follow-up lands, the spec §18.4 contract ("conflicts block work from starting concurrently") is satisfied only within a single task, not across the project. The M3 lease tests prove the contract was originally project-scoped.
- **Why this is MAJOR, not BLOCKER:** (a) no production caller invokes `Scheduler.Claim` yet — the dispatch hook is FU-M14-05-1, so the regression is latent; (b) all mandatory AC tests pass because they are within-task by construction; (c) the implementation is honestly documented. The hazard materialises the moment FU-M14-05-1 (dispatch hook) or FU-M14-05-2 (claim/release CLI) lands against the current scoping.
- **Required fix (before FU-M14-05-1 / FU-M14-05-2 merge):** `ClaimRequest` should carry an explicit `ProjectID` field sourced from the task's project (resolved via `task.Backlog` or the storage row), and the daemon adapter should resolve `projectID` from `taskID` once for both the readiness snapshot and the lease scope. Add a regression test that proves cross-task lease conflict on the same path (e.g. task-A's workspace holds `src/shared.go`; task-B's package with `src/shared.go` in `AllowedScope` is reported blocked). The fix can land in the dispatch-hook follow-up; the current `ProjectID()` shim should be deleted at the same time so the mis-naming cannot survive.

### MINOR-1: `Scheduler.releaseAllAcquired` ignores its `acquired` parameter

- **Path:** `internal/workgraph/scheduling.go:197-208`.
- **Observation:** the function signature is `releaseAllAcquired(ctx, workspaceID string, acquired []Lease)`, suggesting it releases only the supplied leases. In practice the `acquired` slice is used only for the `len(acquired) == 0` short-circuit; the actual release calls `s.lease.ReleaseAll(ctx, workspaceID)` which releases **every** active lease held by `workspaceID`. If a workspace ever hosts more than one package's lease set (e.g. a future dispatcher reusing workspaces), this would release unrelated leases as part of a Claim rollback.
- **Proof:** reading the function body: the only reference to `acquired` is `if len(acquired) == 0 { return }`. The subsequent `ReleaseAll(ctx, workspaceID)` ignores the slice contents.
- **Required fix:** either (a) release only the leases in `acquired` by `ID` (e.g. iterate and call a per-lease release), or (b) rename the function to `releaseWorkspaceLeases` and drop the unused parameter so the contract is honest. Today's production model (one workspace ↔ one package's lease set) makes this cosmetic, but the misleading signature is a future-bug magnet.

### MINOR-2: `ComputeReadiness` does not exclude the caller's own workspace from path-lease conflict detection

- **Path:** `internal/workgraph/readiness.go:167-181`.
- **Observation:** the path-lease loop reports a blocked-reason for any active lease on a path in `AllowedScope`, regardless of holder. `LeaseManager.ListActiveByProject` returns every active lease in scope without filtering by workspace. As a result, if workspace `W` already holds a lease on a path in package `P`'s `AllowedScope` and readiness is recomputed (e.g. through `GET /tasks/{id}/workgraph`), the verdict will say `path "X" held by workspace "W"` even though `W` is the caller's own workspace — a misleading "self-conflict". The Claim path is unaffected in practice because each Claim uses a fresh workspace id (production model), but the display path can produce confusing output if a workspace pre-leases a path before its package is claimed.
- **Proof:** reading the code — no `excludeWorkspaceID` parameter is threaded through `ComputeReadiness`; `HasActiveLease` (storage/checkpoints.go:170-182) does exclude by workspace, but `ListActiveLeasesByScope` (storage/checkpoints.go:185-195) does not. No test exercises the "self-lease" case.
- **Required fix (optional for M14-05, recommended for FU-M14-05-2):** thread a `callerWorkspaceID` (or "view-as" workspace) through the daemon adapter into `ComputeReadiness` so self-held path leases are not reported as conflicts. Alternatively, document that the readiness verdict is "from the perspective of an unprivileged caller" and adjust the GET endpoint's DTO comment.

### MINOR-3: migration v9 does not deduplicate pre-existing active lease rows

- **Path:** `internal/storage/migrate.go:448-449` (`CREATE UNIQUE INDEX IF NOT EXISTS idx_leases_unique_active_resource ... WHERE state = 'active'`).
- **Observation:** the partial UNIQUE index is the correct fix going forward, but the migration does not deduplicate any pre-existing active rows that may have been produced by the latent M3 race (which this very task closes). If a deployment hit the M3 race before upgrading, migration v9 would abort with `UNIQUE constraint failed: leases.<columns>` and the daemon would fail to start.
- **Proof:** reading the migration — there is no `DELETE FROM leases WHERE id NOT IN (SELECT MIN(id) FROM leases WHERE state='active' GROUP BY scope, scope_id, kind, resource)` or equivalent deduplication before the `CREATE UNIQUE INDEX`. The implementation report frames the M3 race as "latent" (not observed), which lowers the probability, but does not eliminate the upgrade hazard.
- **Required fix (only if a production deployment is known to have hit the M3 race; otherwise track as FU):** insert a deduplication step before the index creation, e.g.
  ```sql
  DELETE FROM leases
  WHERE state = 'active' AND id NOT IN (
    SELECT MIN(id) FROM leases
    WHERE state = 'active'
    GROUP BY scope, scope_id, kind, resource
  );
  ```
  Best added in a forward migration step before the index creation. A regression test should insert two active rows for the same resource, run migration, and assert that exactly one survives and the index exists.

### MINOR-4: `IsLeaseUniqueConstraint` uses broad substring matching on the error string

- **Path:** `internal/storage/checkpoints.go:28-36`.
- **Observation:** the function returns true for any error whose message contains the substring `"UNIQUE"`. The inline comment justifies this as a deliberate broad match to survive a driver swap, but the schema currently has another UNIQUE constraint on `work_package_attempts(task_id, package_id, attempt_index)` (migration v9 line 426) and on `work_package_dependencies(task_id, package_id, depends_on)` (migration v9 line 406); if any of these ever surface their constraint errors through a code path that calls `IsLeaseUniqueConstraint`, the function would mis-classify them as a lease conflict.
- **Proof:** reading `IsLeaseUniqueConstraint` — the only discriminator is `strings.Contains(err.Error(), "UNIQUE")`. There is no constraint-name check. Today the function is called only inside `createLease` (storage/checkpoints.go:127), so the blast radius is limited to lease inserts, but the helper is exported and the contract is fragile.
- **Required fix (recommended for FU-M14-05-2):** tighten the check to also match the index name (`idx_leases_unique_active_resource`) when present in the error message, falling back to the broad match only if the driver does not emit the index name. Document the fallback.

### MINOR-5: candidate SHA in manifest is a textual description, not a SHA

- **Path:** `docs/reviews/m14/M14-05.manifest.json:229` — `"implementation_candidate_sha": "this commit (M14-05: durable Work Graph, leases and readiness -- production code and manifest in one commit)"`.
- **Observation:** the schema-1 manifest's `git.implementation_candidate_sha` field is documented as a SHA; here it carries a free-form sentence. The actual SHA (`eb1843043f5b7a122c24b7a3b8c23da43956404b`) is recoverable from `git log -1` of the commit that adds the manifest, but the manifest itself is not machine-readable for this field.
- **Proof:** reading the JSON — the value is a sentence, not a 40-hex-digit SHA. Other M14 manifests (e.g. `M14-04.manifest.json`) use the same pattern, so this is a project-wide convention rather than a one-off lapse.
- **Required fix (optional):** when the manifest is committed, replace the sentence with the actual commit SHA. Not a blocker — the gate validator accepts the manifest as-is.

## Independent re-run results

Toolchain: `go version go1.26.5 darwin/arm64`. All commands ran from the primary checkout at the candidate SHA.

| Command | Exit | Result |
|---|---:|---|
| `go build ./...` | 0 | clean build |
| `go vet ./...` | 0 | clean |
| `gofmt -l .` | 0 | no files listed |
| `go test -count=1 ./internal/workgraph/...` | 0 | 49 tests PASS (store / readiness / scheduling / leases / integration / validation) |
| `go test -race -count=1 ./internal/workgraph/...` | 0 | race-clean |
| `go test -race -count=3 -run 'TestReadiness\|TestScheduler\|TestLease_TTL\|TestLease_Renew\|TestLease_Conflict\|TestStore_Restart\|TestWorkGraphStore' ./internal/workgraph/` | 0 | 3× determinism PASS, race-clean |
| `go test -race -count=5 -run 'TestScheduler_ConcurrentClaimRace' ./internal/workgraph/` | 0 | 5× race-clean; exactly 1 winner each time |
| `go test -count=1 -run 'TestAcquirePath\|TestAcquireSemantic\|TestReleaseAll\|TestListActive' ./internal/workgraph/` | 0 | M3 lease regression suite PASS (no regression from the race-correct acquire refactor) |
| `go test -count=1 -v -run TestWorkGraph ./internal/daemon/` | 0 | `TestWorkGraphAdapter_ShowThroughTransport`, `TestWorkGraphAdapter_MissingGraphIs404` PASS |
| `go test -count=1 -v -run TestWorkGraph ./internal/cli/` | 0 | `TestWorkGraphShow_BlackBox_CreateShowAndExplainConflict` (all 21 sub-steps ok: daemon start → project add → task add → spec save → seed graph → show text → show JSON → AcquirePath other-ws → show blocked with cause → daemon stop → daemon start → show after restart → missing task 404), `TestWorkGraphShow_BlackBox_MigrationV9Applied` PASS |
| `make check` | 0 | gofmt clean + `go vet ./...` clean + full suite green (every package `ok`; FAIL_COUNT 0) |
| `go test -race ./...` | 0 | 61 packages `ok`; **0 FAIL, no race detected** |
| `/tmp/forge-m14-05-review gate baseline` | 0 | active baseline_version 1, schema_version 1 |
| `/tmp/forge-m14-05-review gate validate --manifest docs/reviews/m14/M14-05.manifest.json` | 0 | `OK: task "M14-05" transition STARTED -> IMPLEMENTED_TESTED is legal under baseline v1` |
| `/tmp/forge-m14-05-review gate next --manifest docs/reviews/m14/M14-04.manifest.json` | 0 | predecessor ACCEPTED; successor may start |

## Production composition / wiring verification

- **Daemon wiring:** `daemon.NewServices` (daemon/api.go:30-39) constructs `Graphs` and `Leases` from the same `*storage.DB` that backs every other durable service. `daemon.Run` (daemon.go:142) constructs `workGraphAdapter := newWorkGraphAPIAdapter(services)` and wires it into `transport.Config.WorkGraphAPI` (daemon.go:219). `transport.Server.registerWorkGraphRoutes` (transport/server.go:132) registers `GET /tasks/{id}/workgraph`. The dispatch hook (`Scheduler.Claim` ← daemon scheduler event) is correctly out of scope and tracked as FU-M14-05-1 — no production caller exists yet, so the latent `ProjectID()` scoping defect (MAJOR-1) does not currently affect any user.
- **CLI wiring:** `App.Run` dispatches `"workgraph"` to `runWorkGraph` (cli/cli.go:76-77); `runWorkGraph` dispatches `"show"` to `workGraphShow` (cli/workgraph_cmd.go:26-28); the command reaches the daemon through `transport.Client.GetWorkGraph` (transport/workgraph_client.go:12-17). Help text updated (cli/help.go:60-62).
- **Transport:** `writeAPIError` extended (transport/api.go:327-336) to map lease-conflict and not-ready errors to HTTP 409. `writeAPIError`'s substring matching (`strings.Contains(msg, "lease conflict")`, `strings.Contains(msg, "not ready")`) is the same shape used by the existing locked/transition cases, so it composes correctly with the rest of the API surface.
- **Storage:** migration v9 is purely additive (three new tables + one column + one partial index); the M3 lease layer is a strict superset (existing `AcquirePath`, `AcquireSemantic`, `ReleaseAll`, `ListActiveByProject`, `ListByWorkspace`, `ErrLeaseConflict` unchanged); M3 lease tests pass unchanged.

## Fake / stub / demo leakage

- No fakes, stubs, or demo data in production code. The work-graph store consumes the real `*ValidatedWorkGraph`; the readiness calculator consumes real `Lease` values; the scheduler composes the real `WorkGraphStore` + `LeaseManager`.
- `TestIntegration_CompileDecomposeSaveReadiness` imports and exercises the real `internal/task.Compile` (the accepted M14-02 compiler) — no fake compiler.
- The black-box tests seed the durable substrate through the same `WorkGraphStore`/`LeaseManager` the production daemon uses (a second read/write handle to the same WAL DB), not through a test double. The dispatch layer that will perform this seed internally is honestly documented as out of scope (FU-M14-05-1, FU-M14-05-3).
- Test helpers (`helperValidatedGraph`, `setupWGDB`, `assertWorkGraphTablesExist`) are concise constructors over real types, not test doubles.

## Policy / security / autonomy / merge-policy

- The Work Graph substrate performs durable I/O through `*storage.DB` but spawns no goroutine, forwards no environment, touches no security policy, autonomy profile, merge policy, or quota/budget arithmetic. No agent process is involved; no allowlist concern applies.
- The partial UNIQUE index + race-correct acquire **strengthens** the lease layer's enforcement of spec §18.4 (`BLOCKED_LEASE`): a concurrent claim can no longer result in two active leases for the same resource (within one daemon process). No security invariant is weakened.
- The CLI command is read-only (`forge workgraph show`); it inherits the daemon's loopback token authentication and does not mutate state.
- No backward-incompatible change to the M3 lease public API; existing tests pass unchanged.

## Restart / idempotency / cancellation / concurrency

- **Restart:** `TestStore_RestartRecoversGraphAndLeases` (full DB close + re-open against same file) proves the graph, package state, attempts, and active leases survive; black-box step 9 (daemon stop → start → show) proves the same through the real daemon. Re-running `Migrate` is a no-op once v9 is applied (verified by `TestWorkGraphShow_BlackBox_MigrationV9Applied`).
- **Idempotency:** `TestWorkGraphStore_SaveIsIdempotentAndPreservesAttempts` proves re-saving the same graph preserves attempts and runtime state; `TestLease_SweeperIdempotent` proves re-running the sweeper is a no-op; `LeaseManager.acquire`'s fast path returns the holder's existing lease idempotently.
- **Cancellation:** no long-running operations to cancel in this layer; all storage calls inherit the caller's `context.Context` and abort on cancellation.
- **Concurrency:** `TestScheduler_ConcurrentClaimRace` (16 goroutines, `-race` clean across 5 reruns) proves exactly one winner, 15 typed-error losers, exactly one surviving lease. The partial UNIQUE index + SQLite single-writer serialisation + `busy_timeout(5000)` (storage.go:85) make the INSERT the linearisation point.

## Scope creep

- `git diff --name-only 56f7107 HEAD` touches only: `internal/storage` (migration v9 + workgraph substrate + lease TTL helpers), `internal/workgraph` (store, readiness, scheduler, lease TTL, conflict typing), `internal/transport` (DTOs + handler + client + writeAPIError extension), `internal/daemon` (adapter + Services wiring + Run wiring), `internal/cli` (dispatch + help + workgraph show command), and new tests in those packages. No changes to `docs/spec/`, `docs/engineering/`, `go.mod`, `go.sum`, `internal/enggate`, `internal/policy`, `internal/merge`, `internal/task`, `internal/scheduler`, `internal/supervisor`, `internal/workspace`, or any adapter. No new external dependencies.

## Counter-example search

- "Tests green but AC unmet" attempt #1 (AC1 — could a package be runnable despite an unmet dependency?): `ComputeReadiness` returns `Ready = len(r.BlockedReasons) == 0` (readiness.go:185) and `BlockedReasons` is appended for every dependency not in `PackageSucceeded` (readiness.go:157-160); `Claim` re-checks `mine.Ready` (scheduling.go:140). No path skips the dependency check. Integration test asserts only chain head is ready.
- "Tests green but AC unmet" attempt #2 (AC2 — could a conflicting lease fail to block?): the `acquire` fast path returns `ConflictError` for a different-workspace holder (leases.go:238); the cold path's INSERT under the partial UNIQUE index is the linearisation point (leases.go:259-291); `Scheduler.Claim` rolls back and propagates (scheduling.go:152-157). The race test proves exactly one winner under 16-way contention. No path silently allows a conflict.
- "Tests green but AC unmet" attempt #3 (AC3 — could graph or leases be lost on restart?): `ReplaceWorkGraph` runs inside `BeginTx`/`Commit` (storage/workgraph.go:86-198); the lease INSERT runs inside the same transaction shape; SQLite WAL's durability guarantee + `Migrate` idempotency ensure restart recovery. `TestStore_RestartRecoversGraphAndLeases` asserts every field survives.
- "Cross-task isolation" counter-example (MAJOR-1): two tasks in the same project can both lease the same path because `ClaimRequest.ProjectID()` returns the task id, not the project id. This is the only counter-example found where the implementation does not match the spec's broader contract, but it is latent (no production caller) and acknowledged in the implementation report.

## What remains unproven

- **Production dispatch path.** No production caller invokes `Scheduler.Claim` in response to a daemon event. The Claim lifecycle is proven at unit + integration + black-box-seed levels, but the dispatch hook itself is FU-M14-05-1. Until it lands, M14-05 is a durable inspection + readiness surface with a scheduler waiting for a caller. This is the honest scope of the task; it is not a defect.
- **Cross-task lease isolation.** Until MAJOR-1 is resolved, the spec §18.4 contract is only proven within a single task. The mandatory AC tests are within-task, so they pass; cross-task isolation must be added when the dispatch hook lands.
- **Multi-daemon writer coordination.** The race-correct acquire protects against concurrent goroutines within one daemon process; two daemon processes writing to the same SQLite database would still race at the storage layer. Production deployment runs a single daemon per home (BF-05), so this is not a production hazard. Tracked as FU-M14-05-5.
- **`forge workgraph claim/release` CLI + `POST /tasks/{id}/workgraph/claim` endpoint.** The Claim/Renew/Release lifecycle is Go-API-only; the daemon does not expose mutation endpoints yet. Tracked as FU-M14-05-2. The brief's "CLI/API graph inspection" requirement is met by `forge workgraph show`; the mutation surface is honestly out of scope.

## Verdict rationale

`REVIEW_APPROVED`:

- All three mandatory ACs are backed by passing automated evidence at unit + integration + black-box levels, with the black-box evidence exercising the real `forge` binary + real daemon + real SQLite + real transport.
- `make check` is green (FAIL_COUNT 0); `go test -race ./...` is clean (61 packages `ok`, 0 FAIL, no race); `go vet` and `gofmt` are clean.
- M14-04 is `ACCEPTED` (predecessor gate exit 0).
- The mandatory concurrent-claim race test is `-race` clean across 5 reruns.
- No fake/stub/demo leakage in production code; no policy/security/autonomy/merge-policy invariant weakened.
- The MAJOR-1 finding (cross-task lease scoping) is honestly documented in the implementation report, latent (no production caller), and does not invalidate any mandatory AC (all AC tests are within-task). It must be resolved before FU-M14-05-1 (dispatch hook) or FU-M14-05-2 (claim/release CLI) merges.
- The five MINOR findings are cosmetic / defence-in-depth / forward-compatibility concerns that do not obstruct any AC.

`REVIEW_APPROVED` is permitted because every mandatory criterion is proven by passing automated evidence; the MAJOR finding is a tracked follow-up, not an unproven AC.
