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

Only bootstrap commands are implemented in the current milestone (M0 scaffold).
The full command surface is defined in docs/spec/NEUROFORGE_SPEC.md (section 30)
and is delivered across milestones M0-M13. Unimplemented requirements are tracked
explicitly in docs/spec/COMPLIANCE_MATRIX.md.

Usage:
  forge <command> [flags]

Implemented commands:
  version          Print version, commit and build/platform information
  help             Show this help message

  Aliases:
    version  -> -v, -version, --version
    help     -> -h, --help

Running "forge" with no arguments is intended to open the interactive TUI
(spec AC-1), but the TUI is not implemented yet.

Not implemented (planned, by milestone):
  forge (interactive TUI)        M0
  forge project ...              M1
  forge task ...                 M1
  forge agent ...                M2-M5
  forge model ... / route ...    M6
  forge image-provider ...       M9
  forge quota / usage / cost     M6
  forge plugin ...               M2
  forge audit                    M0
  forge emergency-stop           M0
  forge cleanup                  M0
  forge init / doctor / update   M13

Exit codes:
  0   success
  1   error (unknown command or runtime failure)
`
