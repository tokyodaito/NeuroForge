# M14-02 — Independent Review Report

**Task:** M14-02 — Deterministic Task Compiler.
**Reviewer actor:** `M14-02-review-session` (independent of the implementer).
**Verdict:** `CHANGES_REQUESTED`

## SHAs

- **Starting SHA:** `f894f648fb5545b89cb5e797e6d146a35d2c379a` (M14-01 acceptance commit).
- **Candidate SHA reviewed:** `ee738b2edd9138e7e2ef33eb18ff05fc6e95b43d` (branch `main`,
  commit `M14-02: deterministic task compiler + forge spec compile`). This is the current
  `HEAD`; the working tree matches the candidate SHA for every file under review
  (`git diff HEAD -- <touched files>` is empty). No newer HEAD drift.

## Scope reviewed

Full diff `f894f648..ee738b2`:

```
internal/task/compiler.go                  | +795 (new)
internal/task/compiler_test.go             | +719 (new)
internal/cli/spec_cmd.go                   | +194 (new)
internal/cli/spec_compile_blackbox_test.go | +255 (new)
internal/cli/cli.go                        |   +2 (dispatch case)
internal/cli/help.go                       |   +5 (help text)
docs/reviews/m14/M14-02_IMPLEMENTATION.md  | rewritten
```

No changes to `docs/spec/NEUROFORGE_SPEC.md`, `internal/daemon`, `internal/scheduler`,
`internal/storage`, `internal/policy`, or `internal/merge` (scope is clean; baseline rule 3
honoured). Product spec untouched. No security/autonomy/delivery/merge-policy invariant
weakened.

The candidate-SHA tree matches the code I read during review (verified with
`git diff HEAD -- internal/task/compiler.go internal/cli/spec_cmd.go ...` → empty).

## Acceptance criterion matrix

| Mandatory AC | Where implemented | Test(s) | Independently re-run? | Verdict |
|---|---|---|---|---|
| **AC1** Compiler creates a complete valid specification for typical tasks | `Compile` in `internal/task/compiler.go:101-157`; structured-section parser `parseSections` (`compiler.go:171-222`); risk cascade reuses `risk.Classify` (`compiler.go:549-568`); local complexity classifier (`compiler.go:614-661`); visual requirements (`compiler.go:667-677`) | Unit: `TestCompile_Fixture_Feature/Bugfix/UITask/AuthPaymentRisky`, `TestCompile_RiskLevels`, `TestCompile_ACsHaveStableIDs`, `TestCompile_SynthesisesACWhenAbsent`. Black-box: `TestSpecCompile_BlackBox_DeterministicOutput`, `TestSpecCompile_BlackBox_AttachmentMetadata` | yes — `go test -count=1 -run TestCompile ./internal/task/` PASS; black-box PASS | **MET** — every fixture is re-validated through the production `ValidateSpecification` (`specification.go:156-202`); the bugfix fixture is the spec §9.1 example verbatim |
| **AC2** Unsafe ambiguities are explicitly flagged | `deriveConfidenceAndClarifications` (`compiler.go:682-752`): R4 always emits a Clarification even with structured sections; vague/attachment-only → `Confidence=LOW` + Clarification | Unit: `TestCompile_Fixture_VagueTask`, `TestCompile_Fixture_AttachmentOnly`, `TestCompile_Fixture_AuthPaymentRisky`, `TestCompile_RejectsEmptyInput`. Black-box: `TestSpecCompile_BlackBox_VagueInputLowConfidence`, `TestSpecCompile_BlackBox_RiskyTaskFlagsClarification` | yes — all green | **MET** — both the *presence* of a Clarification and the *reason text* are asserted; observable through the compiled binary |
| **AC3** Identical input ⇒ deterministic output | `Compile` is pure: no I/O, no clock, no randomness (`compiler.go:91-157`). Lists are appended in fixed order; `AttachmentRoles` is a map but JSON-marshalled with sorted keys | Unit: `TestCompile_Deterministic`, `TestCompile_Deterministic_AcrossVaryingWhitespace`, `TestCompile_ACsHaveStableIDs`. Black-box: `TestSpecCompile_BlackBox_DeterministicOutput` (byte-identical stdout across two binary invocations) | yes — `go test -count=20 -run TestCompile_Deterministic$` PASS (20×); `go test -count=5 -run TestSpecCompile_BlackBox_DeterministicOutput` PASS (5×) | **MET** — the black-box byte-identity assertion is the strongest possible determinism proof |
| **AC4** Compiler never mutates a locked specification | `Compile` returns `Specification{Version:0, Locked:false}` by construction (`compiler.go:111-156`); the compiler function signature has no storage handle. `SpecificationStore.Save` (`specification.go:246-314`) rejects locked versions via `ErrSpecificationLocked` | Unit: `TestCompile_NeverMutatesLockedSpec` (compiler-level purity). End-to-end: `TestCompile_LockedSpecCannotBeMutatedViaSave` (compile → save → lock → compile+save → locked v1 byte-identical, new v2 allocated) | yes — both PASS | **MET** — the end-to-end test exercises the real `SpecificationStore` + SQLite + audit wiring; baseline rule 7 satisfied |

