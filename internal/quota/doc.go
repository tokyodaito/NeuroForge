// Package quota tracks provider quota and circuit-breaker state.
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §20): record quota confidence levels
// (EXACT/PROVIDER_REPORTED/ESTIMATED/INFERRED/UNKNOWN), quota states, and the
// circuit breaker (CLOSED/OPEN/HALF_OPEN). Rate limits use retry-after and do
// NOT exhaust the account (§20.3); auth failures stop automatic retry.
//
// Boundary: quota never performs provider calls itself; it consumes snapshots
// produced by adapters. All arithmetic is deterministic (rule §22.6) and never
// delegated to an LLM. Estimated quota is never surfaced as exact (rule §36.10):
// the confidence level is carried end-to-end through to the dashboard.
package quota
