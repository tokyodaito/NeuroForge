package cli

import (
	"flag"
	"fmt"
	"io"

	"neuroforge/internal/enggate"
)

// runGate implements `forge gate <validate|next|baseline>` — the engineering
// baseline gate (docs/engineering/ENGINEERING_BASELINE.md). It is META
// tooling: it governs engineering work on this repository, not the product
// runtime. The command is wired into the real CLI dispatch (cli.go) so the
// enforcement is observable through the compiled `forge` binary.
func (a *App) runGate(args []string) int {
	if len(args) == 0 {
		writeGateHelp(a.Err)
		return ExitErr
	}
	switch args[0] {
	case "validate":
		return a.runGateValidate(args[1:])
	case "next":
		return a.runGateNext(args[1:])
	case "baseline":
		return a.runGateBaseline(args[1:])
	case "-h", "--help", "help":
		writeGateHelp(a.Out)
		return ExitOK
	default:
		fmt.Fprintf(a.Err, "%s: gate: unknown subcommand %q\n\n", a.Name, args[0])
		writeGateHelp(a.Err)
		return ExitErr
	}
}

func (a *App) runGateValidate(args []string) int {
	fs := flag.NewFlagSet("gate validate", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	fs.Usage = func() { writeGateHelp(a.Err) }
	var manifest string
	fs.StringVar(&manifest, "manifest", "", "path to the evidence manifest JSON")
	fs.StringVar(&manifest, "m", "", "shorthand for -manifest")
	if err := fs.Parse(args); err != nil {
		return ExitErr
	}
	if manifest == "" {
		fmt.Fprintf(a.Err, "%s: gate validate: --manifest is required\n", a.Name)
		fs.Usage()
		return ExitErr
	}
	m, err := enggate.LoadManifest(manifest)
	if err != nil {
		fmt.Fprintf(a.Err, "%s: gate validate: load manifest: %v\n", a.Name, err)
		return ExitErr
	}
	if err := enggate.ValidateTransition(m.PreviousState, m.State, m); err != nil {
		fmt.Fprintf(a.Err, "%s: gate validate: %s -> %s REJECTED for task %q\n%s\n",
			a.Name, m.PreviousState, m.State, m.TaskID, err.Error())
		return ExitErr
	}
	fmt.Fprintf(a.Out, "OK: task %q transition %s -> %s is legal under baseline v%s\n",
		m.TaskID, m.PreviousState, m.State, enggate.BaselineVersion)
	return ExitOK
}

func (a *App) runGateNext(args []string) int {
	fs := flag.NewFlagSet("gate next", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	fs.Usage = func() { writeGateHelp(a.Err) }
	var manifest string
	fs.StringVar(&manifest, "manifest", "", "path to the PREDECESSOR task's evidence manifest JSON")
	fs.StringVar(&manifest, "m", "", "shorthand for -manifest")
	if err := fs.Parse(args); err != nil {
		return ExitErr
	}
	if manifest == "" {
		fmt.Fprintf(a.Err, "%s: gate next: --manifest is required (the predecessor task's manifest)\n", a.Name)
		fs.Usage()
		return ExitErr
	}
	m, err := enggate.LoadManifest(manifest)
	if err != nil {
		fmt.Fprintf(a.Err, "%s: gate next: load predecessor manifest: %v\n", a.Name, err)
		return ExitErr
	}
	if err := enggate.CanStartNext(m); err != nil {
		fmt.Fprintf(a.Err, "%s: gate next: BLOCKED: %v\n", a.Name, err)
		return ExitErr
	}
	fmt.Fprintf(a.Out, "OK: predecessor %q is ACCEPTED; successor task may start\n", m.TaskID)
	return ExitOK
}

func (a *App) runGateBaseline(args []string) int {
	_ = args
	schema, baseline, doc := enggate.ActiveVersions()
	fmt.Fprintf(a.Out, "active schema_version: %d\n", schema)
	fmt.Fprintf(a.Out, "active baseline_version: %s\n", baseline)
	fmt.Fprintf(a.Out, "baseline document: %s\n", doc)
	return ExitOK
}

func writeGateHelp(w io.Writer) {
	_, _ = io.WriteString(w, gateHelpText)
}

const gateHelpText = `forge gate — engineering baseline gate (docs/engineering/ENGINEERING_BASELINE.md).

META tooling: governs engineering work on this repository. Not the product
runtime. Every task's implementation report must record the literal invocations
below as black-box evidence.

Usage:
  forge gate baseline
      Print the active schema/baseline version and the baseline document path.

  forge gate validate --manifest <path.json>
      Validate the manifest's claimed transition (previous_state -> state).
      Exit 0 only if the transition is legal under the active baseline:
      required evidence present, mandatory criteria backed by passing
      automated unit/integration/blackbox evidence, actor separation enforced.

  forge gate next --manifest <path.json>
      Check the PREDECESSOR task's manifest. Exit 0 only if its state is
      ACCEPTED, i.e. the successor task may start. Use the predecessor task's
      manifest path, not the successor's.

Flags:
  --manifest path, -m path   Path to the evidence manifest JSON.

Exit codes:
  0   transition legal (validate) / predecessor ACCEPTED (next)
  1   transition rejected, or a runtime/usage error
`
