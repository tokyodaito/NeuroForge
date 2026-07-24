// Package tui implements the interactive terminal user interface.
//
// STATUS: scaffold — not implemented (planned for milestone M0 as a shell).
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §6): full-screen interactive UI reachable
// by running "forge" with no arguments (AC-1). Must not depend on a specific
// terminal emulator's image support (§6.7). Communicates with the daemon only
// through package transport.
//
// Boundaries: the TUI must not call adapters, storage, or Git directly.
package tui
