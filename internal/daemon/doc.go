// Package daemon hosts the long-running Forge daemon process.
//
// STATUS: scaffold — not implemented (planned for milestone M0, Foundation).
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §11.2): own process and durable workflow
// state, start and supervise agent processes, manage the task queue, stream
// events to the TUI, and safely resume or restart attempts after a crash.
//
// Boundaries: durable state lives in package storage (SQLite); this package must
// not contain adapter implementations, routing logic, or VCS network operations.
// The daemon never holds merge credentials (§28).
package daemon
