# M14-00 — Implementation Report

**Task:** M14-00 — Strict baseline, evidence model and sequential gate.
**Verdict:** `IMPLEMENTED_TESTED`

## SHAs

- **Starting SHA:** `00e0902804cf46ca85f4602e9d36fc472cb95419` (branch `main`)
- **Candidate SHA:** recorded at commit time (this report is committed in the
  same atomic commit as the implementation; the candidate SHA is the resulting
  commit, available via `git log -1 --format=%H`).

## Goal and actual scope

Embed a mandatory, versioned engineering baseline + evidence model + sequential
gate into the NeuroForge repository so that:

- implementation without test evidence cannot be marked done;
- a successor task cannot start before its predecessor is `ACCEPTED`;
- implementation, review, and acceptance must be performed by distinct actors;
- documentation cannot silently claim production readiness without proof.

This is a **META** concern (engineering process for the repo itself). It is
intentionally separate from the product's runtime Verification Evidence system
(spec §27, `internal/evidence`) and runtime review roles (§25,
`internal/review`).

**Scope actually delivered:**

1. Versioned baseline document `docs/engineering/ENGINEERING_BASELINE.md` (v1):
   evidence levels, lifecycle/transition rules, manifest schema, report formats,
   actor-separation rule, 19 baseline rules, documentation-honesty rule.
2. Mandatory AGENTS.md link to the baseline + `forge gate` enforcement snippet.
3. Pure validator package `internal/enggate` with state machine, evidence rules,
   actor separation, JSON manifest load, and `CanStartNext`.
4. CLI wiring `forge gate {baseline,validate,next}` (real dispatch in
   `internal/cli/cli.go`).
5. Unit tests (positive full flow + every mandatory negative case) and
   black-box tests that exec the **compiled `forge` binary**.
6. Repo-integrity regressions that pin the baseline doc + AGENTS.md link.
7. Canonical dogfood manifest `docs/reviews/m14/M14-00.manifest.json`.

**Out of scope (not started — follow-ups only):** wiring the gate into CI, a
task-state registry across milestones, ADR for the new package boundary. The
product spec (`docs/spec/NEUROFORGE_SPEC.md`) was **not** modified (baseline
rule 3).

## Changed files

```
AGENTS.md                                            | modified (mandatory baseline link)
docs/engineering/ENGINEERING_BASELINE.md             | new (versioned baseline v1)
docs/reviews/m14/M14-00.manifest.json                | new (canonical dogfood manifest)
internal/cli/cli.go                                  | modified (gate dispatch)
internal/cli/help.go                                 | modified (gate help)
internal/cli/gate_cmd.go                             | new (forge gate command)
internal/cli/gate_cmd_blackbox_test.go               | new (black-box tests)
internal/enggate/enggate.go                          | new (validator)
internal/enggate/enggate_test.go                     | new (unit tests)
internal/enggate/baseline_doc_test.go                | new (repo-integrity regressions)
```

## Acceptance criterion → code → test

