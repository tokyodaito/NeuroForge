# M14-02 Acceptance

## Acceptance identity

- acceptance actor/session ID: `M14-02-accept-session` (fresh, independent
  session; performed no implementation, no remediation, no review, no
  re-review of M14-02, and is not the M14-01 accept actor)
- implementation actor/session ID: `M14-02-impl-session`
  (per `M14-02_IMPLEMENTATION.md`)
- remediation actor/session ID: `M14-02-remediation-session`
  (per `M14-02_IMPLEMENTATION.md` "Review remediation"; produced `fad77d5`)
- review actor/session ID (original): `M14-02-review-session`
  (verdict `CHANGES_REQUESTED`; per `M14-02_REVIEW.md` part I)
- re-review actor/session ID: `M14-02-rereview-session`
  (verdict `REVIEW_APPROVED` on `fad77d5`; per `M14-02_REVIEW.md` part II)
- independence confirmed: **yes** — all six role-bound ids are pairwise
  distinct. The acceptor re-checked every implementation/review claim against
  the checked-out code, tests, and the compiled `forge` binary rather than
  trusting any report. This session authored no production code, no tests,
  performed no remediation, and does not perform M14-03.
- acceptance date: 2026-07-27

## Git baseline

- accepted predecessor SHA (M14-01 acceptance): `f894f648fb5545b89cb5e797e6d146a35d2c379a`
  (M14-02 starting SHA per `M14-02_IMPLEMENTATION.md`; M14-01 manifest
  `state = ACCEPTED`)
- original candidate SHA: `ee738b2edd9138e7e2ef33eb18ff05fc6e95b43d`
  (`M14-02: deterministic task compiler + forge spec compile`)
- remediated candidate SHA: `fad77d5f558bf6da840e14704c02e8d9124db4ce`
  (`M14-02: address review findings`)
- re-review report commit SHA: `02726b89949fbe160cb35b0c70a2669bb3c0ed18`
  (`M14-02: re-review remediated task compiler` — sole added file
  `docs/reviews/m14/M14-02_REVIEW.md`, 256 insertions; verified via
  `git show --stat --oneline 02726b8`)
- acceptance starting HEAD: `02726b89949fbe160cb35b0c70a2669bb3c0ed18`
- acceptance commit SHA: recorded below after the commit is created.

Ancestry verified (all `git merge-base --is-ancestor` → exit 0):

- `ee738b2` is an ancestor of `fad77d5`.
- `fad77d5` is an ancestor of `02726b8` (the re-review report commit).
- `f894f648` (M14-01 acceptance) is an ancestor of `ee738b2`.

The re-review report commit (`02726b8`) is **not** the implementation
candidate and is not labelled as such; it adds only the review artifact. Work
was performed in a disposable `git worktree` at the re-review commit on branch
`m14-02-accept`; the user's primary checkout (`main` @ `02726b8`) and all
pre-existing worktrees/branches were left untouched. Pre-existing unrelated
working-tree docs (`docs/reviews/MINIMAL_RUN_*`, `docs/reviews/M12_M13_REVIEW.md`)
predate this task and were not touched (same position as M14-00 / M14-01).

## Predecessor gate

- M14-01 manifest: `docs/reviews/m14/M14-01.manifest.json`
- M14-01 state: `ACCEPTED`
- command: `forge gate next --manifest docs/reviews/m14/M14-01.manifest.json`
- exit code: **0** — `OK: predecessor "M14-01" is ACCEPTED; successor task may
  start`. `forge gate baseline` reports active schema_version 1,
  baseline_version 1, doc `docs/engineering/ENGINEERING_BASELINE.md`. The
  sequential gate is satisfied; M14-02 was lawfully allowed to start.

## Review prerequisite

- reviewed candidate (re-review): `fad77d5f558bf6da840e14704c02e8d9124db4ce`
  — **matches** the required remediated candidate exactly.
- verdict: `REVIEW_APPROVED` (`M14-02_REVIEW.md` part II, line "Verdict").
- blocker findings: **0**
- major findings: **0** (MAJOR-1 is **CLOSED** in the re-review; verified
  independently below)
