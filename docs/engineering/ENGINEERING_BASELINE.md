# NeuroForge Engineering Baseline

**Version: 1** (schema-stable; bumps require a new version field in every
evidence manifest and an ADR).

This is the **versioned engineering baseline** for every contributor — human or
coding agent — working on NeuroForge. It is mandatory. It is distinct from the
product specification ([`../spec/NEUROFORGE_SPEC.md`](../spec/NEUROFORGE_SPEC.md)),
which remains the immutable source of truth for *what* NeuroForge must be. This
document defines *how* work on NeuroForge must be proven and gated.

`AGENTS.md` links this file and marks it required reading. Every machine-readable
evidence manifest (see §4) must declare `baseline_version: 1`. The validator
(`internal/enggate`) refuses any manifest whose baseline version differs from the
active one, so a stale baseline cannot be satisfied silently.

> Scope note. This baseline governs **engineering work on the NeuroForge
> repository itself** (milestone tasks, fixes, refactors). It is not the runtime
> Verification Evidence system of the product (spec §27, `internal/evidence`),
> which links product acceptance criteria to runtime test artifacts. The two are
> intentionally separate concerns with separate packages.

---

## 1. Why

A green `make check` alone does not prove that a requirement is met in
production. A unit test of an isolated component does not prove the component is
wired into the real pipeline. A fake-only test does not prove production
readiness. A stub that prints instructions is not an implementation.

This baseline makes those distinctions **enforceable** rather than aspirational:
no task may be marked done, reviewed, or accepted without the required weight of
evidence, and no successor task may start until its predecessor is `ACCEPTED`.

---

## 2. Evidence levels

Every piece of evidence has exactly one level. The level determines how much
weight it carries toward a mandatory acceptance criterion.

| Level         | Tag          | What it is                                                                                          | Mandatory-eligible? |
|---------------|--------------|-----------------------------------------------------------------------------------------------------|---------------------|
| Unit          | `unit`       | Automated test of one pure component in-process with deterministic fakes (no daemon, no binary).    | Yes (necessary, never sufficient alone for production wiring) |
| Integration   | `integration`| Multiple real domain packages composed in-process (real storage/services, no compiled binary).      | Yes                 |
| Black-box     | `blackbox`   | The **compiled `forge` binary** (or the real daemon loopback transport) is driven end-to-end; the test sees only observable outputs (stdout/exit code, HTTP/JSON, filesystem, Git state) and has no access to internal Go state. | Yes (required for any claim that touches the production path) |
| Live (opt-in) | `live`       | Real paid models, real image generation, real device harnesses, or real network actions.            | **Never.** Opt-in only; can never be the sole or mandatory evidence (spec §33, rule §36.5). |

Rules:

- A `skipped`, `manual`, or `live`-only test can **never** be the sole evidence
  for a mandatory criterion (baseline rule 9).
- A unit test alone never proves production wiring (baseline rule 7). If the
  criterion concerns the production path, at least one `blackbox` evidence entry
  is required.
- A fake-only test never proves production readiness (baseline rule 8).

---

## 3. Task lifecycle and transitions

Each task has exactly one state at a time. The state machine is enforced by
`internal/enggate.ValidateTransition`.

```
STARTED
   │  (impl agent, with full evidence)
   ▼
IMPLEMENTED_TESTED ──┐ (remediation loop)
   │  (independent    │
   │   reviewer)      ▼
   ▼               CHANGES_REQUESTED
REVIEW_APPROVED ◄─────
   │  (independent acceptor)
   ▼
ACCEPTED
```

Failure / terminal states (no successor work implied):

- `BLOCKED` — implementation could not proceed; a proven blocker exists.
- `FAILED` — implementation attempted and could not meet criteria.
- `REJECTED` — reviewer rejects; requires re-implementation.
- `ACCEPTANCE_FAILED` — acceptor finds an unproven mandatory criterion.

Transition rules (all checked by the validator):

1. `STARTED → IMPLEMENTED_TESTED` requires:
   - every **mandatory** criterion has ≥1 passing automated evidence at
     `unit`, `integration`, or `blackbox` level;
   - at least one `unit`-level passing evidence exists;
   - at least one `blackbox`-level passing evidence exists **unless** the
     manifest declares `blackbox.exempt = true` with a non-empty
     `blackbox.exempt_reason` AND ≥1 `integration`-level passing evidence covers
     the affected criteria (exemption is for tasks that provably touch no
     production wiring — never a convenience);
   - `make check` command result is `passed`.
2. `IMPLEMENTED_TESTED → REVIEW_APPROVED` requires:
   - the prior state was `IMPLEMENTED_TESTED`;
   - a review report path is recorded;
   - the reviewer actor is set and **differs** from the implementer;
   - every mandatory criterion still has passing evidence.
