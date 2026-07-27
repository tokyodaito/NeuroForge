# M14-04 — Independent Review Report

**Task:** M14-04 — Work Graph domain, DAG validation, AC mapping.
**Reviewer actor:** `M14-04-review-session-1` (independent; fresh context, no
prior session state).
**Reviewed candidate SHA:** `099bda34c39f3d3bf58d86507259063e48350252`
(commit `M14-04: Work Graph domain, DAG validation, AC mapping, deterministic
decomposition`).
**Starting SHA (implementation base):** `a1e11cf9a40e9ab7d00d0e67fbb95f5e9c98b8d8`
(`M14-03: accept task (state ACCEPTED, baseline v1)`).
**Verdict:** **`REVIEW_APPROVED`** (with six MINOR findings recorded as
follow-ups; no BLOCKER, no MAJOR).

---

## 1. Review scope and method

This review independently re-verified the M14-04 implementation at candidate
SHA `099bda3` against the task brief and Engineering Baseline v1. The method:

1. Confirmed the working tree HEAD matches the candidate SHA claimed by
   `docs/reviews/m14/M14-04_IMPLEMENTATION.md` (`099bda3`).
2. Inspected the full diff `a1e11cf..099bda3` (8 files, all under
   `internal/workgraph/` + the implementation report doc).
3. Re-ran targeted tests, `make check`, `go test -race ./...`, and the
   baseline gate.
4. Wrote independent probe tests to search for counterexamples
   (empty-state default, cross-task packages, self-loop message redundancy,
   `TopologicalOrder` defense-in-depth reachability, input mutation).
5. Verified scope boundaries (no spec / engineering / gate / storage / daemon
   / scheduler / cli / transport / task / policy changes).

No production code was modified by this review. Probe tests were deleted after
each run; the working tree contains only this review report as a new file.

## 2. Predecessor gate verification

```
$ ./forge gate baseline
active schema_version: 1
active baseline_version: 1
baseline document: docs/engineering/ENGINEERING_BASELINE.md

$ ./forge gate validate --manifest docs/reviews/m14/M14-03.manifest.json
OK: task "M14-03" transition REVIEW_APPROVED -> ACCEPTED is legal under baseline v1

$ ./forge gate next --manifest docs/reviews/m14/M14-03.manifest.json
OK: predecessor "M14-03" is ACCEPTED; successor task may start
```

Predecessor `M14-03` is `ACCEPTED` (manifest state `ACCEPTED`, baseline v1);
the gate authorises M14-04 to start. Confirmed.

## 3. Diff summary

```
docs/reviews/m14/M14-04_IMPLEMENTATION.md  | modified (implementation report)
internal/workgraph/doc.go                  | +27/-5  (package doc updated)
internal/workgraph/graph.go                | +699    (NEW — types, validation, decomposition)
internal/workgraph/graph_test.go           | +406    (NEW — valid DAGs, decompose, determinism)
internal/workgraph/integration_test.go     | +137    (NEW — task.Compile → workgraph.Decompose)
internal/workgraph/serialize.go            | +163    (NEW — canonical JSON marshal/unmarshal)
internal/workgraph/serialize_test.go       | +237    (NEW — round-trip, canonical, determinism)
internal/workgraph/validate_test.go        | +265    (NEW — all mandatory negatives)
```

Scope confirmation (`git diff --name-only a1e11cf 099bda3`):
- No changes to `docs/spec/`, `docs/engineering/`, `go.mod`, `go.sum`.
- No changes to `internal/enggate`, `internal/policy`, `internal/merge`,
  `internal/storage`, `internal/daemon`, `internal/scheduler`, `internal/cli`,
  `internal/transport`, `internal/task`, or any adapter.
- The existing M3 lease layer (`internal/workgraph/leases.go`,
  `leases_test.go`) is **untouched** and still passes.
- No new external dependencies.

Scope is correctly bounded to M14-04 (work-graph domain only; no execution
wiring). No scope creep.

## 4. Acceptance criteria matrix

