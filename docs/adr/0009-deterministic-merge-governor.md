# ADR-0009: Deterministic Merge Governor

- **Status:** Accepted
- **Date:** 2026-07-24
- **Spec refs:** §28 (Merge Governor), §24.5 (auto-merge policy), §36.6 (no LLM
  for policy enforcement), AC-28/AC-29

## Context

Allowing autonomous merge (profile `AUTONOMOUS`, §4.4) is high-risk. The spec
(§28) defines the Merge Governor as **deterministic code** that gates every
delivery action, and forbids using an LLM for Git or policy enforcement
(rule §36.6). A task override must not bypass non-disableable project security
policy (AC-29).

## Decision

Implement `internal/merge` as a **pure, deterministic decision function** with no
side effects and no LLM involvement. Inputs (§28):

- specification_locked, scope_valid, required_checks_passed,
  acceptance_evidence_complete, blocker_findings==0, major_findings==0,
  target_allowed, branch_current, budget_policy_satisfied,
  visual_policy_satisfied.

Outputs (exactly one): `ALLOW_LOCAL_RESULT`, `ALLOW_PUSH`,
`ALLOW_CHANGE_REQUEST`, `ALLOW_MERGE`, `REQUIRE_REBASE`, `REQUIRE_REPAIR`,
`POLICY_BLOCKED`, `QUARANTINE`.

Rules:

- The Governor only **authorises**; the actual delivery is performed by
  `adapter/vcs`, which is the only holder of merge credentials and is unreachable
  without an `ALLOW_*` decision.
- Project security policy is merged into the Governor inputs and cannot be
  weakened by a task override (AC-29).
- The function is exhaustively unit-tested (every gate, every output) so the
  decision table is the source of truth.

## Consequences

**Positive**

- Merge safety is auditable and replayable; decisions are explainable.
- Satisfies §36.6 and AC-29 by construction.

**Negative / trade-offs**

- Determinism means nuance (e.g. "almost meets bar") is handled by routing back
  to repair/review rather than by discretion — this is intentional.

## Alternatives considered

- **LLM-based review as the merge gate.** Rejected: violates §36.6; non-
  reproducible and prompt-injectable.
- **Ad-hoc checks scattered across delivery code.** Rejected: un-auditable;
  centralising the gate is the point of the Governor.
