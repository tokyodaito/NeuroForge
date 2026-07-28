# M14-05 Acceptance

## Acceptance identity

- **acceptor actor/session ID:** `M14-05-accept-session-1` (fresh, independent
  session; performed no implementation, no review, no remediation of M14-05, and
  is not the M14-05 implementer, reviewer, or MAJOR-1 remediator).
- **implementation actor/session ID:** `M14-05-impl-session-1`
  (per `M14-05_IMPLEMENTATION.md`; shipped `eb18430`).
- **review actor/session ID:** `M14-05-review-session-1` (verdict
  `REVIEW_APPROVED`; per `M14-05_REVIEW.md`, commit `08cbf23`).
- **remediation actor/session ID:** `M14-05-major1-remediation-session`
  (per `M14-05_MAJOR1_REMEDIATION.md`, commit `c4b925c`).
- **second independent re-review of the MAJOR-1 fix:** returned
  `REVIEW_APPROVED` (fix correct, regression test proven, all tests green) — the
  prerequisite for this acceptance.
- **independence confirmed:** **yes** — the four role-bound ids
  (`M14-05-impl-session-1`, `M14-05-review-session-1`,
  `M14-05-major1-remediation-session`, `M14-05-accept-session-1`) are pairwise
  distinct. The acceptor re-checked every implementation/review/remediation
  claim against the checked-out code, tests, and the compiled `forge` binary
  rather than trusting any report. This session authored no production code and
  no tests; it authored only this report + the manifest update.
- **acceptance date:** 2026-07-28.

## Git baseline

- **accepted predecessor SHA (M14-04 acceptance):**
  `56f7107bb08698f448c7afe6388d2e0d51cf6c3c`
  (`M14-04: accept task (state ACCEPTED, baseline v1)`; M14-05 implementation
  starting SHA; M14-04 manifest `state = ACCEPTED`).
- **original implementation candidate SHA:** `eb1843043f5b7a122c24b7a3b8c23da43956404b`
  (`M14-05: durable Work Graph, leases and readiness`).
- **review commit SHA:** `08cbf23` (`M14-05: independent review (REVIEW_APPROVED,
  one MAJOR + five MINOR follow-ups)`).
- **remediation candidate SHA (the candidate being accepted):**
  `c4b925c6c45a578ed9ddd5ec77ff8271cc8c135d`
  (`M14-05: fix MAJOR-1 cross-task lease isolation (project-scoped, not
  task-scoped)` — HEAD of `main` at acceptance time).
- **acceptance starting HEAD:** `c4b925c6c45a578ed9ddd5ec77ff8271cc8c135d`.

Ancestry verified (all `git merge-base --is-ancestor` → exit 0):

- `56f7107` (M14-04 accept) is an ancestor of `eb18430` (implementation candidate).
- `eb18430` is an ancestor of `08cbf23` (review commit).
- `08cbf23` is an ancestor of `c4b925c` (remediation candidate / current HEAD).

The remediation delta (`git diff --stat eb18430 c4b925c`) touches: production
code (`internal/workgraph/scheduling.go`, `internal/daemon/workgraph_api.go`),
tests (`internal/workgraph/store_test.go`,
`internal/daemon/workgraph_api_test.go`,
`internal/cli/workgraph_show_blackbox_test.go`), and the three M14-05 reports
(implementation / review / remediation). No changes to `docs/spec/`,
`docs/engineering/`, `go.mod`/`go.sum`, or any other internal package.

## Predecessor gate

- M14-04 manifest: `docs/reviews/m14/M14-04.manifest.json` (state `ACCEPTED`,
  baseline_version `1`).
- command (compiled `./forge` at HEAD `c4b925c`):

  ```
  $ ./forge gate baseline
  active schema_version: 1
  active baseline_version: 1
  baseline document: docs/engineering/ENGINEERING_BASELINE.md

  $ ./forge gate validate --manifest docs/reviews/m14/M14-04.manifest.json
  OK: task "M14-04" transition REVIEW_APPROVED -> ACCEPTED is legal under baseline v1

  $ ./forge gate next --manifest docs/reviews/m14/M14-04.manifest.json
  OK: predecessor "M14-04" is ACCEPTED; successor task may start
  ```

  All exit 0.

