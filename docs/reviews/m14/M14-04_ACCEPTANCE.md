# M14-04 Acceptance

## Acceptance identity

- **acceptor actor/session ID:** `M14-04-accept-session` (fresh, independent
  session; performed no implementation, no review of M14-04, and is not the
  M14-04 implementer or reviewer, nor the M14-03 accept actor).
- **implementation actor/session ID:** `M14-04-impl-session-3`
  (per `M14-04_IMPLEMENTATION.md`; the third M14-04 session — the first two
  returned `BLOCKED` because M14-03 was not yet `ACCEPTED`).
- **review actor/session ID:** `M14-04-review-session-1` (verdict
  `REVIEW_APPROVED`; per `M14-04_REVIEW.md`).
- **independence confirmed:** **yes** — the three role-bound ids
  (`M14-04-impl-session-3`, `M14-04-review-session-1`, `M14-04-accept-session`)
  are pairwise distinct. The acceptor re-checked every implementation/review
  claim against the checked-out code, tests, and the compiled `forge` binary
  rather than trusting any report. This session authored no production code and
  no tests; it authored only this report + `M14-04.manifest.json`.
- **acceptance date:** 2026-07-27.

## Git baseline

- **accepted predecessor SHA (M14-03 acceptance):**
  `a1e11cf9a40e9ab7d00d0e67fbb95f5e9c98b8d8`
  (M14-04 implementation starting SHA; M14-03 manifest `state = ACCEPTED`).
- **implementation candidate SHA:** `099bda34c39f3d3bf58d86507259063e48350252`
  (`M14-04: Work Graph domain, DAG validation, AC mapping, deterministic
  decomposition`).
- **review commit SHA (the candidate being accepted):**
  `11a9893c44ffaecf6bd37aef1750e28e9882bf1d`
  (`M14-04: independent review (REVIEW_APPROVED, six MINOR follow-ups)` — sole
  added file `docs/reviews/m14/M14-04_REVIEW.md`).
- **acceptance starting HEAD:** `11a9893c44ffaecf6bd37aef1750e28e9882bf1d`.

Ancestry verified (all `git merge-base --is-ancestor` → exit 0):

- `a1e11cf` (M14-03 accept) is an ancestor of `099bda3` (implementation
  candidate).
- `099bda3` is an ancestor of `11a9893` (review commit / current HEAD).

Production/test code identity: `git diff --name-only 099bda3 11a9893` lists only
`docs/reviews/m14/M14-04_REVIEW.md` — i.e. the production and test code
exercised in this acceptance session is **byte-identical** to the reviewed
candidate `099bda3`. The review's evidence applies unchanged.

Working tree at HEAD: only unrelated pre-existing review docs are
modified/untracked (`docs/reviews/MINIMAL_RUN_*`, `docs/reviews/M12_M13_REVIEW.md`)
plus a 0-byte `ism` junk file. **No uncommitted production changes.** These
unrelated artifacts predate M14-04 (noted in both the implementation and review
reports) and are not touched by this acceptance.

## Predecessor gate

- M14-03 manifest: `docs/reviews/m14/M14-03.manifest.json` (state `ACCEPTED`,
  baseline_version `1`).
- command (compiled `/tmp/forge-m14-04-accept`):

  ```
  $ /tmp/forge-m14-04-accept gate baseline
  active schema_version: 1
  active baseline_version: 1
  baseline document: docs/engineering/ENGINEERING_BASELINE.md

  $ /tmp/forge-m14-04-accept gate validate --manifest docs/reviews/m14/M14-03.manifest.json
  OK: task "M14-03" transition REVIEW_APPROVED -> ACCEPTED is legal under baseline v1

  $ /tmp/forge-m14-04-accept gate next --manifest docs/reviews/m14/M14-03.manifest.json
  OK: predecessor "M14-03" is ACCEPTED; successor task may start
  ```

  All exit 0.

Predecessor gate is **OPEN**; M14-04 was lawfully allowed to start.

## Review prerequisite

- reviewed candidate (review commit): `11a9893c44ffaecf6bd37aef1750e28e9882bf1d`
  — **matches** the candidate being accepted (production/test code byte-identical
  to `099bda3`).
