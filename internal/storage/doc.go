// Package storage provides the durable SQLite backing store for NeuroForge.
//
// STATUS: scaffold — not implemented (planned for milestone M0).
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §11.4, §31): run SQLite in WAL mode, apply
// schema migrations, persist state before every external action (attempt, PID,
// workspace, checkpoint, route, budget, last event), and keep large artifacts on
// the filesystem rather than as BLOBs. See ADR-0003 for the driver choice.
//
// Boundaries: this package must not call coding/image adapters or perform Git
// network operations; it is a pure persistence layer invoked by the daemon.
package storage
