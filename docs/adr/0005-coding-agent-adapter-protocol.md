# ADR-0005: Coding-agent adapter protocol

- **Status:** Accepted
- **Date:** 2026-07-24
- **Spec refs:** §12 (engines + adapter interface), §13 (extensibility), §22
  (token optimization), §33 (fake agent)

## Context

NeuroForge must support many coding engines (Codex, Claude Code, Grok Build, Kimi
Code, OpenCode, Gemini CLI — §12) plus user-defined ones, *without* changes to the
scheduler, database schema, dashboard or routing core (§13.3). An engine is not a
model (§12.1): the same engine can target different models/accounts. The fake
coding agent must be built first (rule §36.6) and the protocol stabilised before
concrete adapters (§36.7).

## Decision

Define a single `CodingAgentAdapter` interface in `internal/adapter/codingagent`
covering exactly the §12.2 surface: `ID`, `Detect`, `Version`, `Health`,
`Capabilities`, `ListModels`, `InspectQuota`, `Start`, `Resume`, `SendMessage`,
`Cancel`, `ClassifyFailure`. Adapters emit the **normalized event set** of §12.4
(`run.started` … `run.cancelled`) through an `EventSink`.

Two registration paths (§13):

1. **Declarative command adapter** (§13.1): a YAML file describes a CLI agent —
   no Go code changes.
2. **Native plugin** (§13.2): JSON-RPC 2.0 over stdin/stdout with the mandatory
   methods of §13.2.

The supervisor (`internal/supervisor`) is the **only** core consumer of adapters;
the scheduler, router and schema interact with capabilities/events, never with a
specific engine. A conformance suite (`forge plugin test`, §13.3) validates
handshake, event ordering, malformed output, cancellation, timeout, quota, resume,
crash and version compatibility.

Adapter process environment is allowlisted (§29.2) and **never** receives merge
credentials or the daemon auth token.

## Consequences

**Positive**

- Adding a 7th agent is purely additive (AC-6); core stays stable.
- Normalized events make failure classification and failover uniform.

**Negative / trade-offs**

- The interface is the stability boundary — changing it affects all adapters;
  mitigated by the conformance suite and staging it in M2 before adapters.

## Alternatives considered

- **Per-engine core code.** Rejected: violates §13.3; unmaintainable.
- **Single "best" engine.** Rejected: spec requires six (AC-5) and provider
  diversity for failover.
