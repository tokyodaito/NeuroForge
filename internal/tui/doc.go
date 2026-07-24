// Package tui implements the interactive terminal user interface.
//
// STATUS: foundation implemented for milestone M0 (a minimal full-screen
// shell, spec §6 / AC-1).
//
// Implemented in M0:
//   - `forge` (no args) opens a full-screen shell (alternate screen buffer)
//     that renders a banner, a "no projects" placeholder, key hints and the
//     daemon status. It exits cleanly on 'q'/Ctrl-C/EOF or context cancel.
//   - Graceful degradation: if stdout is not a terminal (e.g. CI/piped), it
//     prints a notice instead of taking over the screen (§6.7 spirit).
//
// Boundaries: the TUI talks to the daemon only via package transport; it never
// calls adapters, storage, or Git directly. The rich, raw-mode/mouse TUI and a
// framework choice are deferred to later milestones (tracked in the compliance
// matrix).
package tui
