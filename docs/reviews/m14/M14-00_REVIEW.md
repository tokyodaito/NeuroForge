# M14-00 — Independent Review Report

**Task:** M14-00 — Strict baseline, evidence model and sequential gate.
**Reviewer actor:** `M14-00-review-session` (distinct from `M14-00-impl-session`).
**Reviewed candidate SHA:** `9ef4eda6756cc822cef0af1486e2741d3e0f44b2` (initial),
`3bad1b5f97e3e67f850a73577b429a289236b0bd` (after remediation).
**Starting SHA:** `00e0902804cf46ca85f4602e9d36fc472cb95419`
**Verdict:** `REVIEW_APPROVED` (re-review after remediation — see §"Re-review").

The review was performed against a clean checkout of the candidate SHA. The
diff is 11 files / +1927 lines, all additive except `AGENTS.md`, `cli.go`, and
`help.go`. No product-specification files were modified. No pre-existing review
artifacts in `docs/reviews/` were touched by the implementation commit.

## Independent re-run results

| Command | Result |
|---------|--------|
| `go test ./internal/enggate/ -v -count=1` | PASS (24 tests) |
| `go test -race ./internal/enggate/` | PASS |
| `go test -race -run 'TestGateBlackBox' -count=1 ./internal/cli/` | PASS (8 tests) |
| `make check` | PASS (exit 0) |
| `./forge gate validate --manifest docs/reviews/m14/M14-00.manifest.json` | exit 0 |
| `./forge gate next --manifest docs/reviews/m14/M14-00.manifest.json` | exit 0 |
| `./forge gate baseline` | exit 0 |

All gates green at the candidate SHA. The BLOCKER below is a robustness defect
demonstrated by constructing a counterexample manifest, not a failure of the
existing green suite.

## Acceptance matrix

| AC | Where implemented (candidate SHA) | Test(s) | Reviewer assessment |
|----|-----------------------------------|---------|---------------------|
| AC1 baseline available | `docs/engineering/ENGINEERING_BASELINE.md`; AGENTS.md "Engineering baseline (MANDATORY)"; `enggate.ActiveBaselinePath`/`ActiveVersions`; `forge gate baseline` | `TestActiveBaselinePathExists`, `TestAgentsMandatesBaseline`, `TestGateBlackBoxBaseline` | **Met.** Doc exists with version 1; AGENTS.md links it and marks it MANDATORY; the compiled binary prints the version and path. Observable through the real binary. |
| AC2 no done/accepted without evidence | `enggate.ValidateTransition` + per-transition checks; `forge gate validate` | positive + 11 negative unit tests; 3 black-box negative tests | **Met for structural/rules enforcement.** Every rule has a failing-then-passing test. (Authenticity of evidence *references* is a separate concern — see MINOR-1.) |
| AC3 no successor before ACCEPTED | `enggate.CanStartNext`; `forge gate next` | `TestNegativeCanStartNextBeforeAccepted`; `TestGateBlackBoxNegativeNextBeforeAccepted` | **NOT met.** `gate next` trusts the manifest's bare `state` field and does not verify the ACCEPTED claim was validly earned. See **BLOCKER-1**. |
| AC4 no undocumented production-readiness claim | baseline §9; M14-00 docs make no such claim; product docs audited | `TestBaselineDefinesEvidenceLevelsAndHonesty`, `TestActiveBaselinePathExists` | **Met.** Honesty rule is pinned in the baseline by an automated regression; README/COMPLIANCE_MATRIX/help audited, no M14-contradicting overclaim. |

## Findings

### BLOCKER-1 — `forge gate next` unlocks successor for an unvalidated ACCEPTED claim

- **Where:** `internal/enggate/enggate.go` `CanStartNext` (reads only
  `predecessor.State == StateAccepted`); `internal/cli/gate_cmd.go` `runGateNext`
  (calls only `enggate.CanStartNext`).
