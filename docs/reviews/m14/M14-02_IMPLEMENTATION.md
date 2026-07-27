# M14-02 — Implementation Report

**Task:** M14-02 — Deterministic Task Compiler.
**Implementer actor:** `M14-02-impl-session` (initial), `M14-02-remediation-session` (review remediation).
**Verdict:** `IMPLEMENTED_TESTED`

## SHAs

- **Starting SHA:** `f894f648fb5545b89cb5e797e6d146a35d2c379a` (branch `main`, the
  M14-01 acceptance commit `M14-01: add acceptance evidence`).
- **Initial candidate SHA:** `ee738b2edd9138e7e2ef33eb18ff05fc6e95b43d`
  (`M14-02: deterministic task compiler + forge spec compile`).
- **Review SHA:** `92dd82661f4a4f2cbf44aba83813fee4ff0cd405`
  (`M14-02: independent review (CHANGES_REQUESTED, MAJOR-1 CLI attachment gap)`).
- **Remediation candidate SHA:** produced by the local commit
  `M14-02: address review findings` at the end of this task (resolve with
  `git rev-parse HEAD` after the commit lands).

## Preconditions verified

- Previous M14 task (`M14-01`) verdict is `ACCEPTED` (acceptance report at
  `docs/reviews/m14/M14-01_ACCEPTANCE.md`; manifest
  `docs/reviews/m14/M14-01.manifest.json`).
- Compiled-binary gate check (the enforcement point M14-00 put in place):

  ```sh
  $ /tmp/forge_m14_02 gate baseline
  active schema_version: 1
  active baseline_version: 1
  baseline document: docs/engineering/ENGINEERING_BASELINE.md

  $ /tmp/forge_m14_02 gate next --manifest docs/reviews/m14/M14-01.manifest.json
  OK: predecessor "M14-01" is ACCEPTED; successor task may start     # exit 0

  $ /tmp/forge_m14_02 gate validate --manifest docs/reviews/m14/M14-01.manifest.json
  OK: task "M14-01" transition REVIEW_APPROVED -> ACCEPTED is legal under baseline v1
  ```

  The gate is open. M14-02 may start.
- Working tree at the starting SHA contained only pre-existing unrelated review
  docs (`docs/reviews/MINIMAL_RUN_*`, `docs/reviews/M12_M13_REVIEW.md`); they
  predate this task and are **not** touched (identical situation to M14-00 and
  M14-01).

## Goal and actual scope

Transform free-form task text + metadata into a structured `Specification`
(the durable model added by M14-01) via a **deterministic** compiler:
extract objective, ACs, non-goals, assumptions, constraints, risk and
complexity; emit explicit confidence + uncertainty reasons; classifier cascade
without an external model call as a hard dependency; safe-assumption vs.
needs-clarification rules; attachment metadata as input only; never mutate a
locked specification; identical input ⇒ identical output (spec §18.1, §18.2,
§9, §26, §27, §28).

**Scope delivered:**

1. **Pure compiler** (`internal/task/compiler.go`): `Compile(CompileInput)
   (CompileResult, error)`. Performs no I/O (no daemon, no storage, no clock,
   no external model call) so identical input deterministically produces
   identical output. Returns `Specification{Version:0, Locked:false}`; the
   caller assigns the version via `SpecificationStore.Save`.
2. **Structured-section parser**: case-insensitive headers (`Objective:`,
   `Acceptance Criteria:`, `Non-goals:`, `Assumptions:`, `Constraints:`,
   `Scope:`); supports both inline bodies (`Objective: Build X.`) and
   multi-line bullet/numbered lists (`-`, `*`, `•`, `1.`). CRLF/CR-tolerant.
3. **Classifier cascade (no external model call)**:
   - **Risk:** reuses the accepted `internal/risk.Classify` (M6-3) over
     description + attachment filenames; structural flags (TouchesAuth,
     TouchesPayments, …) drive the §26 taxonomy deterministically. The
     `risk.Level` is translated into the M14-01 `task.Risk` type.
   - **Complexity:** a local deterministic classifier emits
     `task.Complexity` (C0..C3, the M14-01 band set). Risky topics floor
     at C2; architectural/migration roles push upward.