- accepted minor follow-ups: **1** — MINOR-6 (whitespace-only description
  inconsistency at the CLI surface), pre-existing, AC-compliant, tracked as
  `FU-M14-02-9`.

Acceptance was permitted because the re-review examined the exact remediated
candidate `fad77d5...` and returned `REVIEW_APPROVED`.

## Previous findings status

Each finding was reproduced against the checked-out code and the compiled
binary; the regression test that guards it was confirmed to exist and pass.

| Finding | Fix | Acceptance evidence | Status |
|---|---|---|---|
| **MAJOR-1** CLI `--attach` could not carry Filename/MIME/Size; attachment-only CLI tasks emitted a degenerate `()` objective. | `splitAttachFlag` rewritten to `hash=ROLE[:filename[:mimeType[:size]]]` with role + size validation (`spec_cmd.go:172-214`); `synthesiseObjectiveFromAttachments` omits the file clause when filename is empty (`compiler.go:515-520`); legacy `hash=ROLE` preserved. | `TestSplitAttachFlag_Grammar` (14 cases, exists at `spec_cmd_test.go:14`); `TestCompile_Regression_AttachmentOnlyEmptyFilename` (`compiler_test.go:429`, asserts no `()`); `TestSpecCompile_BlackBox_AttachmentOnlyWithFilename`, `...LegacyHashRole`, `...InvalidAttachSizeRejected`, `...InvalidAttachRoleRejected` all exist and PASS. **Black-box**: scenario 3 objective = `Implement changes based on the attached requirements (requirements.md).` (no `()`); scenario 4 (legacy) = `...attached requirements.` (no `()`); scenarios 5a/5b (bad size) and 7 (unknown role) exit 1. | **CLOSED** |
| **MINOR-1** Text output path (`--json=false`) untested. | `TestSpecCompile_BlackBox_TextOutput` added. | Test exists at `spec_compile_blackbox_test.go:359` and PASS. **Black-box**: scenario 1 default output is human-readable text (starts with `TaskID:`), not JSON; contains Objective, Risk, Complexity, Confidence, AC-1/AC-2. | **CLOSED** |
| **MINOR-2** `--json` defaulted to `true` (inconsistent with every other `forge` command). | default flipped to `false` (`spec_cmd.go:79`: `fs.Bool("json", false, ...)`). | Verified at source: `jsonOut := fs.Bool("json", false, ...)`. **Black-box**: scenario 1 (no `--json`) emits text, not JSON. Existing JSON black-box tests pass `--json` explicitly (targeted run PASS). | **CLOSED** |
| **MINOR-3** `--priority` was not validated. | validation switch added (`spec_cmd.go:94-98`); unknown value exits non-zero echoing the bad value. | `TestSpecCompile_BlackBox_InvalidPriorityRejected` exists at `spec_compile_blackbox_test.go:397` and PASS. **Black-box**: scenario 6 `--priority BOGUS` → exit 1, stderr `Error: --priority must be one of LOW\|NORMAL\|HIGH\|URGENT, got "BOGUS"`, 0 stdout bytes. | **CLOSED** |
| **MINOR-4** Determinism test sorted `ComplexityReasons` before comparing. | `sameCompileResult` compares `ComplexityReasons` in order (no sort); unused `sort` import removed. | Verified at source: `compiler_test.go:677-688` compares in-order with an explicit "MINOR-4 review fix" comment; `compiler_test.go` imports are `errors/strings/testing` only (no `sort`). `TestCompile_Deterministic` ×20 PASS. | **CLOSED** |
| **MINOR-5** Dead code `_ = i` in `writeSpecCompileText`. | replaced with `for _, ac := range` (`spec_cmd.go:230`). | Verified at source: loop is `for _, ac := range spec.AcceptanceCriteria`; no `_ = i`. `go vet ./...` exit 0; `gofmt -l .` clean. | **CLOSED** |
| **MINOR-6** Whitespace-only description bypasses the CLI empty-check (pre-existing). | not fixed in this task; tracked as `FU-M14-02-9`. | **Reproduced independently** (scenario 11): `"    "` → exit 0, `Objective=""`, `Confidence=LOW`, 1 clarification; compiled spec fails `ValidateSpecification` (empty objective + 0 ACs, `specification.go:162,165`); **no durable state written** (temp HOME empty before/after). No panic, no corruption, no persisted invalid spec. Defect is byte-identical in `ee738b2` and `fad77d5`. | **ACCEPTED_FOLLOW_UP** (`FU-M14-02-9`) |

