// Package codingagent defines the coding-agent adapter protocol and registry.
//
// STATUS: scaffold — not implemented (planned for milestone M2; concrete adapters
// in M4/M5).
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §12, §13): the CodingAgentAdapter interface
// (Detect/Version/Health/Capabilities/ListModels/InspectQuota/Start/Resume/
// SendMessage/Cancel/ClassifyFailure), normalized events (§12.4), the declarative
// command adapter, and the native JSON-RPC plugin protocol. Adding an agent must
// not require changes to scheduler, schema, dashboard or routing core (§13.3).
// See ADR-0005.
//
// Boundaries: adapters never decide routes, never persist durable state, and never
// receive merge credentials. The agent engine is distinct from the model (§12.1).
package codingagent
