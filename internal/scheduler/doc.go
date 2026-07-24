// Package scheduler dispatches work packages to agent runs.
//
// STATUS: scaffold — not implemented (planned for milestones M2/M3).
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §10): honour parallelism limits, semantic
// leases, budgets and quotas when assigning work packages to routes/accounts.
//
// Boundaries: routing decisions come from package router; the scheduler selects
// what runs next, not how a run is executed.
package scheduler
