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

// runMemory dispatches `forge memory <subcommand>`.
func (a *App) runMemory(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(a.Err, memoryUsage)
		return ExitErr
	}
	switch args[0] {
	case "list":
		return a.memoryList(args[1:])
	case "learn":
		return a.memoryLearn(args[1:])
	case "-h", "--help":
		fmt.Fprintln(a.Out, memoryUsage)
		return ExitOK
	default:
		fmt.Fprintf(a.Err, "%s: unknown memory subcommand %q\n\n", a.Name, args[0])
		fmt.Fprintln(a.Err, memoryUsage)
		return ExitErr
	}
}

const memoryUsage = `Usage: forge memory <subcommand> [flags]

Subcommands:
  list <project-id> [--json]                 List project memory (§22.9)
  learn <project-id> -c <cat> -k <key> -v <val>   Learn a memory fact

Categories: architecture_fact | build_command | design_system_rule |
            known_failure | accepted_decision | provider_quirk
Confidence: high | medium | low | unverified (default: medium)`

func (a *App) memoryList(args []string) int {
	fs := flag.NewFlagSet("memory list", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return ExitErr
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(a.Err, "Usage: forge memory list <project-id> [--json]")
		return ExitErr
	}
	id := fs.Arg(0)

	cli, err := a.ensureDaemon()
	if err != nil {
		a.errf("connect to daemon: %v", err)
		return ExitErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	rows, err := cli.ListMemory(ctx, id)
	if err != nil {
		if *jsonOut {
			fmt.Fprintln(a.Out, jsonError(err))
		} else {
			a.errf("list memory: %v", err)
		}
		return ExitErr
	}
	if *jsonOut {
		b, _ := json.MarshalIndent(rows, "", "  ")
		fmt.Fprintln(a.Out, string(b))
		return ExitOK
	}
	if len(rows) == 0 {
		fmt.Fprintln(a.Out, "No project memory records.")
		return ExitOK
	}
	for _, r := range rows {
		fmt.Fprintf(a.Out, "[%s] %s = %s  (confidence=%s, v%d)\n", r.Category, r.Key, r.Value, r.Confidence, r.Version)
	}
	return ExitOK
}

func (a *App) memoryLearn(args []string) int {
	fs := flag.NewFlagSet("memory learn", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	category := fs.String("c", "", "category (required)")
	key := fs.String("k", "", "key (required)")
	value := fs.String("v", "", "value (required)")
	confidence := fs.String("confidence", "medium", "high | medium | low | unverified")
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return ExitErr
	}
	if fs.NArg() < 1 || *category == "" || *key == "" || *value == "" {
		fmt.Fprintln(a.Err, "Usage: forge memory learn <project-id> -c <cat> -k <key> -v <val>")
		return ExitErr
	}
	id := fs.Arg(0)

	cli, err := a.ensureDaemon()
	if err != nil {
		a.errf("connect to daemon: %v", err)
		return ExitErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	rec, err := cli.LearnMemory(ctx, id, transport.LearnMemoryRequest{
		ProjectID: id, Category: *category, Key: *key, Value: *value, Confidence: *confidence,
	})
	if err != nil {
		if *jsonOut {
			fmt.Fprintln(a.Out, jsonError(err))
		} else {
			a.errf("learn memory: %v", err)
		}
		return ExitErr
	}
	if *jsonOut {
		b, _ := json.MarshalIndent(rec, "", "  ")
		fmt.Fprintln(a.Out, string(b))
	} else {
		fmt.Fprintf(a.Out, "Learned [%s] %s (v%d)\n", rec.Category, rec.Key, rec.Version)
	}
	return ExitOK
}

// runQuality dispatches `forge quality` — shows token accounting + per-model
// success rates (§6.1, §19.1).
func (a *App) runQuality(args []string) int {
	fs := flag.NewFlagSet("quality", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return ExitErr
	}
	cli, err := a.ensureDaemon()
	if err != nil {
		a.errf("connect to daemon: %v", err)
		return ExitErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	stats, err := cli.QualityStats(ctx)
	if err != nil {
		if *jsonOut {
			fmt.Fprintln(a.Out, jsonError(err))
		} else {
			a.errf("quality: %v", err)
		}
		return ExitErr
	}
	if *jsonOut {
		b, _ := json.MarshalIndent(stats, "", "  ")
		fmt.Fprintln(a.Out, string(b))
		return ExitOK
	}
	fmt.Fprintf(a.Out, "Overall success rate: %.1f%%\n", stats.OverallSuccessRate*100)
	if len(stats.ByModel) > 0 {
		fmt.Fprintln(a.Out, "\nPer-model:")
		fmt.Fprintln(a.Out, "  ENGINE        MODEL         ATTEMPTS  SUCCESS%")
		for _, m := range stats.ByModel {
			fmt.Fprintf(a.Out, "  %-12s  %-12s  %8d  %6.1f\n", m.Engine, m.Model, m.Attempts, m.SuccessRate*100)
		}
	}
	t := stats.Totals
	fmt.Fprintf(a.Out, "\nUsage totals:\n")
	fmt.Fprintf(a.Out, "  coding input:  %d (cached %d)\n", t.CodingInput, t.CachedInput)
	fmt.Fprintf(a.Out, "  coding output: %d\n", t.CodingOutput)
	if t.ImageGenerations > 0 {
		fmt.Fprintf(a.Out, "  image gens:    %d\n", t.ImageGenerations)
	}
	fmt.Fprintf(a.Out, "  est cost:      $%.4f (%d events)\n", t.EstimatedCostUSD, t.EventCount)
	return ExitOK
}

// usageForProject handles `forge usage --project <id>` by querying the daemon's
// per-project usage endpoint when a project is specified.
func (a *App) usageForProject(projectID string, jsonOut bool) int {
	cli, err := a.ensureDaemon()
	if err != nil {
		a.errf("connect to daemon: %v", err)
		return ExitErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	totals, err := cli.ProjectUsage(ctx, projectID)
	if err != nil {
		if jsonOut {
			fmt.Fprintln(a.Out, jsonError(err))
		} else {
			a.errf("usage: %v", err)
		}
		return ExitErr
	}
	if jsonOut {
		b, _ := json.MarshalIndent(totals, "", "  ")
		fmt.Fprintln(a.Out, strings.TrimSpace(string(b)))
		return ExitOK
	}
	fmt.Fprintf(a.Out, "Project %s usage:\n", projectID)
	fmt.Fprintf(a.Out, "  coding input:  %d (cached %d)\n", totals.CodingInput, totals.CachedInput)
	fmt.Fprintf(a.Out, "  coding output: %d\n", totals.CodingOutput)
	fmt.Fprintf(a.Out, "  est cost:      $%.4f (%d events)\n", totals.EstimatedCostUSD, totals.EventCount)
	return ExitOK
}