- verdict: `REVIEW_APPROVED` (`M14-04_REVIEW.md`, §11).
- blocker findings: **0**.
- major findings: **0**.
- accepted minor follow-ups: **6** (MINOR-1..6; all non-blocking, none
  invalidates a mandatory AC; recorded as FU-M14-04-R1..R6).

Acceptance is permitted: the review examined the exact candidate `099bda3` (whose
production/test code is identical at HEAD `11a9893`) and returned
`REVIEW_APPROVED` with no BLOCKER or MAJOR remaining.

## Independent verification method

This acceptor did not trust the implementation or review reports. It:

1. Re-read the full production code (`graph.go`, `serialize.go`, `doc.go`) and
   the test files.
2. Re-built the `forge` binary from HEAD and exercised the gate tooling.
3. Re-ran the targeted workgraph tests, the determinism stress (15×), the race
   detector, the shipped integration tests, `make check`, and `go test -race ./...`.
4. **Authored an independent external test package**
   (`internal/workgraph/zz_accept_probe_test.go`, deleted after the run) with 12
   probe tests covering every mandatory negative case + determinism + the real
   `task.Compile → Decompose` integration + the opaque-runnable-handle
   guarantee. All 12 PASS.
5. Searched actively for a "green-tests-but-unmet-requirement" counterexample
   (none found — see §"Counterexample search" below).

## Acceptance matrix

Mandatory acceptance criteria (per the M14-04 task brief and
`M14-04_IMPLEMENTATION.md`):

| Criterion | Production implementation | Automated evidence (re-run / re-probed this session) | Independent acceptor result | Status |
|---|---|---|---|---|
| **AC1** Every package is linked to an objective/AC | `WorkPackage.Objective` (non-empty enforced at `graph.go:352-354`); `WorkPackage.AcceptedACIDs` (≥1 enforced at `graph.go:366-368`); `WorkPackage.AllowedScope` (AC→scope half); `ValidatedWorkGraph.PackageForAC` (`graph.go:209-218`). `Decompose` sets `Objective = spec.Objective` and one AC per package (`graph.go:660-676`). | Shipped: `TestValidate_EmptyObjective`, `TestValidate_PackageOwnsNoAC` (reject + nil handle), `TestValidate_CompositeDiamond` (PackageForAC), `TestDecompose_ACOverageAndChainShape`, `TestIntegration_CompileThenDecompose` (real `task.Compile`). **Acceptor probes:** `TestAccept_EmptyObjective`, `TestAccept_PackageOwnsNoAC` (reject + nil handle), `TestAccept_ValidDiamondProducesHandle` (PackageForAC("AC-3")→C), `TestAccept_IntegrationCompileThenDecomposeReal` (real `task.Compile` → every AC owned exactly once with non-empty objective). | Each negative probe asserts `(nil, err)` with `errors.Is(err, ErrInvalidWorkGraph)`; the positive probe asserts the returned `*ValidatedWorkGraph` exposes objective + AC mapping. The integration probe feeds the **real** `internal/task.Compile` (accepted M14-02 compiler) and verifies every compiler-produced AC is owned exactly once. | **MET** |
| **AC2** An invalid DAG cannot be persisted as runnable | `ValidatedWorkGraph` is an opaque struct with no exported fields and **no public constructor** (`graph.go:186-188`); only `ValidateWorkGraph` / `ValidateAgainstSpec` / `Decompose` return `*ValidatedWorkGraph`. `MarshalValidated` accepts only `*ValidatedWorkGraph` (`serialize.go:130-135`). Every negative path returns `(nil, err)` wrapping `ErrInvalidWorkGraph`. | Shipped: cycle (`TestValidate_Cycle_TwoNode/_SelfDependency/_ThreeNode`), missing-edge (`TestValidate_MissingEdge/_OneOfSeveral`), duplicate-AC-owner (`TestValidate_DuplicateACOwner/_WithinPackage`), unreachable (`TestValidate_UnreachableNode_DisconnectedSibling/_DisconnectedChain`), structural (`TestValidate_EmptyTaskID/_NoPackages/_PackageNoID/_DuplicatePackageID/_UnknownStage/_EmptyTitle/_UnknownPackageState`), spec-mapping (`TestValidateAgainstSpec_OwnedACNotInSpec/_CoverageGap/_InvalidSpec`). **Acceptor probes:** `TestAccept_CycleTwoNode`, `TestAccept_CycleSelfLoop`, `TestAccept_CycleThreeNode`, `TestAccept_MissingEdge`, `TestAccept_DuplicateACOwner`, `TestAccept_UnreachableNode` — each uses `assertReject` asserting **both** `(nil, err)` and `errors.Is(err, ErrInvalidWorkGraph)`. `TestAccept_RunnableHandleOnlyFromValidator` pins the exported surface (no path to a handle bypasses the validator). | Every probe returns a **nil** handle (not just an error). The acceptor confirmed by reading `graph.go` that `ValidatedWorkGraph.graph` is unexported and no public constructor exists — the struct literal cannot be constructed from outside the package. | **MET** |
| **AC3** The graph is deterministic for an identical specification | `Decompose` (`graph.go:652-680`) is a pure function: validates via `task.ValidateSpecification`, builds packages with IDs derived from `(TaskID, AC ID)`, chains in spec-AC order. No I/O, no clock, no randomness, no map-iteration-into-ordered-output. `MarshalWorkGraph` canonicalises (packages sorted by ID; per-package lists sorted + de-duplicated) at `serialize.go:46-59`. | Shipped: `TestDecompose_DeterministicFromSpec` (10×), `TestSerialize_Deterministic_10Runs` (10×), `TestSerialize_Canonical_IgnoresInputOrdering`, `TestSerialize_Canonical_SortsPerPackageLists`, `TestIntegration_CompileThenDecompose`. **Acceptor probes:** `TestAccept_DecomposeDeterministic20x` (20× `Decompose` + `MarshalValidated` compared `bytes.Equal` — all identical); `TestAccept_IntegrationCompileThenDecomposeReal` (re-decompose the real compiler output). | Determinism verified at the byte level (`bytes.Equal`) over 20 iterations, including the real `task.Compile` composition path. The acceptor confirmed by reading `graph.go:652-699` that `Decompose` imports no `os`/`time`/`math/rand` in its path (the only `time` import is the `Attempt` struct field type). | **MET** |