All four mandatory acceptance criteria are proven by automated evidence at unit and
black-box levels.

## Required test-class coverage (task brief)

| Required class | Covered | Notes |
|---|---|---|
| Fixtures: bugfix / feature / UI / auth-payment risky / vague / attachment-only | yes | `TestCompile_Fixture_*` (six fixtures). The bugfix fixture is the spec §9.1 example. |
| Positive/negative confidence and clarification decisions | yes | HIGH (`Feature`), MEDIUM (`SynthesisesACWhenAbsent`, R4-with-sections), LOW (`VagueTask`, `AttachmentOnly`, `RejectsEmptyInput`). R4 clarification present even with structured sections. |
| Stable output for identical input | yes | Unit value-level + black-box byte-identical JSON. |
| Regression tests on found parser defects | yes | Five named regression tests (`TrailingSpacesInHeader`, `CaseInsensitiveHeaders`, `NumberedListACs`, `SectionHeaderAtEOF`, `AttachmentRoleMapping`). |
| `make check` | yes | exit 0 (independently verified). |

## Independent re-run results

```sh
$ go test -count=1 -run 'TestCompile' ./internal/task/
ok  	neuroforge/internal/task	0.910s

$ go test -count=1 -run 'TestSpecCompile' ./internal/cli/
ok  	neuroforge/internal/cli	2.310s

$ go test -race -count=1 -run 'TestCompile|TestSpecCompile|TestValidate|TestSpecification' \
    ./internal/task/ ./internal/cli/
ok  	neuroforge/internal/task	2.267s
ok  	neuroforge/internal/cli	3.580s

$ make check
gofmt: clean
go vet ./...
# every package ok; FAIL_COUNT 0
make check exit=0

$ go test -race ./...
# every package ok; no FAIL, no race detected

$ go test -count=20 -run 'TestCompile_Deterministic$' ./internal/task/   # extra determinism stress
ok  	neuroforge/internal/task	0.709s

$ go test -count=5 -run 'TestSpecCompile_BlackBox_DeterministicOutput' ./internal/cli/
ok  	neuroforge/internal/cli	1.775s
```

Toolchain: `go version go1.26.5 darwin/arm64`.

Gate enforcement (the engineering-baseline check M14-00 put in place):

```sh
$ forge gate baseline
active schema_version: 1
active baseline_version: 1
baseline document: docs/engineering/ENGINEERING_BASELINE.md

$ forge gate next --manifest docs/reviews/m14/M14-01.manifest.json
OK: predecessor "M14-01" is ACCEPTED; successor task may start     # exit 0
```

The candidate was lawfully allowed to start.

## Production composition / wiring (baseline rule 7)

- The compiler is a pure function `task.Compile(CompileInput) (CompileResult, error)` with
  no I/O, no clock, no storage handle (`compiler.go:101`). Identical input → identical
  output by construction.
- Production reachability is proven through the compiled `forge` binary via
  `forge spec compile` (`internal/cli/spec_cmd.go`), dispatched from the real CLI switch
  in `internal/cli/cli.go:74-75` (`case "spec": return a.runSpec(args[1:])`). Six black-box
  tests in `internal/cli/spec_compile_blackbox_test.go` drive the production binary, not
  in-memory Go objects.
- The end-to-end locked-spec invariant (AC4) is proven through the **production**
  `SpecificationStore` + SQLite + audit path, not just the pure compiler
  (`TestCompile_LockedSpecCannotBeMutatedViaSave`).

## Findings

### [MAJOR-1] CLI `--attach` flag does not expose attachment Filename/MIME/Size; attachment-only tasks via the production CLI produce a degenerate objective with empty parens `()`

**Location:**
- `internal/cli/spec_cmd.go:106-116` — `--attach` parsing only fills `Hash` and `Role`;
  `Filename`, `MimeType`, `Size` are left zero.
- `internal/task/compiler.go:481-512` — `synthesiseObjectiveFromAttachments` interpolates
  `a.Filename` into the placeholder objective.
- `internal/task/compiler.go:549-554` — `classifyRisk` passes `a.Filename` as a path hint
  to `risk.Classify`.

**Observed violation:**

The CLI `--attach hash=ROLE` flag cannot supply `Filename`, `MimeType`, or `Size`. The
compiler is designed to consume this metadata (per the implementation report's own
"Known limitations" §3: *"The compiler consumes metadata only (hash/filename/MIME/role)"*),
but the production CLI path supplies only hash+role.

For an attachment-only task (no description) via the CLI, `synthesiseObjectiveFromAttachments`
builds the placeholder objective by interpolating the empty filename, producing visibly
broken output:

```sh
$ forge spec compile --project p --attach "sha256:abc=REQUIREMENTS"
{
  "result": {
    "Specification": {
      "Objective": "Implement changes based on the attached requirements ().",
      ...
```