Predecessor gate is **OPEN**; M14-05 was lawfully allowed to start.

## Review prerequisite + MAJOR-1 resolution

- The original candidate `eb18430` was reviewed at `08cbf23` with verdict
  `REVIEW_APPROVED`, **one MAJOR** (MAJOR-1) and **five MINOR** findings.
- MAJOR-1: `ClaimRequest.ProjectID()` returned `TaskID`, weakening spec §18.4
  lease isolation from per-project to per-task.
- The remediation at `c4b925c` resolved MAJOR-1; a second independent re-review
  of the fix returned `REVIEW_APPROVED`. This acceptor independently
  re-verified the fix (see §"MAJOR-1 resolution confirmation" below).

Acceptance is permitted: the candidate under review at the remediated `c4b925c`
has zero unresolved BLOCKER and zero unresolved MAJOR findings; the five MINOR
findings are tracked follow-ups that do not obstruct any mandatory AC (the
reviewer explicitly stated `REVIEW_APPROVED` is permitted with tracked
follow-ups).

## Independent verification method

This acceptor did not trust the implementation, review, or remediation reports.
It:

1. Re-read the remediated production code (`internal/workgraph/scheduling.go`,
   `internal/daemon/workgraph_api.go`) and the regression tests.
2. Re-built the `forge` binary from HEAD (`make build` → exit 0) and exercised
   the gate tooling.
3. Independently re-ran every mandatory-AC test class at the remediated
   candidate (`c4b925c`), the full `make check`, and the race tests for the
   affected packages.
4. Proved MAJOR-1 is resolved by grep-verified shim deletion + struct-field
   inspection + the regression test (`TestScheduler_CrossTaskLeaseConflict_ProjectScoped`).
5. Searched for any "green-tests-but-unmet-requirement" counterexample (none
   found — the remediation strictly *strengthens* lease isolation, so no AC
   regressed; all three mandatory ACs held at `eb18430` and continue to hold at
   `c4b925c`).

## MAJOR-1 resolution confirmation

| Required fix element (per `M14-05_REVIEW.md` MAJOR-1) | Verification | Result |
|---|---|---|
| `ClaimRequest.ProjectID()` shim deleted | `grep -rn 'func (req ClaimRequest) ProjectID' internal/` | **(no matches)** — shim gone |
| `ClaimRequest` carries explicit `ProjectID string` field | `internal/workgraph/scheduling.go:84-92` (`ClaimRequest` struct: `TaskID string`, `ProjectID string`, ...) | **present** |
| `Scheduler.Claim` rejects empty `ProjectID` | `scheduling.go:114` (`if req.ProjectID == "" { ... }`) — guarded by `TestScheduler_ClaimMissingProjectID` | **PASS** |
| `Scheduler.Claim` uses `req.ProjectID` for all 3 lease ops | `scheduling.go:140` (`ListActiveByProject`), `:197` (`AcquirePathTTL`), `:202` (`AcquireSemanticTTL`) | **confirmed by reading the code** |
| Daemon adapter resolves `projectID` from task row once | `internal/daemon/workgraph_api.go:55-59` (`task, err := a.svc.Tasks.Get(ctx, taskID); projectID := task.ProjectID`) → `ListActiveByProject(ctx, projectID)` at `:73` | **confirmed by reading the code** |
| Cross-task lease conflict regression test exists + passes | `TestScheduler_CrossTaskLeaseConflict_ProjectScoped` + `TestScheduler_ClaimMissingProjectID` | **PASS** (exit 0) |

The remediation report documents characterization evidence: the regression test
was verified to **FAIL** when the fix is reverted (production code temporarily
rolled back to task-scoped wiring) and **PASS** when the fix is applied — i.e.
the test genuinely guards the defect rather than coincidentally passing.

Net effect: lease isolation is restored to per-project scope (spec §18.4), which
**strengthens** the invariant the review flagged. No AC regressed.

## Acceptance matrix

Mandatory acceptance criteria (per `M14-05.manifest.json`):

