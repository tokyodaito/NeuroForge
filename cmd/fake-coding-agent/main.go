// Command fake-coding-agent is the §33.1 fake coding agent executable.
//
// It is a deterministic, network-free agent used by NeuroForge's declarative
// command adapter and native JSON-RPC plugin conformance suite so that no real
// (paid) AI API is ever called (rule §36.5, spec §33.1). The same scenario
// scripts back the in-process fake adapter, so behaviour is identical across all
// three surfaces.
//
// Modes:
//
//	fake-coding-agent --version                 # detect/health probe (prints JSON)
//	fake-coding-agent --mode command ...        # JSONL declarative mode (spec §13.1)
//	fake-coding-agent --mode jsonrpc ...        # native JSON-RPC plugin (spec §13.2)
//
// The scenario is selected via --scenario (command mode) or the FAKE_SCENARIO
// environment variable (jsonrpc mode, since the plugin is spawned once).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"neuroforge/internal/adapter/codingagent/fake"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Stdin))
}

func run(args []string, stdout, stderr io.Writer, stdin io.Reader) int {
	fs := flag.NewFlagSet("fake-coding-agent", flag.ContinueOnError)
	fs.SetOutput(stderr)
	mode := fs.String("mode", "command", "command (JSONL) | jsonrpc")
	scenario := fs.String("scenario", string(fake.ScenarioSuccess), "scenario name")
	version := fs.Bool("version", false, "print version JSON and exit")
	resume := fs.Bool("resume", false, "start as a resume (emit run.resumed)")
	runID := fs.String("run-id", "fake-run", "run id")
	engine := fs.String("engine", "fake", "engine id")
	model := fs.String("model", "fake/standard", "model id")
	workspace := fs.String("workspace", "", "workspace path (for file writes)")
	sessionID := fs.String("session-id", "", "provider session id")
	// Absorb and ignore the declarative adapter's extra positional template
	// tokens (prompt file, etc.) so the executable is a drop-in for the §13.1
	// command template.
	if err := fs.Parse(args); err != nil {
		return 1
	}

	if *version {
		return printVersion(stdout)
	}

	sc := fake.Scenario(*scenario)

	switch strings.ToLower(*mode) {
	case "version":
		return printVersion(stdout)
	case "command", "":
		p := fake.RunParams(*workspace, *engine, *model, *runID, *sessionID, sc, *resume)
		return fake.RunCommand(stdout, stderr, p)
	case "jsonrpc":
		// jsonrpc mode honours FAKE_SCENARIO when --scenario is left at default.
		if env := os.Getenv("FAKE_SCENARIO"); env != "" && *scenario == string(fake.ScenarioSuccess) {
			sc = fake.Scenario(env)
		}
		if err := fake.ServeJSONRPC(stdin, stdout, stderr, sc); err != nil {
			fmt.Fprintf(stderr, "fake-coding-agent: %v\n", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(stderr, "fake-coding-agent: unknown mode %q\n", *mode)
		return 1
	}
}

func printVersion(w io.Writer) int {
	v := map[string]string{
		"engine":   "fake",
		"version":  "1.0.0-fake",
		"protocol": "1",
		"detail":   "fake coding agent (spec §33.1)",
	}
	b, _ := json.Marshal(v)
	fmt.Fprintln(w, string(b))
	return 0
}