MAJOR-1 and MINOR-1..5 are **CLOSED**. MINOR-6 is **ACCEPTED_FOLLOW_UP** — it
does not violate any mandatory AC (the compiler degrades safely to LOW +
clarification; the invalid spec cannot be persisted; the CLI is read-only).

## Acceptance matrix

| Criterion | Production implementation | Automated evidence | Independent black-box result | Status |
|---|---|---|---|---|
| **AC1** Complete valid specification for typical tasks (bugfix/feature/UI/auth-security-payment) | `task.Compile` (`compiler.go:101-157`); structured-section parser; risk cascade reuses `risk.Classify`; local complexity classifier; `deriveVisualRequirements` | Unit: `TestCompile_Fixture_Bugfix/Feature/UITask/AuthPaymentRisky`, `TestCompile_RiskLevels`, `TestCompile_ACsHaveStableIDs`, `TestCompile_SynthesisesACWhenAbsent` (each re-validated through production `ValidateSpecification`). Black-box: `TestSpecCompile_BlackBox_DeterministicOutput`, `...AttachmentMetadata` | scenarios 1/2 (feature, HIGH), 8 (auth+payment → R4), 9 (UI + DESIGN_REFERENCE → `VisualRequirements.Required=true`); every compiled spec passes validation | **MET** |
| **AC2** Unsafe ambiguities explicitly flagged (vague/attachment-only/high-risk); no fabricated requirements | `deriveConfidenceAndClarifications` (`compiler.go:691-761`): LOW + Clarification for vague/attachment-only; R4 surfaces a Clarification even with structured sections | Unit: `TestCompile_Fixture_VagueTask`, `...AttachmentOnly`, `...AuthPaymentRisky`, `TestCompile_RejectsEmptyInput`. Black-box: `...VagueInputLowConfidence`, `...RiskyTaskFlagsClarification`, `...AttachmentOnly*` | scenario 8 (security) → R4 + MEDIUM + 1 clarification; scenarios 3/4 (attachment-only) → LOW + 1 clarification; scenario 11 (whitespace) → LOW + 1 clarification, no fabricated objective | **MET** |
| **AC3** Identical input ⇒ deterministic output (compiler, CLI, repeated runs; fixed list ordering) | `Compile` is pure (no I/O, no clock, no randomness); ordered slices appended in fixed sequence; `AttachmentRoles` JSON-serialised with sorted keys | Unit: `TestCompile_Deterministic` (in-order `ComplexityReasons`), `...AcrossVaryingWhitespace`, `...ACsHaveStableIDs`. Black-box: `TestSpecCompile_BlackBox_DeterministicOutput` (byte-identical JSON) | 20× unit PASS; 10× black-box PASS; 10 manual binary JSON runs byte-identical (SHA-256 `87a6bc71…`); 10 text runs byte-identical; timestamp fields zero-valued (`0001-01-01T00:00:00Z`) | **MET** |
| **AC4** Compiler never mutates a locked specification | `Compile` returns `Specification{Version:0, Locked:false}`; pure (no storage handle by construction); `SpecificationStore.Save` enforces `ErrSpecificationLocked` (M14-01) | Unit: `TestCompile_NeverMutatesLockedSpec`. Integration (production store + SQLite + audit): `TestCompile_LockedSpecCannotBeMutatedViaSave` (compile→save→lock→compile+save → locked v1 byte-identical `Objective=="Original."`, fresh v2 allocated, provenance preserved) | PASS (race-clean). Boundary explicitly proven: the compiler creates a new spec and never updates an existing version; `forge spec compile` is read-only (no storage handle). | **MET** |
| **AC5** CLI production path (registered, help, default text, `--json`, invalid priority/size/role rejected, attachment-only + legacy grammar, deterministic, no partial durable state on validation failure) | `forge spec compile` dispatched from `cli.go` switch; `specCompile` (`spec_cmd.go:70-152`); `splitAttachFlag` grammar; priority validation; `--json` opt-in | Black-box: `TestSpecCompile_BlackBox_TextOutput`, `...DeterministicOutput`, `...InvalidPriorityRejected`, `...InvalidAttachRoleRejected`, `...InvalidAttachSizeRejected`, `...AttachmentOnlyWithFilename`, `...AttachmentOnlyLegacyHashRole`, `...MissingProject`, `...EmptyInput` | scenarios 1–11 reproduced through compiled `/tmp/forge-m14-02-accept`; default=text, `--json`=valid JSON, invalid priority/size/role exit non-zero with clear errors, attachment-only + legacy work, output deterministic, **no durable state written** by any path (read-only compiler) | **MET** |
| **AC6** Remediation findings closed | MAJOR-1 + MINOR-1..5 fixes each guarded by ≥1 named regression test | `TestSplitAttachFlag_Grammar`, `TestCompile_Regression_AttachmentOnlyEmptyFilename`, `TestSpecCompile_BlackBox_TextOutput`, `...InvalidPriorityRejected`, `TestCompile_Deterministic` (in-order), `go vet`/`gofmt` clean | every regression test exists and PASS; sensitivity checks documented in re-review are consistent with the current tests; MINOR-6 reproduced and accepted as `FU-M14-02-9` | **MET** |