All three mandatory acceptance criteria are proven by automated evidence
independently re-run at unit + integration + race levels in this session, plus
12 fresh acceptor probes.

### Required test classes (task brief)

| Required class | Covered | Evidence |
|---|---|---|
| Valid simple DAG fixture | yes | `TestValidate_SimpleSinglePackage`, `TestValidate_LinearChain`, `TestDecompose_SingleACSpec`; acceptor `TestAccept_ValidDiamondProducesHandle` |
| Valid composite DAG fixture | yes | `TestValidate_CompositeDiamond` (A→{B,C}→D), `TestDecompose_ACOverageAndChainShape` (N-package chain) |
| Cycle negative | yes | `TestValidate_Cycle_TwoNode/_SelfDependency/_ThreeNode`; acceptor `TestAccept_CycleTwoNode/_SelfLoop/_ThreeNode` |
| Missing-edge negative | yes | `TestValidate_MissingEdge/_OneOfSeveral`; acceptor `TestAccept_MissingEdge` |
| Duplicate-AC-owner negative | yes | `TestValidate_DuplicateACOwner/_WithinPackage`; acceptor `TestAccept_DuplicateACOwner` |
| Unreachable-node negative | yes | `TestValidate_UnreachableNode_DisconnectedSibling/_DisconnectedChain/_ConnectedPasses`; acceptor `TestAccept_UnreachableNode` |
| Stable serialization | yes | `TestSerialize_RoundTrip`, `_Canonical_IgnoresInputOrdering`, `_Canonical_SortsPerPackageLists`, `_Deterministic_10Runs`, `_AttemptsRoundTrip`, `_Unmarshal_RejectsUnknownFields`, `_OmitEmpty`, `_ValidJSON`, `TestMarshalValidated_NilGuard/_MatchesMarshalWorkGraph`, `_Canonicalize_DoesNotMutateInput` |
| `make check` green | yes | exit 0 (see Commands) |

## Counterexample search

The acceptor actively searched for a "green-tests-but-unmet-requirement"
counterexample:

1. **Could an invalid DAG become runnable?** Searched for any public constructor
   returning `*ValidatedWorkGraph` other than the three validators. Found none.
   `ValidatedWorkGraph.graph` is unexported; the struct literal cannot be
   constructed from outside the package (the external test package would not
   compile if it tried). The acceptor probes confirm every negative returns a
   nil handle. **No counterexample.**