The empty `()` is the missing filename. The risk classifier also loses path-based hints
for CLI-supplied attachments (the `paths` slice passed to `risk.Classify` is empty).

The unit tests bypass this by constructing `task.Attachment{Filename: "requirements.md", ...}`
directly; the black-box tests never exercise an attachment-only task through the CLI.

**Why this matters:** The task brief lists *"Attachment metadata как вход"* as in-scope,
and spec §9.5 defines attachment metadata as SHA-256 + filename + MIME type + size + source
+ project + task + confidentiality label. The CLI production path is lossy: it cannot
supply the metadata the compiler is documented to consume. The implementation report does
not acknowledge this CLI-level gap (it only documents that attachment *content* is not
read, which is a different statement).

**Required fix (either is acceptable):**

1. Extend the `--attach` flag to carry filename (and optionally MIME/size), e.g.
   `--attach hash=ROLE:filename` or a separate `--attach-file path=ROLE` form that
   content-addresses the file like `forge task add -a` does; **or**
2. Document the limitation explicitly in the implementation report's "Known limitations"
   section: state that the `forge spec compile` CLI currently supplies only hash+role and
   that filename/MIME/size are zero on the CLI path, so attachment-only tasks produce a
   degenerate placeholder objective. Add a regression test that asserts the CLI path
   still produces a valid (LOW-confidence + clarification) specification despite the
   missing filename, so the degenerate objective is at least pinned.

---

### [MINOR-1] Text output path (`--json=false`) has no test coverage

**Location:** `internal/cli/spec_cmd.go:124-127` (the `if !*jsonOut` branch) and
`internal/cli/spec_cmd.go:165-194` (`writeSpecCompileText`).

**Observed violation:** Every black-box test runs with the default `--json=true`. The
human-readable text formatter is never exercised by any test. A nil-pointer or format
regression in `writeSpecCompileText` would not be caught.

**Required fix:** Add one black-box test that runs
`forge spec compile --json=false --project p "<text>"` and asserts the text output
contains the TaskID, Objective, AC IDs, and Confidence fields.

---

### [MINOR-2] `--json` defaults to `true`, inconsistent with every other `forge` command

**Location:** `internal/cli/spec_cmd.go:70` — `fs.Bool("json", true, "emit machine-readable JSON")`.

**Observed violation:** All other commands (`project add`, `task add`, `workspace create`,
`daemon status`, etc.) default `--json` to `false` and require the user to opt in. The
`spec compile` command inverts this convention without documenting why. A user piping
output to another tool expecting text would silently receive JSON.

**Required fix:** Either align with the convention (default `false`, require `--json`),
or document the rationale in the help text. The convention should be consistent across
the CLI surface.

---

### [MINOR-3] `--priority` is not validated against known values

**Location:** `internal/cli/spec_cmd.go:69` — `priority := fs.String("priority", "NORMAL", ...)`.

**Observed violation:** `--priority BOGUS` is silently accepted (exit 0) and passed
through as `task.Priority("BOGUS")`. The `--attach` flag validates roles against the
known set (`splitAttachFlag` at `spec_cmd.go:157-162`); priority has no equivalent check.

**Required fix:** Validate `--priority` against `PriorityLow|PriorityNormal|PriorityHigh|
PriorityUrgent` and exit non-zero with a clear error for unknown values, matching the
`--attach` role-validation behaviour.

---

### [MINOR-4] Determinism test sorts `ComplexityReasons` before comparing, weakening the assertion

**Location:** `internal/task/compiler_test.go:639-647` — inside `sameCompileResult`.

**Observed violation:** The determinism helper compares `UncertaintyReasons` and
`RiskReasons` in order, but copies `ComplexityReasons` into fresh slices, sorts both,
then compares. If a future refactor introduced non-deterministic ordering in
`classifyComplexity`'s reason list, this test would still pass. The black-box
byte-identity test compensates at the JSON level, but the unit-level determinism proof
is weaker than it appears for this one field.

**Required fix:** Compare `ComplexityReasons` in order (no sort), matching the treatment
of `RiskReasons` and `UncertaintyReasons`. The production code produces reasons in a
fixed sequence, so the test should assert that.

---

### [MINOR-5] Dead code: `_ = i` in `writeSpecCompileText`

**Location:** `internal/cli/spec_cmd.go:172-175`.

**Observed violation:**
```go
for i, ac := range spec.AcceptanceCriteria {
    fmt.Fprintf(a.Out, "  %s: %s\n", ac.ID, ac.Statement)
    _ = i
}
```
The loop variable `i` is unused; the `_ = i` line silences the compiler unnecessarily.

**Required fix:** Replace with `for _, ac := range spec.AcceptanceCriteria`.

---

## Counterexample search (no AC invalidated)

I attempted the following counterexamples; none invalidated a mandatory AC:

1. **Markdown-style headers** (`## Objective:`) — correctly stripped by `matchHeader`
   (`compiler.go:282`). ✓