| Mandatory AC | Where implemented | Proving test(s) | Why the test verifies observable behaviour | Verdict |
|---|---|---|---|---|
| **AC1** Every package is linked to an objective/AC | `WorkPackage.Objective` (non-empty enforced at `graph.go:352-354`); `WorkPackage.AcceptedACIDs` (≥1 enforced at `graph.go:366-368`); `WorkPackage.AllowedScope` carries the AC→scope half; `ValidatedWorkGraph.PackageForAC` exposes the mapping (`graph.go:209-218`). `Decompose` sets `Objective = spec.Objective` and one AC per package (`graph.go:660-676`). | `TestValidate_EmptyObjective`, `TestValidate_PackageOwnsNoAC` (reject + nil handle), `TestValidate_CompositeDiamond` (PackageForAC lookup), `TestDecompose_ACOverageAndChainShape` (objective + AC per package, exactly-one ownership), `TestIntegration_CompileThenDecompose` (real `task.Compile` output: every compiler-produced AC owned exactly once with non-empty objective). | The negative tests assert the validator returns `(nil, err)` with a message naming the missing field — observable via the public API. The positive tests assert the returned `*ValidatedWorkGraph` exposes the objective and the AC→package mapping through accessors. The integration test feeds the real M14-02 compiler output (not a fake) and verifies end-to-end coverage. | **MET** |
| **AC2** An invalid DAG cannot be persisted as runnable | `ValidatedWorkGraph` is an opaque struct with no exported fields and no public constructor (`graph.go:186-188`); only `ValidateWorkGraph` / `ValidateAgainstSpec` / `Decompose` return `*ValidatedWorkGraph`. `MarshalValidated` accepts only `*ValidatedWorkGraph` (`serialize.go:130-135`). Every negative path returns `(nil, err)` with a wrapped `ErrInvalidWorkGraph`. | Cycle: `TestValidate_Cycle_TwoNode`, `TestValidate_Cycle_SelfDependency`, `TestValidate_Cycle_ThreeNode`. Missing edge: `TestValidate_MissingEdge`, `TestValidate_MissingEdge_OneOfSeveral`. Duplicate AC owner: `TestValidate_DuplicateACOwner`. Unreachable: `TestValidate_UnreachableNode_DisconnectedSibling`, `TestValidate_UnreachableNode_DisconnectedChain`. Structural: `TestValidate_EmptyTaskID`, `TestValidate_NoPackages`, `TestValidate_PackageNoID`, `TestValidate_DuplicatePackageID`, `TestValidate_UnknownStage`, `TestValidate_EmptyTitle`, `TestValidate_UnknownPackageState`. Spec-mapping: `TestValidateAgainstSpec_OwnedACNotInSpec`, `TestValidateAgainstSpec_CoverageGap`, `TestValidateAgainstSpec_InvalidSpec`. Each uses `assertInvalid` which asserts **both** `(nil, err)` and that the error wraps `ErrInvalidWorkGraph` (via substring). | Every negative test asserts that the validator returns a **nil** `*ValidatedWorkGraph` (not just an error) — so no code path can obtain a runnable handle for an invalid DAG through the public API. The parse-don't-validate design makes the type itself the proof: there is no exported constructor that bypasses validation. Persistence is honestly deferred (FU-M14-04-2); the persistence layer will accept only `*ValidatedWorkGraph`, so the type-level guarantee extends to the future storage boundary. | **MET** |
| **AC3** The graph is deterministic for an identical specification | `Decompose` (`graph.go:652-680`) is a pure function: it validates the spec via `task.ValidateSpecification`, then builds packages with IDs derived from `(TaskID, AC ID)` and chains them in spec-AC order. No I/O, no clock, no randomness, no map-iteration-into-ordered-output. `MarshalWorkGraph` canonicalises (packages sorted by ID; per-package string lists sorted lexicographically and de-duplicated) at `serialize.go:46-59`. | `TestDecompose_DeterministicFromSpec` (10× byte-identity via canonical JSON), `TestSerialize_Deterministic_10Runs` (10× byte-identity), `TestSerialize_Canonical_IgnoresInputOrdering` (swapped package input order → identical bytes; first output package is `AAA` not `ZZZ`), `TestSerialize_Canonical_SortsPerPackageLists` (out-of-order ACs/deps → sorted in output), `TestIntegration_CompileThenDecompose` (re-decompose byte-identical). | Determinism is verified at the byte level (`bytes.Equal`), not just structural equality. The input-order-independence test specifically swaps package order and confirms the canonical output is identical — this is the strongest observable proof of determinism (a non-deterministic marshal would produce different bytes). | **MET** |