4. **Confidence + clarifications (§9.7 rules):**
   - `HIGH` only when both Objective and ACs are given explicitly.
   - `MEDIUM` when safe assumptions are made (synthesised default AC) and
     input is otherwise actionable.
   - `LOW` when input is too vague, attachment-only (content not read), or
     shorter than 16 characters.
   - A `Clarification` is emitted only when a safe reversible assumption is
     impossible (R4 security/money/destructive topics; attachment-only;
     vague input). This is the §9.7 rule encoded.
5. **Attachment metadata as input only (no design orchestration):**
   - `CompileResult.AttachmentRoles` mirrors the supplied hash → role map.
   - `VisualRequirements.Required = true` iff a `DESIGN_REFERENCE` or
     `BUG_SCREENSHOT` attachment is present; references list the hashes.
   - The compiler never reads attachment content; an attachment-only task
     emits `Confidence=LOW` + a clarification by design.
6. **Compiler never mutates a locked specification:** the compiler is pure
   (cannot reach storage by construction) and always returns
   `Specification{Version:0, Locked:false}`. Persistence is the caller's
   responsibility; `SpecificationStore.Save` (M14-01) enforces
   `ErrSpecificationLocked` on a locked version. End-to-end proof is in
   `TestCompile_LockedSpecCannotBeMutatedViaSave`.
7. **Production wiring** (`forge spec compile`, `internal/cli/spec_cmd.go`):
   the compiler is reachable through the compiled `forge` binary via the
   real CLI dispatch (`internal/cli/cli.go`). A `--json` document carries
   the full `CompileResult`; `--project/--task/--title/--priority/--attach
   hash=ROLE` flags mirror the `task add` UX. No daemon is required (the
   compiler is pure).

**Out of scope (explicit follow-ups, not started):**

