# M14-00 — Final Independent Acceptance

**Task:** M14-00 — Strict baseline, evidence model and sequential gate.
**Acceptor actor:** `M14-00-accept-session` (distinct from `M14-00-impl-session`
and `M14-00-review-session`).
**Accepted candidate SHA:** `c7f8f573884e9df55700a06885b49114891b8396`
(production code last changed at `3bad1b5f97e3e67f850a73577b429a289236b0bd`; the
tip commit only adds the re-review report).
**Starting SHA:** `00e0902804cf46ca85f4602e9d36fc472cb95419`.
**Verdict:** `ACCEPTED`.

## Preconditions

- Last review verdict (`docs/reviews/m14/M14-00_REVIEW.md`) is `REVIEW_APPROVED`.
  ✓
- Working tree has no uncommitted M14-00 production changes. The only
  unstaged/untracked entries are pre-existing review docs unrelated to M14-00
  (`docs/reviews/MINIMAL_RUN_*`, `docs/reviews/M12_M13_REVIEW.md`), present at
  the starting SHA and untouched by this task. ✓
- Implementation report present at
  `docs/reviews/m14/M14-00_IMPLEMENTATION.md` (with the Review remediation
  section). ✓
- Canonical evidence manifest present at
  `docs/reviews/m14/M14-00.manifest.json`; its `actors` are three pairwise
  distinct role-bound ids. ✓

## Acceptance matrix

| AC | Requirement | Evidence (automated) | Acceptance assessment |
|----|-------------|----------------------|-----------------------|
| AC1 | Baseline automatically available to every coding agent | integration `TestActiveBaselinePathExists`, `TestAgentsMandatesBaseline`; blackbox `TestGateBlackBoxBaseline` | **Met.** Doc exists at the canonical path with version 1; AGENTS.md links it and marks it MANDATORY; `forge gate baseline` prints version + path through the compiled binary. |
| AC2 | No done/accepted without required evidence | unit positive + 11 negatives (`TestValidateTransitionPositiveFullFlow`, `TestNegativeMissingTestEvidence`, `TestNegativeMissingBlackBoxProof`, `TestNegativeMissingUnit`, `TestNegativeMissingMakeCheck`, `TestNegativeSelfReview`, `TestNegativeSelfAcceptance`, `TestNegativeAcceptorEqualsReviewer`, `TestNegativeSchemaMismatch`, `TestNegativeBaselineMismatch`, `TestNegativeTerminalWithoutNote`); blackbox `TestGateBlackBoxNegativeMissingEvidence`, `TestGateBlackBoxNegativeSameActor`, `TestGateBlackBoxNegativeInvalidTransition` | **Met.** Every rule has a failing-then-passing test; structural completeness and actor separation are enforced by `forge gate validate`. |
| AC3 | No successor unlocked before ACCEPTED | unit `TestNegativeCanStartNextBeforeAccepted` + the BLOCKER-1 regressions `TestNegativeCanStartNextFabricatedAcceptedWrongSchema`, `TestNegativeCanStartNextFabricatedAcceptedSelfAccept`; blackbox `TestGateBlackBoxNegativeNextBeforeAccepted`, `TestGateBlackBoxNegativeNextFabricatedAccepted`; positive in `TestGateBlackBoxPositiveFlow` | **Met.** `forge gate next` now validates the predecessor's ACCEPTED claim (not just its state field); a fabricated `state: "ACCEPTED"` manifest is rejected through the compiled binary. |
| AC4 | No undocumented production-readiness claim | integration `TestBaselineDefinesEvidenceLevelsAndHonesty`, `TestActiveBaselinePathExists`; README/COMPLIANCE_MATRIX/help audited | **Met.** Honesty rule (§9) and evidence model pinned by automated regression; no M14-contradicting overclaim in audited docs; M14-00's own docs make no production-readiness claim. |

## Exact commands and results (independent re-run at the accepted SHA)