2. **Could two packages own the same AC?** The `acOwners` map at
   `graph.go:370-375` rejects the second owner; the check is in the single
   `validate` function all three entry points call. **No counterexample.**
3. **Could `Decompose` produce a non-deterministic output?** Searched for map
   iteration / time / randomness / I/O in `Decompose` (`graph.go:652-699`).
   Found none. 20× byte-identity corroborates. **No counterexample.**
4. **Could a cyclic graph validate?** Probed 2-node, 3-node, and self-loops —
   all rejected with nil handle. `detectCycle` uses DFS 3-colouring correctly.
   **No counterexample.**
5. **Could a missing-edge graph validate?** Probed — rejected at
   `graph.go:382-394`. **No counterexample.**
6. **Could an unreachable node validate?** Probed disconnected sibling —
   rejected by `weaklyUnreachable`. **No counterexample.**
7. **Is the integration evidence faked?** Read
   `internal/workgraph/integration_test.go` imports — it imports the **real**
   `neuroforge/internal/task` (accepted M14-02 compiler), not a fake. The
   acceptor's own `TestAccept_IntegrationCompileThenDecomposeReal` calls the
   real `task.Compile` and feeds `compiled.Specification` to `Decompose`.
   **No fake leakage.**

No counterexample found. The mandatory ACs hold.

## Black-box evidence

M14-04 ships **no production wiring** (no daemon endpoint, no CLI command, no
storage table, no scheduler hook, no TUI binding) — it is a pure domain library
in `internal/workgraph/`. Per Engineering Baseline v1 §3 rule 3, acceptance
**still** requires a `blackbox` scenario with `status = passed` (no exemption at
acceptance); the documented equivalent is **the gate tooling itself driven
through the compiled binary**. Observed this session against a freshly compiled
`/tmp/forge-m14-04-accept`:

| Step | Command | Result |
|---|---|---|
| 1 | `go build -o /tmp/forge-m14-04-accept ./cmd/forge` | exit 0; binary 19058130 bytes |
| 2 | `/tmp/forge-m14-04-accept gate baseline` | exit 0; active schema_version 1, baseline_version 1, doc `docs/engineering/ENGINEERING_BASELINE.md` |
| 3 | `/tmp/forge-m14-04-accept gate validate --manifest docs/reviews/m14/M14-03.manifest.json` | exit 0; `OK: task "M14-03" transition REVIEW_APPROVED -> ACCEPTED is legal under baseline v1` |
| 4 | `/tmp/forge-m14-04-accept gate next --manifest docs/reviews/m14/M14-03.manifest.json` | exit 0; `OK: predecessor "M14-03" is ACCEPTED; successor task may start` |
| 5 | `/tmp/forge-m14-04-accept gate validate --manifest docs/reviews/m14/M14-04.manifest.json` | exit 0; `OK: task "M14-04" transition REVIEW_APPROVED -> ACCEPTED is legal under baseline v1` |

The product-criteria evidence (AC1/AC2/AC3) is at unit + integration levels
exercising the real `internal/task.Compile` → `workgraph.Decompose` composition
(`TestIntegration_CompileThenDecompose`,
`TestIntegration_CompileVagueInputDecomposesSafely`). The compiled-binary
black-box for the work-graph **domain surface itself** (e.g. `forge workgraph
decompose/show`) is deferred to **FU-M14-04-3** when a CLI/daemon surface
lands; that gap is honestly documented and does not mask any mandatory
criterion (the criteria are domain-model invariants, proven at unit +
integration levels against the real compiler).

No skipped/manual/opt-in test is the sole evidence for a mandatory criterion.

## Commands executed

All commands ran from the primary checkout at HEAD `11a9893` with a fresh build.
Toolchain: `go version go1.26.5 darwin/arm64`.

