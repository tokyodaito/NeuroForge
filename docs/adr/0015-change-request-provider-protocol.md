# ADR-0015: Change-request provider protocol & merge authority

- **Status:** Accepted
- **Date:** 2026-07-25
- **Spec refs:** §17.6 (VCS adapters), §28 (Merge Governor), §3.3 (PR/MR without
  merge), §5/§5.1 (toggles + dependency rules), §29 (security), AC-7 (no network
  ops in LOCAL_REVIEW), AC-14 (independent push/CR/merge), AC-28 (no merge
  credentials to agents), AC-29 (override cannot weaken security)

## Context

M11 delivers remote delivery: push, GitHub PRs, GitLab MRs, and merge. The spec
demands three hard guarantees:

1. **AC-7**: `LOCAL_REVIEW` performs zero Git network operations — by
   construction, not convention.
2. **§28 / AC-28**: only the Merge Governor has merge authority, and agent
   processes never hold merge/VCS credentials.
3. **AC-14**: push, PR/MR and merge are independently switchable, yet a disabled
   push must automatically forbid PR/MR and remote merge.

A naïve design (providers called directly by orchestration code) would violate
all three: nothing would force a policy check before a network call, nothing
would prevent a second merge path, and the policy cascade would be advisory
rather than structural.

## Decision

Introduce a single delivery chokepoint with a strict layering:

```
policy.Resolve ──► merge.Decide (Governor, pure) ──► merge.Authority ──► vcs.ChangeRequestProvider
```

1. **`vcs.ChangeRequestProvider`** (§17.6 interface) — Local Git, GitHub, GitLab.
   A [Capabilities] struct advertises what each supports; `IsNetwork` flags
   remote providers. Providers NEVER read process env for credentials — the
   daemon injects a `CredentialResolver` (AC-28).

2. **`merge.Authority`** — the ONLY holder of merge authority. Every delivery
   call (push, CR, auto-merge, merge, revert) flows through it. For each call it
   re-checks:
   - the Governor decision is at the required ALLOW level (`requiresDecision`);
   - `policy.Allows(action)` is true (defense-in-depth);
   - a network provider is not used in a network-locked profile (AC-7,
     structural).

   Agent processes never hold an `Authority` reference, so they structurally
   cannot perform delivery.

3. **`merge.Queue`** — a deterministic FIFO merge queue. It re-validates branch
   currency at execution time (the §28 `branch_current` gate can go stale) and
   falls back to a **local merge** via the Local Git provider only in the §5.1 R5
   local-merge mode (`merge=true` while `change_request.create=false`).

4. **Audit (§29.4)** — every push, PR/MR, merge, revert, and denied-delivery is
   recorded by the Authority.

The §5.1 dependency rules (already in `policy.Normalize`) provide the cascade:
`push=false ⇒ change_request.create=false ⇒ merge=false`. The Authority consults
the resolved (normalised) policy, so a disabled push structurally forbids PR/MR
and remote merge (AC-14, M11 requirement "disabled push auto-forbids PR/MR and
remote merge").

## Consequences

**Positive**

- AC-7 is structural: in LOCAL_REVIEW the Authority is unreachable for every
  network action, and a network provider is refused even with a stale decision.
- §28 "sole merge authority" is enforced by type: there is no `Provider.Merge`
  call site outside `Authority.Merge`.
- AC-28 is enforced at two layers: the env allowlist (supervisor, M3) and the
  provider's credential resolver (daemon-injected, never env).
- AC-14/AC-29 are enforced because the Authority consults the *resolved* policy,
  which has already clamped overrides and applied the dependency cascade.

**Negative / trade-offs**

- One extra indirection (Authority) on every delivery call. Acceptable: delivery
  is low-frequency and the audit value is high.
- Real network tests are opt-in (`network` build tag) per rule §33; CI runs only
  fake-HTTP fixture tests.

## Alternatives considered

- **Direct provider calls from orchestration.** Rejected: no single chokepoint,
  no structural AC-7/§28 guarantee.
- **Network sandbox only.** Rejected as primary (ADR-0008): the guarantee must
  hold without a sandbox; policy-level enforcement is primary, a sandbox is
  defense-in-depth later.
