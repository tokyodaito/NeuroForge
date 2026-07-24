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

// runProject dispatches `forge project <subcommand>`.
func (a *App) runProject(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(a.Err, projectUsage)
		return ExitErr
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "add":
		return a.projectAdd(rest)
	case "list":
		return a.projectList(rest)
	case "show":
		return a.projectShow(rest)
	case "start":
		return a.projectLifecycle(rest, "start")
	case "pause":
		return a.projectLifecycle(rest, "pause")
	case "stop":
		return a.projectLifecycle(rest, "stop")
	case "remove":
		return a.projectRemove(rest)
	case "-h", "--help":
		fmt.Fprintln(a.Out, projectUsage)
		return ExitOK
	default:
		fmt.Fprintf(a.Err, "%s: unknown project subcommand %q\n\n", a.Name, sub)
		fmt.Fprintln(a.Err, projectUsage)
		return ExitErr
	}
}

const projectUsage = `Usage: forge project <subcommand> [flags]

Subcommands:
  add <path>          Register a Git repository as a NeuroForge project
  list                List all registered projects (--json supported)
  show <id>           Show project details (--json supported)
  start <id>          Start the factory for a project (DISABLED -> IDLE)
  pause <id>          Pause the factory (IDLE/RUNNING -> PAUSED)
  stop <id>           Stop the factory (any -> DISABLED)
  remove <id>         Unregister a project (files are NOT deleted)`

func (a *App) projectAdd(args []string) int {
	fs := flag.NewFlagSet("project add", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	name := fs.String("name", "", "display name (defaults to directory basename)")
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return ExitErr
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(a.Err, "Usage: forge project add <path> [--name NAME] [--json]")
		return ExitErr
	}
	path := fs.Arg(0)

	cli, err := a.ensureDaemon()
	if err != nil {
		a.errf("connect to daemon: %v", err)
		return ExitErr
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	p, err := cli.AddProject(ctx, transport.AddProjectRequest{Path: path, Name: *name})
	if err != nil {
		if *jsonOut {
			fmt.Fprintln(a.Out, jsonError(err))
		} else {
			a.errf("add project: %v", err)
		}
		return ExitErr
	}

	if *jsonOut {
		b, _ := json.MarshalIndent(p, "", "  ")
		fmt.Fprintln(a.Out, string(b))
	} else {
		fmt.Fprintf(a.Out, "Project registered: %s\n", p.ID)
		fmt.Fprintf(a.Out, "  name:    %s\n", p.Name)
		fmt.Fprintf(a.Out, "  path:    %s\n", p.Path)
		if p.Remote != "" {
			fmt.Fprintf(a.Out, "  remote:  %s\n", p.Remote)
		}
		fmt.Fprintf(a.Out, "  state:   %s\n", p.State)
		fmt.Fprintf(a.Out, "  profile: %s\n", p.Profile)
	}
	return ExitOK
}

func (a *App) projectList(args []string) int {
	fs := flag.NewFlagSet("project list", flag.ContinueOnError)
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	projects, err := cli.ListProjects(ctx)
	if err != nil {
		if *jsonOut {
			fmt.Fprintln(a.Out, jsonError(err))
		} else {
			a.errf("list projects: %v", err)
		}
		return ExitErr
	}

	if *jsonOut {
		b, _ := json.MarshalIndent(projects, "", "  ")
		fmt.Fprintln(a.Out, string(b))
		return ExitOK
	}

	if len(projects) == 0 {
		fmt.Fprintln(a.Out, "No projects registered. Use 'forge project add <path>' to add one.")
		return ExitOK
	}

	fmt.Fprintf(a.Out, "%-20s %-12s %-10s %s\n", "ID", "STATE", "PROFILE", "NAME")
	fmt.Fprintln(a.Out, strings.Repeat("-", 70))
	for _, p := range projects {
		fmt.Fprintf(a.Out, "%-20s %-12s %-10s %s\n", p.ID, p.State, p.Profile, p.Name)
	}
	return ExitOK
}

func (a *App) projectShow(args []string) int {
	fs := flag.NewFlagSet("project show", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return ExitErr
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(a.Err, "Usage: forge project show <id> [--json]")
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

	p, err := cli.GetProject(ctx, id)
	if err != nil {
		if *jsonOut {
			fmt.Fprintln(a.Out, jsonError(err))
		} else {
			a.errf("show project: %v", err)
		}
		return ExitErr
	}

	if *jsonOut {
		b, _ := json.MarshalIndent(p, "", "  ")
		fmt.Fprintln(a.Out, string(b))
		return ExitOK
	}

	fmt.Fprintf(a.Out, "ID:       %s\n", p.ID)
	fmt.Fprintf(a.Out, "Name:     %s\n", p.Name)
	fmt.Fprintf(a.Out, "Path:     %s\n", p.Path)
	fmt.Fprintf(a.Out, "Remote:   %s\n", p.Remote)
	fmt.Fprintf(a.Out, "State:    %s\n", p.State)
	fmt.Fprintf(a.Out, "Profile:  %s\n", p.Profile)
	fmt.Fprintf(a.Out, "Created:  %s\n", p.CreatedAt)
	fmt.Fprintf(a.Out, "Updated:  %s\n", p.UpdatedAt)
	return ExitOK
}

func (a *App) projectLifecycle(args []string, action string) int {
	fs := flag.NewFlagSet("project "+action, flag.ContinueOnError)
	fs.SetOutput(a.Err)
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return ExitErr
	}
	if fs.NArg() < 1 {
		fmt.Fprintf(a.Err, "Usage: forge project %s <id> [--json]\n", action)
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
		p    transport.ProjectDTO
		err2 error
	)
	switch action {
	case "start":
		p, err2 = cli.StartProject(ctx, id)
	case "pause":
		p, err2 = cli.PauseProject(ctx, id)
	case "stop":
		p, err2 = cli.StopProject(ctx, id)
	}
	if err2 != nil {
		if *jsonOut {
			fmt.Fprintln(a.Out, jsonError(err2))
		} else {
			a.errf("%s project: %v", action, err2)
		}
		return ExitErr
	}

	if *jsonOut {
		b, _ := json.MarshalIndent(p, "", "  ")
		fmt.Fprintln(a.Out, string(b))
	} else {
		fmt.Fprintf(a.Out, "Project %s: %s -> state=%s\n", id, action, p.State)
	}
	return ExitOK
}

func (a *App) projectRemove(args []string) int {
	fs := flag.NewFlagSet("project remove", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	if err := fs.Parse(args); err != nil {
		return ExitErr
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(a.Err, "Usage: forge project remove <id>")
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

	if err := cli.RemoveProject(ctx, id); err != nil {
		a.errf("remove project: %v", err)
		return ExitErr
	}
	fmt.Fprintf(a.Out, "Project %s removed (files on disk are untouched)\n", id)
	return ExitOK
}

// jsonError formats a client error as a JSON object.
func jsonError(err error) string {
	b, _ := json.Marshal(map[string]string{"error": err.Error()})
	return string(b)
}