2. **Colon inside an AC statement** (`The endpoint /api:v2 returns 200.`) — correctly
   preserved as a single AC. ✓
3. **Inline objective with no newline** (`Objective: Build X.` as the entire description)
   — correctly parsed; synthesised AC; MEDIUM confidence. ✓
4. **20× repeated `TestCompile_Deterministic`** — no flakiness; identical output every
   time. ✓
5. **5× repeated black-box determinism test** — byte-identical JSON every time. ✓
6. **Attachment-only via CLI with no filename** — produces a valid (LOW + clarification)
   specification, but the objective is degenerate (see MAJOR-1). The spec still passes
   `ValidateSpecification`, so AC1 is technically met; the quality gap is MAJOR-1.

## Restart / idempotency / cancellation / concurrency

- The compiler is pure (no mutable state, no I/O) — restart and cancellation are
  not applicable to `Compile` itself.
- The end-to-end locked-spec test (`TestCompile_LockedSpecCannotBeMutatedViaSave`)
  exercises the storage layer's transactional version allocation under the real SQLite
  driver; `go test -race ./internal/task/ ./internal/storage/` is clean.
- No goroutines are introduced by this change; concurrency is not a concern for the
  compiler. The CLI command is a single-shot process.

## Backward compatibility

- The change is purely additive: one new CLI dispatch case (`spec`), one new subcommand
  (`compile`), two new packages' worth of files (`compiler.go`, `spec_cmd.go`). No
  existing command, flag, or output format is modified.
- The M14-01 `Specification` model and `SpecificationStore` are unchanged.

## Fake / stub / demo leakage

- No fakes, stubs, or demo data in production code.
- The risk classifier reuses the accepted `internal/risk.Classify` (M6-3) — no
  duplicate or fake taxonomy.
- The complexity classifier is a new local implementation, but the implementation report
  justifies not reusing `router.ClassifyComplexity` (different band sets C0..C3 vs
  C0..C4; layering concern task → router). This is a documented scope decision, not a
  stub.
- No `TODO` / `FIXME` / `panic("unimplemented")` in the new files.

## Policy / security bypass

- None. The compiler is a pure transformation; it does not touch security policy,
  autonomy profiles, merge policy, or quota/budget arithmetic.
- The compiler never reads attachment content (only metadata), matching spec §9.4/§9.6.
- No agent process is spawned; no environment is forwarded.

## Documentation / claim verification

The implementation report's claims were checked against the code:

- "Pure compiler ... no I/O, no clock, no external model call" — **verified**
  (`Compile` signature and body).
- "Returns `Specification{Version:0, Locked:false}`" — **verified**
  (`compiler.go:111-156`).
- "HIGH only when both Objective and ACs are given explicitly" — **verified**
  (`compiler.go:740-742`).
- "R4 surfaces a Clarification even with structured sections" — **verified**
  (`compiler.go:715-721`; black-box test confirms).
- "The compiler consumes metadata only (hash/filename/MIME/role)" — **partially
  verified**: the compiler *accepts* all four metadata fields, but the production CLI
  supplies only hash+role (see MAJOR-1). The report does not acknowledge this gap.
- `make check` exit 0, `go test -race ./...` clean — **independently verified**.
- Line counts (795 / 719 / 194 / 255) — **verified** with `wc -l`.

## What remains unproven

Nothing among the four mandatory ACs is unproven. All are backed by automated evidence
that I independently re-ran (unit + race + black-box + `make check`).

The MAJOR-1 finding is a production-wiring quality gap (degenerate objective for
attachment-only tasks via CLI) that does not strictly violate an AC but does contradict
the implementation report's implicit claim that the compiler consumes full attachment
metadata on the production path. It should be fixed or explicitly documented before
acceptance.

## Follow-up problems (tracked, not addressed by this review)

The implementation report's follow-up list (FU-M14-02-1 through FU-M14-02-6) is
reasonable. I add:

- **FU-M14-02-7 (this review):** Address MAJOR-1 — extend `--attach` to carry filename
  or document the CLI-level metadata gap; add a regression test for the attachment-only
  CLI path.
- **FU-M14-02-8 (this review):** Address MINOR-1 through MINOR-5 (text-output test,
  `--json` default alignment, priority validation, determinism-test sort, dead code).

## Verdict

`CHANGES_REQUESTED`

All four mandatory acceptance criteria are proven by automated evidence that I
independently re-ran (unit, race, black-box, `make check` — all green). However,
**MAJOR-1** represents a real production-wiring gap: the `forge spec compile` CLI cannot
supply attachment filenames, so attachment-only tasks via the production path produce a
visibly degenerate objective (`"... attached requirements ()."`), and this limitation is
not acknowledged in the implementation report. The fix is small (extend `--attach` or
document + pin the limitation with a regression test).

The five MINOR findings are quality improvements that should be tracked as follow-ups;
none of them invalidate an AC.

