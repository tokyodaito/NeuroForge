package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strings"
	"time"

	"neuroforge/internal/transport"
)

// runWorkGraph dispatches `forge workgraph <subcommand>`. M14-05 implements
// the durable Work Graph inspection surface (§18.3). `forge workgraph show`
// reaches the daemon-mediated GET /tasks/{id}/workgraph endpoint and prints
// the graph + readiness verdicts (mandatory black-box AC: "graph show через
// daemon").
func (a *App) runWorkGraph(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(a.Err, workGraphUsage)
		return ExitErr
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "show":
		return a.workGraphShow(rest)
	case "-h", "--help":
		fmt.Fprint(a.Out, workGraphUsage)
		return ExitOK
	default:
		fmt.Fprintf(a.Err, "%s: unknown workgraph subcommand %q\n\n", a.Name, sub)
		fmt.Fprint(a.Err, workGraphUsage)
		return ExitErr
	}
}

const workGraphUsage = `Usage: forge workgraph <subcommand> [flags]

Subcommands:
  show -t <id> [--json]
                            Read the task's durable work graph (packages,
                            dependencies, per-package readiness verdicts and
                            active leases) from the daemon. Survives daemon
                            restart.

Flags:
  -t, --task <id>        Task ID (required)
      --json             Emit machine-readable JSON (opt-in; default is text)
`

// workGraphShow implements `forge workgraph show -t <id>`: read the task's
// work graph through the daemon and print it (text by default, JSON on
// --json).
func (a *App) workGraphShow(args []string) int {
	fs := flag.NewFlagSet("workgraph show", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	fs.Usage = func() { fmt.Fprint(a.Err, workGraphUsage) }
	taskID := fs.String("task", "", "task ID (required)")
	fs.StringVar(taskID, "t", "", "shortcut for --task")
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return ExitErr
	}
	if *taskID == "" {
		fmt.Fprintln(a.Err, "Error: --task (-t) is required")
		return ExitErr
	}

	cli, err := a.ensureDaemon()
	if err != nil {
		a.errf("connect to daemon: %v", err)
		return ExitErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	graph, err := cli.GetWorkGraph(ctx, *taskID)
	if err != nil {
		if *jsonOut {
			fmt.Fprintln(a.Out, jsonError(err))
		} else {
			a.errf("workgraph show: %v", err)
		}
		return ExitErr
	}
	if *jsonOut {
		b, _ := json.MarshalIndent(graph, "", "  ")
		fmt.Fprintln(a.Out, string(b))
		return ExitOK
	}
	writeWorkGraphText(a, graph)
	return ExitOK
}

// writeWorkGraphText renders a transport.WorkGraphDTO as human-readable text.
// The format mirrors writeSpecDTOText's shape so the CLI's spec/workgraph
// commands share a consistent look.
func writeWorkGraphText(a *App, g transport.WorkGraphDTO) {
	fmt.Fprintf(a.Out, "TaskID:     %s\n", g.TaskID)
	fmt.Fprintf(a.Out, "Packages:   %d\n", len(g.Packages))
	for _, p := range g.Packages {
		fmt.Fprintf(a.Out, "\n  %s  [%s]  state=%s\n", p.ID, p.Stage, p.State)
		if p.Title != "" {
			fmt.Fprintf(a.Out, "    Title:     %s\n", p.Title)
		}
		if p.Objective != "" {
			fmt.Fprintf(a.Out, "    Objective: %s\n", p.Objective)
		}
		if len(p.AcceptedACIDs) > 0 {
			fmt.Fprintf(a.Out, "    ACs:       %s\n", strings.Join(p.AcceptedACIDs, ", "))
		}
		if len(p.AllowedScope) > 0 {
			fmt.Fprintf(a.Out, "    Scope:     %s\n", strings.Join(p.AllowedScope, ", "))
		}
		if len(p.Dependencies) > 0 {
			fmt.Fprintf(a.Out, "    Deps:      %s\n", strings.Join(p.Dependencies, ", "))
		}
		if p.Readiness != nil {
			verdict := "ready"
			if !p.Readiness.Ready {
				verdict = "blocked"
			}
			fmt.Fprintf(a.Out, "    Readiness: %s\n", verdict)
			for _, reason := range p.Readiness.BlockedReasons {
				fmt.Fprintf(a.Out, "      - %s\n", reason)
			}
		}
	}
	if len(g.ActiveLeases) > 0 {
		fmt.Fprintf(a.Out, "\nActive Leases (%d):\n", len(g.ActiveLeases))
		for _, l := range g.ActiveLeases {
			expires := ""
			if l.ExpiresAt != "" {
				expires = " (expires " + l.ExpiresAt + ")"
			}
			fmt.Fprintf(a.Out, "  - %s %s held by %s%s\n",
				l.Kind, l.Resource, l.WorkspaceID, expires)
		}
	}
}
