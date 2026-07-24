// Package supervisor runs and supervises coding-agent processes.
//
// STATUS: scaffold — not implemented (planned for milestone M3).
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §10, §12): start/resume agent runs through
// the adapter protocol, stream normalized events, enforce turn limits (§22.7),
// capture checkpoints, and classify failures (§32) to drive retry/failover.
//
// Boundaries: agent processes run with a restricted allowlisted environment and
// never receive merge credentials or unrelated API keys (§29.2).
package supervisor
