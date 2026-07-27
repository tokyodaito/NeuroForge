# M14-04 — Implementation Report

**Task:** M14-04 — Work Graph domain, DAG validation and AC mapping.
**Implementer actor:** `M14-04-impl-session`.
**Verdict:** `BLOCKED`

## SHAs

- **Starting SHA:** `3c26aa0b4351f0ac5869290cb242190d5eb7de7d`
  (`M14-03: re-review after remediation (REVIEW_APPROVED)`, current `HEAD` of
  `main`).
- **Candidate SHA:** this report's commit. No production code, no test code
  changed. The only file added is this report itself.

## Preconditions checked

The Engineering Baseline (v1, rule "No successor task may start until its
predecessor is `ACCEPTED`") and the task brief (step 1: "Убедись, что
предыдущая задача имеет verdict `ACCEPTED`. Если нет — поставь `BLOCKED`,
ничего не реализуй.") require that the immediate predecessor — **M14-03** —
be in the `ACCEPTED` state before M14-04 may begin.

### State of M14-03 (verified independently in this session)

| Artifact | Expected for `ACCEPTED` | Actual |
|---|---|---|
| `docs/reviews/m14/M14-03_IMPLEMENTATION.md` | present | present — self-verdict `IMPLEMENTED_TESTED` |
| `docs/reviews/m14/M14-03_REVIEW.md` | present, verdict `REVIEW_APPROVED` | present — verdict **`REVIEW_APPROVED`** (last line: "`REVIEW_APPROVED` … The candidate is eligible for a separate acceptance session.") |
| `docs/reviews/m14/M14-03_ACCEPTANCE.md` | present, verdict `ACCEPTED` | **MISSING** |
| `docs/reviews/m14/M14-03.manifest.json` | present, `state: "ACCEPTED"`, `baseline_version: 1` | **MISSING** |

Directory listing of `docs/reviews/m14/`:

```
M14-00.manifest.json         M14-01_IMPLEMENTATION.md   M14-02_IMPLEMENTATION.md
M14-00_ACCEPTANCE.md         M14-01_REVIEW.md           M14-02_REVIEW.md
M14-00_IMPLEMENTATION.md     M14-02.manifest.json       M14-03_IMPLEMENTATION.md
M14-00_REVIEW.md             M14-02_ACCEPTANCE.md       M14-03_REVIEW.md
M14-01.manifest.json         M14-01_ACCEPTANCE.md
```

For comparison, every prior accepted task (M14-00, M14-01, M14-02) has both a
`*_ACCEPTANCE.md` and a `*.manifest.json`. M14-03 has neither.

### Independent gate evidence (compiled `./forge` at the starting SHA)

```
$ make build
go build -ldflags "…v0.1.0-21-g3c26aa0-dirty…" -o forge ./cmd/forge
# exit 0

$ ./forge gate baseline
active schema_version: 1
active baseline_version: 1
baseline document: docs/engineering/ENGINEERING_BASELINE.md
# exit 0

$ ./forge gate next --manifest docs/reviews/m14/M14-03.manifest.json
forge: gate next: load predecessor manifest: open docs/reviews/m14/M14-03.manifest.json: no such file or directory
# exit 1

$ ./forge gate validate --manifest docs/reviews/m14/M14-03.manifest.json
forge: gate validate: open docs/reviews/m14/M14-03.manifest.json: no such file or directory
# exit 1
```

The baseline's mandatory successor gate (`forge gate next --manifest
<predecessor>.json`) cannot even load a predecessor manifest for M14-03
because none exists, and even a hypothetical manifest would have to declare
`state: "ACCEPTED"` for the gate to return exit 0.

The `M14-03_REVIEW.md` explicitly states the candidate is "eligible for a
**separate acceptance session**" — i.e. acceptance has not happened. Per the
baseline lifecycle, the legal task transitions are
`STARTED → IMPLEMENTED_TESTED → REVIEW_APPROVED → ACCEPTED`, and only
`ACCEPTED` unblocks a successor.

## Conclusion

M14-03 is at `REVIEW_APPROVED`, **not** `ACCEPTED`. The Engineering Baseline
(v1) and the task brief forbid starting M14-04 in this state. Per the brief's
explicit instruction ("Если есть реальная блокировка — докажи её и верни
`BLOCKED`"), this task is **BLOCKED** on predecessor acceptance.

No production code, no test code, no spec/baseline/gate enforcement, and no
package boundary was modified. Only this report is added.

## Scope NOT started (carried forward to the unblocked M14-04 session)

For traceability, the M14-04 scope (unchanged from the task brief) that the
next session must perform once M14-03 is `ACCEPTED`:

- Work package types, stages, dependencies, states and attempts
  (in `internal/workgraph`, which currently contains only the M3 lease layer
  `leases.go` + `doc.go`).
- Mapping package → acceptance criteria → allowed scope (ties into the
  M14-01/M14-02 compiled `Specification` AC IDs).
- DAG validation: cycles, missing nodes, duplicate AC ownership, unreachable
  nodes.
- Deterministic graph decomposition boundary (must reuse the deterministic
  `task.Compile` contract from M14-02/M14-03 so identical specifications
  produce byte-identical graphs).
- Serialization model with stable ordering.

Mandatory tests that the next session must add:
- Valid simple and composite DAG fixtures.
- Cycle, missing edge, duplicate AC owner, unreachable node negative cases.
- Stable serialization (byte-identical for identical input across runs).
- `make check` green; `go test -race ./internal/workgraph/...` if any shared
  mutable state is introduced.

The existing `internal/workgraph` package already carries a partial
implementation (`leases.go`: path + semantic leases from M3). The M14-04
session must not break the lease contract and must not connect the new DAG
model to execution (per the brief: "не подключая execution").

## Files changed

| File | Change |
|---|---|
| `docs/reviews/m14/M14-04_IMPLEMENTATION.md` | NEW (this report) |

No other files touched. The pre-existing working-tree noise
(`docs/reviews/MINIMAL_RUN_*`, `docs/reviews/M12_M13_REVIEW.md`) predates
this task and was not touched, matching the M14-03 review's note.

## Acceptance criterion matrix

| Mandatory AC | Implementation | Test | Status |
|---|---|---|---|
| Every package is linked to an objective/AC | — | — | **NOT STARTED** (predecessor not ACCEPTED) |
| An invalid DAG cannot be persisted as runnable | — | — | **NOT STARTED** |
| The graph is deterministic for an identical specification | — | — | **NOT STARTED** |

No mandatory AC can be evidenced because no implementation work was permitted.

## Commands executed and results

| Command | Exit | Result |
|---|---:|---|
| `git rev-parse HEAD` | 0 | `3c26aa0b4351f0ac5869290cb242190d5eb7de7d` |
| `git branch --show-current` | 0 | `main` |
| `git status --short` | 0 | only pre-existing unrelated review docs (untouched) |
| `make build` | 0 | `./forge` builds cleanly at the starting SHA |
| `./forge gate baseline` | 0 | active baseline v1, schema v1 |
| `./forge gate next --manifest docs/reviews/m14/M14-03.manifest.json` | **1** | `load predecessor manifest: open …: no such file or directory` |
| `./forge gate validate --manifest docs/reviews/m14/M14-03.manifest.json` | **1** | `open …: no such file or directory` |
| `ls docs/reviews/m14/M14-03_ACCEPTANCE.md docs/reviews/m14/M14-03.manifest.json` | **1** | both files MISSING |

Toolchain: `go version go1.26.5 darwin/arm64`.

No `make check` / `go test -race ./...` run is required for a `BLOCKED` task
with zero production/test code changes; the working tree is untouched outside
this report. (`make build` was run only to obtain the compiled gate binary
used to prove the predecessor gate is closed.)

## Black-box evidence

Not applicable — no implementation was performed. The only black-box action
was running the compiled `./forge gate …` commands above, which independently
prove the predecessor gate is closed.

## Known limitations

None beyond the blocker itself.

## Follow-up problems

- **FU-M14-04-0 (blocker):** M14-03 must be advanced to `ACCEPTED` by an
  independent acceptor actor (separate from both the implementation and the
  review actors per baseline rule "No task may be `REVIEW_APPROVED` or
  `ACCEPTED` without an independent actor"). That requires:
  1. An independent acceptance session producing
     `docs/reviews/m14/M14-03_ACCEPTANCE.md`.
  2. A `docs/reviews/m14/M14-03.manifest.json` declaring
     `baseline_version: 1` and `state: "ACCEPTED"`.
  3. `./forge gate next --manifest docs/reviews/m14/M14-03.manifest.json`
     returning exit 0.
  Only then may M14-04 start.

## Verdict

**`BLOCKED`**

Rationale (per task brief rule 19: "`IMPLEMENTED_TESTED` запрещён, если хотя
бы один обязательный критерий не доказан"; and the explicit instruction to
return `BLOCKED` when the predecessor is not `ACCEPTED`):

- The immediate predecessor M14-03 is at `REVIEW_APPROVED`, not `ACCEPTED`.
- `docs/reviews/m14/M14-03_ACCEPTANCE.md` does not exist.
- `docs/reviews/m14/M14-03.manifest.json` does not exist.
- The baseline gate `forge gate next --manifest docs/reviews/m14/M14-03.manifest.json`
  fails (exit 1) because the manifest is absent.
- Per Engineering Baseline v1 rule "No successor task may start until its
  predecessor is `ACCEPTED`", M14-04 may not begin.
- No production code, test code, spec, baseline, or gate enforcement was
  modified. Only this report is added.

This `BLOCKED` is a hard, proven blocker — not a soft defer. The next session
must verify M14-03 is `ACCEPTED` (manifest present + gate returns exit 0)
before doing any M14-04 implementation work.