Per the review rules, `REVIEW_APPROVED` is forbidden when any mandatory criterion is
unproven — all mandatory criteria *are* proven here, but the MAJOR-1 production-quality
defect and the report's incomplete "Known limitations" section warrant
`CHANGES_REQUESTED` before the task moves to `ACCEPTED`. Once MAJOR-1 is resolved (and
ideally the MINOR findings are addressed or explicitly deferred), the task should be
re-reviewed and can proceed to acceptance.

---

# M14-02 — Independent Re-review (after remediation)

**Task:** M14-02 — Deterministic Task Compiler.
**Re-review actor:** `M14-02-rereview-session` (independent of `M14-02-impl-session`,
`M14-02-remediation-session`, and `M14-02-review-session`).
**Verdict:** `REVIEW_APPROVED`

This section is additive; the historical review above is preserved unchanged.

## Review identity

- re-review actor/session ID: `M14-02-rereview-session`
- implementation actor/session ID: `M14-02-impl-session`
- remediation actor/session ID: `M14-02-remediation-session`
- original review actor/session ID: `M14-02-review-session`
- independence confirmed: yes — this is a fresh session; it authored no production code,
  no tests, performed no remediation, and will not perform acceptance. Actor IDs are
  distinct from implementation, remediation, original-review, and M14-01 accept actors.

## Git baseline

- accepted predecessor SHA (M14-01 acceptance): `f894f648fb5545b89cb5e797e6d146a35d2c379a`
- original candidate SHA: `ee738b2edd9138e7e2ef33eb18ff05fc6e95b43d`
- remediated candidate SHA (reviewed): `fad77d5f558bf6da840e14704c02e8d9124db4ce`
- review report commit SHA: recorded below in the commit step (this commit).

Ancestry verified:
- `ee738b2` is an ancestor of `fad77d5` (`git merge-base --is-ancestor` → exit 0).
- `f894f648` (M14-01 acceptance) is an ancestor of both `ee738b2` and `fad77d5`.
- Commit chain `f894f648..fad77d5`: `ee738b2` (impl) → `92dd826` (original review) →
  `fad77d5` (remediation). HEAD of the working tree is exactly the remediated candidate
  (`git rev-parse HEAD` == `fad77d5…`); no newer HEAD drift.

Working tree at the candidate: every M14-02 file matches the candidate SHA
(`git status --short -- <all M14-02 paths>` is empty). The only working-tree entries are
the pre-existing unrelated `docs/reviews/MINIMAL_RUN_*` and `docs/reviews/M12_M13_REVIEW.md`
files (same situation as M14-00 / M14-01; they predate this task and are untouched).

## Predecessor gate

`forge gate next --manifest docs/reviews/m14/M14-01.manifest.json` →
`OK: predecessor "M14-01" is ACCEPTED; successor task may start` (exit 0).
`forge gate validate` on the M14-01 manifest → exit 0
(`REVIEW_APPROVED -> ACCEPTED is legal under baseline v1`). The candidate was lawfully
allowed to start; baseline version is 1.

## Remediation diff (actual vs claimed)

`git diff --stat ee738b2..fad77d5` touches exactly the claimed files and nothing else
in production code:

```
docs/reviews/m14/M14-02_IMPLEMENTATION.md  | remediation section added
docs/reviews/m14/M14-02_REVIEW.md          | original review (committed at 92dd826)
internal/cli/help.go                       | +2/-2  (grammar + --json opt-in text)
internal/cli/spec_cmd.go                   | +86/-35 (MAJOR-1 + MINOR-2/3/5)
internal/cli/spec_cmd_test.go              | +136 (new: TestSplitAttachFlag_Grammar)
internal/cli/spec_compile_blackbox_test.go | +205/-7 (MAJOR-1 + MINOR-1/3 binary tests)
internal/task/compiler.go                  | +10/-3 (MAJOR-1 defensive empty-filename)
internal/task/compiler_test.go             | +45/-10 (MINOR-4 in-order + MAJOR-1 regression)
```

No changes to `docs/spec/NEUROFORGE_SPEC.md`, `internal/daemon`, `internal/scheduler`,
`internal/storage`, `internal/policy`, `internal/merge`, or any baseline/gate enforcement.
Scope is bounded to the six findings — no scope creep, no M14-03 work, no weakened
invariants. Legacy `hash=ROLE` form is preserved (verified in code and via binary).

## Previous findings status

