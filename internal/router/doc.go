// Package router selects coding engine + model + account + runtime for a run.
//
// STATUS: scaffold — not implemented (planned for milestone M6).
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §19): consume routing signals (role,
// complexity, risk, quota, cost, latency, history, provider diversity) and pick a
// route; never hard-code current model names — names come from a configurable
// catalog. Supports escalation/de-escalation (§19.4, §19.5) and "route explain".
//
// Boundaries: must not invoke adapters or persist state; it only computes a route
// decision that the scheduler and supervisor execute.
package router
