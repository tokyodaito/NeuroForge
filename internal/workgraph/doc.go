// Package workgraph builds the work DAG and manages semantic leases.
//
// STATUS: scaffold — not implemented (planned for milestones M1/M2).
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §18.3, §18.4): decompose a large task into a
// DAG of work packages (research -> contract -> integration -> verification) and
// lease both file paths and semantic resources (schema, navigation graph,
// subscription contract, design system, build configuration).
//
// Boundaries: leases are advisory records; this package does not itself perform
// Git mutations.
package workgraph
