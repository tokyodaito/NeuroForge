// Package storage provides the durable SQLite backing store for NeuroForge.
//
// STATUS: foundation implemented for milestone M0 (ADR-0003, ADR-0010).
//
// Implemented in M0:
//   - Open a SQLite database in WAL mode with sane per-connection pragmas
//     (busy_timeout, synchronous=NORMAL, foreign_keys).
//   - A forward-only, idempotent schema migration runner (schema_migrations).
//   - The audit_events table and its append-only persistence helpers (the rest
//     of the §31 table set lands with later milestones as it is needed).
//
// Large artifacts are stored on the filesystem, not as BLOBs (§31); only
// references/hashes live here.
//
// Boundaries: this package must not call coding/image adapters or perform Git
// network operations; it is a pure persistence layer invoked by the daemon.
package storage