3. `REVIEW_APPROVED → ACCEPTED` requires:
   - the prior state was `REVIEW_APPROVED`;
   - the acceptor actor is set and **differs** from both implementer and
     reviewer;
   - a `blackbox` scenario is recorded with status `passed` (no exemption at
     acceptance — even exemption-eligible implementation tasks must still be
     exercised black-box at acceptance via the gate tooling itself, or via a
     documented equivalent);
   - `make check` command result is `passed`.
4. `CHANGES_REQUESTED → IMPLEMENTED_TESTED` is the only remediation re-entry; it
   requires a non-empty remediation note in the manifest.
5. Any transition not listed above is invalid and is rejected.

A successor task may start **only** when its declared predecessor is `ACCEPTED`
(`internal/enggate.CanStartNext`). For the first task in a milestone there is no
predecessor; it is startable unconditionally but must itself reach `ACCEPTED`
before its own successor starts.

---

## 4. Machine-readable evidence manifest

Each task ships a JSON manifest at
`docs/reviews/<milestone>/<TASK_ID>.manifest.json`. The schema (version 1):

```json
{
  "schema_version": 1,
  "baseline_version": 1,
  "task_id": "M14-00",
  "predecessor_task_id": "",
  "previous_state": "STARTED",
  "state": "IMPLEMENTED_TESTED",
  "criteria": [
    {"id": "AC1", "description": "...", "mandatory": true}
  ],
  "evidence": [
    {"criterion": "AC1", "level": "unit",      "reference": "internal/enggate.TestX", "status": "passed", "automated": true},
    {"criterion": "AC1", "level": "blackbox",  "reference": "internal/cli.TestGateBlackBox", "status": "passed", "automated": true}
  ],
  "commands": [
    {"label": "make check", "status": "passed"}
  ],
  "actors": {
    "implementer": "<distinct actor id>",
    "reviewer":    "<distinct actor id>",
    "acceptor":    "<distinct actor id>"
  },
  "reports": {
    "implementation": "docs/reviews/m14/M14-00_IMPLEMENTATION.md",
    "review":         "docs/reviews/m14/M14-00_REVIEW.md",
    "acceptance":     "docs/reviews/m14/M14-00_ACCEPTANCE.md"
  },
  "blackbox": {
    "compiled_binary": "forge",
    "scenario": "forge gate validate --manifest docs/reviews/m14/M14-00.manifest.json",
    "status": "passed",
    "exempt": false,
    "exempt_reason": ""
  },
  "remediation_note": ""
}
```

Field semantics:

- `schema_version` / `baseline_version` — must equal the active versions or the
  manifest is rejected.
- `previous_state` / `state` — the transition the manifest is claiming.
- `criteria[].mandatory` — if `true`, the criterion must be backed by passing
  automated evidence at an eligible level.
- `actors` — the three roles; the validator enforces they are pairwise distinct
  at the transitions where each role signs off.
- `blackbox.exempt` — see §3 rule 1. Acceptance (§3 rule 3) never accepts
  exemption.

**What the validator checks, and what it does not.** `ValidateTransition`
enforces *structural completeness* and the *rules*: schema/baseline version,
state-machine legality, that every mandatory criterion has a passing automated
evidence entry at an eligible level, actor separation, and the `make check`
result recorded in the manifest. It does **not** execute the referenced tests or
verify that an `evidence[].reference` string resolves to a real, passing test.
A manifest could therefore pass `forge gate validate` with fabricated reference
strings. The authenticity of the references is established separately and
independently by the review (§5.2) and acceptance (§5.3) phases, which re-run
the targeted tests, `make check`, the race detector, and the black-box gates.
`forge gate next` additionally calls `ValidateTransition` on the predecessor's
ACCEPTED claim, so a fabricated or stale `state: "ACCEPTED"` manifest cannot
unlock a successor.

---

## 5. Report formats

Three human-readable reports accompany each task, all under
`docs/reviews/<milestone>/`:

### 5.1 `<TASK_ID>_IMPLEMENTATION.md`

Fields: starting SHA; candidate SHA; goal and actual scope; changed files;
`acceptance criterion → code → test` mapping; exact test commands and actual
results; black-box evidence; known limitations; follow-up issues; verdict.
Allowed verdicts: `IMPLEMENTED_TESTED`, `BLOCKED`, `FAILED`. `IMPLEMENTED_TESTED`
is forbidden if any mandatory criterion is unproven.

### 5.2 `<TASK_ID>_REVIEW.md`

