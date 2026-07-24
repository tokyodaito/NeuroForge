# ADR-0008: LOCAL_REVIEW security model

- **Status:** Accepted
- **Date:** 2026-07-24
- **Spec refs:** §3.2 (manual review scenario), §4.2 (LOCAL_REVIEW), §29
  (security), AC-7 (no network ops), AC-28 (no merge credentials), §29.3 (prompt
  injection priority), §36.13 (no push in LOCAL_REVIEW)

## Context

For professional codebases the user needs a profile where the factory builds and
verifies code locally but performs **zero** Git network operations, sends no
artifacts to external systems (beyond model context allowed by policy), and never
holds the credentials that could do so (§3.2, §4.2). This must be enforceable
deterministically, not merely "off by default".

## Decision

Make `LOCAL_REVIEW` a **policy-enforced, structurally unreachable** state for any
delivery network operation:

1. `internal/policy` resolves the pipeline toggle dependencies (§5.1): when
   `git.push=false`, it forces `change_request.create=false`, `merge.enabled=false`
   and `post_merge.enabled=false`. In `LOCAL_REVIEW` `push` is always false.
2. The `adapter/vcs` network path (`PushBranch`, `CreateChangeRequest`, `Merge`,
   …) is reachable **only** when the Merge Governor (ADR-0009) emits
   `ALLOW_PUSH` / `ALLOW_CHANGE_REQUEST` / `ALLOW_MERGE`; in `LOCAL_REVIEW` only
   `ALLOW_LOCAL_RESULT` is ever emitted.
3. Merge/push credentials are held outside agent processes (§28, AC-28) and are
   not even loaded in `LOCAL_REVIEW`.
4. The prompt-injection priority (§29.3) is fixed in code; an instruction inside
   repo docs or attachments cannot enable push or disable security (§29.3).
5. Agent processes receive an allowlisted environment only (§29.2).

## Consequences

**Positive**

- AC-7 is guaranteed by construction, not convention; safe for professional
  codebases (the core product promise, §37).
- Prompt-injection resistance is structural.

**Negative / trade-offs**

- Requires discipline that policy checks are never bypassed by task overrides
  (AC-29); covered by Merge Governor tests.

## Alternatives considered

- **"Off by default" flags.** Rejected: a single misconfiguration or injected
  instruction could push; the spec demands a hard guarantee.
- **Container/network sandbox only.** Useful as defense-in-depth later, but the
  guarantee must hold even without a sandbox, so policy-level enforcement is
  primary.
