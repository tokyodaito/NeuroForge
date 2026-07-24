package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strings"

	"neuroforge/internal/router/fakes"
)

// runRoute dispatches `forge route <subcommand>`.
func (a *App) runRoute(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(a.Err, routeUsage)
		return ExitErr
	}
	switch args[0] {
	case "explain":
		return a.routeExplain(args[1:])
	case "-h", "--help":
		fmt.Fprintln(a.Out, routeUsage)
		return ExitOK
	default:
		fmt.Fprintf(a.Err, "%s: unknown route subcommand %q\n\n", a.Name, args[0])
		fmt.Fprintln(a.Err, routeUsage)
		return ExitErr
	}
}

const routeUsage = `Usage: forge route explain [flags] <description>

Explain the deterministic route decision for a task (spec §19.6).

The router never uses an LLM for route selection (rule §22.6). It scores the
configured model catalog against the task's complexity and risk, then prints the
selected route, the ranked alternatives, the fallback chain, the estimated cost,
the per-account quota, and the reasons other routes were excluded.

Flags:
  --complexity C0..C4   Override the deterministic complexity classifier
  --risk R0..R4         Override the deterministic risk classifier
  --files N             Estimated files the task touches (complexity signal)
  --turns N             Estimated agent turns (complexity signal)
  --images              Task needs image input (multimodal model required)
  --strong              Prefer the stronger tier when a band spans two (§19.3)
  --json                Emit machine-readable JSON`

func (a *App) routeExplain(args []string) int {
	fs := flag.NewFlagSet("route explain", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	complexity := fs.String("complexity", "", "override complexity C0..C4")
	riskFlag := fs.String("risk", "", "override risk R0..R4")
	files := fs.Int("files", 0, "estimated files touched")
	turns := fs.Int("turns", 0, "estimated agent turns")
	images := fs.Bool("images", false, "task needs image input")
	strong := fs.Bool("strong", false, "prefer the stronger tier")
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return ExitErr
	}
	description := strings.Join(fs.Args(), " ")

	r := fakes.DefaultRouter()

	// Complexity: explicit override, else classify deterministically.
	cRes := routerClassify(description, *files, *turns)
	complexityVal := cRes
	if *complexity != "" {
		if c, ok := parseComplexity(*complexity); ok {
			complexityVal = c
		} else {
			fmt.Fprintf(a.Err, "invalid --complexity %q (use C0..C4)\n", *complexity)
			return ExitErr
		}
	}
	// Risk: explicit override, else classify deterministically.
	rRes := routerRisk(description)
	riskVal := rRes
	if *riskFlag != "" {
		if rv, ok := parseRisk(*riskFlag); ok {
			riskVal = rv
		} else {
			fmt.Fprintf(a.Err, "invalid --risk %q (use R0..R4)\n", *riskFlag)
			return ExitErr
		}
	}

	ex, err := r.Route(context.Background(), routeRequest(complexityVal, riskVal, description, *files, *turns, *images, *strong))
	if err != nil {
		a.errf("route: %v", err)
		return ExitErr
	}

	if *jsonOut {
		enc, _ := json.MarshalIndent(explanationJSON(ex, cRes, rRes, *complexity != "", *riskFlag != ""), "", "  ")
		fmt.Fprintln(a.Out, string(enc))
		return ExitOK
	}

	fmt.Fprint(a.Out, routerRender(ex))
	fmt.Fprintf(a.Out, "\nDerived complexity: %s\n", complexityVal)
	fmt.Fprintf(a.Out, "Derived risk:       %s\n", riskVal)
	return ExitOK
}
