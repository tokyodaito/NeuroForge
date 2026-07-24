// Package budget enforces spending limits across scopes.
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §23): global/daily/monthly/project/task
// and per-provider budgets, including a separate image budget. Soft limit
// selects a cheaper route or fewer design variants; hard limit blocks new paid
// runs and may move a task to BUDGET_EXCEEDED.
//
// Invariants (rule §22.6): budget arithmetic is deterministic and never
// delegated to an LLM. Subscription-included usage is accounted separately from
// paid API cost (§23) — included usage never counts against a paid hard limit,
// and a hard budget forbids a paid run but still permits subscription-included
// routes when policy allows.
package budget