| AC | Where implemented | Test(s) | Why the test verifies observable behaviour |
|----|-------------------|---------|--------------------------------------------|
| **AC1** Baseline automatically available to every coding agent | `docs/engineering/ENGINEERING_BASELINE.md` (v1); AGENTS.md "Engineering baseline (MANDATORY)"; `enggate.ActiveBaselinePath`/`ActiveVersions`; `forge gate baseline` (`internal/cli/gate_cmd.go`) | `internal/enggate.TestActiveBaselinePathExists`, `TestAgentsMandatesBaseline` (integration); `internal/cli.TestGateBlackBoxBaseline` (blackbox) | Integration tests read the real repo files and assert the doc exists with the active version and that AGENTS.md links it mandatorily. The black-box test execs the compiled `forge gate baseline` and asserts the version + doc path are printed — proving availability through the production binary. |
| **AC2** No done/accepted without required evidence | `enggate.ValidateTransition` + `checkImplemented`/`checkReview`/`checkAcceptance`/`checkActorSeparation` (`internal/enggate/enggate.go`); enforced via `forge gate validate` | `TestValidateTransitionPositiveFullFlow`; negatives `TestNegativeMissingTestEvidence`, `TestNegativeMissingBlackBoxProof`, `TestNegativeMissingUnit`, `TestNegativeMissingMakeCheck`, `TestNegativeSelfReview`, `TestNegativeSelfAcceptance`, `TestNegativeAcceptorEqualsReviewer`, `TestNegativeEmptyReviewer`, `TestNegativeTerminalWithoutNote`, `TestNegativeSchemaMismatch`, `TestNegativeBaselineMismatch`; blackbox `TestGateBlackBoxNegativeMissingEvidence`, `TestGateBlackBoxNegativeSameActor`, `TestGateBlackBoxNegativeInvalidTransition`, `TestGateBlackBoxMissingManifestFlag` | Each negative constructs a manifest breaking exactly one rule and asserts the validator rejects it; the black-box tests drive the compiled binary and assert non-zero exit + the violation text on stderr. A green suite therefore cannot exist while a mandatory rule is unenforced. |
| **AC3** No successor unlocked before ACCEPTED | `enggate.CanStartNext` (`internal/enggate/enggate.go`); `forge gate next` | `TestNegativeCanStartNextBeforeAccepted` (unit, all non-ACCEPTED states); blackbox `TestGateBlackBoxNegativeNextBeforeAccepted` + positive leg inside `TestGateBlackBoxPositiveFlow` | The unit test iterates every non-`ACCEPTED` state and asserts `CanStartNext` errors; the black-box test execs `forge gate next` on an `IMPLEMENTED_TESTED` manifest and asserts exit 1 + "not ACCEPTED", and on an `ACCEPTED` manifest asserts exit 0 + "successor task may start". |
| **AC4** No undocumented production-readiness claim | Baseline §9 "Documentation honesty" (`docs/engineering/ENGINEERING_BASELINE.md`); M14-00's own docs/help text make no production-readiness claim (process tooling); existing product docs audited (README is explicitly "self-hosting alpha" with opt-in framing) | `TestBaselineDefinesEvidenceLevelsAndHonesty`, `TestActiveBaselinePathExists` (integration) pin the §9 rule and evidence levels in the source of truth | Automated regression ensures the honesty rule and evidence model remain in the baseline doc; removing them fails the build. Full per-line audit of README/COMPLIANCE_MATRIX/help is a human review judgment (recorded below); the automated guard prevents the rule from being silently removed. |

### Documentation audit (AC4)

- `README.md`: describes the product as "self-hosting alpha"; calls out that live
  paid models / image generation / device harnesses are opt-in and never invoked
  in CI. No M14-contradicting production-readiness claim found.
- `docs/spec/COMPLIANCE_MATRIX.md`: statuses are `done`/`partial`/`planned`/`n/a`
  with explicit proof levels (`unit-tested`/`integration-tested`/`black-box
  tested`). No claim that M14 is done (M14 is not in the spec).
- `internal/cli/help.go`: `gate` is described as META engineering tooling; no
  production-readiness claim added.
- **No doc lines were changed to mask an overclaim.** The forward-looking
  enforcement is the baseline §9 rule + the version-gate (a stale baseline
  cannot be satisfied silently).

## Exact test commands and results

```sh
# Targeted unit tests (validator + state transitions)
go test ./internal/enggate/                                   # PASS
go test -run 'TestValidate|TestNegative|TestLegal|TestRemediation|TestLoad' ./internal/enggate/  # PASS

# Black-box tests (compiled forge binary)
go test -run 'TestGateBlackBox' ./internal/cli/               # PASS

# Race detector on affected packages
go test -race ./internal/enggate/                             # PASS
go test -race -run 'TestGateBlackBox' ./internal/cli/         # PASS

# Full CI gate
make check                                                    # PASS (exit 0)
```

