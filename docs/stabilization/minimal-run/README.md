# Stabilization — Minimal Reliable Run (`forge run`)

This directory is the **specification** for the first stabilization track of
NeuroForge: making one vertical scenario — `forge run "..."` — reliably
produce a real, verifiable repository result. **It is specification only.**
No production code is changed by this track's documents. Implementation is
tracked in `ACCEPTANCE_MATRIX.md` (every row starts `NOT IMPLEMENTED`).

> **Source of truth:** [`../../spec/NEUROFORGE_SPEC.md`](../../spec/NEUROFORGE_SPEC.md).
> Where any document here disagrees with the spec, **the spec wins**.
> Architectural deviation: [ADR-0019](../../adr/0019-minimal-run-stabilization.md)
> (Proposed).

## Reading order

1. [`REQUIREMENTS.md`](REQUIREMENTS.md) — the user contract, functional /
   non-functional / safety requirements, and explicit non-goals.
2. [`STATE_MACHINE.md`](STATE_MACHINE.md) — task / workspace / attempt / run
   states, legal transitions, terminal (absorbing) states, restart rules.
3. [`OUTCOME_CONTRACT.md`](OUTCOME_CONTRACT.md) — the disjoint outcome set,
   human output, the `--json` document shape, exit codes, retry semantics.
4. [`TEST_PLAN.md`](TEST_PLAN.md) — unit, integration, black-box,
   reliability, opt-in smoke, and failure-artifact collection.
5. [`ACCEPTANCE_MATRIX.md`](ACCEPTANCE_MATRIX.md) — every requirement mapped
   to its proof and pass criteria (initially `NOT IMPLEMENTED`).
6. [`IMPLEMENTATION_SLICES.md`](IMPLEMENTATION_SLICES.md) — the twelve
   sequenced coding slices (S1–S12) with dependencies and review criteria.
7. [`REVIEW_CHECKLIST.md`](REVIEW_CHECKLIST.md) — the independent reviewer's
   anti-false-PASS checklist (Gate E).
8. [`KNOWN_FAILURES.md`](KNOWN_FAILURES.md) — the confirmed current defects,
   with file:line references, reproduction, and expected vs actual.

## Who does what

- **Implementing agent** — works slice-by-slice per
  `IMPLEMENTATION_SLICES.md`; updates `ACCEPTANCE_MATRIX.md` rows to
  `PARTIAL`/`PASS`; never self-certifies (Gate E is authoritative).
- **Independent reviewer (third agent)** — runs `REVIEW_CHECKLIST.md`,
  reproduces every claim, flips rows to `PASS` (or back to `FAIL`), writes
  the verdict.

## Hard constraints (apply to every slice)

- No push, no PR, no merge into `main` (AGENTS.md).
- No change to `docs/spec/NEUROFORGE_SPEC.md`.
- No change to Protocol v1 (`internal/adapter/codingagent/protocol/`).
- No mass deletion of existing subsystems (bypass, do not remove).
- No paid model and no network in the default test suite (rule §36.5).
- `make check` green after every slice; `gofmt` / `go vet` /
  `git diff --check` clean.

## TL;DR — the invariants

1. Process success ≠ task success.
2. Git is the source of truth after a run.
3. Outcomes are disjoint and total.
4. A no-change run is a failure (non-zero exit).
5. A committed result carries the real commit SHA.
6. An uncommitted result is preserved and honestly labelled.
7. Result refs live only under `refs/heads/forge/result/<task-id>`.
8. Terminal states are absorbing.
9. Cancellation precedence; timeout ≠ cancellation.
10. LOCAL_REVIEW is a hard wall (no Git network, no creds to the agent).
11. `--json` is exactly one valid document.
12. The primary checkout is never modified.