- `forge spec {create,get,lock,versions}` over the daemon transport (CRUD
  surface) — depends on a transport endpoint that does not yet exist (M14-01
  follow-up #2).
- Cross-version acceptance-criterion identity tracking (M14-01 follow-up #3).
- Wiring `Compile` into `forge task add` so every new task is compiled
  automatically (changes existing user flow; not required by the ACs).
- Content-aware attachment parsing (the compiler deliberately does not read
  attachment content; it consumes metadata only — spec §9.4).
- ADR for the package-boundary decision (compiler lives in `internal/task`
  next to the `Specification` model it produces, mirroring the existing
  `task.Task`/`storage.Task` split — same precedent as M14-01).

The product spec (`docs/spec/NEUROFORGE_SPEC.md`) was **not** modified
(baseline rule 3). No security/autonomy/delivery/merge-policy invariant was
weakened.

## Changed files

Initial implementation (commit `ee738b2`):

```
internal/task/compiler.go                               | new (pure compiler + parser + classifier cascade)
internal/task/compiler_test.go                          | new (fixtures, determinism, regressions, locked-spec)
internal/cli/spec_cmd.go                                | new (forge spec compile command + flag parsing)
internal/cli/spec_compile_blackbox_test.go              | new (black-box tests through the compiled binary)
internal/cli/cli.go                                     | modified (2 lines: dispatch case for "spec")
internal/cli/help.go                                    | modified (spec compile in help)
docs/reviews/m14/M14-02_IMPLEMENTATION.md               | rewritten (this report; replaces the prior BLOCKED report)
```

Review remediation (commit `M14-02: address review findings`):

```
internal/cli/spec_cmd.go                                | modified (MAJOR-1 + MINOR-2/3/5)
internal/cli/spec_cmd_test.go                           | new (MAJOR-1 unit-level regression for splitAttachFlag)
internal/cli/spec_compile_blackbox_test.go              | extended (MAJOR-1 + MINOR-1/3 binary regressions)
internal/cli/help.go                                    | modified (MAJOR-1 grammar in help text)
internal/task/compiler.go                               | modified (MAJOR-1 defensive empty-filename handling)
internal/task/compiler_test.go                          | modified (MINOR-4 in-order ComplexityReasons; MAJOR-1 regression)
docs/reviews/m14/M14-02_IMPLEMENTATION.md               | this section
```

`git diff --stat f894f648..HEAD` will show only the above plus the
pre-existing unrelated working-tree entries (`docs/reviews/MINIMAL_RUN_*`,
`docs/reviews/M12_M13_REVIEW.md`) that were already present at the starting
SHA and are intentionally left untouched (same position as M14-00 / M14-01).

## Review remediation

The independent review (`docs/reviews/m14/M14-02_REVIEW.md`,
`CHANGES_REQUESTED`) raised one MAJOR and five MINOR findings. Every finding
is addressed below; each fix has a regression test that would fail if the
defect returned.

| Finding | Fix | Regression test(s) |
|---|---|---|
| **MAJOR-1** CLI `--attach` cannot carry Filename/MIME/Size; attachment-only tasks via CLI produce a degenerate objective with empty parens `()`. The implementation report did not acknowledge the gap. | Extended the `--attach` grammar to `hash=ROLE[:filename[:mimeType[:size]]]` (`internal/cli/spec_cmd.go:splitAttachFlag`); the legacy `hash=ROLE` form remains valid (backward compatible). The compiler's `synthesiseObjectiveFromAttachments` is now defensive: an empty filename omits the trailing `(...)` clause instead of emitting `()`. The CLI supplies the full metadata the compiler is documented to consume (spec §9.5). | `TestSplitAttachFlag_Grammar` (14 table cases, unit-level parsing); `TestCompile_Regression_AttachmentOnlyEmptyFilename` (compiler-level: no `()` even with empty filename); `TestSpecCompile_BlackBox_AttachmentOnlyWithFilename` (binary: filename appears in objective, LOW + clarification); `TestSpecCompile_BlackBox_AttachmentOnlyLegacyHashRole` (binary: legacy form still works, no `()`); `TestSpecCompile_BlackBox_InvalidAttachSizeRejected` (binary: non-numeric size rejected); `TestSpecCompile_BlackBox_InvalidAttachRoleRejected` (binary: unknown role rejected). |
| **MINOR-1** Text output path (`--json=false`) had no test coverage. | Added a black-box test that drives the default text formatter through the compiled binary and asserts the presence of TaskID, Objective, AC IDs (`AC-1`/`AC-2`), and Confidence. | `TestSpecCompile_BlackBox_TextOutput` (binary: default output is text, contains all required fields). |
| **MINOR-2** `--json` defaulted to `true`, inconsistent with every other `forge` command. | Aligned with the CLI convention: `--json` now defaults to `false` (text is the default; `--json` is opt-in, matching `task add`, `project list`, `daemon status`, etc.). Help text documents the opt-in. Existing JSON black-box tests were updated to pass `--json` explicitly. | `TestSpecCompile_BlackBox_TextOutput` (binary: default output is text, not JSON); existing JSON tests now explicitly pass `--json` (`TestSpecCompile_BlackBox_DeterministicOutput`, etc.). |
| **MINOR-3** `--priority` was not validated against known values (`BOGUS` was silently accepted). | Added validation at the CLI surface: `--priority` must be one of `LOW|NORMAL|HIGH|URGENT`; an unknown value exits non-zero with a clear error echoing the bad value, matching the `--attach` role-validation behaviour. | `TestSpecCompile_BlackBox_InvalidPriorityRejected` (binary: `--priority BOGUS` exits non-zero, stderr names `--priority` and `BOGUS`). |
| **MINOR-4** Determinism test sorted `ComplexityReasons` before comparing, weakening the assertion. | `sameCompileResult` now compares `ComplexityReasons` in order (no sort), matching the treatment of `RiskReasons` and `UncertaintyReasons`. The unused `sort` import was removed. | `TestCompile_Deterministic` (now asserts in-order `ComplexityReasons` equality; a non-deterministic reason order would fail). |
| **MINOR-5** Dead code: `_ = i` in `writeSpecCompileText`. | Replaced `for i, ac := range ...` with `for _, ac := range ...`; the `_ = i` line is gone. | `go vet ./...` clean; `TestSpecCompile_BlackBox_TextOutput` exercises the rewritten loop through the binary. |

No MAJOR or MINOR finding is left unaddressed.

## Acceptance criterion → code → test

| Mandatory AC (task brief) | Where implemented | Test(s) | Why the test verifies observable behaviour |
|---|---|---|---|
| **AC1** Compiler creates a complete valid specification for typical tasks | `Compile` produces a fully-populated `Specification` (`internal/task/compiler.go`); structured-section parser handles Objective/ACs/Non-goals/Assumptions/Constraints/Scope; risk classifier reuses `risk.Classify`; complexity classifier emits `task.Complexity`; `deriveVisualRequirements` reflects attachment roles | `TestCompile_Fixture_Feature` (explicit sections → every field populated, HIGH confidence); `TestCompile_Fixture_Bugfix` (free-form bug report → synthesised objective + AC, valid spec); `TestCompile_Fixture_UITask` (UI task with DESIGN_REFERENCE attachment → `VisualRequirements.Required=true`); `TestCompile_Fixture_AuthPaymentRisky` (R4 task with valid spec); `TestCompile_RiskLevels` (every R0..R4 band); `TestCompile_ACsHaveStableIDs` (deterministic AC-1..N); `TestCompile_SynthesisesACWhenAbsent` (MEDIUM confidence + synthesised AC) | Each fixture compiles to a specification that is then re-validated with `ValidateSpecification` (`mustValidateSpec`) — a green test means the compiled spec is structurally valid through the **production** validator (M14-01). The bugfix fixture is the spec §9.1 example verbatim. |
| **AC2** Unsafe ambiguities are explicitly flagged | `deriveConfidenceAndClarifications` emits `Clarification` records when a safe reversible assumption is impossible (§9.7); `Confidence=LOW` for vague/attachment-only input; R4 surfaces a Clarification even with structured sections | `TestCompile_Fixture_VagueTask` (LOW confidence + ≥1 clarification); `TestCompile_Fixture_AttachmentOnly` (LOW confidence + clarification, content not read); `TestCompile_Fixture_AuthPaymentRisky` (R4 task must surface ≥1 clarification); `TestCompile_RejectsEmptyInput` (empty input → LOW + clarification, spec fails ValidateSpecification so caller cannot persist it); **black-box**: `TestSpecCompile_BlackBox_VagueInputLowConfidence`, `TestSpecCompile_BlackBox_RiskyTaskFlagsClarification` | Tests assert both the *presence* of a Clarification and the *reason text*. The black-box tests observe the same through the compiled `forge` binary's JSON output. |
| **AC3** Identical input ⇒ deterministic output | `Compile` is pure: no I/O, no clock, no randomness, no map iteration affecting output (lists are ordered; `sort.Strings` is used in the comparison helper for the determinism test only, not in production). | `TestCompile_Deterministic` (two `Compile` calls produce identical `CompileResult`); `TestCompile_Deterministic_AcrossVaryingWhitespace` (CRLF vs LF → identical objective + AC statements); `TestCompile_ACsHaveStableIDs` (AC-1..N IDs are stable across calls); **black-box**: `TestSpecCompile_BlackBox_DeterministicOutput` (two binary invocations produce byte-identical JSON) | Determinism is asserted at the value level (objective, AC IDs, risk, complexity, clarifications, attachment roles, visual requirements) — a single differing byte fails the test. The CRLF test guards against line-ending parser drift. The black-box test is the strongest possible end-to-end determinism proof (same binary, same input → byte-identical stdout). |
| **AC4** Compiler never mutates a locked specification | `Compile` returns `Specification{Version:0, Locked:false}`; the compiler has no storage handle by construction (purity). The caller's `SpecificationStore.Save` (M14-01) enforces `ErrSpecificationLocked` on a locked version. | `TestCompile_NeverMutatesLockedSpec` (freshly-compiled spec is never locked and never carries a version); `TestCompile_LockedSpecCannotBeMutatedViaSave` (compile → save → lock → compile+save-again leaves the locked v1 byte-identical and allocates a fresh v2 instead) | The end-to-end test is the headline proof: it exercises the full compiler + storage path and asserts both (a) the locked content is unchanged (`Objective == "Original."`, `LockedBy == "reviewer-1"`) and (b) the new compilation produces a new version. The test is the AC's exact wording turned into an assertion. |

### Required test classes (task brief) — coverage matrix

| Required class | Tests |
|---|---|
| Fixtures: bugfix / feature / UI / auth-payment risky / vague / attachment-only | `TestCompile_Fixture_Bugfix`, `TestCompile_Fixture_Feature`, `TestCompile_Fixture_UITask`, `TestCompile_Fixture_AuthPaymentRisky`, `TestCompile_Fixture_VagueTask`, `TestCompile_Fixture_AttachmentOnly` |
| Positive/negative confidence and clarification decisions | `TestCompile_Fixture_Feature` (HIGH, no clarifications); `TestCompile_SynthesisesACWhenAbsent` (MEDIUM, safe assumption); `TestCompile_Fixture_VagueTask` (LOW + clarification); `TestCompile_Fixture_AuthPaymentRisky` (R4 clarification); `TestCompile_RejectsEmptyInput` (LOW + spec invalid) |
| Stable output for identical input | `TestCompile_Deterministic`, `TestCompile_Deterministic_AcrossVaryingWhitespace`, `TestCompile_ACsHaveStableIDs` |
| Regression tests on found parser defects | `TestCompile_Regression_TrailingSpacesInHeader`, `TestCompile_Regression_CaseInsensitiveHeaders`, `TestCompile_Regression_NumberedListACs`, `TestCompile_Regression_SectionHeaderAtEOF`, `TestCompile_Regression_AttachmentRoleMapping` |
| `make check` | exit 0 — verified below |

## Exact commands and results

```sh
# Targeted tests (the touched packages).
go test -count=1 -run 'TestCompile' ./internal/task/                         # PASS (0.84s)
go test -count=1 -run 'TestSpecCompile' ./internal/cli/                     # PASS (2.54s)
go test -race -count=1 -run 'TestCompile|TestSpecCompile|TestValidate' \
        ./internal/task/ ./internal/cli/                                    # PASS (no race)
go test -race -count=1 ./internal/task/ ./internal/storage/ ./internal/cli/ # PASS (no race)

# Whole-module gates.
make check                                                                  # exit 0 (fmt-check + vet + full suite; FAIL_COUNT 0)
go test -race ./...                                                         # every package ok; no FAIL, no race detected
```

### Remediation re-run (after review fixes)

```sh
# Targeted regression tests added/changed by the remediation.
go test -count=1 -run 'TestSplitAttachFlag' ./internal/cli/                  # PASS (14 table cases)
go test -count=1 -run 'TestCompile_Regression_AttachmentOnlyEmptyFilename' ./internal/task/  # PASS
go test -count=1 -run 'TestCompile_Deterministic$' ./internal/task/         # PASS (in-order ComplexityReasons)

# All compiler + spec-compile tests (unit + black-box).
go test -count=1 -run 'TestCompile|TestSpecCompile|TestSplitAttachFlag' \
        ./internal/task/ ./internal/cli/                                    # PASS

# Determinism stress (remediation did not regress determinism).
go test -count=20 -run 'TestCompile_Deterministic$' ./internal/task/        # PASS (20×)
go test -count=5  -run 'TestSpecCompile_BlackBox_DeterministicOutput' ./internal/cli/  # PASS (5×, byte-identical)

# Race detector on the touched packages (compiler is pure; storage exercises
# the locked-spec end-to-end path).
go test -race -count=1 -run 'TestCompile|TestSpecCompile|TestSplitAttachFlag|TestValidate|TestSpecification' \
        ./internal/task/ ./internal/cli/                                    # PASS (no race)

# Whole-module gates (no M0–M13 + M14-00/M14-01 regression).
make check                                                                  # exit 0 (FAIL_COUNT 0)
go test -race ./...                                                         # every package ok; no FAIL, no race
```

Toolchain: `go version go1.26.5 darwin/arm64`.

| Command                                              | Exit | Result                                                                                            |
|------------------------------------------------------|-----:|---------------------------------------------------------------------------------------------------|
| `gofmt -l internal/cli/ internal/task/`              | 0    | clean (gofmt -w applied to the 4 new files during implementation)                                 |
| `go vet ./...`                                       | 0    | clean                                                                                            |
| `go test -count=1 ./internal/task/`                  | 0    | `ok neuroforge/internal/task 0.776s`                                                              |
| `go test -count=1 ./internal/cli/`                   | 0    | `ok neuroforge/internal/cli 135.383s` (full suite; black-box builds + runs the binary)            |
| `make check`                                         | 0    | every package `ok` (FAIL_COUNT 0); no M0–M13 + M14-00/M14-01 regression                          |
| `go test -race ./...`                                | 0    | every package `ok`; **no FAIL, no race detected**                                                 |

## Black-box evidence (compiled `forge` binary, observable)

`internal/cli/spec_compile_blackbox_test.go` drives the production binary, not
internal Go objects (engineering baseline §2):

1. **`TestSpecCompile_BlackBox_DeterministicOutput`** — `forge spec compile
   --project work-app "<text>"` invoked **twice** with identical input
   produces byte-identical JSON. The parsed JSON is asserted field-by-field:
   `TaskID="work-app-compiled"`, `Version=0`, `Locked=false`,
   `Objective` contains "retry button", two ACs with stable IDs `AC-1`/`AC-2`,
   `Confidence=HIGH`, `NonGoals` and `Constraints` lists preserved. This is the
   headline AC3 (determinism) + AC1 (complete spec) proof at the binary level.
2. **`TestSpecCompile_BlackBox_VagueInputLowConfidence`** — `forge spec
   compile --project p "fix it"` emits `Confidence=LOW` and ≥1 Clarification
   (AC2).
3. **`TestSpecCompile_BlackBox_AttachmentMetadata`** — `--attach
   sha256:deadbeef=DESIGN_REFERENCE` propagates into
   `VisualRequirements.Required=true`, `References=["sha256:deadbeef"]`, and
   `AttachmentRoles["sha256:deadbeef"]="DESIGN_REFERENCE"` (AC1: attachment
   metadata as input).
4. **`TestSpecCompile_BlackBox_RiskyTaskFlagsClarification`** — an OAuth
   rotation task compiles to `Risk=R4` AND surfaces a Clarification (AC2:
   unsafe ambiguities explicitly flagged, even with structured sections).
5. **`TestSpecCompile_BlackBox_MissingProject`** — missing `--project` exits
   non-zero with a stderr message naming `project`.
6. **`TestSpecCompile_BlackBox_EmptyInput`** — empty input exits non-zero
   with the literal `description or attachment is required` message.
7. **`TestSpecCompile_BlackBox_AttachmentOnlyWithFilename`** (MAJOR-1) — the
   extended `--attach sha256:feedface=REQUIREMENTS:requirements.md:text/markdown:512`
   grammar propagates filename into the synthesised objective; the objective
   contains `requirements.md` and has **no** degenerate `()` clause; confidence
   is LOW with a clarification.
8. **`TestSpecCompile_BlackBox_AttachmentOnlyLegacyHashRole`** (MAJOR-1) — the
   legacy `hash=ROLE` form (no filename) still works and produces a
   non-degenerate objective (defensive empty-filename handling).
9. **`TestSpecCompile_BlackBox_TextOutput`** (MINOR-1/2) — default output
   (no `--json`) is human-readable text containing TaskID, Objective, AC IDs,
   and Confidence.
10. **`TestSpecCompile_BlackBox_InvalidPriorityRejected`** (MINOR-3) —
    `--priority BOGUS` exits non-zero with a clear stderr naming `--priority`.
11. **`TestSpecCompile_BlackBox_InvalidAttachRoleRejected`** — unknown
    `--attach` role exits non-zero (role validation observable through binary).
12. **`TestSpecCompile_BlackBox_InvalidAttachSizeRejected`** (MAJOR-1) —
    non-numeric size in the extended `--attach` grammar exits non-zero.

The `if testing.Short() { t.Skip(...) }` guard is the standard opt-out; the
tests run under both `make check` and `go test -race ./...` (verified: full
suite green, race detector clean). These are **not** skipped/manual/opt-in
tests masquerading as mandatory evidence.

## End-to-end locked-spec invariant (AC4)

`TestCompile_LockedSpecCannotBeMutatedViaSave` (in
`internal/task/compiler_test.go`) is the headline proof of the "compiler never
mutates a locked specification" AC at the **production** composition root
(baseline rule 7: a unit test of the compiler alone does not prove the
end-to-end invariant):

1. `Compile` a structured description → `Specification{Version:0}`.
2. `SpecificationStore.Save` reserves version 1.
3. `SpecificationStore.Lock(ctx, taskID, 1, "reviewer-1")` locks v1.
4. `Compile` a CHANGED description ("TAMPERED") → still
   `Specification{Version:0}`.
5. `SpecificationStore.Save` reserves version **2** (not 1).
6. `store.Get(ctx, taskID, 1)` returns the original spec:
   `Objective=="Original."`, `Locked==true`, `LockedBy=="reviewer-1"`.

The locked v1 is byte-identical to its pre-lock state; the new compilation
landed as a new version, exactly as §28 demands. The compiler cannot mutate a
locked spec by construction (it is pure), and the storage layer rejects any
attempt to overwrite a locked version (M14-01).

## Regression coverage of parser defects

Five regression tests pin parser defects discovered during implementation:

1. **`TestCompile_Regression_TrailingSpacesInHeader`** — section headers with
   trailing whitespace (`Objective:   \n`) are recognised.
2. **`TestCompile_Regression_CaseInsensitiveHeaders`** — `objective:`,
   `OBJECTIVE:`, `Objective:`, `ObJeCtIvE:` all map to the same key.
3. **`TestCompile_Regression_NumberedListACs`** — numbered ACs (`1.`/`2.`/
   `3.`) parse as separate items, not a single collapsed string.
4. **`TestCompile_Regression_SectionHeaderAtEOF`** — a header at the very end
   of the description with no body falls back to synthesised defaults instead
   of crashing.
5. **`TestCompile_Regression_AttachmentRoleMapping`** — attachment roles are
   surfaced in `CompileResult.AttachmentRoles` keyed by hash.

Each test names the defect it pins in its name and docstring; a future parser
refactor that reintroduces the defect will fail the named test.

## Known limitations

1. **`forge spec compile` is a read-only compiler surface.** It does not
   persist specifications (no daemon transport, no `forge spec create`
   command). Persistence is the caller's responsibility via
   `SpecificationStore.Save`. The compiler's purity is preserved by design
   (AC3 determinism depends on no I/O). A spec-CRUD CLI + transport endpoint
   is a follow-up (M14-01 follow-up #2).
2. **Cross-version AC identity** is not tracked: the compiler emits fresh
   `AC-1`/`AC-2` IDs on every compilation. Cross-version identity (the same
   logical "AC-1" carried across versions) is a compiler concern tracked as
   M14-01 follow-up #3; it is absent from this task's ACs.
3. **Attachment content is not read.** The compiler consumes metadata only
   (hash/filename/MIME/role) per the task brief ("Attachment metadata как
   вход"). After the MAJOR-1 remediation the `forge spec compile` CLI
   supplies the full metadata set via the extended `--attach` grammar
   (`hash=ROLE[:filename[:mimeType[:size]]]`); the legacy `hash=ROLE` form
   remains valid. An attachment-only task therefore produces
   `Confidence=LOW` with a clarification by design; a future milestone may
   add content-aware parsing.
4. **The complexity classifier is intentionally simpler than the router's
   `ClassifyComplexity`** (M6-2): the M14-01 Specification model only has four
   bands (C0..C3), whereas the router has five (C0..C4) tied to model tiers.
   Reusing `router.ClassifyComplexity` would require a lossy C4→C3 collapse
   and would create a layering violation (task → router). The local classifier
   is therefore purpose-built for `task.Complexity`. This is a deliberate
   scope decision, not a defect.
5. **Section parsing uses a curated header dictionary** (`knownSectionHeaders`
   in `compiler.go`). Unknown sections (`Foo:`) are silently ignored (their
   lines are not lost — they fall into the preamble — but they are not
   surfaced as structured fields). This matches the spec §18.1 enumeration of
   structured fields.
6. **The `Priority` field on `CompileInput` is currently informational** — the
   M14-01 `Specification` model does not store priority (priority lives on
   `task.Task`). The compiler accepts it for forward compatibility.

## Follow-up problems (not addressed — out of scope)

1. **FU-M14-02-1:** `forge spec {create,get,lock,versions}` over the daemon
   transport (CRUD surface) + a binary-driven spec-CRUD black-box test.
2. **FU-M14-02-2:** Wire `Compile` into `forge task add` so every new task is
   compiled automatically (currently opt-in via `forge spec compile`).
3. **FU-M14-02-3:** Cross-version acceptance-criterion identity tracking.
4. **FU-M14-02-4:** Content-aware attachment parsing (read text/markdown
   attachments as supplementary context, with the policy redaction pipeline
   applied per §9.6).
5. **FU-M14-02-5:** ADR for the package-boundary decision (compiler lives in
   `internal/task` next to the `Specification` model, mirroring the existing
   `task.Task`/`storage.Task` split — same precedent as M14-01).
6. **FU-M14-02-6:** Address the M14-01 MINOR findings when relevant
   (audit-fidelity of idempotent re-lock; negative audit regression tests).
   They do not touch the compiler's inputs/outputs and remain M14-01
   follow-ups.

## Verdict

`IMPLEMENTED_TESTED` — every mandatory acceptance criterion is proven by
passing automated evidence at unit, integration, and black-box levels:

- AC1 (complete valid spec for typical tasks): six fixtures (bugfix, feature,
  UI, auth-payment risky, vague, attachment-only) all compile to
  `ValidateSpecification`-passing specifications.
- AC2 (unsafe ambiguities flagged): `Confidence=LOW` + Clarifications for
  vague/attachment-only inputs; explicit Clarification for R4 tasks even when
  structured sections are present.
- AC3 (deterministic): pure compiler; identical input ⇒ byte-identical output
  at the value level (unit) and at the stdout level (black-box binary).
- AC4 (never mutates locked spec): pure compiler cannot reach storage;
  end-to-end compile → save → lock → compile+save leaves the locked v1
  byte-identical.

After the review remediation, the compiler is exercised by **twelve**
black-box tests through the compiled `forge` binary (six original + six new
regression tests covering MAJOR-1 and MINOR-1/3). Every MAJOR and MINOR
finding from `docs/reviews/m14/M14-02_REVIEW.md` is addressed; each fix has
a named regression test that would fail if the defect returned (see the
"Review remediation" table above).

`make check` is green across every M0–M13 + M14-00/M14-01/M14-02 package
(no regression). `go test -race ./...` is clean (no race detected). There
are no TODOs, no fake stubs, no hard-coded data presented as a feature. The
product spec was not modified; no security/autonomy/delivery/merge-policy
invariant was weakened.

The successor task **M14-03 may start** once an independent reviewer and
acceptor have moved M14-02 through `REVIEW_APPROVED` → `ACCEPTED`.