```sh
go clean -testcache
make check                                                        # exit 0
go test -race ./internal/enggate/                                 # PASS
go test -race -run 'TestGateBlackBox' -count=1 ./internal/cli/    # PASS
gofmt -l .                                                        # clean
go test ./internal/enggate/ -count=1                              # PASS (27 tests)
go test -run 'TestGateBlackBox' -count=1 ./internal/cli/          # PASS (9 tests)
```

All green; `make check` exit 0 across every package (no regression in M0–M13
suites).

## Black-box evidence (compiled `forge` binary, observable)

The acceptance scenario drives the production binary, not internal Go objects:

```sh
$ ./forge gate baseline
active schema_version: 1
active baseline_version: 1
baseline document: docs/engineering/ENGINEERING_BASELINE.md

$ ./forge gate validate --manifest docs/reviews/m14/M14-00.manifest.json
OK: task "M14-00" transition REVIEW_APPROVED -> ACCEPTED is legal under baseline v1   # exit 0

$ ./forge gate next --manifest docs/reviews/m14/M14-00.manifest.json
OK: predecessor "M14-00" is ACCEPTED; successor task may start                       # exit 0

# Counterexample (BLOCKER-1 regression) — rejected:
$ ./forge gate next --manifest /tmp/evil.json   # state=ACCEPTED, schema 999, no evidence
forge: gate next: BLOCKED: enggate: predecessor "M14-EVIL" claims ACCEPTED but is not validly accepted: schema_version 999 != active 1
# exit 1
```

Additionally, `internal/cli.TestGateBlackBoxPositiveFlow` drives the full
lifecycle `STARTED → IMPLEMENTED_TESTED → REVIEW_APPROVED → ACCEPTED` plus
`gate next` through the compiled binary, and nine `TestGateBlackBox*` tests
assert exit codes + stderr text.

## Cross-cutting acceptance checks

- **Production wiring:** `forge gate` is wired into the real CLI dispatch and is
  observable through the compiled binary. It is a pure, read-only META command;
  it does not touch daemon/storage/scheduler or weaken any product invariant. ✓
- **No fake/demo/stub leakage:** no production stubs, TODOs, or hard-coded fake
  data. Test fixtures live inside `_test.go` files only. ✓
- **No policy/security bypass:** the change only adds enforcement; it removes
  none. The BLOCKER-1 fix hardened the AC3 enforcement point. ✓
- **Restart / idempotency / concurrency:** `enggate` is pure and stateless; the
  only I/O is read-only `LoadManifest`. Race detector clean. No migration
  surface (no schema/storage changes). ✓
- **Backward compatibility:** additive change; no existing command altered; no
  product-spec change. ✓
- **Scope:** matches the scoped task; no successor M14 task started; ADR for the
  new package boundary deferred as FU-2 (not blocking). ✓
- **No regression of prior accepted work:** `make check` covers all M0–M13
  packages and is green. M14-00 is the first M14 task (no prior M14 predecessor). ✓

## Residual limitations (non-blocking, documented)

1. Actor separation is enforced at the data level (distinct actor ids), not
   cryptographically. Residual trust belongs to the project owner who schedules
   the three phases. (Baseline §6.)
2. `ValidateTransition` validates structural completeness and rules, not that
   `evidence[].reference` strings resolve to real passing tests. Reference
   authenticity is established by independent re-runs (this acceptance did so).
   (Baseline §4, made explicit by the MINOR-1 remediation.)
3. CI integration of `forge gate validate`/`next` is a follow-up (FU-1).

## Verdict

`ACCEPTED`. Every mandatory acceptance criterion is proven by passing automated
evidence at unit, integration, and black-box levels; `make check` is green; the
race detector is clean on affected packages; the compiled binary demonstrates the
gate end-to-end and correctly rejects the BLOCKER-1 counterexample. The
successor M14 task **may start** (`forge gate next` on this manifest returns
exit 0).
