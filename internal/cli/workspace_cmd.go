package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"neuroforge/internal/transport"
)

// runWorkspace dispatches `forge workspace <subcommand>`.
func (a *App) runWorkspace(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(a.Err, workspaceUsage)
		return ExitErr
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "create":
		return a.workspaceCreate(rest)
	case "list":
		return a.workspaceList(rest)
	case "show":
		return a.workspaceShow(rest)
	case "run":
		return a.workspaceRun(rest)
	case "checkpoint":
		return a.workspaceCheckpoint(rest)
	case "result":
		return a.workspaceResult(rest)
	case "review":
		return a.workspaceReview(rest)
	case "diff":
		return a.workspaceDiff(rest)
	case "patch":
		return a.workspacePatch(rest)
	case "delete":
		return a.workspaceDelete(rest)
	case "checkpoints":
		return a.workspaceCheckpoints(rest)
	case "-h", "--help":
		fmt.Fprintln(a.Out, workspaceUsage)
		return ExitOK
	default:
		fmt.Fprintf(a.Err, "%s: unknown workspace subcommand %q\n\n", a.Name, sub)
		fmt.Fprintln(a.Err, workspaceUsage)
		return ExitErr
	}
}

const workspaceUsage = `Usage: forge workspace <subcommand> [flags]

Subcommands:
  create  -t <task>        Create a worktree for a task (--wp, --base, --json)
  list    [-t <task>]      List workspaces (--project, --json)
  show    <id>             Show workspace details (--json)
  run     <id>             Run an agent in the workspace (--engine, --model, --prompt, --prompt-file, --json)
  checkpoint <id>          Create a checkpoint commit (--moment, --message)
  result  <id>             Create the local result branch (forge/result/<task>)
  review  <id> -a <action> Review: keep | reject | ask (--json)
  diff    <id>             Show the diff (base..HEAD)
  patch   <id>             Export the result as a patch
  delete  <id>             Delete the workspace (worktree only, never user data)
  checkpoints <id>         List checkpoints for a workspace (--json)`

