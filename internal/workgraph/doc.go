// Package workgraph builds the work DAG and manages semantic leases.
//
// STATUS: partially implemented (milestone M3 — leases done; full DAG in a
// later milestone).
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §18.3, §18.4): the lease layer is
// implemented — it prevents concurrent work packages from modifying the same
// file paths or semantic resources (schema, navigation graph, subscription
// contract, design system, build configuration). The full DAG decomposition
// (§18.3: research → contract → integration → verification) is planned.
//
// Boundaries: leases are advisory records stored in SQLite; this package does
// not itself perform Git mutations.
package workgraph