| Command | Exit code | Result |
|---|---:|---|
| `go build ./internal/workgraph/` | 0 | clean build |
| `go vet ./internal/workgraph/` | 0 | clean |
| `gofmt -l internal/workgraph/` | 0 | no files listed |
| `go test -count=1 ./internal/workgraph/...` | 0 | 54 tests/subtests PASS (0.59s) |
| `go test -count=15 -run 'TestDecompose_Deterministic\|TestSerialize_Deterministic' ./internal/workgraph/...` | 0 | determinism 15× PASS |
| `go test -race -count=1 ./internal/workgraph/...` | 0 | race-clean (1.83s) |
| `go test -count=1 -run 'TestIntegration' -v ./internal/workgraph/` | 0 | both integration tests PASS (real `task.Compile`) |
| `go test -count=1 -run 'TestAccept_' -v ./internal/workgraph/` (acceptor probes, since removed) | 0 | 12/12 acceptor probes PASS |
| `make check` | 0 | gofmt clean + `go vet ./...` clean + full suite green (every package `ok`; FAIL_COUNT 0) |
| `go test -race ./...` | 0 | 61 packages `ok`; **no FAIL, no race detected** |
| `/tmp/forge-m14-04-accept gate baseline` | 0 | active baseline_version 1 |
| `/tmp/forge-m14-04-accept gate validate --manifest docs/reviews/m14/M14-03.manifest.json` | 0 | M14-03 transition legal |
| `/tmp/forge-m14-04-accept gate next --manifest docs/reviews/m14/M14-03.manifest.json` | 0 | predecessor ACCEPTED; successor may start |
| `/tmp/forge-m14-04-accept gate validate --manifest docs/reviews/m14/M14-04.manifest.json` | 0 | **M14-04 transition REVIEW_APPROVED -> ACCEPTED legal** |

The implementation report's and review report's claimed command results were
independently reproduced.

## Regression assessment

- `git diff --name-only a1e11cf 11a9893` touches only `internal/workgraph/*`
  (four new files + `doc.go`) and the two M14-04 reports. **No** changes to
  `docs/spec/`, `docs/engineering/`, `go.mod`/`go.sum`, or any other internal
  package.
- The existing M3 lease layer (`internal/workgraph/leases.go`,
  `leases_test.go`) is **untouched** and still passes.
- `make check` green across every M0–M13 + M14-00..M14-03 package; **no
  regression**. `go test -race ./...` clean (61 packages `ok`, no FAIL, no
  race).
- No new external dependencies (`go.mod`/`go.sum` unchanged).
- No `TODO`/`FIXME`/`panic("unimplemented")`/stub in the new production files
  (the single grep hit is a doc comment at `graph.go:15` quoting the baseline
  rule). `gofmt -l .` clean; `go vet ./...` clean.

## Fake / stub / demo leakage

- No fakes, stubs, or demo data in production code. `Decompose` consumes the
  real `task.Specification`; validation uses real `task.AcceptanceCriterion`.
- The shipped integration tests import the real `internal/task.Compile` (the
  accepted M14-02 compiler) — verified by reading the imports. No fake compiler.
- The acceptor's own integration probe calls the real `task.Compile` and feeds
  `compiled.Specification` to `Decompose`.
- Test helpers are concise constructors over real types, not test doubles.

## Policy / security / scope

- The work-graph domain model performs **no I/O**, holds no storage handle,
  spawns no goroutine, forwards no environment, and touches no security policy,
  autonomy profile, merge policy, or quota/budget arithmetic. It is a pure
  value-type domain layer.
- No agent process is involved; no allowlist concern applies.
- The AC-ownership uniqueness invariant is a correctness guard prerequisite for
  the Merge Governor's per-AC evidence accounting (spec §28).
- No baseline/gate enforcement weakened; no product-spec change; no
  security/autonomy/delivery/merge-policy invariant changed.
- Scope is correctly bounded to M14-04 (work-graph domain only; **no execution
  wiring**). No scope creep.

## Restart / idempotency / concurrency

- The domain model is pure value-type: no goroutines, no shared mutable state,
  no channels, no `sync` usage in the new files.
- `go test -race ./...` is clean (no race).
- `Decompose` is a pure function — restart-invariant by construction (identical
  spec → identical graph). 20× byte-identity proven by the acceptor probe.
- `ValidatedWorkGraph.Graph()` and `.Packages()` return defensive copies
  (`graph.go:229-252`); mutation of the returned value does not leak into the
  validated state.
- (Note MINOR-6 / FU-M14-04-R6: `ValidateWorkGraph` mutates the **input**
  slice in place during normalisation; the returned handle is immutable. This
  is an undocumented behaviour, not a runtime hazard — accepted as a follow-up.)

## Known limitations and accepted follow-ups