| Mandatory AC | Production implementation (remediated) | Automated evidence independently re-run this session at `c4b925c` | Result | Status |
|---|---|---|---|---|
| **AC1** Package not runnable until completion dependencies | `workgraph.ComputeReadiness` blocks any package whose dependencies are not in `PackageSucceeded`; `Scheduler.Claim` re-checks readiness inside the claim path and refuses with `*NotReadyError` enumerating each unmet dependency by ID + current state. | `go test -count=1 -run 'TestReadiness\|TestScheduler_ClaimSuccess\|TestScheduler_ClaimBlockedByDependency\|TestIntegration_CompileDecomposeSaveReadiness' ./internal/workgraph/` | exit 0; `ok neuroforge/internal/workgraph 0.454s` | **MET** |
| **AC2** Conflicting lease blocks execution with explainable cause | `LeaseManager.acquire` returns typed `*ConflictError` (wraps `ErrLeaseConflict`) naming resource + holding workspace + (when known) expiry; `Scheduler.Claim` propagates + rolls back; `ComputeReadiness` statically predicts path-lease conflicts from `AllowedScope`; `writeAPIError` maps to HTTP 409. **Remediated:** lease scope is now project-scoped (`ClaimRequest.ProjectID`), so conflicts block across tasks in the same project, not just within one task. | `go test -count=1 -run 'TestLease_Conflict\|TestScheduler_ClaimBlockedByLeaseConflict\|TestScheduler_ClaimSemanticTTL\|TestScheduler_CrossTaskLeaseConflict\|TestScheduler_ClaimMissingProjectID' ./internal/workgraph/` → exit 0; `go test -count=1 -run TestWorkGraphAdapter ./internal/daemon/` → exit 0 (project-scoped lease through real transport) | exit 0; both `ok` | **MET** |
| **AC3** After restart graph and leases recover safely | `WorkGraphStore.Save` persists via migration v9 substrate idempotently; `LeaseManager` backed by durable `leases` table (+ `expires_at`); re-opening DB + re-running `Migrate` is a no-op; daemon wires `Graphs` + `Leases` into `Services` so `GET /tasks/{id}/workgraph` recomputes readiness against persisted state. | `go test -count=1 -run 'TestStore_RestartRecoversGraphAndLeases\|TestWorkGraphStore_SaveIsIdempotent' ./internal/workgraph/` → exit 0; `go test -count=1 -run TestWorkGraphShow_BlackBox ./internal/cli/` → exit 0 (compiled `forge` binary + real daemon: graph + lease survive restart) | exit 0; both `ok` | **MET** |

All three mandatory acceptance criteria are proven by passing automated evidence
independently re-run at unit + integration + black-box (compiled binary + real
daemon) levels in this session at the remediated candidate `c4b925c`.

## Required test classes (task brief)

| Required class | Covered | Evidence (re-run at `c4b925c`) |
|---|---|---|
| Dependency readiness tests | yes | `TestReadiness_BlockedByDependency`, `_UnblocksAfterDependencySucceeds`, `_TerminalStateNotReady`, `_Deterministic`; integration `TestIntegration_CompileDecomposeSaveReadiness` (real `task.Compile`) |
| Lease conflict, expiry and reclaim tests | yes | `TestLease_ConflictExplainsCause`, `_TTL_ExpiryReclaim`, `_Renew`, `_RenewDoesNotAffectPerpetual`, `_SweeperIdempotent`; `TestScheduler_ClaimSemanticTTL`; **`TestScheduler_CrossTaskLeaseConflict_ProjectScoped`** (new regression) |
| Persistence / restart | yes | `TestStore_RestartRecoversGraphAndLeases`; `TestWorkGraphStore_SaveIsIdempotent`; black-box step 9 (daemon stop + restart + show) |
| Concurrent claim race tests | yes | `TestScheduler_ConcurrentClaimRace` (16 goroutines; `-race` clean; exactly 1 winner) — passed under `make check` + race run this session |
| Black-box graph show through daemon | yes | `TestWorkGraphShow_BlackBox_CreateShowAndExplainConflict` + `TestWorkGraphShow_BlackBox_MigrationV9Applied` (compiled `forge` + real daemon) — exit 0 |
| `make check` green | yes | exit 0 (FAIL_COUNT 0) |
| `go test -race ./...` clean | yes | affected packages workgraph + daemon race-clean this session (exit 0) |

## Black-box evidence