All six mandatory acceptance criteria are proven by automated evidence
independently re-run at unit, integration, black-box, and race levels.

## Commands executed

All commands ran from the disposable acceptance worktree at `02726b8` with a
fresh build. Toolchain: `go version go1.26.5 darwin/arm64`.

| Command | Exit code | Result |
|---|---:|---|
| `git merge-base --is-ancestor ee738b2 fad77d5` | 0 | ANCESTOR_OK |
| `git merge-base --is-ancestor fad77d5 02726b8` | 0 | ANCESTOR_OK |
| `git show --stat --oneline 02726b8` | 0 | re-review commit adds only `M14-02_REVIEW.md` (256 ins) |
| `git diff --name-only f894f648..fad77d5 \| grep spec/baseline/gate/policy/merge` | 1 | no spec/baseline/gate/policy/merge files touched (clean scope) |
| `go build -o /tmp/forge-m14-02-accept ./cmd/forge` | 0 | compiled binary produced |
| `/tmp/forge-m14-02-accept gate baseline` | 0 | schema 1, baseline 1, doc path correct |
| `/tmp/forge-m14-02-accept gate next --manifest docs/reviews/m14/M14-01.manifest.json` | 0 | predecessor M14-01 ACCEPTED |
| `go vet ./...` | 0 | clean |
| `gofmt -l .` | 0 | clean (no files listed) |
| `go test -count=1 -run 'TestCompile\|TestSpecCompile\|TestSplitAttachFlag' ./internal/task/ ./internal/cli/` | 0 | PASS (task 0.607s, cli 2.274s) |
| `go test -count=20 -run 'TestCompile_Deterministic$' ./internal/task/` | 0 | PASS (20×) |
| `go test -count=10 -run 'TestSpecCompile_BlackBox_DeterministicOutput' ./internal/cli/` | 0 | PASS (10×, byte-identical) |
| `go test -race -count=1 -run 'TestCompile\|TestSpecCompile\|TestSplitAttachFlag' ./internal/task/ ./internal/cli/` | 0 | PASS, no race |
| `make check` | 0 | gofmt clean + `go vet ./...` clean + full suite green (FAIL_COUNT 0; every package `ok`; no M0–M13 + M14-00/M14-01 regression) |
| `go test -race ./...` | 0 | every package `ok`; **no FAIL, no race detected** |

No skipped/manual/opt-in test is the sole evidence for a mandatory criterion.
The black-box tests run under both `make check` and `go test -race ./...`
(verified: full suite green, race detector clean).

## Text and JSON output verification

