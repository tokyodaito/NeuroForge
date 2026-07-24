// Package quota tracks provider quota and circuit-breaker state.
//
// STATUS: scaffold — not implemented (planned for milestone M6).
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §20): record quota confidence levels
// (EXACT/PROVIDER_REPORTED/ESTIMATED/INFERRED/UNKNOWN), quota states, and the
// circuit breaker (CLOSED/OPEN/HALF_OPEN). Rate limits use retry-after with
// jitter and do not exhaust the account; auth failures stop automatic retry.
//
// Boundaries: does not perform provider calls itself; it consumes snapshots
// produced by adapters.
package quota
