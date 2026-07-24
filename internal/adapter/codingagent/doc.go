// Package codingagent defines the coding-agent adapter interface and registry.
//
// The protocol data structures (the versioned stability boundary) live in the
// sibling [neuroforge/internal/adapter/codingagent/protocol] package at version
// [protocol.ProtocolVersion] (currently 1, stabilised in M2). This package adds
// the [CodingAgentAdapter] interface, the [EventSink] abstraction, the adapter
// [Registry], and the shared [DefaultClassify] failure classifier.
//
// Adding a coding agent must not require changes to the scheduler, the database
// schema, the dashboard, or the routing core (spec §13.3). There are two
// registration paths, both implemented here as [CodingAgentAdapter] values:
//
//   - the declarative command adapter (spec §13.1, subpackage [declarative]);
//   - the native JSON-RPC plugin (spec §13.2, subpackage [plugin]).
//
// The supervisor is the only core consumer of adapters. Adapters never decide
// routes, never persist durable state, and never receive merge credentials
// (spec §29.2, AC-28). The fake coding agent (spec §33.1) lives in the
// test-only [fake] subpackage and the cmd/fake-coding-agent binary.
package codingagent
