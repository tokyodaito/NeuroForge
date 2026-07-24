// Package daemon hosts the long-running Forge daemon process.
//
// STATUS: foundation implemented for milestone M0 (ADR-0002).
//
// Implemented in M0:
//   - Global runtime directory layout (NEUROFORGE_HOME or ~/.neuroforge).
//   - The daemon server loop: open storage (SQLite/WAL), run migrations, open
//     the append-only audit recorder, create the internal event bus, serve the
//     loopback transport API, and shut down cleanly on context cancellation,
//     SIGTERM/SIGINT or a /shutdown request.
//   - Process lifecycle: start (spawn detached child), stop (graceful then
//     forceful), status and logs.
//   - Single-instance guard: a repeated start never creates a second daemon;
//     stale/corrupted runtime state is reclaimed.
//
// Durable workflow state lives in package storage; durable audit history in
// package audit; the wire API in package transport. The daemon never holds
// merge credentials (§28) and performs no Git network operations.
package daemon