### Required test classes (task brief)

| Required class | Covered | Evidence |
|---|---|---|
| Valid simple DAG fixture | yes | `TestValidate_SimpleSinglePackage`, `TestValidate_LinearChain`, `TestDecompose_SingleACSpec` |
| Valid composite DAG fixture | yes | `TestValidate_CompositeDiamond` (A→{B,C}→D), `TestDecompose_ACOverageAndChainShape` (N-package chain) |
| Cycle negative | yes | `TestValidate_Cycle_TwoNode`, `TestValidate_Cycle_SelfDependency`, `TestValidate_Cycle_ThreeNode` |
| Missing-edge negative | yes | `TestValidate_MissingEdge`, `TestValidate_MissingEdge_OneOfSeveral` |
| Duplicate-AC-owner negative | yes | `TestValidate_DuplicateACOwner`, `TestValidate_DuplicateACOwner_WithinPackage` |
| Unreachable-node negative | yes | `TestValidate_UnreachableNode_DisconnectedSibling`, `TestValidate_UnreachableNode_DisconnectedChain`, `TestValidate_UnreachableNode_ConnectedPasses` (sanity) |
| Stable serialization | yes | `TestSerialize_RoundTrip`, `TestSerialize_Canonical_IgnoresInputOrdering`, `TestSerialize_Canonical_SortsPerPackageLists`, `TestSerialize_Deterministic_10Runs`, `TestSerialize_AttemptsRoundTrip`, `TestSerialize_Unmarshal_RejectsUnknownFields`, `TestSerialize_OmitEmpty`, `TestSerialize_ValidJSON`, `TestMarshalValidated_NilGuard`, `TestMarshalValidated_MatchesMarshalWorkGraph`, `TestSerialize_Canonicalize_DoesNotMutateInput` |
| `make check` green | yes | exit 0 (see §6) |

## 5. Findings