M14-05 ships a full production path (migration v9 + storage substrate + domain
`WorkGraphStore` + `Readiness` calculator + `Scheduler.Claim/Renew/Release/Expire`
+ transport `/tasks/{id}/workgraph` endpoint + daemon adapter +
`forge workgraph show` CLI). The compiled-binary black-box
`TestWorkGraphShow_BlackBox_CreateShowAndExplainConflict` drives the production
CLI end-to-end through the real daemon and was independently re-run this session
(exit 0). It exercises:

- daemon start → project add → task add → spec save → seed Work Graph →
  `forge workgraph show` (text + JSON);
- AcquirePath from a different workspace at the **project scope** → readiness
  verdict reports blocked with cause naming path + workspace;
- daemon stop → daemon start (restart recovery) → `forge workgraph show` (graph
  AND lease survive);
- missing-task 404.

`TestWorkGraphShow_BlackBox_MigrationV9Applied` proves the production daemon
applies migration v9 idempotently across a restart.

The remediated daemon adapter's `projectID` resolution
(`a.svc.Tasks.Get(ctx, taskID)` → `task.ProjectID` →
`ListActiveByProject(ctx, projectID)`) is exercised against the compiled binary
by step 8 of this black-box test.

## Commands executed

All commands ran from the primary checkout at HEAD `c4b925c` with a fresh build.
Toolchain: `go version go1.26.5 darwin/arm64`.

| Command | Exit code | Result |
|---|---:|---|
| `make build` | 0 | `./forge` built (version `v0.1.0-31-gc4b925c-dirty`) |
| `./forge gate baseline` | 0 | active schema_version 1, baseline_version 1, doc `docs/engineering/ENGINEERING_BASELINE.md` |
| `./forge gate validate --manifest docs/reviews/m14/M14-04.manifest.json` | 0 | M14-04 transition REVIEW_APPROVED -> ACCEPTED legal |
| `./forge gate next --manifest docs/reviews/m14/M14-04.manifest.json` | 0 | predecessor M14-04 ACCEPTED; successor may start |
| `go test -count=1 -run 'TestReadiness\|TestScheduler_ClaimSuccess\|TestScheduler_ClaimBlockedByDependency\|TestIntegration_CompileDecomposeSaveReadiness' ./internal/workgraph/` (AC1) | 0 | `ok neuroforge/internal/workgraph 0.454s` |
| `go test -count=1 -run 'TestLease_Conflict\|TestScheduler_ClaimBlockedByLeaseConflict\|TestScheduler_ClaimSemanticTTL\|TestScheduler_CrossTaskLeaseConflict\|TestScheduler_ClaimMissingProjectID' ./internal/workgraph/` (AC2 workgraph) | 0 | `ok neuroforge/internal/workgraph 1.200s` |
| `go test -count=1 -run TestWorkGraphAdapter ./internal/daemon/` (AC2 transport) | 0 | `ok neuroforge/internal/daemon 2.155s` |
| `go test -count=1 -run 'TestStore_RestartRecoversGraphAndLeases\|TestWorkGraphStore_SaveIsIdempotent' ./internal/workgraph/` (AC3 store) | 0 | `ok neuroforge/internal/workgraph 0.751s` |
| `go test -count=1 -run TestWorkGraphShow_BlackBox ./internal/cli/` (AC3 compiled binary + real daemon) | 0 | `ok neuroforge/internal/cli 6.977s` |
| `go test -count=1 -v -run 'TestScheduler_CrossTaskLeaseConflict_ProjectScoped\|TestScheduler_ClaimMissingProjectID' ./internal/workgraph/` (MAJOR-1 regression) | 0 | both PASS (0.02s, 0.01s) |
| `grep -rn 'func (req ClaimRequest) ProjectID' internal/` (shim deletion) | 1 (no match) | **shim gone** |
| `make check` (600000ms timeout) | 0 | gofmt clean + `go vet ./...` clean + full suite green (every package `ok`; FAIL_COUNT 0) |
| `go test -race -count=1 ./internal/workgraph/ ./internal/daemon/` | 0 | both `ok` (7.039s, 23.331s); **no race detected** |

The implementation, review, and remediation reports' claimed command results
were independently reproduced at the remediated candidate.

## Counterexample search

The acceptor actively searched for a "green-tests-but-unmet-requirement"
counterexample at the remediated candidate:

1. **AC1 — could a package be runnable despite an unmet dependency?**
   `ComputeReadiness` returns `Ready = len(r.BlockedReasons) == 0`, and
   `BlockedReasons` is appended for every dependency not in `PackageSucceeded`;
   `Claim` re-checks `mine.Ready`. The remediation did not touch this path.
   Targeted AC1 test run → exit 0. **No counterexample.**
2. **AC2 — could a conflicting lease fail to block?** The `acquire` fast path
   returns `ConflictError` for a different-workspace holder; the cold path's
   INSERT under the partial UNIQUE index is the linearisation point; `Claim`
   rolls back and propagates. The remediation *strengthened* this by correcting
   the scope from per-task to per-project, closing the cross-task leak. The new
   `TestScheduler_CrossTaskLeaseConflict_ProjectScoped` proves two tasks in the
   same project conflict on the same path. **No counterexample.**
3. **AC3 — could graph or leases be lost on restart?** `ReplaceWorkGraph` runs
   inside `BeginTx`/`Commit`; lease INSERT runs inside the same transaction
   shape; SQLite WAL durability + `Migrate` idempotency ensure restart recovery.
   The remediation did not touch the persistence path. Targeted AC3 test run
   (incl. compiled-binary daemon restart) → exit 0. **No counterexample.**

No counterexample found. The mandatory ACs hold at the remediated candidate.

## Policy / security / autonomy / merge-policy

- The Work Graph substrate performs durable I/O through `*storage.DB` but spawns
  no goroutine, forwards no environment, touches no security policy, autonomy
  profile, merge policy, or quota/budget arithmetic. No agent process is
  involved; no allowlist concern applies.
- The partial UNIQUE index + race-correct acquire **strengthen** the lease
  layer's enforcement of spec §18.4 (`BLOCKED_LEASE`).
- **MAJOR-1 remediation strictly strengthens** lease isolation (per-task →
  per-project), restoring the spec §18.4 contract. No security invariant is
  weakened; the fix *closes* a latent isolation hazard.
- The CLI command is read-only (`forge workgraph show`); it inherits the
  daemon's loopback token authentication and does not mutate state.
- No baseline/gate enforcement weakened; no product-spec change; no
  security/autonomy/delivery/merge-policy invariant changed.

## Regression assessment

- `git diff --stat eb18430 c4b925c` touches only the MAJOR-1 remediation scope
  (production: `scheduling.go` + `workgraph_api.go`; tests: `store_test.go` +
  `workgraph_api_test.go` + `workgraph_show_blackbox_test.go`) plus the three
  M14-05 reports. No changes to `docs/spec/`, `docs/engineering/`,
  `go.mod`/`go.sum`, or any other internal package.
- The M3 lease layer's public API is unchanged; M3 lease tests pass.
- `make check` green across every M0–M13 + M14-00..M14-04 package; **no
  regression**. `go test -race -count=1 ./internal/workgraph/ ./internal/daemon/`
  clean (both `ok`, no race).
- No new external dependencies (`go.mod`/`go.sum` unchanged).

## Fake / stub / demo leakage

- No fakes, stubs, or demo data in production code. The work-graph store
  consumes the real `*ValidatedWorkGraph`; the readiness calculator consumes
  real `Lease` values; the scheduler composes the real `WorkGraphStore` +
  `LeaseManager`.
- `TestIntegration_CompileDecomposeSaveReadiness` exercises the real
  `internal/task.Compile` (accepted M14-02 compiler) — no fake compiler.
- The black-box tests seed the durable substrate through the same
  `WorkGraphStore`/`LeaseManager` the production daemon uses, not through a
  test double.

## Known limitations and accepted follow-ups

The review returned `REVIEW_APPROVED` with one MAJOR (now resolved) and five
MINOR findings. The MINOR findings plus the implementation-reported follow-ups
are tracked and do not obstruct any mandatory AC:

### Five MINOR findings from `M14-05_REVIEW.md` (tracked, non-blocking)

1. **MINOR-1:** `Scheduler.releaseAllAcquired` ignores its `acquired` parameter
   (releases all workspace leases). Cosmetic today (1 workspace ↔ 1 package's
   lease set); future-bug magnet.
