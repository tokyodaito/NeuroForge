package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/conformance"
	"neuroforge/internal/adapter/codingagent/fake"
	"neuroforge/internal/adapter/codingagent/plugin"
)

// runPlugin dispatches `forge plugin <subcommand>` (spec §13.3, §30).
func (a *App) runPlugin(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(a.Err, pluginUsage)
		return ExitErr
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "test":
		return a.pluginTest(rest)
	case "list":
		fmt.Fprintln(a.Out, "No plugins registered yet (M2: protocol ready; registration wiring arrives with the supervisor).")
		return ExitOK
	case "-h", "--help":
		fmt.Fprintln(a.Out, pluginUsage)
		return ExitOK
	default:
		fmt.Fprintf(a.Err, "%s: unknown plugin subcommand %q\n\n", a.Name, sub)
		fmt.Fprintln(a.Err, pluginUsage)
		return ExitErr
	}
}

const pluginUsage = `Usage: forge plugin <subcommand> [flags]

Subcommands:
  test <executable> [flags]   Run the §13.3 conformance suite against a plugin
  list                        List registered plugins

Plugin test flags:
  --json                      Emit machine-readable JSON results
  --timeout <duration>        Per-check timeout (default 15s)
  --arg <value>               Extra argument to pass to the plugin (repeatable)
  --scenario <name>           Override FAKE_SCENARIO (for the fake agent)`

// pluginTest runs the conformance suite against a plugin executable that speaks
// the native JSON-RPC plugin protocol (spec §13.2/§13.3). The plugin is spawned
// fresh per check with FAKE_SCENARIO set so scenario-aware plugins (the fake
// agent) exercise every protocol path. AC-6: a 7th agent passes here with no
// core changes.
func (a *App) pluginTest(args []string) int {
	fs := flag.NewFlagSet("plugin test", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON results")
	timeout := fs.Duration("timeout", 15*time.Second, "per-check timeout")
	scenario := fs.String("scenario", "", "override FAKE_SCENARIO for the plugin (fake agent)")
	var extraArgs []string
	fs.Func("arg", "extra argument passed to the plugin (repeatable)", func(s string) error {
		extraArgs = append(extraArgs, s)
		return nil
	})
	// The Go flag package stops at the first non-flag argument; reorder so the
	// natural `plugin test <exe> --json` form works.
	if err := fs.Parse(reorderFlags(args)); err != nil {
		return ExitErr
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(a.Err, "Usage: forge plugin test <executable> [flags]")
		return ExitErr
	}
	execPath := fs.Arg(0)
	pluginArgs := append([]string{"--mode", "jsonrpc"}, extraArgs...)

	factory := func(ctx context.Context, sc fake.Scenario) (codingagent.Adapter, func(), error) {
		env := append([]string{}, os.Environ()...)
		// Scenario env: honour an explicit --scenario override, else the
		// per-check scenario (fake agent honours it; real plugins ignore it).
		s := *scenario
		if s == "" {
			s = string(sc)
		}
		env = append(env, "FAKE_SCENARIO="+s)
		ad, err := plugin.DialAdapter(ctx, execPath, pluginArgs, env)
		if err != nil {
			return nil, nil, fmt.Errorf("dial plugin %q: %w", execPath, err)
		}
		return ad, func() {
			if pa, ok := ad.(interface{ Close() error }); ok {
				_ = pa.Close()
			}
		}, nil
	}

	suite := &conformance.Suite{Factory: factory, Timeout: *timeout}
	// A whole-suite budget: per-check timeout × (checks + handshake slack).
	budget := timeoutMultiplier(*timeout, len(conformance.Names())+2)
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	results := suite.Run(ctx)
	return reportConformance(a, results, *jsonOut)
}

func timeoutMultiplier(base time.Duration, n int) time.Duration {
	return time.Duration(int64(base) * int64(n))
}

// reorderFlags moves flag tokens (and their values) before positional tokens so
// the stdlib flag package parses flags that appear after the executable path
// (e.g. `plugin test ./agent --json`). valueFlags lists flags that consume the
// following token as their value.
func reorderFlags(args []string) []string {
	valueFlags := map[string]bool{
		"timeout": true, "arg": true, "scenario": true,
	}
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			positional = append(positional, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			continue // --name=value carries its value inline
		}
		if valueFlags[name] && i+1 < len(args) {
			flags = append(flags, args[i+1])
			i++
		}
	}
	return append(flags, positional...)
}

func reportConformance(a *App, results []conformance.CheckResult, jsonOut bool) int {
	if jsonOut {
		b, _ := json.MarshalIndent(results, "", "  ")
		fmt.Fprintln(a.Out, string(b))
	} else {
		fmt.Fprintln(a.Out, "NeuroForge plugin conformance (spec §13.3)")
		fmt.Fprintln(a.Out)
		for _, r := range results {
			status := "PASS"
			if !r.Passed {
				status = "FAIL"
			}
			fmt.Fprintf(a.Out, "  [%s]  %-22s %s\n", status, r.Name, r.Detail)
		}
		passed, total := conformance.Summary(results)
		fmt.Fprintf(a.Out, "\n%d/%d checks passed\n", passed, total)
	}
	passed, total := conformance.Summary(results)
	if passed != total {
		return ExitErr
	}
	return ExitOK
}
