# M14-06 — Implementation Report

**Task:** M14-06 — Durable stage state machine (unified orchestration state
machine for the full pipeline with deterministic stage handlers).
**Implementer actor:** `M14-06-impl-session-1`.
**Verdict:** `BLOCKED`

## SHAs

- **Starting SHA:** `08cbf233333511ac7fedf538cac83c28dc0533c5`
  (`M14-05: independent review (REVIEW_APPROVED, one MAJOR + five MINOR follow-ups)`,
  current `HEAD` of `main`).
- **Candidate SHA:** this report's commit. **No production code, no test code,
  no spec, no baseline, and no gate-enforcement file was modified.** The only
  file added is this report itself.

## Preconditions checked

The Engineering Baseline (v1) rule *"No successor task may start until its
predecessor is `ACCEPTED`"* and the task brief step 1 (*"Убедись, что
предыдущая задача имеет verdict `ACCEPTED`. Если нет — поставь `BLOCKED`,
ничего не реализуй."*) require that the immediate predecessor — **M14-05** —
be in the `ACCEPTED` state before M14-06 may begin. The lifecycle defined by
the baseline is `STARTED → IMPLEMENTED_TESTED → REVIEW_APPROVED → ACCEPTED`,
and only an independent acceptance may move a task to `ACCEPTED`.

### State of M14-05 (verified independently in this session)

| Artifact | Expected for `ACCEPTED` | Actual |
|---|---|---|
| `docs/reviews/m14/M14-05_IMPLEMENTATION.md` | present | present — self-verdict `IMPLEMENTED_TESTED` |
| `docs/reviews/m14/M14-05_REVIEW.md` | present, verdict `REVIEW_APPROVED` | present — verdict **`REVIEW_APPROVED`** (last line: "`REVIEW_APPROVED` is permitted because every mandatory criterion is proven by passing automated evidence; the MAJOR finding is a tracked follow-up, not an unproven AC."). Reviewer explicitly defers acceptance: "The candidate is eligible for a separate acceptance session." |
| `docs/reviews/m14/M14-05_ACCEPTANCE.md` | present, verdict `ACCEPTED` | **MISSING** |
| `docs/reviews/m14/M14-05.manifest.json` | present, `state: "ACCEPTED"`, `baseline_version: 1` | present but `state: "IMPLEMENTED_TESTED"` (lines 7, 6) — **not `ACCEPTED`** |

Directory listing of `docs/reviews/m14/` (verified this session):

```
M14-00.manifest.json         M14-02_IMPLEMENTATION.md   M14-04_ACCEPTANCE.md
M14-00_ACCEPTANCE.md         M14-02_REVIEW.md           M14-04_IMPLEMENTATION.md
M14-00_IMPLEMENTATION.md     M14-03.manifest.json       M14-04_REVIEW.md
M14-00_REVIEW.md             M14-03_ACCEPTANCE.md       M14-05.manifest.json
M14-01.manifest.json         M14-03_IMPLEMENTATION.md   M14-05_IMPLEMENTATION.md
M14-01_ACCEPTANCE.md         M14-03_REVIEW.md           M14-05_REVIEW.md
M14-01_IMPLEMENTATION.md     M14-04.manifest.json
M14-01_REVIEW.md             M14-04_ACCEPTANCE.md
M14-02.manifest.json         (no M14-05_ACCEPTANCE.md)
M14-02_ACCEPTANCE.md
```

For comparison, every prior accepted task (M14-00 through M14-04) has both a
`*_ACCEPTANCE.md` and a `*.manifest.json` whose `state` is `ACCEPTED`. M14-05
has neither: no `M14-05_ACCEPTANCE.md` exists, and its manifest still declares
`state: "IMPLEMENTED_TESTED"`.

### Independent gate evidence (compiled `./forge` at the starting SHA)

Toolchain at verification time: `go1.26.5 darwin/arm64`.