2. **MINOR-2:** `ComputeReadiness` does not exclude the caller's own workspace
   from path-lease conflict detection (potential misleading "self-conflict" on
   the display path). Claim path unaffected.
3. **MINOR-3:** migration v9 does not deduplicate pre-existing active lease rows
   (upgrade hazard only if a deployment previously hit the latent M3 race —
   unobserved).
4. **MINOR-4:** `IsLeaseUniqueConstraint` uses broad substring matching on the
   error string (`strings.Contains(err, "UNIQUE")`). Limited blast radius today
   (called only from `createLease`).
5. **MINOR-5:** candidate SHA in the original manifest was a textual description,
   not a SHA. **CLOSED by this acceptance** (manifest updated to the real SHA).

### Implementation follow-ups (`accepted_follow_ups` in the manifest)

- **FU-M14-05-1:** dispatch hook wiring `Scheduler.Claim` into the daemon's
  scheduler service (the MAJOR-1 fix's required prerequisite is now met; the
  caller will be forced to supply a real `ProjectID`).
- **FU-M14-05-2:** daemon endpoints for claim/release lifecycle + matching CLI
  (must carry `ProjectID` through the transport DTO).
- **FU-M14-05-3:** `forge workgraph decompose` CLI command.
- **FU-M14-05-4:** per-lease `Renew(leaseID, ttl)` instead of workspace-scoped
  `RenewAll`.
- **FU-M14-05-5:** multi-daemon writer coordination for the Work Graph.
- **FU-M14-05-6:** per-package `SemanticNeeds` field for static semantic-conflict
  prediction.
- **FU-M14-05-7:** TUI bindings for visualising the work graph.
- **FU-M14-05-8** (inherited from M14-04 R1): `ValidateWorkGraph`
  empty-state-default inconsistency.
- **FU-M14-05-9:** document the M3 lease-race defect this task closed (partial
  UNIQUE index) in an ADR.

None of the above obstructs any mandatory acceptance criterion or the sequential
gate.

## Actor separation

- implementer: `M14-05-impl-session-1`
- reviewer: `M14-05-review-session-1`
- remediator (MAJOR-1): `M14-05-major1-remediation-session`
- acceptor: `M14-05-accept-session-1`
- pairwise distinct: **yes**

## Verdict

**`ACCEPTED`**

Every mandatory acceptance criterion is met and proven by passing automated
evidence independently re-run at unit, integration, race, and compiled-binary
levels in this session at the remediated candidate `c4b925c`:

- **AC1** (package not runnable until completion dependencies): targeted test
  run → exit 0; integration test exercises real `task.Compile`.
- **AC2** (conflicting lease blocks execution with explainable cause): targeted
  workgraph + daemon test runs → exit 0; **the remediation strengthens this AC
  to project-scoped isolation** (spec §18.4), proven by the new regression test
  `TestScheduler_CrossTaskLeaseConflict_ProjectScoped`.
- **AC3** (graph and leases recover after restart): targeted workgraph + CLI
  black-box (compiled `forge` + real daemon) test runs → exit 0.

- `make check` exit 0 (FAIL_COUNT 0); `go test -race -count=1
  ./internal/workgraph/ ./internal/daemon/` clean (both `ok`, no race);
  `gofmt`/`go vet` clean.
- M14-04 is `ACCEPTED` (predecessor gate exit 0).
- MAJOR-1 is **resolved**: the misleading `ProjectID()` shim is deleted
  (grep-verified); `ClaimRequest.ProjectID string` is an explicit field;
  `Scheduler.Claim` uses `req.ProjectID` for all three lease operations and
  rejects an empty one; the daemon adapter resolves `projectID` from the task
  row once; the regression test was proven to FAIL when the fix is reverted and
  PASS when applied.
- The five MINOR findings are non-blocking and tracked as follow-ups
  (MINOR-5 closed by this acceptance).
- Actor separation is pairwise distinct across implementation / review /
  remediation / acceptance.
- No scope creep; product spec, baseline, and gate enforcement untouched; no
  security/autonomy/delivery/merge-policy invariant changed — the remediation
  strictly *strengthens* lease isolation.

The successor task **M14-06 may now start** (`./forge gate next --manifest
docs/reviews/m14/M14-05.manifest.json` returns exit 0 once the manifest update
from this acceptance is in place — verifiable below).