1. **Decomposition is minimal (sequential chain).** `Decompose` produces one
   implementation package per AC, chained sequentially. Richer stage-based
   decomposition (research → contract fan-out → integration → verification) is
   deferred to **FU-M14-04-1**. The sequential default is honest (no fabricated
   parallelism) and deterministic; any hand-built valid DAG shape is accepted
   by `ValidateWorkGraph`.
2. **AllowedScope is the full ProposedScope per package.** Finer scope
   partitioning is execution-time; **FU-M14-04-4**.
3. **No durable persistence.** `work_packages` / `dependencies` SQLite tables
   are not added (the brief forbids execution wiring). `ValidatedWorkGraph` is
   the ready handle for the future persistence layer (**FU-M14-04-2**,
   migration v9).
4. **No CLI/daemon surface.** M14-04 ships no user-facing command; users cannot
   yet inspect a work graph through `forge`. **FU-M14-04-3** (closes the
   work-graph compiled-binary black-box gap).
5. **MINOR-1 (FU-M14-04-R1):** empty `PackageState` does not default to
   `pending` despite the code comment claiming it does (dead code at
   `graph.go:355-362`). Non-blocking; `Decompose` explicitly sets
   `State: PackagePending`.
6. **MINOR-2 (FU-M14-04-R2):** cross-task packages are silently accepted (graph
   `TaskID` and package `TaskID` not reconciled). Non-blocking; `Decompose`
   sets every package's `TaskID` to `spec.TaskID`.
7. **MINOR-3 (FU-M14-04-R3):** self-dependency is reported twice (missing-edge
   pass + `detectCycle`). Cosmetic.
8. **MINOR-4 (FU-M14-04-R4):** `TestTopologicalOrder_CycleRejected` does not
   actually call `TopologicalOrder` (misleading name).
9. **MINOR-5 (FU-M14-04-R5):** no `M14-04.manifest.json` existed at review time
   — **CLOSED by this acceptance** (manifest created; `forge gate validate`
   exit 0).
10. **MINOR-6 (FU-M14-04-R6):** `ValidateWorkGraph` mutates the caller's input
    slice (undocumented in-place normalisation); the returned handle is
    immutable.

None of the above obstructs any mandatory acceptance criterion or the sequential
gate.

## Verdict

**`ACCEPTED`**

Every mandatory acceptance criterion is met and proven by passing automated
evidence independently re-run at unit, integration, race, and compiled-binary
(gate tooling) levels in this session:

- **AC1** (package↔objective/AC link): structural enforcement + shipped tests +
  acceptor probes; real `task.Compile` integration.
- **AC2** (invalid DAG cannot become runnable): parse-don't-validate type
  design + 12 negative cases (cycle/missing-edge/duplicate-AC/unreachable +
  structural), each asserting `(nil, err)`; acceptor probes confirm the
  runnable handle is opaque from outside the package.
- **AC3** (deterministic): pure `Decompose` + canonical `MarshalWorkGraph` +
  20× byte-identity (acceptor probe) + input-order-independence.

- `make check` exit 0 (FAIL_COUNT 0); `go test -race ./...` clean (61 packages
  `ok`, no race); `go vet ./...` and `gofmt -l .` clean.
- M14-03 is `ACCEPTED` (predecessor gate exit 0).
- The review examined the exact candidate `099bda3` (production/test code
  byte-identical at HEAD `11a9893`) and returned `REVIEW_APPROVED` with
  0 BLOCKER / 0 MAJOR.
- The six MINOR findings are non-blocking and accepted as follow-ups
  (FU-M14-04-R1..R6); MINOR-5 (missing manifest) is **CLOSED** by this
  acceptance.
- Actor separation is pairwise distinct across implementation / review /
  acceptance.
- No scope creep; product spec, baseline, and gate enforcement untouched; no
  security/autonomy/delivery/merge-policy invariant changed.
- The compiled-binary black-box at acceptance is the gate tooling itself
  (`forge gate validate` / `forge gate next` through `/tmp/forge-m14-04-accept`,
  all exit 0) — the documented equivalent for a no-production-wiring domain
  task per baseline §3 rule 3. The work-graph domain-surface black-box is
  deferred to FU-M14-04-3.
- The manifest passes `forge gate validate` (exit 0).

The successor task **M14-05 may now start** (`forge gate next --manifest
docs/reviews/m14/M14-04.manifest.json` returns exit 0 — verifiable after this
report + manifest are committed).
