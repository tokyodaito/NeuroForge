# ADR-0012: Versioned coding-agent protocol package (v1)

- **Status:** Accepted
- **Date:** 2026-07-24
- **Spec refs:** §12 (engines + adapter interface), §13 (extensibility), §22
  (usage), §32 (failure taxonomy), §33.1 (fake agent)
- **Supersedes / extends:** ADR-0005 (records the concrete realisation of the
  adapter protocol agreed there; ADR-0005 remains the architectural decision)

## Context

ADR-0005 decided on a single `CodingAgentAdapter` interface in
`internal/adapter/codingagent`, normalised events, and two registration paths
(declarative command adapter + native JSON-RPC plugin). M2 turns that decision
into a stabilised, shippable protocol.

Two structural choices needed a recorded decision:

1. **Where the protocol types live.** The spec (§12.2/§12.4) shows the interface
   and events together in the adapter package. The M2 task explicitly required
   that the *protocol structures live in a separate, stable, versioned package*,
   so adding an engine and evolving the wire format have an obvious, narrow
   stability boundary.
2. **How streamed events reach the daemon over JSON-RPC.** §13.2 lists the
   mandatory request/response methods but does not name the streaming transport.
3. **YAML for the declarative manifest.** §13.1 shows YAML; the project keeps
   external dependencies minimal (AGENTS.md, ADR-0010).

## Decision

1. **Split the versioned contract into `protocol`.** All wire/stable data
   structures — `ProtocolVersion` (fixed at **1**), `AgentCapabilities`,
   `AgentRunRequest`/`ResumeRequest`/`RunHandle`, the §12.4 `NormalizedEvent`
   set, the §32 `FailureClass` taxonomy, and the JSON-RPC 2.0 envelope/method
   constants — live in `internal/adapter/codingagent/protocol`. The
   `CodingAgentAdapter` interface, `EventSink`, registry and the shared
   `DefaultClassify` live in the parent `codingagent` package and reference
   `protocol.*` types. `ProtocolVersion == 1` is the stability boundary;
   additive changes (new optional fields, new event types) do **not** bump the
   major version, and consumers must ignore unknown event types rather than fail.

2. **`run.event` JSON-RPC notification for streaming.** A native plugin streams
   normalized events during a run as JSON-RPC notifications on the additive
   method `run.event` (one `NormalizedEvent` per notification). This is
   additional to the §13.2 mandatory request/response methods. It keeps the
   transport single-channel (stdin/stdout) and matches the SSE/event-sink model
   the supervisor already uses.

3. **Zero-dependency manifest parser.** The declarative manifest is parsed by a
   deliberately small YAML-subset parser (`declarative/manifest.go`) supporting
   the §13.1 grammar (nested maps, block and flow sequences, scalars, comments).
   No third-party YAML library is introduced; this honours the minimal-dependency
   principle (AGENTS.md, ADR-0010). Richer manifests can be supplied as JSON.

## Consequences

**Positive**

- A clear, versioned boundary: a new engine or a future protocol v2 touches only
  the `protocol` package + adapters, never the core.
- The conformance suite (`forge plugin test`, §13.3) pins the v1 surface; a 7th
  agent passes with no core changes (AC-6).
- No new runtime dependency.

**Negative / trade-offs**

- The minimal YAML parser is not a general YAML implementation; users with
  exotic manifests are pointed at the JSON form. Acceptable: the §13.1 grammar
  is small and stable.
- Splitting types from the interface means the spec's illustrative Go snippet
  gains `protocol.` qualifiers; documented here so it is not a surprise.

## Alternatives considered

- **Keep everything in `codingagent`.** Rejected: no obvious stability boundary,
  contradicts the explicit M2 requirement for a separate versioned package.
- **Add `gopkg.in/yaml.v3`.** Rejected for now: the manifest grammar is tiny and
  the project deliberately minimises dependencies; revisit if manifests grow
  complex.
- **Per-event JSON-RPC method names for streaming.** Rejected: a single
  `run.event` notification is simpler and lets the dispatcher share one code
  path with the declarative JSONL parser (`protocol.ParseEventLine`).
