package cli

import "io"

func (a *App) runHelp() int {
	writeHelp(a.Out)
	return ExitOK
}

func writeHelp(w io.Writer) {
	_, _ = io.WriteString(w, helpText)
}

const helpText = `NeuroForge — autonomous multi-model development factory (local-first).

Foundation commands are implemented in milestone M0. The full command surface
is defined in docs/spec/NEUROFORGE_SPEC.md (section 30) and is delivered across
milestones M0-M13. Unimplemented requirements are tracked explicitly in
docs/spec/COMPLIANCE_MATRIX.md.

Usage:
  forge <command> [flags]

Implemented commands:
  version             Print version, commit and build/platform information
  help                Show this help message
  doctor              Run basic system checks
  daemon run          Run the daemon in the foreground
  daemon start        Start the daemon as a detached background process
  daemon stop         Stop a running daemon
  daemon status       Print daemon lifecycle status (--json supported)
  daemon logs         Print the daemon structured log (-f follows live events)

  Aliases:
    version  -> -v, -version, --version
    help     -> -h, --help

Running "forge" with no arguments opens the interactive TUI shell (spec AC-1).
In M0 this is a minimal full-screen shell; the full TUI is delivered across
later milestones.

Not implemented (planned, by milestone):
  forge project ...              M1
  forge task ...                 M1
  forge agent ...                M2-M5
  forge model ... / route ...    M6
  forge image-provider ...       M9
  forge quota / usage / cost     M6
  forge plugin ...               M2
  forge audit                    M1+
  forge emergency-stop           M1+
  forge init / update            M13

Exit codes:
  0   success
  1   error (unknown command or runtime failure)
`
