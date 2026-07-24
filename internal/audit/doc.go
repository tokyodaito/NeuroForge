// Package audit records the tamper-evident audit trail.
//
// STATUS: foundation implemented for milestone M0 (append-only store over
// SQLite, spec §29.4 / AC-30).
//
// The audit trail is append-only: events can be recorded and queried, never
// updated or deleted. Mutation is rejected at the storage layer (triggers) and
// the Recorder exposes no update/delete API. A full per-task history (input ->
// specification -> route -> attempts -> usage -> changes -> verification ->
// delivery) is reconstructable by querying events by scope/scope id.
//
// Boundaries: the audit store must never be writable by agent processes.
package audit
