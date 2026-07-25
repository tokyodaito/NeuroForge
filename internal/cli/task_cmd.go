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

// runTask dispatches `forge task <subcommand>`.
func (a *App) runTask(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(a.Err, taskUsage)
		return ExitErr
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "add":
		return a.taskAdd(rest)
	case "list":
		return a.taskList(rest)
	case "show":
		return a.taskShow(rest)
	case "pause":
		return a.taskLifecycle(rest, "pause")
	case "cancel":
		return a.taskLifecycle(rest, "cancel")
	case "dispatch":
		return a.taskDispatch(rest)
	case "post-merge":
		return a.taskPostMerge(rest)
	case "reopen":
		return a.taskReopen(rest)
	case "-h", "--help":
		fmt.Fprintln(a.Out, taskUsage)
		return ExitOK
	default:
		fmt.Fprintf(a.Err, "%s: unknown task subcommand %q\n\n", a.Name, sub)
		fmt.Fprintln(a.Err, taskUsage)
		return ExitErr
	}
}

const taskUsage = `Usage: forge task <subcommand> [flags]

Subcommands:
  add [flags] <description>   Create a task with free-form text
  list [flags]                List tasks (--project to filter, --json supported)
  show <id> [--json]          Show task details
  pause <id>                  Pause a task
  cancel <id>                 Cancel a task
  dispatch <id> [flags]       Dispatch a task through the production scheduler
  post-merge <id> [flags]     Run the post-merge sentinel (AUTONOMOUS only)
  reopen <id> [--reason]      Idempotently reopen a task (§37)

Task add flags:
  -p, --project <id>    Project ID (required)
  --title <title>       Optional task title
  --priority <level>    LOW | NORMAL | HIGH | URGENT (default NORMAL)
  -a, --attach <path>   Attach a file (repeatable)

Task dispatch flags:
  --engine <id>         Coding engine (default: fake)
  --model <name>        Model override
  --base <branch>       Base branch (default: project's current)
  --timeout <dur>       Run timeout (default: 5m)
  --context-pack        Build a token-budgeted Context Pack (§22.3)

Task post-merge flags:
  --commit <sha>        Merged commit SHA
  --base <branch>       Target branch (default: main)
  --check <n>=<status>  Inject a smoke check (repeatable; status: passed|failed)`

