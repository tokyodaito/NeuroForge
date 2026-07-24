// Package router selects a coding engine + model + account + runtime for a run.
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §19): consume routing signals (role,
// complexity, risk, quota, cost, latency, history, provider diversity) and pick
// a route; never hard-code current model names — names come from a configurable
// catalog. Supports escalation/de-escalation (§19.4, §19.5) and "route explain"
// (§19.6).
//
// Invariants (spec §19, rule §22.6/§36.8):
//   - Route selection is deterministic scoring, NEVER an LLM call (rule §22.6).
//   - Model names live in the catalog and are provider-supplied; the core never
//     hard-codes them (rule §36.8, §19.2).
//   - Engine, model and account are distinct (§12.1, §19): a route binds one of
//     each.
//   - An exhausted account is excluded from new routes (§20.3).
//   - Every decision is fully explainable: the chosen route, alternatives, the
//     estimated cost, quota and the reasons other routes were excluded (§19.6).
//
// Boundaries: the router computes a decision only; it never invokes adapters or
// persists state. The scheduler/supervisor execute the decision.
package router