```
$ make build
go build -ldflags "…v0.1.0-29-g08cbf23-dirty…" -o forge ./cmd/forge
# exit 0

$ ./forge gate baseline
active schema_version: 1
active baseline_version: 1
baseline document: docs/engineering/ENGINEERING_BASELINE.md
# exit 0

$ ./forge gate next --manifest docs/reviews/m14/M14-05.manifest.json
forge: gate next: BLOCKED: enggate: cannot start successor task: predecessor "M14-05" state is IMPLEMENTED_TESTED, not ACCEPTED
# exit 1
```

The successor gate (`forge gate next`) refuses with exit 1 and the explicit
message *"predecessor 'M14-05' state is IMPLEMENTED_TESTED, not ACCEPTED"*.
This is the authoritative, machine-checked confirmation that the baseline's
predecessor-ACCEPTED precondition for M14-06 is not satisfied.

## Why this is a hard block (not a soft one)

The baseline lifecycle is explicit and machine-enforced:

1. `STARTED → IMPLEMENTED_TESTED`: implementer self-attestation with evidence
   manifest. M14-05 reached this.
2. `IMPLEMENTED_TESTED → REVIEW_APPROVED`: independent reviewer. M14-05
   reached this (`M14-05_REVIEW.md` verdict `REVIEW_APPROVED`).
3. `REVIEW_APPROVED → ACCEPTED`: **independent acceptor** — a distinct actor
   from both the implementer and the reviewer. M14-05 has **not** reached
   this: no `M14-05_ACCEPTANCE.md`, no manifest transition to `ACCEPTED`, and
   the reviewer explicitly stated the candidate "is eligible for a separate
   acceptance session".

Baseline rule (ENGINEERING_BASELINE.md, "actor-separation rule"): *"No task
may be `REVIEW_APPROVED` or `ACCEPTED` without an independent actor."* The
implementer of M14-06 is not permitted to perform the acceptance of M14-05 —
that would be a self-acceptance of the predecessor, violating actor
separation.

The `REVIEW_APPROVED` verdict in `M14-05_REVIEW.md` ships a **MAJOR-1**
finding (cross-task lease scoping defect in `ClaimRequest.ProjectID()` that
returns `TaskID` instead of the project id, weakening lease isolation to
per-task instead of per-project) plus five MINOR findings. The reviewer
requires that MAJOR-1 be resolved before the dispatch hook (FU-M14-05-1) or
the claim/release CLI (FU-M14-05-2) lands. The orchestrator that M14-06 is
supposed to build (the unified pipeline state machine that drives
compile → plan → design → implement → test → review → visual → repair →
finalize → delivery → post-merge) is precisely the layer that will eventually
call `Scheduler.Claim` and exercise the lease layer — i.e. it is the
FU-M14-05-1 dispatch hook. Starting M14-06 now would either (a) bypass the
lease layer entirely (un-accepting the production wiring M14-05 was built
for) or (b) integrate against the MAJOR-1 scoping defect, baking the latent
hazard into the orchestrator. Both are prohibited by the baseline's
sequential-gate invariant.

Therefore M14-06 cannot begin until:

- an independent acceptance session moves M14-05 to `ACCEPTED`
  (produces `M14-05_ACCEPTANCE.md` and updates `M14-05.manifest.json`
  `state` → `ACCEPTED`); AND
- `forge gate next --manifest docs/reviews/m14/M14-05.manifest.json` exits 0.

## Scope actually performed in this session

**None.** No production code, no test code, no migration, no spec, no baseline
document, and no gate-enforcement file was modified. The only change is the
addition of this report.

In particular, the mandatory acceptance criteria for M14-06 (workflow state
not RAM-only; declared task/project states reachable; mandatory stage cannot
be disabled by task override; crash does not cause unknown double execution)
were **not** implemented and **not** tested. Doing so would violate the
baseline's sequential-gate invariant and would risk integrating against an
un-accepted predecessor that ships an acknowledged MAJOR finding directly in
the path M14-06 must exercise.

## Verification of clean working tree for this commit

Pre-existing untracked/modified files in the working tree at session start
were left untouched (they are not mine and predate this session):

```
$ git status --short
 M docs/reviews/MINIMAL_RUN_POST_ROLLBACK_REREVIEW.md
?? docs/reviews/M12_M13_REVIEW.md
?? docs/reviews/MINIMAL_RUN_FINAL_REVIEW.md
?? docs/reviews/MINIMAL_RUN_IMPLEMENTATION_REVIEW.md
?? ism
```

