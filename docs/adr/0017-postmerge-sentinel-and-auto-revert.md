# ADR-0017: Post-merge sentinel & auto-revert behind the merge authority

- **Status:** Accepted
- **Date:** 2026-07-25
- **Spec refs:** §4.4 (AUTONOMOUS), §17.6 (Revert), §28 (Merge Governor),
  §37 (post-merge check), AC-7 (no network ops in LOCAL_REVIEW), AC-28 (no merge
  credentials to agents)

## Context

M12 adds the post-merge sentinel (§37): after a merge, smoke checks run, and on
regression the change is auto-reverted and the task reopened. Auto-revert is a
delivery mutation (it rewrites the target branch), so it must obey the same
authority rules as merge (§28) and be structurally impossible outside
AUTONOMOUS (§4.4).

## Decision

`internal/postmerge.Sentinel` is pure decision logic. It:

1. is a structural **no-op** when `post_merge.enabled` is false (every profile
   except AUTONOMOUS — §4.4). In LOCAL_REVIEW the merge would already have been
   refused by the Governor, so the sentinel never runs (AC-7).
2. runs smoke checks; on regression it auto-reverts **only** when
   `post_merge.auto_revert` is policy-enabled.
3. performs the revert through a `Reverter` interface that, in production, is the
   `merge.Authority.Revert` — the single merge-authority chokepoint (§28,
   ADR-0015). The sentinel therefore holds no credentials (AC-28).
4. reopens the task (§37 full pipeline) via a `TaskReopener`.
5. downgrades to `ALERT_ONLY` + reopen when a revert fails or a check errored —
   never a silent partial state.

## Consequences

**Positive**

- Auto-revert is reachable only through the merge authority; agent processes
  cannot trigger it (AC-28).
- §4.4 is structural: auto-revert cannot fire outside AUTONOMOUS.
- No silent failure: a failed revert always reopens the task for a human.

**Negative / trade-offs**

- The sentinel is wired in-process for now; the scheduler invocation (driving it
  after a real merge) lands with the scheduler follow-up. The pure decision +
  its composition are complete and tested.

## Alternatives considered

- **Agent-driven revert.** Rejected: violates §28/AC-28 (agents cannot hold
  merge authority).
- **Silent revert retry loop.** Rejected (rule §32): one bounded attempt, then
  ALERT_ONLY.