**Text output (scenario 1, no `--json`):** exit 0; output begins `TaskID:`
(plain text, not JSON). Contains `Objective: Add a retry button to the login
form.`, `Risk: R1`, `Complexity: C1`, `Confidence: HIGH`, `AC-1`, `AC-2`.
Output is non-empty and human-readable.

**JSON output (scenario 2, `--json`):** exit 0; output parses as valid JSON
with `python3 -m json.tool`. Top-level `{ "result": { "Specification": …,
"Confidence": …, "UncertaintyReasons", "Clarifications", "RiskReasons",
"ComplexityReasons", "AttachmentRoles" } }`. Specification carries every
applicable field: `TaskID`, `Version=0`, `Locked=false`, `Objective`,
`AcceptanceCriteria` (stable `AC-1`/`AC-2`), `NonGoals`, `Assumptions`,
`Constraints`, `ProposedScope`, `Risk`, `Complexity`, `VisualRequirements`,
`CreatedAt`, `CreatedBy`, `LockedAt`, `LockedBy`. `CreatedAt`/`LockedAt` are
zero-valued (`0001-01-01T00:00:00Z`) and `CreatedBy`/`LockedBy` are `""` —
the compiler is pure and sets no timestamps, so no nondeterministic value
appears in the output.

## Attachment grammar verification

**Extended grammar (scenario 3):** `--attach sha256:feedface=REQUIREMENTS:requirements.md:text/markdown:512`
→ objective `Implement changes based on the attached requirements (requirements.md).`
(filename reflected; **no** degenerate `()`), `Confidence=LOW`, 1
clarification, spec validates. `AttachmentRoles["sha256:feedface"] = "REQUIREMENTS"`.

**Legacy grammar (scenario 4):** `--attach sha256:abc=REQUIREMENTS` → objective
`Implement changes based on the attached requirements.` (defensive clause
omitted, **no** `()`), `Confidence=LOW`, 1 clarification. Backward compatible.

**Invalid size (scenario 5):** `...:-5` (negative) and `...:notanum`
(non-numeric) both → exit 1 with `Error: --attach expects hash=ROLE[:filename[:mimeType[:size]]], got …`.

**Unknown role (scenario 7):** `--attach sha256:abc=BOGUS_ROLE` → exit 1 with
the same clear `--attach` error.

**UI + DESIGN_REFERENCE (scenario 9):** `VisualRequirements.Required=true`,
`References=["sha256:designdeadbeef"]`, role mirrored in `AttachmentRoles`.

## Risk and ambiguity verification

- **Vague input:** `"fix it"` → `Confidence=LOW` + ≥1 clarification (unit +
  black-box).
- **Security/auth/payment (scenario 8):** `Risk=R4`, `Confidence=MEDIUM`, 1
  clarification (`Confirm the security/money impact and review posture.`) even
  with explicit structured sections. High-risk classification is correct; no
  fabricated requirements.
- **Attachment-only (scenarios 3/4):** `Confidence=LOW` + clarification; the
  compiler does not read attachment content and surfaces this explicitly.
- **Whitespace-only (scenario 11):** `Confidence=LOW` + clarification; no
  fabricated objective; compiled spec fails validation (see follow-up section).
- **Empty input:** `""` (no attachment) → exit 1 `description or attachment is
  required`.

No unsafe ambiguity is hidden behind a fabricated assumption.

## Determinism verification

- `TestCompile_Deterministic` ×20: PASS (in-order `ComplexityReasons`, no sort).
- `TestSpecCompile_BlackBox_DeterministicOutput` ×10: PASS (byte-identical JSON).
- 10 manual binary JSON runs (`forge spec compile --json ...` identical input):
  all share SHA-256 `87a6bc7178472b12ffd246a0a0b14fee6542f8fb3b4cd49c9345db2938ee6834`
  — byte-identical (`cmp run_1 run_10` → identical; 1 unique hash).
- 10 manual binary text runs: byte-identical (1 unique hash).
- Production map usage audited (`compiler.go`, `spec_cmd.go`): maps are used
  only as keyed accumulators (`parseSections`) or as the `AttachmentRoles`
  output field, which `encoding/json` serialises with sorted keys. Ordered
  outputs (ACs, References, reasons) are built by appending over input slices
  in fixed sequence. No map iteration feeds ordered output.

