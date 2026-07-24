// Package risk classifies task risk (R0..R4) and its consequences.
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §26): map tasks to R0 (docs/mechanical)
// … R4 (auth/payments/permissions/destructive). Risk influences model tier,
// reviews, tests, visual validation, runtime isolation, delivery permissions,
// rollback and budget.
//
// Classification is deterministic/heuristic, never delegated to an LLM (rule
// §22.6, §26). The classifier consumes structured signals produced by the task
// compiler / change analysis and returns the highest applicable level plus the
// human-readable reasons that justify it (so a route decision is fully
// explainable, §19.6).
package risk