| Finding | Fix | Regression evidence | Status |
|---|---|---|---|
| **MAJOR-1** CLI `--attach` cannot carry Filename/MIME/Size; attachment-only CLI emits degenerate `()` objective | `splitAttachFlag` rewritten to parse `hash=ROLE[:filename[:mimeType[:size]]]` with role + size validation; `synthesiseObjectiveFromAttachments` omits the file clause when filename is empty (`internal/task/compiler.go:507-520`); legacy form preserved; help updated | `TestSplitAttachFlag_Grammar` (14 cases), `TestCompile_Regression_AttachmentOnlyEmptyFilename`, `TestSpecCompile_BlackBox_AttachmentOnlyWithFilename`, `TestSpecCompile_BlackBox_AttachmentOnlyLegacyHashRole`, `TestSpecCompile_BlackBox_InvalidAttachSizeRejected`, `TestSpecCompile_BlackBox_InvalidAttachRoleRejected`; **sensitivity check confirmed** the regression tests fail when the defensive handling is removed | **CLOSED** |
| **MINOR-1** Text output path untested | `TestSpecCompile_BlackBox_TextOutput` added — asserts default output is text (not JSON) and contains TaskID, Objective, AC-1/AC-2, Confidence; **sensitivity check confirmed** it fails when `--json` default is reverted to `true` | as above | **CLOSED** |
| **MINOR-2** `--json` defaulted to `true` (inconsistent) | default changed to `false` (`spec_cmd.go:79`); `--json` is opt-in; existing JSON black-box tests now pass `--json` explicitly; help documents "opt-in; default is text" | `TestSpecCompile_BlackBox_TextOutput`; explicit `--json` in `DeterministicOutput`, `VagueInputLowConfidence`, `AttachmentMetadata`, `RiskyTaskFlagsClarification` | **CLOSED** |
| **MINOR-3** `--priority` unvalidated | `switch task.Priority(*priority)` validates against `PriorityLow/Normal/High/Urgent`; non-zero exit + clear error echoing the bad value (`spec_cmd.go:91-98`); **sensitivity check confirmed** the test fails when validation is removed | `TestSpecCompile_BlackBox_InvalidPriorityRejected` | **CLOSED** |
| **MINOR-4** Determinism test sorted `ComplexityReasons` | `sameCompileResult` compares in-order (no sort); unused `sort` import removed; **sensitivity check confirmed** the test detects injected map-iteration nondeterminism | `TestCompile_Deterministic` | **CLOSED** |
| **MINOR-5** Dead code `_ = i` | replaced with `for _, ac := range`; `go vet ./...` clean; `gofmt -l .` clean | `go vet` exit 0; `TestSpecCompile_BlackBox_TextOutput` exercises the rewritten loop | **CLOSED** |

Every previous MAJOR and MINOR finding is closed, and each fix is guarded by a named
regression test that I independently proved fails when the defect is reintroduced (see
Sensitivity checks).

## Acceptance matrix

| Criterion | Implementation | Test evidence | Independent result | Status |
|---|---|---|---|---|
| **AC1** Complete valid specification for typical tasks (bugfix/feature/UI/security-auth-payment) | `Compile` (`compiler.go:101-157`); structured parser; risk + complexity cascades; `deriveVisualRequirements` | `TestCompile_Fixture_*` (6 fixtures, each re-validated through production `ValidateSpecification`); black-box `DeterministicOutput`, `AttachmentMetadata`; binary reproduces feature/UI/security scenarios with correct Risk/Visual fields | targeted tests PASS; binary reproduces feature (HIGH), UI (`VisualRequirements.Required=true`), security/auth/payment (R4) | **MET** |
| **AC2** Unsafe ambiguities explicitly flagged (vague/attachment-only/high-risk) | `deriveConfidenceAndClarifications` (`compiler.go:691-756`): LOW + Clarification for vague/attachment-only; R4 surfaces a Clarification even with structured sections | `TestCompile_Fixture_VagueTask`, `TestCompile_Fixture_AttachmentOnly`, `TestCompile_Fixture_AuthPaymentRisky`, `TestCompile_RejectsEmptyInput`; black-box `VagueInputLowConfidence`, `RiskyTaskFlagsClarification`, `AttachmentOnly*` | binary reproduces R4 clarification + attachment-only LOW+clarification | **MET** |
| **AC3** Identical input ⇒ deterministic output | `Compile` is pure (no I/O, no clock, no randomness); maps used only as keyed accumulators or JSON output (sorted keys); ordered slices built from input order | `TestCompile_Deterministic` (in-order `ComplexityReasons`), `TestCompile_Deterministic_AcrossVaryingWhitespace`, `TestCompile_ACsHaveStableIDs`; black-box `DeterministicOutput` (byte-identical JSON) | 20× unit PASS; 10× black-box PASS; 5× manual binary runs byte-identical (same SHA-256) | **MET** |
| **AC4** Compiler never mutates a locked specification | `Compile` returns `Specification{Version:0, Locked:false}`; pure (no storage handle by construction); `SpecificationStore.Save` (M14-01) enforces `ErrSpecificationLocked` | `TestCompile_NeverMutatesLockedSpec`; end-to-end `TestCompile_LockedSpecCannotBeMutatedViaSave` (compile → save → lock → compile+save → locked v1 byte-identical, fresh v2 allocated) | PASS (race-clean) | **MET** |

All four mandatory ACs are proven by automated evidence I independently re-ran at unit,
black-box, and race levels.

## Commands executed