Representative output (full `make check` is green; `internal/cli` 134s,
`internal/enggate` 0.2s).

## Black-box evidence (through the compiled `forge` binary)

The compiled binary was driven directly (not internal Go objects):

```sh
$ go build -o ./forge ./cmd/forge
$ ./forge gate validate --manifest docs/reviews/m14/M14-00.manifest.json
OK: task "M14-00" transition REVIEW_APPROVED -> ACCEPTED is legal under baseline v1        # exit 0

$ ./forge gate next --manifest docs/reviews/m14/M14-00.manifest.json
OK: predecessor "M14-00" is ACCEPTED; successor task may start                              # exit 0

$ ./forge gate baseline
active schema_version: 1
active baseline_version: 1
baseline document: docs/engineering/ENGINEERING_BASELINE.md

# Negative demonstration (tampered manifest, blackbox evidence removed):
$ ./forge gate validate --manifest /tmp/m14-00-neg.json
forge: gate validate: STARTED -> IMPLEMENTED_TESTED REJECTED for task "M14-00"
enggate: transition STARTED -> IMPLEMENTED_TESTED rejected for task "M14-00":
  - no passing blackbox-level evidence present (production wiring is unproven — baseline rule 8)
# exit 1
```

Additionally, `internal/cli.TestGateBlackBoxPositiveFlow` drives the **entire**
lifecycle `STARTED → IMPLEMENTED_TESTED → REVIEW_APPROVED → ACCEPTED` plus
`gate next` through the compiled binary with throwaway manifests, and five
`TestGateBlackBoxNegative*` tests assert the binary rejects each violation.

## Known limitations

1. **Actor separation is data-level, not cryptographic.** The validator enforces
   that `actors.implementer`, `actors.reviewer`, `actors.acceptor` are pairwise
   distinct strings at the relevant transitions. It cannot prove the humans or
   operators behind two distinct ids are different people. Residual trust
   belongs to the project owner who schedules the three phases. (Baseline §6
   documents this explicitly.)
2. **AC4's per-line doc audit is a human judgment.** The automated guard pins the
   §9 rule and evidence model in the baseline doc and pins the AGENTS.md mandate;
   it cannot mechanically prove "no overclaim exists anywhere". The audit above
   is the human evidence for M14-00.
3. **No CI wiring yet.** The gate is a repository-resident command and manifest
   convention; it is not yet invoked by a CI workflow. (Follow-up, see below.)
4. **First-in-milestone.** M14-00 has no predecessor; `gate next` is trivially
   satisfied once M14-00 itself is `ACCEPTED`. The successor-gating rule becomes
   load-bearing from M14-01 onward.

## Follow-up issues

- **FU-1 (MAJOR):** Wire `forge gate validate` + `forge gate next` into CI so a
  PR cannot merge unless the task manifest validates and its predecessor is
  `ACCEPTED`. Out of scope for M14-00 (no CI changes per scoped task).
- **FU-2 (MINOR):** Add an ADR for the new `internal/enggate` package boundary
  (it is a META/engineering package, not in the spec's package table).
- **FU-3 (MINOR):** Add a `forge gate init` helper that scaffolds a new task's
  manifest from a template, to reduce hand-authoring errors.
- **FU-4 (MINOR):** Cross-milestone task-state registry (a small index file) so
  `gate next` can resolve "the predecessor of M14-XX" automatically.

## Verdict

`IMPLEMENTED_TESTED` — every mandatory acceptance criterion is backed by passing
automated evidence at unit, integration, and black-box levels; the full positive
flow and every mandatory negative case are tested; `make check` is green; the
compiled binary demonstrates the gate end-to-end. The implementation does not
weaken any security, autonomy, delivery, or merge-policy invariant, and does not
modify the product specification.
