# ADR-0011: Terminal raw-mode via golang.org/x/term

| | |
|---|---|
| **Status** | Accepted |
| **Date**   | 2026-07-24 |
| **Context** | [M1 — TUI screens](../milestones/IMPLEMENTATION_PLAN.md#m1--projects-and-local-tasks) |

## Context

The M1 TUI requires interactive keyboard input (arrow keys, Tab, Ctrl-P for the
command palette), terminal-size detection, and mouse tracking. The M0 TUI shell
avoided raw mode by reading single bytes in canonical (line-buffered) mode, which
required the user to press Enter after each key — acceptable for a placeholder
shell, but not for a real TUI with list navigation and a command palette.

Raw terminal mode (disabling canonical processing, echo, and signal generation)
requires platform-specific termios/ioctl syscalls. The Go standard library
`syscall` package exposes these but with different constant names on Linux vs
macOS, and no Windows support. The project already transitively depends on
`golang.org/x/sys` (via `modernc.org/sqlite`).

## Decision

Add **`golang.org/x/term`** as a direct dependency for terminal control.

It provides:
- `term.MakeRaw(fd)` / `term.Restore(fd, state)` — raw mode enter/exit
- `term.GetSize(fd)` — terminal dimensions for layout
- `term.IsTerminal(fd)` — TTY detection (replaces the heuristic char-device check)
- Cross-platform: Unix (termios) and Windows (console mode)

## Justification

- **Go team-maintained** — part of the `golang.org/x` sub-repositories; as close
  to stdlib as an external dependency gets.
- **BSD-3-Clause licensed** — compatible with the project.
- **Minimal** — focused solely on terminal control; no bloat.
- **Already transitively available** — `golang.org/x/sys` is already in the
  dependency tree (indirect, via `modernc.org/sqlite`); promoting it to direct
  adds no new transitive dependencies.
- **No viable stdlib alternative** — the `syscall` package does not abstract the
  platform differences, and has no Windows console-mode support.

Only `internal/tui` imports `golang.org/x/term`; no other package depends on it.

## Alternatives considered

1. **Raw termios syscalls via `syscall`** — rejected: platform-specific constants,
   no Windows support, and significant boilerplate for a solved problem.
2. **A full TUI framework** (bubbletea, tview) — rejected for M1: they bring
  larger dependency trees. The project's TUI is built with raw ANSI escape codes;
  `golang.org/x/term` gives us just the terminal control layer we need.
3. **Continue without raw mode** — rejected: the M0 line-buffered approach cannot
  support arrow-key navigation, command palette input, or mouse tracking.

## Consequences

- `golang.org/x/term` is added to `go.mod` as a direct dependency.
- `golang.org/x/sys` is promoted from indirect to direct (already present).
- The TUI package (`internal/tui`) is the sole consumer.
