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

Foundation and project/task management commands are implemented in milestones
M0 and M1. The full command surface is defined in docs/spec/NEUROFORGE_SPEC.md
(section 30) and is delivered across milestones M0-M13. Unimplemented requirements
are tracked explicitly in docs/spec/COMPLIANCE_MATRIX.md.

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

  project add <path>  Register a Git repository (--name, --json)
  project list        List registered projects (--json)
  project show <id>   Show project details (--json)
  project start <id>  Start the factory (DISABLED -> IDLE)
  project pause <id>  Pause the factory
  project stop <id>   Stop the factory
  project remove <id> Unregister a project (files NOT deleted)

  task add            Create a task (-p/--project, --title, --priority, -a/--attach)
  task list           List tasks (-p/--project, --json)
  task show <id>      Show task details (--json)
  task pause <id>     Pause a task
  task cancel <id>    Cancel a task

  plugin test <exe>   Run the §13.3 coding-agent conformance suite against a
                      plugin executable (--json supported)
  plugin list         List registered plugins

  dashboard           Open the interactive TUI (same as 'forge' with no args)

  Aliases:
    version  -> -v, -version, --version
    help     -> -h, --help

Running "forge" with no arguments opens the interactive TUI (spec AC-1).

Not implemented (planned, by milestone):
  forge project init/settings  M1+
  forge agent ...              M2-M5
  forge model ... / route ...  M6
  forge image-provider ...     M9
  forge quota / usage / cost   M6
  forge audit                  M1+
  forge emergency-stop         M1+
  forge init / update          M13

Exit codes:
  0   success
  1   error (unknown command or runtime failure)
`
