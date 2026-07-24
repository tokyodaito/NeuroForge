# ADR-0004: TUI ↔ daemon loopback transport (HTTP JSON + SSE)

- **Status:** Accepted
- **Date:** 2026-07-24
- **Spec refs:** §11.3 (transport), §6 (TUI)

## Context

The TUI and CLI must communicate with the daemon (ADR-0002) on the same machine.
The spec recommends (§11.3) HTTP JSON for commands, Server-Sent Events for live
events, a random local auth token, and binding only to loopback.

## Decision

Implement the TUI/CLI ↔ daemon transport in `internal/transport` as:

- **Commands:** HTTP/JSON over the loopback interface (`127.0.0.1` / `::1` only).
- **Live events:** Server-Sent Events (SSE) for streaming normalized events to the
  TUI without polling.
- **Auth:** a per-daemon random bearer token generated at startup; the token is
  never exposed to agent processes (§29.2) and is the only accepted credential.
- **Binding:** the listener MUST refuse any non-loopback bind.

## Consequences

**Positive**

- Standard, debuggable, scriptable protocol; the CLI uses the same command API as
  the TUI (so `--json` read commands, §30, are free).
- SSE gives real-time updates with no external broker.

**Negative / trade-offs**

- HTTP has more framing overhead than a raw socket; acceptable for a local UI and
  outweighed by debuggability and the unified CLI/TUI surface.
- Must enforce loopback + token discipline in code and tests (AC-7 boundary).

## Alternatives considered

- **gRPC / Cap'n Proto.** Rejected: heavier toolchain and a dependency for no
  local-only benefit; JSON keeps the CLI surface trivially scriptable.
- **Unix domain socket.** Reasonable, but the spec recommends HTTP+SSE; a domain
  socket can be offered as an additional transport later without changing the
  command model.
- **Shared in-memory (no transport).** Rejected: the TUI must work as a separate
  process from the daemon and survive TUI restarts.