func (a *App) workspaceCreate(args []string) int {
	fs := flag.NewFlagSet("workspace create", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	taskID := fs.String("t", "", "task id (required)")
	taskID2 := fs.String("task", "", "task id (alias for -t)")
	wp := fs.String("wp", "", "work package id (default 'main')")
	base := fs.String("base", "", "base branch/commit (default HEAD)")
	jsonOut := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return ExitErr
	}
	tid := *taskID
	if tid == "" {
		tid = *taskID2
	}
	if tid == "" {
		fmt.Fprintln(a.Err, "Usage: forge workspace create -t <task>")
		return ExitErr
	}

	cli, err := a.ensureDaemon()
	if err != nil {
		a.errf("connect to daemon: %v", err)
		return ExitErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ws, err := cli.CreateWorkspace(ctx, transport.CreateWorkspaceRequest{
		TaskID:        tid,
		WorkPackageID: *wp,
		BaseBranch:    *base,
	})
	if err != nil {
		a.maybeJSON(*jsonOut, err, "create workspace")
		return ExitErr
	}
	if *jsonOut {
		a.printJSON(ws)
	} else {
		fmt.Fprintf(a.Out, "Workspace created: %s\n", ws.ID)
		fmt.Fprintf(a.Out, "  path:   %s\n", ws.Path)
		fmt.Fprintf(a.Out, "  branch: %s\n", ws.Branch)
		fmt.Fprintf(a.Out, "  base:   %s\n", ws.BaseSHA)
		fmt.Fprintf(a.Out, "  state:  %s\n", ws.State)
	}
	return ExitOK
}

func (a *App) workspaceList(args []string) int {
	fs := flag.NewFlagSet("workspace list", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	taskID := fs.String("t", "", "filter by task")
	taskID2 := fs.String("task", "", "filter by task (alias)")
	projectID := fs.String("project", "", "filter by project")
	jsonOut := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return ExitErr
	}
	tid := *taskID
	if tid == "" {
		tid = *taskID2
	}

	cli, err := a.ensureDaemon()
	if err != nil {
		a.errf("connect to daemon: %v", err)
		return ExitErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	workspaces, err := cli.ListWorkspaces(ctx, tid, *projectID)
	if err != nil {
		a.maybeJSON(*jsonOut, err, "list workspaces")
		return ExitErr
	}
	if *jsonOut {
		a.printJSON(workspaces)
		return ExitOK
	}
	if len(workspaces) == 0 {
		fmt.Fprintln(a.Out, "No workspaces found.")
		return ExitOK
	}
	fmt.Fprintf(a.Out, "%-30s %-12s %-10s %s\n", "ID", "STATE", "BRANCH", "PATH")
	fmt.Fprintln(a.Out, strings.Repeat("-", 90))
	for _, ws := range workspaces {
		fmt.Fprintf(a.Out, "%-30s %-12s %-10s %s\n", ws.ID, ws.State, ws.Branch, ws.Path)
	}
	return ExitOK
}

func (a *App) workspaceShow(args []string) int {
	fs := flag.NewFlagSet("workspace show", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	jsonOut := fs.Bool("json", false, "emit JSON")
	positional, err := parseWithPositionalReorder(fs, args)
	if err != nil {
		return ExitErr
	}
	if len(positional) < 1 {
		fmt.Fprintln(a.Err, "Usage: forge workspace show <id> [--json]")
		return ExitErr
	}
	id := positional[0]

	cli, err := a.ensureDaemon()
	if err != nil {
		a.errf("connect to daemon: %v", err)
		return ExitErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ws, err := cli.GetWorkspace(ctx, id)
	if err != nil {
		a.maybeJSON(*jsonOut, err, "show workspace")
		return ExitErr
	}
	if *jsonOut {
		a.printJSON(ws)
		return ExitOK
	}
	fmt.Fprintf(a.Out, "ID:           %s\n", ws.ID)
	fmt.Fprintf(a.Out, "Project:      %s\n", ws.ProjectID)
	fmt.Fprintf(a.Out, "Task:         %s\n", ws.TaskID)
	fmt.Fprintf(a.Out, "WorkPackage:  %s\n", ws.WorkPackageID)
	fmt.Fprintf(a.Out, "Attempt:      %d\n", ws.Attempt)
	fmt.Fprintf(a.Out, "State:        %s\n", ws.State)
	fmt.Fprintf(a.Out, "Path:         %s\n", ws.Path)
	fmt.Fprintf(a.Out, "Branch:       %s\n", ws.Branch)
	if ws.ResultBranch != "" {
		fmt.Fprintf(a.Out, "ResultBranch: %s\n", ws.ResultBranch)
	}
	fmt.Fprintf(a.Out, "BaseSHA:      %s\n", ws.BaseSHA)
	fmt.Fprintf(a.Out, "HeadSHA:      %s\n", ws.HeadSHA)
	if ws.ResultSHA != "" {
		fmt.Fprintf(a.Out, "ResultSHA:    %s\n", ws.ResultSHA)
	}
	if ws.Engine != "" {
		fmt.Fprintf(a.Out, "Engine:       %s\n", ws.Engine)
	}
	if ws.Model != "" {
		fmt.Fprintf(a.Out, "Model:        %s\n", ws.Model)
	}
	return ExitOK
}

func (a *App) workspaceRun(args []string) int {
	fs := flag.NewFlagSet("workspace run", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	engine := fs.String("engine", "fake", "agent engine (fake|codex|claude|gemini|kimi|grok|opencode)")
	model := fs.String("model", "", "model id passed to the engine (e.g. zai-coding-plan/glm-5.2); separate from --engine")
	prompt := fs.String("prompt", "", "inline prompt for the agent (mutually exclusive with --prompt-file)")
	promptFile := fs.String("prompt-file", "", "path to a prompt file; read by the CLI and sent as content (mutually exclusive with --prompt)")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positional, err := parseWithPositionalReorder(fs, args)
	if err != nil {
		return ExitErr
	}
	if len(positional) < 1 {
		fmt.Fprintln(a.Err, workspaceRunUsage)
		return ExitErr
	}
	if len(positional) > 1 {
		fmt.Fprintf(a.Err, "%s: workspace run takes exactly one workspace id; got %d (%v)\n\n%s\n",
			a.Name, len(positional), positional, workspaceRunUsage)
		return ExitErr
	}
	id := positional[0]

	// --prompt and --prompt-file are mutually exclusive. A prompt is required
	// for production engines; the fake engine keeps its legacy promptless mode
	// (validated daemon-side) so existing smoke runs still work.
	var promptBody string
	switch {
	case *prompt != "" && *promptFile != "":
		fmt.Fprintf(a.Err, "%s: --prompt and --prompt-file are mutually exclusive\n", a.Name)
		return ExitErr
	case *prompt != "":
		promptBody = *prompt
	case *promptFile != "":
		b, rerr := os.ReadFile(*promptFile)
		if rerr != nil {
			a.errf("read --prompt-file %q: %v", *promptFile, rerr)
			return ExitErr
		}
		promptBody = string(b)
	}

	cli, err := a.ensureDaemon()
	if err != nil {
		a.errf("connect to daemon: %v", err)
		return ExitErr
	}
	// Long timeout — agent runs may take a while.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	ws, err := cli.RunWorkspace(ctx, id, transport.RunWorkspaceRequest{
		Engine: *engine,
		Model:  *model,
		Prompt: promptBody,
	})
	if err != nil {
		a.maybeJSON(*jsonOut, err, "run workspace")
		return ExitErr
	}
	if *jsonOut {
		a.printJSON(ws)
	} else {
		fmt.Fprintf(a.Out, "Run complete: %s (state=%s)\n", ws.ID, ws.State)
		if ws.Engine != "" {
			fmt.Fprintf(a.Out, "  engine: %s\n", ws.Engine)
		}
		if ws.Model != "" {
			fmt.Fprintf(a.Out, "  model:  %s\n", ws.Model)
		}
		if ws.HeadSHA != ws.BaseSHA {
			fmt.Fprintf(a.Out, "  changes: %s -> %s\n", ws.BaseSHA[:8], ws.HeadSHA[:8])
		}
	}
	return ExitOK
}

// workspaceRunUsage is the canonical help for `forge workspace run`.
const workspaceRunUsage = `Usage: forge workspace run <id> [--engine <e>] [--model <m>] [--prompt <text> | --prompt-file <path>] [--json]

Run a coding agent inside the workspace's isolated worktree. Flags may appear
before or after the workspace id.

Examples:
  forge workspace run ws-1 --engine opencode --model zai-coding-plan/glm-5.2 --prompt "fix the bug"
  forge workspace run --engine opencode --model zai-coding-plan/glm-5.2 --prompt-file task.md ws-1 --json

Engines: fake (default, offline) | codex | claude | gemini | kimi | grok | opencode
--model is independent of --engine (engine != model).
--prompt and --prompt-file are mutually exclusive. For production engines a
prompt is required; the fake engine may run without one.
A prompt file is read locally by the CLI and sent as content (no host path is
forwarded to the agent process).`

// errEmptyPrompt signals a missing prompt for a production engine.
var errEmptyPrompt = errors.New("prompt is required for this engine (use --prompt or --prompt-file)")

func (a *App) workspaceCheckpoint(args []string) int {
	fs := flag.NewFlagSet("workspace checkpoint", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	moment := fs.String("moment", "manual", "checkpoint moment")
	message := fs.String("message", "", "checkpoint message")
	positional, err := parseWithPositionalReorder(fs, args)
	if err != nil {
		return ExitErr
	}
	if len(positional) < 1 {
		fmt.Fprintln(a.Err, "Usage: forge workspace checkpoint <id> [--moment M] [--message M]")
		return ExitErr
	}
	id := positional[0]

	cli, err := a.ensureDaemon()
	if err != nil {
		a.errf("connect to daemon: %v", err)
		return ExitErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ws, err := cli.CheckpointWorkspace(ctx, id, transport.CheckpointRequest{Moment: *moment, Message: *message})
	if err != nil {
		a.errf("checkpoint: %v", err)
		return ExitErr
	}
	fmt.Fprintf(a.Out, "Checkpoint created: %s (head=%s)\n", ws.ID, ws.HeadSHA)
	return ExitOK
}

func (a *App) workspaceResult(args []string) int {
	fs := flag.NewFlagSet("workspace result", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	jsonOut := fs.Bool("json", false, "emit JSON")
	positional, err := parseWithPositionalReorder(fs, args)
	if err != nil {
		return ExitErr
	}
	if len(positional) < 1 {
		fmt.Fprintln(a.Err, "Usage: forge workspace result <id>")
		return ExitErr
	}
	id := positional[0]

	cli, err := a.ensureDaemon()
	if err != nil {
		a.errf("connect to daemon: %v", err)
		return ExitErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ws, err := cli.CreateResult(ctx, id)
	if err != nil {
		a.maybeJSON(*jsonOut, err, "create result")
		return ExitErr
	}
	if *jsonOut {
		a.printJSON(ws)
	} else {
		fmt.Fprintf(a.Out, "Result branch created: %s\n", ws.ResultBranch)
		fmt.Fprintf(a.Out, "  result_sha: %s\n", ws.ResultSHA)
		fmt.Fprintf(a.Out, "  base_sha:   %s\n", ws.BaseSHA)
	}
	return ExitOK
}

func (a *App) workspaceReview(args []string) int {
	fs := flag.NewFlagSet("workspace review", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	action := fs.String("a", "", "review action: keep | reject | ask")
	action2 := fs.String("action", "", "review action (alias for -a)")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positional, err := parseWithPositionalReorder(fs, args)
	if err != nil {
		return ExitErr
	}
	act := *action
	if act == "" {
		act = *action2
	}
	if act == "" {
		fmt.Fprintln(a.Err, "Usage: forge workspace review <id> -a <keep|reject|ask>")
		return ExitErr
	}
	if len(positional) < 1 {
		fmt.Fprintln(a.Err, "Usage: forge workspace review <id> -a <keep|reject|ask>")
		return ExitErr
	}
	id := positional[0]

	cli, err := a.ensureDaemon()
	if err != nil {
		a.errf("connect to daemon: %v", err)
		return ExitErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ws, err := cli.ReviewWorkspace(ctx, id, transport.ReviewRequest{Action: act})
	if err != nil {
		a.maybeJSON(*jsonOut, err, "review")
		return ExitErr
	}
	if *jsonOut {
		a.printJSON(ws)
	} else {
		fmt.Fprintf(a.Out, "Review '%s' applied: %s (state=%s)\n", act, ws.ID, ws.State)
	}
	return ExitOK
}

func (a *App) workspaceDiff(args []string) int {
	fs := flag.NewFlagSet("workspace diff", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	positional, err := parseWithPositionalReorder(fs, args)
	if err != nil {
		return ExitErr
	}
	if len(positional) < 1 {
		fmt.Fprintln(a.Err, "Usage: forge workspace diff <id>")
		return ExitErr
	}
	id := positional[0]

	cli, err := a.ensureDaemon()
	if err != nil {
		a.errf("connect to daemon: %v", err)
		return ExitErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := cli.DiffWorkspace(ctx, id)
	if err != nil {
		a.errf("diff: %v", err)
		return ExitErr
	}
	fmt.Fprint(a.Out, resp.Diff)
	return ExitOK
}

func (a *App) workspacePatch(args []string) int {
	fs := flag.NewFlagSet("workspace patch", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	positional, err := parseWithPositionalReorder(fs, args)
	if err != nil {
		return ExitErr
	}
	if len(positional) < 1 {
		fmt.Fprintln(a.Err, "Usage: forge workspace patch <id>")
		return ExitErr
	}
	id := positional[0]

	cli, err := a.ensureDaemon()
	if err != nil {
		a.errf("connect to daemon: %v", err)
		return ExitErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := cli.PatchWorkspace(ctx, id)
	if err != nil {
		a.errf("patch: %v", err)
		return ExitErr
	}
	fmt.Fprint(a.Out, resp.Patch)
	return ExitOK
}

func (a *App) workspaceDelete(args []string) int {
	fs := flag.NewFlagSet("workspace delete", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	positional, err := parseWithPositionalReorder(fs, args)
	if err != nil {
		return ExitErr
	}
	if len(positional) < 1 {
		fmt.Fprintln(a.Err, "Usage: forge workspace delete <id>")
		return ExitErr
	}
	id := positional[0]

	cli, err := a.ensureDaemon()
	if err != nil {
		a.errf("connect to daemon: %v", err)
		return ExitErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := cli.DeleteWorkspace(ctx, id); err != nil {
		a.errf("delete: %v", err)
		return ExitErr
	}
	fmt.Fprintf(a.Out, "Workspace %s deleted (managed worktree only; primary checkout untouched)\n", id)
	return ExitOK
}

func (a *App) workspaceCheckpoints(args []string) int {
	fs := flag.NewFlagSet("workspace checkpoints", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	jsonOut := fs.Bool("json", false, "emit JSON")
	positional, err := parseWithPositionalReorder(fs, args)
	if err != nil {
		return ExitErr
	}
	if len(positional) < 1 {
		fmt.Fprintln(a.Err, "Usage: forge workspace checkpoints <id> [--json]")
		return ExitErr
	}
	id := positional[0]

	cli, err := a.ensureDaemon()
	if err != nil {
		a.errf("connect to daemon: %v", err)
		return ExitErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cps, err := cli.ListCheckpoints(ctx, id)
	if err != nil {
		a.maybeJSON(*jsonOut, err, "list checkpoints")
		return ExitErr
	}
	if *jsonOut {
		a.printJSON(cps)
		return ExitOK
	}
	if len(cps) == 0 {
		fmt.Fprintln(a.Out, "No checkpoints.")
		return ExitOK
	}
	for _, c := range cps {
		fmt.Fprintf(a.Out, "  %s  %-18s  %s\n", c.CommitSHA[:8], c.Moment, c.Message)
	}
	return ExitOK
}

// printJSON marshals v and prints it.
func (a *App) printJSON(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Fprintln(a.Out, string(b))
}

// maybeJSON prints the error in JSON or text form.
func (a *App) maybeJSON(jsonOut bool, err error, prefix string) {
	if jsonOut {
		fmt.Fprintln(a.Out, jsonError(err))
	} else {
		a.errf("%s: %v", prefix, err)
	}
}