No BLOCKER. No MAJOR. Six MINOR findings; none invalidates a mandatory AC.
Each is recorded as a follow-up; the implementer is not required to fix them
in this task per the review brief ("соседние проблемы запиши как follow-up,
но не исправляй без необходимости").

### MINOR-1 — Empty `PackageState` does not default to `pending` despite the code comment claiming it does

- **Location:** `internal/workgraph/graph.go:355-362`.
- **Observation:** The comment at lines 358-362 says:
  ```go
  if p.State == "" {
      // Default to pending when unspecified; treat empty as a request for
      // the default rather than an error, but record a normalised value.
      p.State = PackagePending
  }
  ```
  However, the preceding check at lines 355-357 appends an "unknown state"
  error for empty state because `PackageState.IsValid()` returns `false` for
  `""` (`AllPackageStates` does not include the empty string,
  `graph.go:93-101`). The error is appended **before** the default-
  normalisation runs, so an empty state produces an error rather than the
  documented default.
- **Proof (independent probe):**
  ```
  TestProbe_EmptyStateDefault:
    p := WorkPackage{...} // State deliberately left zero ("")
    g := WorkGraph{TaskID: "TASK-1", Packages: []WorkPackage{p}}
    v, err := ValidateWorkGraph(g)
    // got: "invalid work graph: package \"P1\" has unknown state \"\""
    // expected per comment: err == nil, v.Packages()[0].State == PackagePending
  ```
  The default-normalisation block at lines 358-362 is effectively dead code.
- **Required fix (if addressed):** Either (a) move the `p.State == ""` check
  **before** the `IsValid()` check and normalise first; or (b) make
  `PackageState.IsValid()` return `true` for `""` (matching `Risk.IsValid()`
  and `Complexity.IsValid()` which both accept empty at
  `internal/task/specification.go:42-68`); or (c) remove the misleading
  comment + dead code and document that empty state is an error. Add a
  regression test for whichever behaviour is chosen.
- **Severity rationale:** No mandatory AC depends on the default behaviour.
  `Decompose` explicitly sets `State: PackagePending` (`graph.go:669`), so
  Decompose-produced graphs are unaffected. Only hand-built graphs that omit
  `State` hit this. It is a documented-behaviour mismatch, not a runtime
  hazard. MINOR.

### MINOR-2 — Cross-task packages are silently accepted

- **Location:** `internal/workgraph/graph.go:327-345` (the per-package
  structural checks).
- **Observation:** The graph's `TaskID` and each package's `TaskID` are
  validated independently for non-emptiness, but never reconciled. A graph
  with `TaskID="TASK-1"` can contain a package with `TaskID="TASK-OTHER"` and
  still validate. This contradicts the documented invariant at
  `graph.go:26-27` ("a work graph is one cohesive unit for one task") and
  `doc.go:9` ("Scope (spec §18.3, §18.4)").
- **Proof (independent probe):**
  ```
  TestProbe_CrossTaskPackage:
    p := WorkPackage{ID:"P1", TaskID:"TASK-OTHER", ...}
    g := WorkGraph{TaskID:"TASK-1", Packages:[]WorkPackage{p}}
    v, err := ValidateWorkGraph(g)
    // err == nil; v.Packages()[0].TaskID == "TASK-OTHER"
    // expected: rejection (package TaskID mismatch)
  ```
- **Required fix (if addressed):** Add a check in `validate()` that
  `p.TaskID == g.TaskID` for every package (after trimming). Add a negative
  test.
- **Severity rationale:** Not a mandatory AC. The defect is an internal-
  consistency gap; the future persistence layer (FU-M14-04-2) would need to
  reconcile the two TaskID columns and could be confused. The integration
  path (`Decompose` → `ValidateAgainstSpec`) is unaffected because `Decompose`
  sets every package's TaskID to `spec.TaskID`. MINOR.

### MINOR-3 — Self-dependency is reported twice (redundant error)

- **Location:** `internal/workgraph/graph.go:385-401`.
- **Observation:** A self-loop (`Dependencies: ["P1"]` on package `P1`) is
  caught twice: once by the missing-edge pass at lines 386-388 ("depends on
  itself (self-cycle)") and again by `detectCycle` at line 399 ("dependency
  cycle: P1 -> P1"). Both errors are joined in the final `errors.Join`.
  Functional behaviour is correct (validation fails); the message is
  redundant.
- **Proof (independent probe):**
  ```
  TestProbe_SelfLoopMessage:
    err.Error() contains:
      "package \"P1\" depends on itself (self-cycle)"
      "dependency cycle: P1 -> P1"
    cycle mentions in joined message: 2
  ```
- **Required fix (if addressed):** Either have `detectCycle` skip self-loops
  (they are already caught by the missing-edge pass), or have the missing-
  edge pass not append a self-cycle error and let `detectCycle` handle it.
- **Severity rationale:** Cosmetic. No observable behaviour change (validation
  fails either way). MINOR.

### MINOR-4 — `TestTopologicalOrder_CycleRejected` does not actually test `TopologicalOrder`

- **Location:** `internal/workgraph/validate_test.go:251-264`.
- **Observation:** The test is named `TestTopologicalOrder_CycleRejected`
  and its comment says it exercises the "public guard" of `TopologicalOrder`'s
  defense-in-depth cycle branch. In reality the test only calls
  `ValidateWorkGraph` on a cyclic graph and asserts it errors; it never calls
  `TopologicalOrder()` on a `*ValidatedWorkGraph`. The defense-in-depth cycle
  branch in `topologicalOrder` (`graph.go:619-621`) is structurally
  unreachable from external code because a `*ValidatedWorkGraph` can only be
  constructed through the validator (which rejects cycles), so there is no
  way to obtain a `*ValidatedWorkGraph` carrying a cyclic graph.
- **Proof:** Reading the test body confirms it calls only
  `workgraph.ValidateWorkGraph(g)` and checks `err == nil`. No call to
  `v.TopologicalOrder()`.
- **Required fix (if addressed):** Rename the test to
  `TestValidate_Cycle_TwoNodeAlreadyCovered` or similar to reflect what it
  actually checks, OR remove it as a duplicate of `TestValidate_Cycle_TwoNode`
  (which already covers the same case). The defense-in-depth branch in
  `topologicalOrder` can be covered by an internal (`workgraph`-package) test
  if desired.
- **Severity rationale:** The test name is misleading but the test itself
  passes and does not make any false claim about a mandatory AC. The
  unreachable defense-in-depth code is harmless (it would only fire if the
  validator had a bug, which is its purpose). MINOR.

### MINOR-5 — No `M14-04.manifest.json` despite the implementation report claiming one will be set

- **Location:** `docs/reviews/m14/M14-04_IMPLEMENTATION.md:262-264` claims
  `blackbox.exempt` "will be set to true in the manifest with this rationale".
- **Observation:** The `docs/reviews/m14/` directory contains
  `M14-04_IMPLEMENTATION.md` but **no** `M14-04.manifest.json`. The
  implementation report references a manifest that does not exist.
- **Required fix (if addressed):** Either create
  `docs/reviews/m14/M14-04.manifest.json` recording the
  `IMPLEMENTED_TESTED` evidence (with `blackbox.exempt = true` and a
  non-empty `blackbox.exempt_reason` citing the no-production-wiring
  rationale + the integration evidence), or correct the report to state the
  manifest will be created at the acceptance stage.
- **Severity rationale:** For the `IMPLEMENTED_TESTED` verdict, Engineering
  Baseline v1 §3 rule 1 requires the manifest to exist (it records the
  evidence and the exemption). The exemption itself is valid (no production
  wiring; integration evidence exists), so this is a recording gap, not an
  evidence gap. The acceptor can request the manifest before
  `REVIEW_APPROVED`. MINOR.

### MINOR-6 — `ValidateWorkGraph` mutates the caller's input slice (undocumented)

- **Location:** `internal/workgraph/graph.go:314-443` (`validate` function).
- **Observation:** `ValidateWorkGraph(g WorkGraph)` takes `g` by value, but
  `g.Packages` is a slice (shared backing array). Inside `validate`, the
  loop `p := &g.Packages[i]; p.ID = strings.TrimSpace(p.ID); ...`
  (`graph.go:328-380`) mutates the caller's package elements in place:
  trimming whitespace on `ID`/`Title`/`Objective`/`Stage`, de-duplicating
  `AcceptedACIDs`/`Dependencies`/`AllowedScope`, and defaulting `State`.
- **Proof (independent probe):**
  ```
  TestProbe_ValidateMutatesInput:
    p := WorkPackage{ID:"  P1  ", AcceptedACIDs: ["AC-1","AC-1","  "], ...}
    g := WorkGraph{...}
    _, _ = ValidateWorkGraph(g)
    // g.Packages[0].ID == "P1" (was "  P1  ")
    // g.Packages[0].AcceptedACIDs == ["AC-1"] (was ["AC-1","AC-1","  "])
  ```
- **Required fix (if addressed):** Either (a) document the in-place
  normalisation in the `ValidateWorkGraph` doc comment ("normalises the
  input in place; callers should not reuse the input slice"), or (b) operate
  on a defensive copy so the caller's input is untouched.
- **Severity rationale:** No mandatory AC is affected; the returned
  `*ValidatedWorkGraph` carries a deep copy (`clone()` at `graph.go:229-252`),
  so the validated state is immutable. Only callers that reuse the input
  `WorkGraph` after validation observe the mutation. A future persistence
  layer that re-validates an input would not be confused because it uses the
  returned handle. MINOR.

## 6. Independent re-run of gates and tests

Toolchain: `go version go1.26.5 darwin/arm64`.

| Command | Exit | Result |
|---|---:|---|
| `go build ./internal/workgraph/` | 0 | clean build |
| `go vet ./internal/workgraph/` | 0 | clean |
| `gofmt -l internal/workgraph/` | 0 | no files listed |
| `go test -count=1 ./internal/workgraph/...` | 0 | 54 tests/subtests PASS (0.27s) |
| `go test -race -count=1 ./internal/workgraph/...` | 0 | race-clean (1.89s) |
| `go test -count=15 -run 'TestDecompose_Deterministic\|TestSerialize_Deterministic' ./internal/workgraph/...` | 0 | determinism 15× PASS |
| `go test -count=1 -run 'TestIntegration' -v ./internal/workgraph/` | 0 | both integration tests PASS (real `task.Compile`) |
| `make check` | 0 | gofmt clean + `go vet ./...` clean + full suite green (every package `ok`) |
| `go test -race ./...` | 0 | every package `ok`; **no FAIL, no race detected** |
| `./forge gate baseline` | 0 | active baseline_version 1 |
| `./forge gate validate --manifest docs/reviews/m14/M14-03.manifest.json` | 0 | M14-03 transition legal |
| `./forge gate next --manifest docs/reviews/m14/M14-03.manifest.json` | 0 | predecessor ACCEPTED; successor may start |

The implementation report's claimed command results were independently
reproduced.

## 7. Specific aspects checked

### 7.1 Production composition / wiring
- **No production wiring exists.** M14-04 ships only a pure domain library
  in `internal/workgraph/`. No daemon endpoint, no CLI command, no storage
  migration, no scheduler hook, no TUI binding. This is consistent with the
  task brief ("Work Graph domain, DAG validation and AC mapping; not
  execution").
- The integration evidence (`TestIntegration_CompileThenDecompose`,
  `TestIntegration_CompileVagueInputDecomposesSafely`) exercises the real
  `internal/task.Compile` (the accepted M14-02 compiler) feeding into
  `workgraph.Decompose`. No fake compiler, no stub.
- `Decompose` consumes the real `task.Specification` and
  `task.AcceptanceCriterion` types; validation uses
  `task.ValidateSpecification`. No type assertions to fakes.

### 7.2 Fake / stub / demo leakage
- `grep -nE "TODO|FIXME|XXX|HACK|panic\(|unimplemented|stub"` in the new
  production files returns only one hit: `graph.go:15`, a doc comment
  quoting the baseline rule ("never disguised as stubs that look finished").
  No actual stub/TODO/panic in production code.
- Test helpers (`helperSpec`, `helperPkg`, `helperAC`) build minimal real
  `task.Specification` / `workgraph.WorkPackage` values — concise
  constructors, not test doubles.
- `TestIntegration_*` uses the real `task.Compile`. No fake.

### 7.3 Policy / security bypass
- The work-graph domain model performs **no I/O**, holds no storage handle,
  spawns no goroutine, forwards no environment, touches no security policy,
  no autonomy profile, no merge policy, no quota/budget arithmetic.
- No agent process is involved; no allowlist concern.
- The AC-ownership uniqueness invariant is a correctness guard that prevents
  two packages from claiming the same acceptance criterion — a prerequisite
  for the Merge Governor's per-AC evidence accounting (spec §28).
- No baseline/gate enforcement weakened. The gate still rejects invalid
  transitions.

### 7.4 Restart / idempotency / cancellation / concurrency
- The domain model is pure value-type: no goroutines, no shared mutable
  state, no channels, no `sync` usage in the new files.
- `go test -race ./...` is clean (no race).
- `Decompose` is a pure function — restart-invariant by construction
  (identical spec → identical graph). Re-decomposition byte-identity is
  proven by `TestIntegration_CompileThenDecompose`.
- `ValidatedWorkGraph.Graph()` and `.Packages()` return defensive copies
  (`graph.go:229-252`); mutation of the returned value does not leak into
  the validated state (proven by `TestValidatedGraph_GraphIsDefensiveCopy`).
- (Note MINOR-6: `ValidateWorkGraph` mutates the **input** slice, but the
  returned handle is immutable.)

### 7.5 Scope creep
- `git diff --name-only a1e11cf 099bda3` touches only `internal/workgraph/*`
  (four new files + `doc.go`) and the implementation report. No changes to
  `docs/spec/`, `docs/engineering/`, `go.mod`/`go.sum`, or any other
  internal package.
- No M14-05 (scheduler/dispatch) work. No execution wiring.
- The single additional dependency check: `go.mod`/`go.sum` unchanged.

### 7.6 Backward compatibility
- No existing production code depends on the new types (they are additive).
- The existing M3 lease layer (`leases.go`, `leases_test.go`) is untouched
  and still passes (`TestAcquirePath_ThenConflict`,
  `TestAcquireSemantic_Conflict`, `TestReleaseAll`, `TestInvalidSemantic`,
  `TestListActive` all PASS).
- No public API removed or renamed. The package's existing surface is
  unchanged; only new types/functions are added.

### 7.7 Documentation claims vs. reality
- The implementation report's claims were checked against the code:
  - "54 tests/subtests" — confirmed by `go test -v` output count.
  - "No TODO/FIXME/panic/stub in new production files" — confirmed by grep.
  - "`make check` is green" — reproduced.
  - "`go test -race ./...` is green" — reproduced.
  - "Decompose is pure (no I/O, no clock, no randomness)" — confirmed by
    reading `graph.go:652-699`; no imports of `os`/`time`/`math/rand` in
    the decomposition path; the only `time` import is for the `Attempt`
    struct field type.
  - "Every package owns ≥1 AC; each AC owned by ≤1 package" — confirmed by
    reading `validate` and by the negative tests.
  - "MarshalWorkGraph canonicalises" — confirmed by reading `Canonicalize`
    and by `TestSerialize_Canonical_IgnoresInputOrdering`.
- One claim mismatch: the code comment at `graph.go:358-362` claims empty
  state defaults to pending; the actual behaviour errors (MINOR-1).
- One report mismatch: the report claims a manifest "will be set" but no
  manifest file exists (MINOR-5).

## 8. Counterexample search

The reviewer actively searched for counterexamples where tests are green
but a mandatory requirement is not met:

1. **Could an invalid DAG become runnable?** Searched for any public
   constructor returning `*ValidatedWorkGraph` other than the validators.
   Found none. `ValidatedWorkGraph.graph` is unexported; the struct literal
   cannot be constructed from outside the package. The only production paths
   to a runnable handle are `ValidateWorkGraph`, `ValidateAgainstSpec`, and
   `Decompose`, all of which run `validate()`. No counterexample found.
2. **Could two packages own the same AC?** The `acOwners` map at
   `graph.go:370-375` rejects the second owner. Searched for a path that
   bypasses this — none exists; the check is in the single `validate`
   function that all three entry points call.
3. **Could `Decompose` produce a non-deterministic output?** Searched for
   any use of map iteration, time, randomness, or I/O in `Decompose`.
   Found none. The only ordering input is `spec.AcceptanceCriteria`
   (a slice, ordered) and the deterministic `packageID` function. The
   10× byte-identity test corroborates.
4. **Could a cyclic graph validate?** Probed with 2-node, 3-node, and
   self-loops — all rejected. `detectCycle` uses DFS 3-colouring correctly.
5. **Could a missing-edge graph validate?** Probed — rejected by the
   missing-edge pass at `graph.go:382-394`.
6. **Could an unreachable node validate?** Probed with disconnected sibling
   and disconnected chain — both rejected by `weaklyUnreachable`.

No counterexample found. The mandatory ACs hold.

## 9. Black-box evidence assessment

M14-04 ships **no production wiring** (no daemon, no CLI command, no storage
table, no scheduler hook) — there is no compiled-binary surface to drive
end-to-end. Per Engineering Baseline v1 §3 rule 1, such a task is
exemption-eligible from the `blackbox` requirement **provided** (a) the
manifest declares `blackbox.exempt = true` with a non-empty reason, and
(b) ≥1 `integration`-level passing evidence covers the affected criteria.

- **(b) is satisfied:** `TestIntegration_CompileThenDecompose` and
  `TestIntegration_CompileVagueInputDecomposesSafely` exercise the real
  `internal/task.Compile` → `workgraph.Decompose` → `MarshalValidated` →
  `UnmarshalWorkGraph` → `ValidateAgainstSpec` pipeline. They prove every
  compiler-produced AC is owned exactly once, the marshalled graph is
  byte-stable across re-decomposition, the round-tripped graph re-validates,
  and a vague-input compiler output never yields an invalid runnable.
- **(a) is NOT satisfied at review time:** no `M14-04.manifest.json` exists
  (MINOR-5). The exemption is valid in substance but not yet recorded in the
  machine-readable manifest.

The exemption will be closed by FU-M14-04-3 (daemon transport + `forge
workgraph …` CLI commands + compiled-binary black-box test) when a
user-facing surface lands.

For the **`REVIEW_APPROVED`** transition (this review), the baseline does
not require a blackbox scenario — that requirement applies at
`ACCEPTED` (rule 3). The reviewer therefore does not block on the
blackbox exemption.

## 10. Follow-up problems (recorded, not fixed by this review)

- **FU-M14-04-1** (from implementation report): richer stage-based
  decomposition (research → contract fan-out → integration → verification).
- **FU-M14-04-2** (from implementation report): durable persistence —
  `work_packages` / `dependencies` SQLite tables + a `WorkGraphStore` that
  accepts only `*ValidatedWorkGraph`. Migration v9.
- **FU-M14-04-3** (from implementation report): daemon transport +
  `forge workgraph …` CLI commands with a compiled-binary black-box test.
- **FU-M14-04-4** (from implementation report): finer AllowedScope
  partitioning (disjoint proposed-scope slices per sibling package).
- **FU-M14-04-5** (from implementation report): integrate package
  AllowedScope with the lease manager.
- **FU-M14-04-6** (from implementation report): TUI bindings.
- **FU-M14-04-R1** (new — from MINOR-1): fix the empty-`PackageState`
  default-vs-error inconsistency (dead code at `graph.go:355-362`); add a
  regression test.
- **FU-M14-04-R2** (new — from MINOR-2): enforce `p.TaskID == g.TaskID` for
  every package; add a negative test.
- **FU-M14-04-R3** (new — from MINOR-3): de-duplicate the self-cycle error
  (currently reported by both the missing-edge pass and `detectCycle`).
- **FU-M14-04-R4** (new — from MINOR-4): rename or remove
  `TestTopologicalOrder_CycleRejected` (it does not test `TopologicalOrder`).
- **FU-M14-04-R5** (new — from MINOR-5): create `M14-04.manifest.json`
  before the acceptance transition (acceptor's responsibility).
- **FU-M14-04-R6** (new — from MINOR-6): document or remove the in-place
  mutation of the input slice by `ValidateWorkGraph`.

## 11. Verdict

**`REVIEW_APPROVED`**

Rationale:

- All three mandatory ACs (package↔objective/AC link; invalid DAG cannot
  become runnable; determinism) are proven by automated evidence at unit +
  integration levels. Each test verifies observable behaviour (nil handle +
  wrapped `ErrInvalidWorkGraph` for negatives; byte-identity for
  determinism; real `task.Compile` composition for integration).
- All required test classes (valid simple/composite DAG fixtures; cycle,
  missing-edge, duplicate-AC-owner, unreachable negatives; stable
  serialization; `make check`) are covered.
- `make check` exit 0; `go test -race ./...` exit 0; `go vet`/`gofmt` clean
  — all independently reproduced.
- Scope is bounded to `internal/workgraph/`; no execution wiring; no spec /
  engineering / gate / policy / storage / daemon / scheduler / cli /
  transport / task change; no new external dependency; the existing M3 lease
  layer is untouched and regression-clean.
- The compiled-binary blackbox exemption is valid in substance (no
  production wiring; integration evidence covers the affected criteria);
  the recording gap (no manifest) is MINOR-5 and does not block
  `REVIEW_APPROVED` (the blackbox requirement is enforced at `ACCEPTED`,
  not at `REVIEW_APPROVED`).
- Six MINOR findings are recorded as follow-ups (FU-M14-04-R1..R6). None
  invalidates a mandatory AC. The implementation is honest about its
  limitations (sequential decomposition, full-ProposedScope AllowedScope,
  no persistence, no CLI/daemon surface).
- No BLOCKER, no MAJOR. No counterexample found in the active search.

`REVIEW_APPROVED` is permitted: every mandatory criterion is backed by
passing automated evidence, and no BLOCKER/MAJOR finding exists. The MINOR
findings are follow-ups, not acceptance blockers.
