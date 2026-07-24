// Package risk classifies task risk (R0..R4) and its consequences.
//
// STATUS: scaffold — not implemented (planned for milestone M6).
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §26): map tasks to R0 (docs/mechanical) …
// R4 (auth/payments/permissions/destructive). Risk influences model tier,
// reviews, tests, visual validation, runtime isolation, delivery permissions,
// rollback and budget.
//
// Boundaries: classification is deterministic/heuristic, not delegated to an LLM.
package risk
