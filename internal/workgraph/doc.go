// Package workgraph builds the work DAG and manages semantic leases.
//
// STATUS: leases implemented (M3); work-graph domain model, DAG validation, AC
// mapping, deterministic decomposition and stable serialization implemented
// (M14-04). Execution wiring (daemon, scheduler, dispatch, durable persistence
// of work_packages/dependencies rows) is a later milestone — M14-04 is
// explicitly scoped to the domain layer only.
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §18.3, §18.4):
//
//   - WorkGraph / WorkPackage / Stage / PackageState / Attempt types and their
//     invariants (graph.go).
//   - DAG validation (ValidateWorkGraph / ValidateAgainstSpec): cycle,
//     missing-edge, duplicate-AC-owner, unreachable-node, and per-package
//     structural defects. A ValidatedWorkGraph is the only "runnable" handle —
//     it is constructible only through the validator, so an invalid DAG cannot
//     become runnable (parse-don't-validate).
//   - Deterministic decomposition (Decompose): a pure function of
//     task.Specification; identical specifications produce byte-identical graphs
//     (mirrors the M14-02 task.Compile contract).
//   - Stable serialization (MarshalWorkGraph / UnmarshalWorkGraph): canonical
//     JSON, byte-identical for structurally-equal inputs.
//   - The lease layer (leases.go) prevents concurrent work packages from
//     modifying the same file paths or semantic resources (schema, navigation
//     graph, subscription contract, design system, build configuration).
//
// Boundaries: leases are advisory records stored in SQLite; this package does
// not itself perform Git mutations. The M14-04 domain model performs no I/O and
// holds no storage handle.
package workgraph