## Locked-spec boundary verification

The compiler is pure: it has no storage handle by construction and always
returns `Specification{Version:0, Locked:false}`. It creates a new
specification on every call and never updates an existing version.

`TestCompile_LockedSpecCannotBeMutatedViaSave` (`compiler_test.go:496`) is the
headline end-to-end proof at the production composition root (baseline rule 7):
it drives the real `SpecificationStore` + SQLite + audit path
(`newSpecStoreDB`) through compile → Save (v1) → Lock → compile a *changed*
description ("TAMPERED") → Save (v2). Assertions: `saved2.Version == 2` (not
1); `store.Get(taskID, 1).Objective == "Original."` (locked v1 byte-identical);
`Locked == true` and `LockedBy == "reviewer-1"` (provenance preserved). The
compiler cannot mutate a locked spec by construction; the storage layer
(M14-01) rejects any attempt to overwrite a locked version. PASS (race-clean).

The implementation report does not overclaim: it states the compiler is
read-only and that persistence is the caller's responsibility — verified
accurate against the code (`forge spec compile` writes no durable state; the
temp HOME is empty before and after every invocation).

## Race verification

`go test -race ./...` → exit 0; every package `ok`; no FAIL, no race detected.
Targeted `-race -count=1` over `TestCompile|TestSpecCompile|TestSplitAttachFlag`
→ PASS, no race. The compiler is pure (no goroutines, no shared mutable
state); the locked-spec end-to-end test exercises the real storage transaction
path under the race detector and is clean.

## Whitespace-only follow-up (MINOR-6 / FU-M14-02-9)

Reproduced independently through the compiled binary in an isolated temp HOME:

| Input | Exit | stdout | stderr | Spec created? | Passes `ValidateSpecification`? | Durable state? |
|---|---:|---|---|---|---|---|
| `""` (no attachment) | 1 | (empty) | `Error: description or attachment is required` | no (CLI guard) | n/a | none |
| `"    "` (whitespace-only) | 0 | JSON with `Objective=""`, `Confidence=LOW`, 1 clarification | (empty) | yes (in-memory) | **NO** — empty objective (`specification.go:162`) + 0 ACs (`specification.go:165`) | **none** (temp HOME empty before/after; `forge spec compile` is read-only) |

Assessment:

- No panic, no corruption, no security bypass.
- The invalid specification is **not** persisted: `forge spec compile` has no
  storage handle, and even if a caller tried `SpecificationStore.Save`, it
  runs `ValidateSpecification` before any write and would reject it.
- AC2 is satisfied (ambiguity is flagged: LOW + clarification).
- The defect is pre-existing (byte-identical CLI guard in `ee738b2` and
  `fad77d5`), not introduced or worsened by remediation.
- No mandatory AC is violated.

MINOR-6 is accepted as a non-blocking follow-up (`FU-M14-02-9`). It is **not**
fixed in this acceptance session (acceptance does not modify production code).
Required future fix: trim before the empty check
(`strings.TrimSpace(description) == ""`) so whitespace-only is rejected
consistently, or a black-box test pinning the current graceful-degradation
behaviour.

## Sensitivity evidence check

The independent re-review (`M14-02_REVIEW.md` part II, "Sensitivity checks")
documented four mutation checks performed in a disposable worktree at
`fad77d5`, each fully reverted afterwards:

| Mutation | Expected failing test | Re-review result | Acceptance cross-check |
|---|---|---|---|
| Revert `--json` default `false`→`true` | `TestSpecCompile_BlackBox_TextOutput` | FAIL (output became JSON) — catches it | default verified `false` at `spec_cmd.go:79`; scenario 1 default is text |
| Remove defensive empty-filename handling | `TestCompile_Regression_AttachmentOnlyEmptyFilename` + `...LegacyHashRole` | both FAIL with `()` objective | defensive clause present at `compiler.go:515-520`; scenarios 3/4 show no `()` |
| Inject map-iteration nondeterminism into `classifyComplexity` reasons | `TestCompile_Deterministic` | FAIL (in-order comparison catches drift) | in-order comparison confirmed at `compiler_test.go:677-688`; 20× PASS |
| Remove `--priority` validation switch | `TestSpecCompile_BlackBox_InvalidPriorityRejected` | FAIL (invalid priority no longer rejected) | validation present at `spec_cmd.go:94-98`; scenario 6 exits 1 |

