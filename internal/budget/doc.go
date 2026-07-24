// Package budget enforces spending limits across scopes.
//
// STATUS: scaffold — not implemented (planned for milestone M6).
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §23): global/daily/monthly/project/task and
// per-provider budgets, including a separate image budget. Soft limit selects a
// cheaper route or fewer design variants; hard limit blocks new paid runs and may
// move a task to BUDGET_EXCEEDED.
//
// Boundaries: budget arithmetic is deterministic and must never be delegated to
// an LLM (rule §22.6).
package budget