| Command | Exit | Result |
|---|---:|---|
| `go build -o /tmp/forge-m14-02-review ./cmd/forge` | 0 | binary builds |
| `/tmp/forge-m14-02-review gate next --manifest docs/reviews/m14/M14-01.manifest.json` | 0 | predecessor ACCEPTED |
| `/tmp/forge-m14-02-review gate validate --manifest docs/reviews/m14/M14-01.manifest.json` | 0 | transition legal |
| `go test -count=1 -run 'TestCompile\|TestSpecCompile\|TestSplitAttachFlag' ./internal/task/ ./internal/cli/` | 0 | PASS (task 0.588s, cli 2.953s) |
| `go test -count=20 -run 'TestCompile_Deterministic$' ./internal/task/` | 0 | PASS (20×) |
| `go test -count=10 -run 'TestSpecCompile_BlackBox_DeterministicOutput' ./internal/cli/` | 0 | PASS (10×, byte-identical) |
| `go test -race -count=1 -run 'TestCompile\|TestSpecCompile\|TestSplitAttachFlag' ./internal/task/ ./internal/cli/` | 0 | PASS, no race |
| `go vet ./...` | 0 | clean |
| `gofmt -l .` | 0 | clean (no files listed) |
| `make check` | 0 | every package ok; FAIL_COUNT 0 |
| `go test -race ./...` | 0 | every package ok; no FAIL, no race detected |

Toolchain: `go version go1.26.5 darwin/arm64`.

## Black-box verification (compiled `forge` binary, isolated temp HOME)

