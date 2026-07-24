# ADR-0006: Image-provider adapter protocol

- **Status:** Accepted
- **Date:** 2026-07-24
- **Spec refs:** §14 (image provider API), §15 (design-to-code), §33.2 (fake
  image provider), §36.9 (keep coding agents and image providers separate)

## Context

Design generation/analysis must be a subsystem distinct from coding agents
(§14, rule §36.9). A coding agent may prepare a design brief or analyse a
reference, but image generation itself is delegated to an `ImageProviderAdapter`.
Required providers are OpenAI GPT Image and Google Nano Banana (§14.1). Image
quota/budget is tracked separately from coding quota (§14.4).

## Decision

Define `ImageProviderAdapter` in `internal/adapter/imageprovider` covering the
§14.2 surface: `ID`, `Health`, `ListModels`, `InspectQuota`, `Generate`, `Edit`,
`AnalyzeFailure`. Image models are exposed by **tier** (`IMAGE_DRAFT`,
`IMAGE_STANDARD`, `IMAGE_HIGH_QUALITY`, §14.3) and the router never hard-binds a
tier to a model name (names come from a catalog). Generation/Edit stream progress
through an `ImageEventSink`; quota snapshots feed `internal/quota` and
`internal/budget` on the separate image budget.

A fake image provider (§33.2) is the first implementation and drives the design
flow (§15) before any real provider is wired.

## Consequences

**Positive**

- Clean separation enforces §36.9 and lets image providers failover
  independently.
- Tier-based routing keeps the catalog swappable (consistent with ADR-0005's
  "never hard-code model names").

**Negative / trade-offs**

- A second adapter family doubles the conformance surface; mitigated by reusing
  the same normalized-event discipline and quota model.

## Alternatives considered

- **Let coding agents call image models directly.** Rejected: violates §36.9 and
  breaks separate quota/budget accounting and failover.
- **Single image provider.** Rejected: spec requires GPT Image and Nano Banana
  (AC-19) with failover (§15.5).