- **Observed violation:** `gate next` returns exit 0 ("successor task may
  start") for a manifest whose `state` is `"ACCEPTED"` but which is otherwise
  invalid: wrong `schema_version`, wrong `baseline_version`, no evidence,
  self-review/self-accept, `blackbox.status = failed`.
- **Proof (reproduced against the candidate binary):**

  ```
  $ cat /tmp/evil.json   # schema_version 999, baseline_version "0",
                         # evidence [], actors all "x", blackbox.status "failed",
                         # state "ACCEPTED"
  $ ./forge gate validate --manifest /tmp/evil.json
  forge: gate validate: REVIEW_APPROVED -> ACCEPTED REJECTED for task "M14-EVIL"
  enggate: schema_version 999 != active 1 (re-emit the manifest)        # exit 1
  $ ./forge gate next --manifest /tmp/evil.json
  OK: predecessor "M14-EVIL" is ACCEPTED; successor task may start       # exit 0  <-- BYPASS
  ```
- **Why it violates a mandatory criterion:** AC3 says a successor task cannot
  be unlocked until the predecessor is `ACCEPTED`. "Accepted" is defined by the
  baseline as the terminal state of a *validly earned* transition
  (`REVIEW_APPROVED -> ACCEPTED` with full evidence + actor separation +
  black-box + `make check`). Trusting a bare `state` string lets any agent
  fabricate an ACCEPTED manifest and unlock the next task, which also bypasses
  AC2 at the unlock point.
- **Required fix:** `CanStartNext` must validate the predecessor's ACCEPTED
  claim, not just its state field. Concretely: when `predecessor.State ==
  StateAccepted`, also require
  `ValidateTransition(predecessor.PreviousState, StateAccepted, predecessor)`
  to return nil (this transitively enforces schema/baseline version, evidence,
  actor separation, black-box, and `make check`). The combined error must be
  surfaced to the caller.
- **Required regression test:** a test that builds a manifest with
  `state == ACCEPTED` but (a) a wrong schema version, and (b) missing actor
  separation / missing evidence, and asserts `CanStartNext` (and the black-box
  `forge gate next`) return non-nil / exit 1. The existing
  `TestGateBlackBoxNegativeNextBeforeAccepted` covers only the "wrong state"
  branch; it must not be the sole guard.

### MINOR-1 — Baseline should state explicitly that the validator checks structure/rules, not reference authenticity

- **Where:** `docs/engineering/ENGINEERING_BASELINE.md` §4 (`evidence[].reference`)
  and §7 (`forge gate validate`).
- **Observed gap:** `ValidateTransition` verifies that every mandatory
  criterion has a passing automated evidence entry at an eligible level, but it
  does not verify that the `reference` string resolves to a real, passing test.
  A manifest could therefore pass `gate validate` with fabricated reference
  strings. This is a defensible design boundary (a pure validator cannot
  safely/portably execute arbitrary test references), and the mitigation —
  independent re-runs by reviewer and acceptor (§5.2/§5.3) — is already part of
  the process. But the baseline does not say so explicitly, which risks a reader
  believing `gate validate` proves the references are real.
- **Required fix:** add one sentence to §4 and/or §7 stating that
  `ValidateTransition` enforces structural completeness and the evidence/rules,
  not that `reference` values resolve to real passing tests; reference
  authenticity is established by independent re-runs (§5.2 review,
  §5.3 acceptance). No code change.

### MINOR-2 — ADR for the new `internal/enggate` package boundary

- **Where:** `AGENTS.md` package-boundary table does not list `internal/enggate`;
  the spec's package table is product-only and M14 is not in the spec.
- **Observation:** the implementation report already records this as FU-2. Not a
  defect; recording here so it is tracked. The package is clearly META
  (engineering process), separate from the runtime `internal/evidence` (§27) and
  `internal/review` (§25). An ADR would make the boundary decision durable.
- **Required fix (optional at remediation):** add a short ADR. May be deferred
  to a follow-up without blocking acceptance.

## Cross-cutting checks

- **Production composition / wiring:** `forge gate` is wired into the real CLI
  dispatch (`internal/cli/cli.go` `case "gate"`) and is observable through the
  compiled binary. It does not touch the daemon, storage, scheduler, or any
  product runtime path; it is a pure read-only META command. ✓
- **Fake / demo / stub leakage:** none. No hard-coded fake data, no `TODO`, no
  "print instructions instead of doing the work". The `goodManifest`/`goodGateManifest`
  helpers are test fixtures living inside `_test.go` files (not production). ✓
- **Policy / security bypass:** the change does not weaken any security,
  autonomy, delivery, or merge-policy invariant. It adds enforcement. The
  BLOCKER-1 finding is itself a gap in the new enforcement, not a weakening of
  existing product security. ✓
- **Restart / idempotency / cancellation / concurrency:** `enggate` is a pure,
  stateless, allocation-only domain package; the only I/O is read-only
  `LoadManifest`. The CLI command is a one-shot process. Race detector clean. No
  mutable shared state, no restart surface. ✓
- **Scope creep:** none observed. The deliverable matches the scoped task; the
  CLI command is the minimal honest black-box wiring; the dogfood manifest is
  reasonable evidence. No product-spec changes, no adapter/core mixing. ✓
- **Backward compatibility:** the change is additive (new top-level `gate`
  command; new package; new docs). No existing command's behaviour changed. The
  pre-existing modified/untracked files under `docs/reviews/` were not staged. ✓
- **Overclaimed documentation:** audited README, `forge help`, and
  `COMPLIANCE_MATRIX`. `gate` is described as META engineering tooling; no
  production-readiness claim added. README remains "self-hosting alpha". No
  overclaim requiring change. ✓

## Counterexample attempts

1. **Fabricated ACCEPTED manifest unlocks successor** → reproduces (BLOCKER-1).
2. **Manifest with fake evidence references passes `gate validate`** →
   reproduces, but is the documented-by-design trust boundary; mitigated by
   independent re-runs. Captured as MINOR-1 (doc clarity), not a code defect.
3. **Wrong `previous_state` + valid `state`** → `gate validate` correctly
   rejects ("illegal transition" + "requires previous_state ..."). No hole.
4. **Empty `task_id`** → rejected. No hole.
5. **Live-only evidence for a mandatory criterion** → rejected (live not
   eligible). No hole.

## Verdict

`CHANGES_REQUESTED`. AC3 is not met because the AC3 enforcement point
(`forge gate next`) trusts a bare state field and unlocks the successor for an
unvalidated ACCEPTED claim (BLOCKER-1). All other criteria are met and the
implementation is otherwise clean, well-tested, and correctly scoped. Once
BLOCKER-1 is fixed with a regression test and MINOR-1's doc sentence is added,
the change should be ready for `REVIEW_APPROVED`.

---

## Re-review after remediation

**Reviewer actor:** `M14-00-review-session`.
**Remediation commit:** `3bad1b5f97e3e67f850a73577b429a289236b0bd` ("M14-00:
address review findings"). The reviewer re-examined only the remediation diff
(`git diff 9ef4eda..3bad1b5`) and re-ran the gates independently.

### BLOCKER-1 — CLOSED

- Fix verified: `enggate.CanStartNext` now calls
  `ValidateTransition(predecessor.PreviousState, StateAccepted, predecessor)`
  after the `State != StateAccepted` short-circuit. A manifest that claims
  `state: "ACCEPTED"` but is otherwise invalid (wrong schema, missing evidence,
  self-accept, missing black-box) no longer unlocks a successor.
- Regression tests verified to catch the defect: the three new tests
  (`internal/enggate.TestNegativeCanStartNextFabricatedAcceptedWrongSchema`,
  `TestNegativeCanStartNextFabricatedAcceptedSelfAccept`,
  `internal/cli.TestGateBlackBoxNegativeNextFabricatedAccepted`) were confirmed
  by the implementer to FAIL on the pre-fix code and PASS after. The reviewer
  independently re-ran them against `3bad1b5`:
  - `go test -run 'TestNegativeCanStartNext' -count=1 ./internal/enggate/` → PASS
  - `go test -run 'TestGateBlackBoxNegativeNext' -count=1 ./internal/cli/` → PASS
- Counterexample re-attempt through the compiled binary:

  ```
  $ ./forge gate next --manifest /tmp/evil.json   # state=ACCEPTED, schema 999, no evidence
  forge: gate next: BLOCKED: enggate: predecessor "M14-EVIL" claims ACCEPTED but is not validly accepted: schema_version 999 != active 1
  # exit 1   (previously exit 0)
  ```
- The positive path is preserved:

  ```
  $ ./forge gate next --manifest docs/reviews/m14/M14-00.manifest.json
  OK: predecessor "M14-00" is ACCEPTED; successor task may start   # exit 0
  ```

### MINOR-1 — CLOSED

- Baseline §4 now contains an explicit "What the validator checks, and what it
  does not" paragraph stating that `ValidateTransition` enforces structural
  completeness and rules (not reference authenticity), and that `forge gate next`
  validates the ACCEPTED claim. §7 mirrors the latter. The reviewer confirms the
  wording is accurate and does not overclaim.

### MINOR-2 — DEFERRED (not blocking)

- ADR for the `internal/enggate` package boundary remains a follow-up (FU-2). It
  does not affect any mandatory criterion.

### Independent re-run at `3bad1b5`

| Command | Result |
|---------|--------|
| `go test ./internal/enggate/ -count=1` | PASS |
| `go test -race ./internal/enggate/` | PASS |
| `go test -race -run 'TestGateBlackBox' -count=1 ./internal/cli/` | PASS |
| `make check` | PASS (exit 0) |
| `gofmt -l .` | clean |
| `./forge gate validate --manifest docs/reviews/m14/M14-00.manifest.json` | exit 0 |
| `./forge gate next --manifest docs/reviews/m14/M14-00.manifest.json` | exit 0 |

### Updated verdict

`REVIEW_APPROVED`. BLOCKER-1 is closed by a regression test that demonstrably
catches the defect and the counterexample is now rejected through the compiled
binary; MINOR-1 is applied; MINOR-2 is deferred without blocking. All four
mandatory acceptance criteria are now met with passing automated evidence at
unit, integration, and black-box levels. The candidate `3bad1b5` is ready for
final independent acceptance.
