// Package codex implements the NeuroForge coding-agent adapter for the Codex CLI
// (AC-5, M4) as an in-process Go adapter (ADAPTER_DEV_GUIDE, "Path 3").
//
// The adapter speaks the versioned coding-agent protocol (package
// [neuroforge/internal/adapter/codingagent/protocol]) at ProtocolVersion == 1.
// It never self-registers: callers construct it with [New] and register it with
// the daemon's [codingagent.Registry] at wiring time.
//
// Design notes:
//
//   - The Codex event schema varies across versions. The parser
//     ([parseCodexLine]) probes/tolerates the event shape rather than pinning one
//     version. Anything it cannot map is forwarded as a recoverable warning
//     carrying the raw bytes (spec: unknown/malformed events never abort a run).
//   - The binary is launched headless via "codex exec" in a new process group
//     ([neuroforge/internal/adapter/codingagent/proctree]) so [Adapter.Cancel]
//     terminates the entire process tree (spec: cancellation ends the whole
//     group). The agent process receives an allowlisted environment only
//     (spec §29.2, AC-28): never merge tokens, the daemon auth token, or
//     unrelated API keys.
//   - No real (paid) API call is ever made from a test (rule §36.5). Unit and
//     conformance tests drive the adapter through a deterministic [Runner] seam
//     using recorded byte-stream fixtures; an opt-in smoke test
//     ("//go:build codexsmoke") exercises a real Codex CLI.
//   - Unimplemented requirements are explicitly marked, never disguised as
//     finished stubs (rule §36.25). See the "Explicitly not implemented" section
//     of docs/adapters/codex.md.
package codex
