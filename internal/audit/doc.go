// Package audit records the tamper-evident audit trail.
//
// STATUS: scaffold — not implemented (planned for milestone M0, extended later).
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §29.4): record commands, changed files,
// provider/model used, attachment transfers, push, PR/MR, merge, revert and policy
// overrides so that a full per-task history (input -> specification -> route ->
// attempts -> usage -> changes -> verification -> delivery) is reconstructable
// (AC-30).
//
// Boundaries: append-only; must not be writable by agent processes.
package audit