func (a *App) taskAdd(args []string) int {
	fs := flag.NewFlagSet("task add", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	projectID := fs.String("project", "", "project ID (required)")
	fs.StringVar(projectID, "p", "", "shortcut for --project")
	title := fs.String("title", "", "optional task title")
	priority := fs.String("priority", "NORMAL", "LOW | NORMAL | HIGH | URGENT")
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")

	var attachments []string
	customAttach := func(s string) error {
		attachments = append(attachments, s)
		return nil
	}
	fs.Func("attach", "attach a file (repeatable)", customAttach)
	fs.Func("a", "shortcut for --attach", customAttach)

	if err := fs.Parse(args); err != nil {
		return ExitErr
	}

	if *projectID == "" {
		fmt.Fprintln(a.Err, "Error: --project (-p) is required")
		return ExitErr
	}

	description := strings.Join(fs.Args(), " ")
	if description == "" && len(attachments) == 0 {
		fmt.Fprintln(a.Err, "Error: description or attachment is required")
		return ExitErr
	}

	cli, err := a.ensureDaemon()
	if err != nil {
		a.errf("connect to daemon: %v", err)
		return ExitErr
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req := transport.AddTaskRequest{
		ProjectID:   *projectID,
		Description: description,
		Title:       *title,
		Priority:    *priority,
	}
	for _, attPath := range attachments {
		req.Attachments = append(req.Attachments, transport.AddAttachmentReq{
			Path: attPath,
		})
	}

	t, err := cli.AddTask(ctx, req)
	if err != nil {
		if *jsonOut {
			fmt.Fprintln(a.Out, jsonError(err))
		} else {
			a.errf("add task: %v", err)
		}
		return ExitErr
	}

	if *jsonOut {
		b, _ := json.MarshalIndent(t, "", "  ")
		fmt.Fprintln(a.Out, string(b))
	} else {
		fmt.Fprintf(a.Out, "Task created: %s\n", t.ID)
		fmt.Fprintf(a.Out, "  project:    %s\n", t.ProjectID)
		if t.Title != "" {
			fmt.Fprintf(a.Out, "  title:      %s\n", t.Title)
		}
		fmt.Fprintf(a.Out, "  priority:   %s\n", t.Priority)
		fmt.Fprintf(a.Out, "  state:      %s\n", t.State)
		if len(t.Attachments) > 0 {
			fmt.Fprintf(a.Out, "  attachments: %d\n", len(t.Attachments))
			for _, att := range t.Attachments {
				fmt.Fprintf(a.Out, "    - %s (%s, %d bytes, %s)\n",
					att.Filename, att.MimeType, att.Size, att.Hash[:12])
			}
		}
	}
	return ExitOK
}

func (a *App) taskList(args []string) int {
	fs := flag.NewFlagSet("task list", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	projectID := fs.String("project", "", "filter by project ID")
	fs.StringVar(projectID, "p", "", "shortcut for --project")
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return ExitErr
	}

	cli, err := a.ensureDaemon()
	if err != nil {
		a.errf("connect to daemon: %v", err)
		return ExitErr
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tasks, err := cli.ListTasks(ctx, *projectID)
	if err != nil {
		if *jsonOut {
			fmt.Fprintln(a.Out, jsonError(err))
		} else {
			a.errf("list tasks: %v", err)
		}
		return ExitErr
	}

	if *jsonOut {
		b, _ := json.MarshalIndent(tasks, "", "  ")
		fmt.Fprintln(a.Out, string(b))
		return ExitOK
	}

	if len(tasks) == 0 {
		fmt.Fprintln(a.Out, "No tasks found.")
		return ExitOK
	}

	fmt.Fprintf(a.Out, "%-16s %-12s %-10s %-8s %s\n", "ID", "STATE", "PRIORITY", "PROJECT", "TITLE/DESCRIPTION")
	fmt.Fprintln(a.Out, strings.Repeat("-", 80))
	for _, t := range tasks {
		label := t.Title
		if label == "" {
			if len(t.Description) > 40 {
				label = t.Description[:40] + "..."
			} else {
				label = t.Description
			}
		}
		fmt.Fprintf(a.Out, "%-16s %-12s %-10s %-8s %s\n", t.ID, t.State, t.Priority, t.ProjectID, label)
	}
	return ExitOK
}

func (a *App) taskShow(args []string) int {
	fs := flag.NewFlagSet("task show", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return ExitErr
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(a.Err, "Usage: forge task show <id> [--json]")
		return ExitErr
	}
	id := fs.Arg(0)

	cli, err := a.ensureDaemon()
	if err != nil {
		a.errf("connect to daemon: %v", err)
		return ExitErr
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	t, err := cli.GetTask(ctx, id)
	if err != nil {
		if *jsonOut {
			fmt.Fprintln(a.Out, jsonError(err))
		} else {
			a.errf("show task: %v", err)
		}
		return ExitErr
	}

	if *jsonOut {
		b, _ := json.MarshalIndent(t, "", "  ")
		fmt.Fprintln(a.Out, string(b))
		return ExitOK
	}

	fmt.Fprintf(a.Out, "ID:          %s\n", t.ID)
	fmt.Fprintf(a.Out, "Project:     %s\n", t.ProjectID)
	if t.Title != "" {
		fmt.Fprintf(a.Out, "Title:       %s\n", t.Title)
	}
	fmt.Fprintf(a.Out, "Description: %s\n", t.Description)
	fmt.Fprintf(a.Out, "Priority:    %s\n", t.Priority)
	fmt.Fprintf(a.Out, "State:       %s\n", t.State)
	fmt.Fprintf(a.Out, "Created:     %s\n", t.CreatedAt)
	if len(t.Attachments) > 0 {
		fmt.Fprintln(a.Out, "Attachments:")
		for _, att := range t.Attachments {
			fmt.Fprintf(a.Out, "  - %s (%s, %d bytes)\n", att.Filename, att.MimeType, att.Size)
			fmt.Fprintf(a.Out, "    hash: %s\n", att.Hash)
			fmt.Fprintf(a.Out, "    role: %s\n", att.Role)
		}
	}
	return ExitOK
}

func (a *App) taskLifecycle(args []string, action string) int {
	fs := flag.NewFlagSet("task "+action, flag.ContinueOnError)
	fs.SetOutput(a.Err)
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return ExitErr
	}
	if fs.NArg() < 1 {
		fmt.Fprintf(a.Err, "Usage: forge task %s <id> [--json]\n", action)
		return ExitErr
	}
	id := fs.Arg(0)

	cli, err := a.ensureDaemon()
	if err != nil {
		a.errf("connect to daemon: %v", err)
		return ExitErr
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var (
		t    transport.TaskDTO
		err2 error
	)
	switch action {
	case "pause":
		t, err2 = cli.PauseTask(ctx, id)
	case "cancel":
		t, err2 = cli.CancelTask(ctx, id)
	}
	if err2 != nil {
		if *jsonOut {
			fmt.Fprintln(a.Out, jsonError(err2))
		} else {
			a.errf("%s task: %v", action, err2)
		}
		return ExitErr
	}

	if *jsonOut {
		b, _ := json.MarshalIndent(t, "", "  ")
		fmt.Fprintln(a.Out, string(b))
	} else {
		fmt.Fprintf(a.Out, "Task %s: %s -> state=%s\n", id, action, t.State)
	}
	return ExitOK
}

// taskDispatch implements `forge task dispatch` — the production execution path
// through the scheduler (spec §10, §22). The task flows scheduler → dispatcher
// (workspace) → supervisor, with usage events, Context Packs, project memory
// and quality statistics recorded on the way.
func (a *App) taskDispatch(args []string) int {
	fs := flag.NewFlagSet("task dispatch", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	engine := fs.String("engine", "fake", "coding engine id")
	model := fs.String("model", "", "model override")
	base := fs.String("base", "", "base branch")
	timeout := fs.Duration("timeout", 5*time.Minute, "run timeout")
	pack := fs.Bool("context-pack", false, "build a token-budgeted Context Pack (§22.3)")
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return ExitErr
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(a.Err, "Usage: forge task dispatch <id> [flags]")
		return ExitErr
	}
	id := fs.Arg(0)

	cli, err := a.ensureDaemon()
	if err != nil {
		a.errf("connect to daemon: %v", err)
		return ExitErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	res, err := cli.DispatchTask(ctx, id, transport.DispatchTaskRequest{
		Engine: *engine, Model: *model, BaseBranch: *base,
		Timeout: *timeout, BuildContextPack: *pack,
	})
	if err != nil {
		if *jsonOut {
			fmt.Fprintln(a.Out, jsonError(err))
		} else {
			a.errf("dispatch: %v", err)
		}
		return ExitErr
	}
	if *jsonOut {
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Fprintln(a.Out, string(b))
	} else {
		fmt.Fprintf(a.Out, "Task dispatched: %s\n", res.TaskID)
		fmt.Fprintf(a.Out, "  workspace:   %s\n", res.WorkspaceID)
		fmt.Fprintf(a.Out, "  outcome:     %s\n", res.Outcome)
		fmt.Fprintf(a.Out, "  usage events:%d\n", res.UsageEvents)
		fmt.Fprintf(a.Out, "  est tokens:  %d\n", res.EstimatedTokens)
		if res.ContextPackBuilt {
			fmt.Fprintln(a.Out, "  context pack:built (§22.3)")
		}
		if res.MemoryLearned {
			fmt.Fprintln(a.Out, "  memory:      learned (§22.9)")
		}
	}
	return ExitOK
}

// taskPostMerge implements `forge task post-merge` — runs the post-merge
// sentinel (§37, §4.4). It is a structural no-op outside AUTONOMOUS.
func (a *App) taskPostMerge(args []string) int {
	fs := flag.NewFlagSet("task post-merge", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	commit := fs.String("commit", "", "merged commit SHA")
	base := fs.String("base", "main", "target branch")
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	var checks []string
	fs.Func("check", "inject a smoke check name=status (repeatable)", func(s string) error {
		checks = append(checks, s)
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return ExitErr
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(a.Err, "Usage: forge task post-merge <id> [flags]")
		return ExitErr
	}
	id := fs.Arg(0)

	cli, err := a.ensureDaemon()
	if err != nil {
		a.errf("connect to daemon: %v", err)
		return ExitErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	req := transport.PostMergeRequest{CommitSHA: *commit, BaseBranch: *base}
	for _, c := range checks {
		name, status, _ := strings.Cut(c, "=")
		if status == "" {
			status = "passed"
		}
		req.Checks = append(req.Checks, transport.SmokeCheckSpec{Name: name, WantStatus: status})
	}
	res, err := cli.RunPostMerge(ctx, id, req)
	if err != nil {
		if *jsonOut {
			fmt.Fprintln(a.Out, jsonError(err))
		} else {
			a.errf("post-merge: %v", err)
		}
		return ExitErr
	}
	if *jsonOut {
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Fprintln(a.Out, string(b))
	} else {
		fmt.Fprintf(a.Out, "Post-merge sentinel: %s\n", res.Decision)
		fmt.Fprintf(a.Out, "  all passed: %v\n", res.AllPassed)
		if res.Reverted {
			fmt.Fprintf(a.Out, "  reverted:   %s\n", res.RevertSHA)
		}
	}
	return ExitOK
}

// taskReopen implements `forge task reopen` — idempotent task reopen (§37).
func (a *App) taskReopen(args []string) int {
	fs := flag.NewFlagSet("task reopen", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	reason := fs.String("reason", "post-merge reopen", "reopen reason")
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return ExitErr
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(a.Err, "Usage: forge task reopen <id> [--reason]")
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

	if err := cli.ReopenTask(ctx, id, *reason); err != nil {
		if *jsonOut {
			fmt.Fprintln(a.Out, jsonError(err))
		} else {
			a.errf("reopen: %v", err)
		}
		return ExitErr
	}
	if *jsonOut {
		fmt.Fprintf(a.Out, `{"reopened":%q}`+"\n", id)
	} else {
		fmt.Fprintf(a.Out, "Task reopened: %s\n", id)
	}
	return ExitOK
}