Independent review. Fields: reviewed candidate SHA; per-criterion mapping
(where / which test / why the test verifies observable behaviour); production
wiring check; fake/stub leakage check; policy/security bypass check;
restart/idempotency/cancellation/concurrency check; scope-creep check; backward
compatibility; overclaimed documentation; independent re-run results; attempt to
find a green-tests-but-unmet-requirement counterexample; findings by severity
(`BLOCKER` / `MAJOR` / `MINOR`); verdict. Allowed verdicts: `REVIEW_APPROVED`,
`CHANGES_REQUESTED`, `REJECTED`. `REVIEW_APPROVED` is forbidden if any mandatory
criterion is unproven.

### 5.3 `<TASK_ID>_ACCEPTANCE.md`

Final independent acceptance. Fields: accepted/rejected candidate SHA;
acceptance matrix; exact commands and results; black-box evidence; failure
reason if any; whether the successor task may start. Allowed verdicts:
`ACCEPTED`, `ACCEPTANCE_FAILED`.

---

## 6. Actor separation

Implementation, independent review, and final acceptance must be performed by
**distinct actors** (distinct sessions / identities). The validator enforces
this on the manifest's `actors` fields at the `REVIEW_APPROVED` and `ACCEPTED`
transitions: an actor may not review or accept their own implementation, and an
acceptor may not equal the reviewer.

Honest limitation: the system enforces *declared* actor separation at the data
level. It cannot cryptographically prove the humans or operators behind two
distinct actor ids are different people. That residual trust belongs to the
project owner who schedules the three phases. The gate guarantees that a single
self-declared actor cannot silently self-approve: identical ids are rejected.

---

## 7. The gate tooling

The baseline is enforced by a compiled-binary command so the enforcement itself
is black-box observable:

```
forge gate baseline                        # print active baseline version + doc path
forge gate validate --manifest <path>      # validate the manifest's claimed transition; exit 0 only if legal
forge gate next --manifest <path>          # exit 0 only if the predecessor task is genuinely ACCEPTED
```

`forge gate next` does not trust the predecessor manifest's bare `state` field:
it calls `ValidateTransition` on the predecessor's `REVIEW_APPROVED ->
ACCEPTED` claim, so a fabricated or stale `state: "ACCEPTED"` manifest cannot
unlock a successor.

Every task's implementation report must record the literal `forge gate validate`
and `forge gate next` invocations and their exit codes as black-box evidence.

---

## 8. Baseline rules (authoritative for engineering work)

These rules bind all engineering work on NeuroForge. They are enforced by review
and by the gate where machine-checkable.

1. Work only in the NeuroForge repository and study its actual current state
   first. Do not trust README, the compliance matrix, or old milestone reports
   without checking the production wiring.
2. Before changing anything, record: current branch; starting SHA;
   `git status --short`; existing tests and the current production path.
3. Never weaken the product specification or the security, autonomy, delivery,
   or merge-policy invariants.
4. Do only the assigned subtask. Record neighbouring issues as follow-ups; do
   not fix them unless necessary.
5. Any added or changed behaviour must have automated tests.
6. Any fixed defect must have a regression test that demonstrably catches it.
7. A unit test of an isolated component does not prove the component is wired
   into the production pipeline.
8. A fake-only test does not prove production readiness. Production wiring
   requires a black-box test through the compiled `forge` binary or the real
   daemon transport.
9. Skipped, manual, or opt-in tests can never be the sole evidence for a
   mandatory criterion.
10. A stub, a `TODO`, a demo fixture, hard-coded fake data, or printing
    instructions is not a finished implementation.
11. Add failing / characterization tests first where reasonable, then implement
    the minimal solution.
12. After changes, run: targeted tests of affected packages; `make check`;
    `go test -race ./...` when daemon / scheduler / storage / processes /
    concurrency / recovery / shared mutable state is touched; task-specific
    black-box tests.
13. Never mask flaky tests with retries, sleeps, or inflated timeouts without a
    proven cause.
14. Never push, open a PR/MR, or merge unless the subtask explicitly requires
    it. Even then, use a fake / sandbox remote unless a real remote is named.
15. Never damage the user's primary checkout. Never delete others' branches,
    worktrees, or artifacts.
16. End with a local atomic commit. Do not push.
17. Produce the implementation report with all required fields.
18. Allowed implementation verdicts: `IMPLEMENTED_TESTED`, `BLOCKED`, `FAILED`.
19. `IMPLEMENTED_TESTED` is forbidden if any mandatory criterion is unproven.

---

## 9. Documentation honesty

Documentation (README, `forge help`, `docs/spec/COMPLIANCE_MATRIX.md`) must not
assert production readiness, "done" status, or capability without corresponding
proof at the appropriate evidence level. A claim that touches the production
path requires `blackbox` evidence; a claim about pure domain logic requires at
least `unit` evidence. Overclaimed lines found during a task must be corrected
in that task or recorded as a follow-up.
