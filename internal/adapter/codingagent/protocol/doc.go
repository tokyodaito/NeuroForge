// Package protocol is the stable, versioned wire contract for coding-agent
// adapters.
//
// This is the NeuroForge coding-agent protocol stability boundary (spec §12,
// §13; ADR-0005). Everything in this package is part of the versioned public
// surface: [ProtocolVersion] is fixed at 1 and changes here are governed by the
// conformance suite. The supervisor, scheduler, router and storage interact only
// with the types defined here and the [CodingAgentAdapter] interface in the
// parent [neuroforge/internal/adapter/codingagent] package — never with a
// specific engine (spec §13.3).
//
// Important invariants enforced by this package:
//
//   - The agent engine and the model are distinct entities (spec §12.1): a
//     [RunHandle] / [AgentRunRequest] carries Engine and Model separately.
//   - No hard-coded model names live here (rule §36.8): models are supplied by
//     adapters via [ModelDescriptor].
//   - Adapter requests never carry credentials (spec §29.2, AC-28): an
//     [AgentRunRequest] references an [Account] by name only — merge tokens, the
//     daemon auth token and unrelated API keys are never passed to an agent.
//
// The package is intentionally free of I/O and process management; transport
// concerns live in the parent package and its subpackages.
package protocol