All four sensitivity checks are documented and are consistent with the current
tests and source. No contradiction found.

## Scope and regression assessment

- `git diff --stat f894f648..fad77d5` touches only: implementation report,
  review report, CLI dispatch (2 lines), help text, `spec_cmd.go`,
  `spec_cmd_test.go`, `spec_compile_blackbox_test.go`, `compiler.go`,
  `compiler_test.go`. **No** changes to `docs/spec/NEUROFORGE_SPEC.md`,
  `docs/engineering/ENGINEERING_BASELINE.md`, `internal/enggate`,
  `internal/policy`, `internal/merge`, `internal/storage`, `internal/daemon`,
  or any gate/baseline enforcement.
- No scope creep; no M14-03 work present (no `M14-03*` files; the active gate
  is still against M14-01).
- Remediation did not remove any useful check; existing JSON black-box tests
  were updated (not deleted) to pass `--json` explicitly after the default
  flip.
- Legacy `hash=ROLE` grammar preserved (verified in code, help, and binary).
- No TODO/FIXME/`panic("unimplemented")` in the new production files;
  `gofmt -l .` clean; `go vet ./...` clean.
- `make check` is green across every M0–M13 + M14-00/M14-01 package; no
  regression. `go test -race ./...` is clean.

## Known limitations and accepted follow-ups

1. **FU-M14-02-9 (MINOR-6, accepted non-blocking follow-up):** whitespace-only
   description bypasses the CLI empty-check and reaches the compiler, which
   degrades safely (LOW + clarification) but the resulting spec fails
   `ValidateSpecification`. Pre-existing; safe; no durable state. See the
   dedicated section above.
2. **FU-M14-02-1:** `forge spec {create,get,lock,versions}` over the daemon
   transport (CRUD surface) — out of scope; not started.
3. **FU-M14-02-2:** wire `Compile` into `forge task add` — out of scope.
4. **FU-M14-02-3:** cross-version AC identity tracking — out of scope.
5. **FU-M14-02-4:** content-aware attachment parsing — out of scope by design
   (compiler consumes metadata only).
6. **FU-M14-02-5:** ADR for the package-boundary decision.

None of the above obstructs any mandatory acceptance criterion or the
sequential gate.

## Verdict

**ACCEPTED**

Every mandatory acceptance criterion (AC1 complete valid specification, AC2
safe ambiguity handling, AC3 determinism, AC4 locked-spec invariant, AC5 CLI
production path, AC6 remediation findings closed) is met and proven by passing
automated evidence independently re-run at unit, integration, black-box, and
race levels. `make check` is green (FAIL_COUNT 0); `go test -race ./...` is
clean (no race detected); `go vet ./...` and `gofmt -l .` are clean.

- M14-01 is `ACCEPTED` (predecessor gate exit 0).
- The re-review examined the exact remediated candidate `fad77d5...` and
  returned `REVIEW_APPROVED`.
- MAJOR-1 and MINOR-1..5 are genuinely **CLOSED**; each fix is guarded by a
  named regression test whose assertion was cross-checked against the source.
- MINOR-6 is a safe, pre-existing, AC-compliant non-blocking follow-up
  (`FU-M14-02-9`); reproduced independently.
- Determinism is proven by 20× unit, 10× black-box, and 10 manual binary runs
  (byte-identical, single SHA-256).
- Actor separation is pairwise distinct across implementation / remediation /
  review / re-review / acceptance.
- No scope creep; product spec, baseline, and gate enforcement untouched.
- The manifest passes `forge gate validate` and `forge gate next` returns
  exit 0.

The successor task **M14-03 may now start** (`forge gate next --manifest
docs/reviews/m14/M14-02.manifest.json` returns exit 0).