Each scenario was driven through `/tmp/forge-m14-02-review` with an isolated
`HOME=$(mktemp -d)` (never the user's real home):

1. **Text output (no `--json`)** — exit 0; human-readable text with `TaskID:`,
   `Objective:`, `Risk:`, `Complexity:`, `Confidence: HIGH`, `AC-1`, `AC-2`. Not JSON.
2. **JSON output (`--json`)** — exit 0; valid JSON with `result.Specification`,
   `Confidence`, `Clarifications`, `RiskReasons`, `ComplexityReasons`, `AttachmentRoles`.
3. **Attachment-only with filename** (`--attach sha256:feedface=REQUIREMENTS:requirements.md:text/markdown:512`)
   — objective = `"Implement changes based on the attached requirements (requirements.md)."`
   (no `()`); `Confidence=LOW`; 1 clarification; role mirrored.
4. **Attachment-only legacy** (`--attach sha256:abc=REQUIREMENTS`) — objective =
   `"Implement changes based on the attached requirements."` (no `()`, defensive clause
   omitted); `Confidence=LOW`.
5. **Invalid attach size** (`...:text/markdown:notanum` and `...:-5`) — exit 1; clear
   `--attach` error.
6. **Invalid priority** (`--priority BOGUS`) — exit 1; `Error: --priority must be one of
   LOW|NORMAL|HIGH|URGENT, got "BOGUS"`.
7. **Empty description** (`""`, no attachment) — exit 1; `description or attachment is required`.
8. **Security/auth/payment** — `Risk=R4`; clarification present; `Confidence=MEDIUM`.
9. **UI + DESIGN_REFERENCE** — `VisualRequirements.Required=true`; references populated.
10. **Unknown role** (`=BOGUS_ROLE`) — exit 1.
11. **Help** — `forge help` and `forge spec compile -h` show the exact grammar
    `hash=ROLE[:filename[:mimeType[:size]]]`, `--priority ... validated`, and
    `--json ... opt-in; default is text`. Help matches implementation.

Edge cases that behave deterministically and gracefully: size `0` accepted; empty
filename (`hash=ROLE:`) → defensive objective; filename with spaces preserved; empty MIME
accepted; trailing empty size (`...:m:`) tolerated; duplicate hash → last role wins
(deterministic, content-addressed hashes are expected to be unique).

## Determinism verification

- `TestCompile_Deterministic` ×20: PASS (in-order `ComplexityReasons`, no sort).
- `TestSpecCompile_BlackBox_DeterministicOutput` ×10: PASS (byte-identical JSON).
- Manual 5× binary run (`forge spec compile --json ...`) with identical input: all five
  outputs share SHA-256 `cb81f4893f8aa67f15f5b46fb47712d832dd7d827999463c220ad18393f62a3e`
  — byte-identical.
- Production map usage audited (`internal/task/compiler.go`, `internal/cli/spec_cmd.go`):
  maps are used only as keyed lookups (`parseSections` accumulators) or as the
  `AttachmentRoles` output field, which `encoding/json` serialises with sorted keys.
  Ordered outputs (ACs, References, reasons) are built by appending over input slices in
  fixed sequence. No map iteration feeds ordered output.

## Sensitivity checks

Performed in an isolated `git worktree` (detached at `fad77d5`); every mutation was fully
reverted (`git diff --stat` empty afterwards) and the clean tree re-run PASSed.

| Mutation | Expected failing test | Result |
|---|---|---|
| Revert `--json` default `false`→`true` (`spec_cmd.go`) | `TestSpecCompile_BlackBox_TextOutput` | **FAIL** (output became JSON) — test catches it ✓ |
| Remove defensive empty-filename handling (`compiler.go:507-520`) | `TestCompile_Regression_AttachmentOnlyEmptyFilename` and `TestSpecCompile_BlackBox_AttachmentOnlyLegacyHashRole` | **both FAIL** with `()` objective — tests catch it ✓ |
| Inject map-iteration nondeterminism into `classifyComplexity` reasons | `TestCompile_Deterministic` | **FAIL** (r1 `[gamma alpha beta…]` vs r2 `[alpha beta gamma…]`) — in-order comparison catches drift the old sorted comparison hid ✓ |
| Remove `--priority` validation switch (`spec_cmd.go:91-98`) | `TestSpecCompile_BlackBox_InvalidPriorityRejected` | **FAIL** (invalid priority no longer rejected) — test catches it ✓ |

All four sensitivity checks confirm the regression tests genuinely guard the fixes.

## Findings

No new BLOCKER or MAJOR findings. One new MINOR observation (pre-existing, not a
remediation regression; does not invalidate any AC and does not block approval):

### [MINOR-6] Whitespace-only description is treated inconsistently with empty description at the CLI surface (pre-existing)

**Location:** `internal/cli/spec_cmd.go:113-117`.

**Requirement:** Baseline rules 8/10 (malformed/empty input must produce a predictable
error and non-zero exit); task-brief mandatory input scenario "пустого или
whitespace-only description".

**Observed:** The CLI guard is `description == "" && len(attachments) == 0`. A
whitespace-only description (`"    "`) is not `""`, so it bypasses the guard and reaches
`Compile`, which produces `Objective=""`, `Confidence=LOW`, 1 clarification, and a spec
that fails `ValidateSpecification` (`specification.go:162`). The empty string `""` is
hard-rejected with exit 1. The two paths are inconsistent and the whitespace path has no
targeted CLI test.

**Evidence:**
```
$ forge spec compile --project p ""            → exit 1 ("description or attachment is required")
$ forge spec compile --project p "    "        → exit 0, Objective="", Confidence=LOW (degenerate)
```
The guard is byte-identical in `ee738b2` and `fad77d5` (`git show <SHA>:internal/cli/spec_cmd.go`), so this is pre-existing, not introduced by remediation.

**Impact:** Limited. The compiler degrades gracefully (LOW + clarification) and the
compiled spec fails `ValidateSpecification`, so it cannot be persisted via
`SpecificationStore`; `forge spec compile` is read-only and writes no state. AC2 is
satisfied (ambiguity is flagged). The defect is a UX/consistency gap, not a correctness
or safety violation.

**Required fix (follow-up):** Either trim before the empty check
(`strings.TrimSpace(description) == ""`) so whitespace-only is rejected consistently, or
add a black-box test pinning the current graceful-degradation behaviour (LOW +
clarification, exit code documented). Tracked as FU-M14-02-9.

## Scope assessment

- Remediation is strictly bounded to the six prior findings; no production behaviour
  outside the findings was changed.
- Product spec untouched; no security/autonomy/delivery/merge-policy invariant weakened;
  no baseline/gate enforcement weakened.
- No M14-03 work present (no `M14-03*` files; `forge gate next` against M14-01 is still
  the active gate).
- No useful old tests removed; existing JSON black-box tests were updated (not deleted) to
  pass `--json` explicitly after the default flip.
- Legacy `hash=ROLE` grammar preserved (verified in code, help, and binary).
- Help text matches the actual grammar (baseline rule 11).

## What remains unproven

Nothing among the four mandatory ACs. All are backed by automated evidence I independently
re-ran (unit + race + black-box + `make check` + `go test -race ./...`).

MINOR-6 is a tracked follow-up (pre-existing, safe, AC-compliant); it does not require
proof for this task's ACs.

## Verdict

`REVIEW_APPROVED`

Rationale:
- M14-01 is `ACCEPTED` (predecessor gate exit 0).
- The remediated candidate `fad77d5` is a descendant of the original candidate `ee738b2`,
  which descends from the M14-01 acceptance SHA `f894f648`.
- All six prior findings (MAJOR-1 + MINOR-1..5) are genuinely closed; each fix is guarded
  by named regression tests that I proved (via sensitivity/mutation checks) fail when the
  defect is reintroduced.
- All four mandatory ACs (AC1–AC4) are proven by automated evidence I independently
  reproduced at unit, black-box, and race levels.
- Determinism is proven by 20× unit, 10× black-box, and 5× manual binary runs
  (byte-identical, same SHA-256).
- `make check` is green (FAIL_COUNT 0); `go test -race ./...` is green (no race detected);
  `go vet ./...` and `gofmt -l .` are clean.
- No new BLOCKER or MAJOR finding. The single new MINOR-6 is pre-existing, AC-compliant,
  and safe (no persistence path); it is tracked as a follow-up and does not block approval.
- No scope creep; actor independence preserved.

A separate acceptance session (distinct from this re-review) may now move M14-02 from
`REVIEW_APPROVED` to `ACCEPTED`. This re-review session does not perform acceptance.