Only `docs/reviews/m14/M14-06_IMPLEMENTATION.md` is staged for this commit.

## Mandatory acceptance-criterion → code → test mapping

Not applicable: no acceptance criterion was implemented. All mandatory ACs
for M14-06 remain unimplemented pending the unblock.

| Mandatory AC | Status |
|---|---|
| Workflow state not RAM-only (durable) | UNIMPLEMENTED (blocked) |
| Declared task/project states reachable (BLOCKED, DEGRADED, WAITING_QUOTA, WAITING_DESIGN_SELECTION, QUARANTINED) | UNIMPLEMENTED (blocked) |
| Mandatory stage cannot be disabled by task override | UNIMPLEMENTED (blocked) |
| Crash does not cause unknown double execution (persist-before-effect) | UNIMPLEMENTED (blocked) |

## Test commands and results

No test commands were run against M14-06 code, because no M14-06 code was
written. The only commands executed in this session were the gate
verification commands reproduced verbatim in the "Independent gate evidence"
section above:

| Command | Exit | Result |
|---|---:|---|
| `make build` | 0 | clean build (needed to run `forge gate`) |
| `./forge gate baseline` | 0 | active baseline_version 1, schema_version 1 |
| `./forge gate next --manifest docs/reviews/m14/M14-05.manifest.json` | **1** | **BLOCKED: predecessor "M14-05" state is IMPLEMENTED_TESTED, not ACCEPTED** |

`make check` and `go test -race ./...` were not re-run because no source
code changed; the repository's test state is unchanged from the starting SHA.

## Black-box evidence

Not applicable: no production behaviour was added or changed in this session.
The only black-box evidence collected is the `forge gate next` refusal
against the compiled `./forge` binary, reproduced above.

## Known limitations

N/A — no implementation was produced.

## Follow-up problems

1. **(Blocker for M14-06)** An independent acceptance session must move
   M14-05 from `REVIEW_APPROVED` to `ACCEPTED`: produce
   `docs/reviews/m14/M14-05_ACCEPTANCE.md` and update
   `docs/reviews/m14/M14-05.manifest.json` `state` from
   `IMPLEMENTED_TESTED` to `ACCEPTED`. Only then does
   `forge gate next --manifest docs/reviews/m14/M14-05.manifest.json`
   exit 0 and unblock M14-06.
2. **(Pre-condition for the eventual M14-06 orchestrator)** MAJOR-1 from
   `M14-05_REVIEW.md` (cross-task lease scoping: `ClaimRequest.ProjectID()`
   returns `TaskID` instead of the project id, breaking per-project lease
   isolation) must be resolved before the orchestrator's dispatch hook can
   safely call `Scheduler.Claim`. The reviewer explicitly required this fix
   to land before FU-M14-05-1 (dispatch hook) — which is exactly the layer
   M14-06 will build. The five MINOR findings from `M14-05_REVIEW.md` are
   tracked there and need not be repeated here.
3. **(Not a defect of this session)** The pre-existing untracked/modified
   files in the working tree (`docs/reviews/MINIMAL_RUN_*`,
   `docs/reviews/M12_M13_REVIEW.md`, `ism`) were left untouched and are out
   of scope for M14-06.

## Verdict

**`BLOCKED`** — the immediate predecessor task **M14-05** is in state
`IMPLEMENTED_TESTED` (manifest) / `REVIEW_APPROVED` (review report) but is
**not** `ACCEPTED`. There is no `M14-05_ACCEPTANCE.md`, the manifest
`state` is not `ACCEPTED`, and the baseline successor gate
`forge gate next --manifest docs/reviews/m14/M14-05.manifest.json` exits 1
with the message *"predecessor 'M14-05' state is IMPLEMENTED_TESTED, not
ACCEPTED"*. Per Engineering Baseline v1 rule *"No successor task may start
until its predecessor is `ACCEPTED`"* and the task brief step 1, no
production code, test code, spec, baseline, or gate-enforcement file was
modified. M14-06 must wait for an independent acceptance of M14-05.
