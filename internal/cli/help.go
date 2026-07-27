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

  spec compile        Deterministically compile free-form task text into a
                      structured task.Specification (§18.1) (--project,
                      --title, --priority, --attach hash=ROLE[:filename[:mimeType[:size]]],
                      --json opt-in for machine-readable output) (M14-02)
  spec save           Daemon-mediated compile-and-save: read a task, compile
                      it, durably persist the resulting Specification
                      (idempotent; survives restart) (M14-03)
  spec show           Show the latest (or --version) persisted Specification
                      for a task (M14-03)
  spec lock           Mark a Specification version immutable (§28) (M14-03)
  spec versions       List persisted Specification versions for a task (M14-03)

  run \"<description>\"  One-shot reliable run: create task + worktree, run one
                      production adapter (opencode), finalize + result ref.
                      (--engine, --model, --file, --base, --timeout, --json,
                       --verbose)

  workspace create -t <task>   Create an isolated Git worktree (--wp, --base, --json)
  workspace list [-t <task>]   List workspaces (--project, --json)
  workspace show <id>          Show workspace details (--json)
  workspace run <id>           Run the fake agent in the workspace (--engine)
  workspace checkpoint <id>    Create a checkpoint commit (--moment, --message)
  workspace result <id>        Create the local result branch (forge/result/<task>)
  workspace review <id> -a X   Review result: keep | reject | ask (--json)
  workspace diff <id>          Show the diff (base..HEAD)
  workspace patch <id>         Export the result as a patch
  workspace delete <id>        Delete workspace (worktree only; never user data)
  workspace checkpoints <id>   List checkpoints (--json)

  plugin test <exe>   Run the §13.3 coding-agent conformance suite against a
                      plugin executable (--json supported)
  plugin list         List registered plugins

  dashboard           Open the interactive TUI (same as 'forge' with no args)

  quota               Show provider quota per account (§20) (--json)
  usage               Show aggregated usage: included vs paid, confidence-tagged (§14.4) (--json)
  cost                Show cost report across scopes (§23) (--json)
  route explain       Explain the deterministic route decision for a task (§19.6) (--json)

  image-provider list     List registered image providers (§14) (--json)
  image-provider doctor   Run the §14 image-provider conformance suite (--json)

  init                Onboarding wizard: scan, plan, confirm, install (§7) (--dry-run)
  init --dry-run      Show the installation plan and change nothing (AC-25)
  init --repair       Reconcile the toolchain with the lock (reinstall missing)
  update              Update toolchain (compat check, conformance, rollback §7.5)

  gate baseline       Print the active engineering baseline version + doc path
  gate validate -m <manifest.json>   Validate a task manifest's claimed transition
  gate next -m <manifest.json>       Exit 0 only if the predecessor task is ACCEPTED
                      (META: engineering baseline gate; see
                       docs/engineering/ENGINEERING_BASELINE.md)

  Aliases:
    version  -> -v, -version, --version
    help     -> -h, --help

Running "forge" with no arguments opens the interactive TUI (spec AC-1).

Not implemented (planned, by milestone):
  forge project init/settings  M1+
  forge agent ...              M2-M5
  forge model ...              M6+
  forge audit                  M1+
  forge emergency-stop         M1+

Exit codes:
  0   success
  1   error (unknown command or runtime failure)
`
