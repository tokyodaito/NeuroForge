package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"neuroforge/internal/transport"
)

// Exit codes per OUTCOME_CONTRACT.md §4.
const (
	ExitValidation = 2 // usage / validation error (no state created)
	ExitInfra      = 3 // infrastructure error (daemon autostart failed)
	ExitCancelled  = 130
	ExitTimedOut   = 124
)

// knownEngines is the client-side list of engine ids `forge run` accepts
// without consulting the daemon (REQUIREMENTS.md §1.2: unknown engine →
// exit 2, no state created). It mirrors the daemon's registered adapters
// (fake + the six first-party production engines). A daemon with extra
// plugin engines may accept more, but the validation here catches the
// common typo (e.g. "bogus") before any daemon round-trip.
var knownEngines = map[string]bool{
	"opencode": true, // default
	"fake":     true, // offline / black-box fixture
	"codex":    true,
	"claude":   true,
	"gemini":   true,
	"kimi":     true,
	"grok":     true,
}

// runCmdArgs holds the parsed flags for `forge run`.
type runCmdArgs struct {
	Description string
	File        string
	Engine      string
	Model       string
	Base        string
	Timeout     time.Duration
	JSON        bool
	Verbose     bool
}

// parseRunArgs parses the `forge run` flags per REQUIREMENTS.md §1.1 +
// §1.2 validation. Returns the parsed args, the validation error (if any),
// and the human-readable error class.
func parseRunArgs(args []string) (runCmdArgs, error) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	engine := fs.String("engine", "opencode", "coding-agent engine id")
	model := fs.String("model", "zai-coding-plan/glm-5.2", "model id forwarded to the engine")
	file := fs.String("file", "", "read the description from a file (mutually exclusive with positional)")
	base := fs.String("base", "", "base branch/commit (default: current branch)")
	timeout := fs.Duration("timeout", 10*time.Minute, "hard wall-clock timeout for the agent run")
	jsonOut := fs.Bool("json", false, "emit a single machine-readable JSON document")
	verbose := fs.Bool("verbose", false, "show internal ids (task/workspace/run) in human output")

	positional, err := parseWithPositionalReorder(fs, args)
	if err != nil {
		return runCmdArgs{}, err
	}
	parsed := runCmdArgs{
		Engine: *engine, Model: *model, File: *file, Base: *base,
		Timeout: *timeout, JSON: *jsonOut, Verbose: *verbose,
	}

	// Validation per REQUIREMENTS.md §1.2.
	switch {
	case len(positional) > 0 && *file != "":
		return parsed, errors.New("forge: --file and a positional description are mutually exclusive")
	case len(positional) > 0:
		parsed.Description = strings.Join(positional, " ")
	case *file != "":
		b, rerr := os.ReadFile(*file)
		if rerr != nil {
			return parsed, fmt.Errorf("forge: read --file %q: %v", *file, rerr)
		}
		parsed.Description = string(b)
	default:
		return parsed, errors.New("forge: a task description (or --file) is required")
	}
	// Validate the engine is known BEFORE doing any daemon work (REQUIREMENTS
	// §1.2: unknown engine → exit 2, no state created).
	if !knownEngines[parsed.Engine] {
		return parsed, fmt.Errorf("forge: unknown engine %q", parsed.Engine)
	}
	return parsed, nil
}

// resolveGitRepo walks up from $PWD to find a .git entry (worktree or common
// dir). FR-1. Returns the absolute repo root or an error.
func resolveGitRepo() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("forge: not inside a git repository")
		}
		dir = parent
	}
}

// runRunCmd implements `forge run` — the user-facing minimal reliable run
// command (REQUIREMENTS.md §1, FR-1..FR-18). It performs input validation
// (no state created on failure — OUTCOME_CONTRACT.md §4), resolves the git
// repo, autostarts the daemon (S9), and dispatches to POST /projects/{id}/run.
//
// Exit codes follow OUTCOME_CONTRACT.md §4 exactly.
func (a *App) runRunCmd(args []string) int {
	parsed, perr := parseRunArgs(args)
	if perr != nil {
		// --json validation error: emit a JSON document so scripts can parse.
		// (OUTCOME_CONTRACT.md §3.2).
		if parsed.JSON {
			a.emitJSONError(parsed, perr.Error(), errorClassForValidation(perr))
		} else {
			fmt.Fprintln(a.Err, perr.Error())
		}
		return ExitValidation
	}

	// Resolve the repo before doing anything else (FR-1).
	repoPath, err := resolveGitRepo()
	if err != nil {
		if parsed.JSON {
			a.emitJSONError(parsed, "not inside a git repository", "NOT_A_REPO")
		} else {
			fmt.Fprintln(a.Err, "forge: not inside a git repository")
		}
		return ExitValidation
	}

	// Progress text goes to stderr (never stdout) so --json stdout is a single
	// document (I.11).
	progress := func(line string) {
		if !parsed.JSON {
			fmt.Fprintln(a.Err, line)
		}
	}

	progress("Preparing workspace...")
	progress("Running OpenCode...")

	// Autostart the daemon (S9).
	cli, err := a.ensureDaemon()
	if err != nil {
		if parsed.JSON {
			a.emitJSONError(parsed, fmt.Sprintf("daemon start failed: %v", err), "DAEMON_START_FAILED")
		} else {
			fmt.Fprintf(a.Err, "forge: daemon start failed: %v\n", err)
			fmt.Fprintln(a.Err, "see `forge daemon logs` for details")
		}
		return ExitInfra
	}
	// Agent runs are long-lived (minutes); disable the HTTP client timeout so
	// the run is not aborted prematurely.
	cli.HTTP.Timeout = 0

	// Resolve the project by repo path (register if missing — this is a
	// convenience for the happy path; REQUIREMENTS.md §1.1 says the repo is
	// the current one).
	//
	// BF-02 / FR-16: SIGINT/SIGTERM at the CLI cancels this context, which
	// cancels the in-flight HTTP request; the daemon observes the request
	// context cancellation, cancels the supervisor run, terminates the agent
	// process group, and finalizes the run as cancelled. The CLI then exits 130
	// (OUTCOME_CONTRACT.md §4).
	sigCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithTimeout(sigCtx, 30*time.Minute)
	defer cancel()

	projectID, err := a.resolveOrCreateProjectID(ctx, cli, repoPath)
	if err != nil {
		if parsed.JSON {
			a.emitJSONError(parsed, fmt.Sprintf("resolve project: %v", err), "INTERNAL_ERROR")
		} else {
			fmt.Fprintf(a.Err, "forge: resolve project: %v\n", err)
		}
		return ExitErr
	}

	// Dispatch the run.
	req := transport.RunTaskRequest{
		Description: parsed.Description,
		Engine:      parsed.Engine,
		Model:       parsed.Model,
		BaseBranch:  parsed.Base,
		Timeout:     parsed.Timeout,
	}
	res, err := cli.RunTask(ctx, projectID, req)
	if err != nil {
		// User-initiated cancellation (SIGINT/SIGTERM): the daemon finalizes the
		// run as cancelled independently; the CLI reports cancelled + exit 130
		// (OUTCOME_CONTRACT.md §4 / BF-02).
		if sigCtx.Err() != nil {
			if parsed.JSON {
				a.emitJSONError(parsed, "cancelled by user (SIGINT)", "CANCELLED")
			} else {
				fmt.Fprintln(a.Err, "Cancelled.")
			}
			return ExitCancelled
		}
		// Network/transport error → infrastructure failure.
		if parsed.JSON {
			a.emitJSONError(parsed, fmt.Sprintf("run: %v", err), "DAEMON_START_FAILED")
		} else {
			fmt.Fprintf(a.Err, "forge: run: %v\n", err)
		}
		return ExitInfra
	}

	// The agent run has finished; now its repository state is being finalized.
	// Per OUTCOME_CONTRACT.md §2 this progress line follows the run, not the
	// dispatch (non-blocking finding: progress ordering).
	progress("Finalizing repository state...")

	if parsed.JSON {
		return a.emitJSONResult(res)
	}
	return a.emitHumanResult(res, parsed.Verbose)
}

// resolveOrCreateProjectID finds the registered project whose path matches
// repoPath. If none exists, it registers one (LOCAL_REVIEW profile, the
// minimal run never pushes — REQUIREMENTS.md §1.1). It returns the project id.
func (a *App) resolveOrCreateProjectID(ctx context.Context, cli *transport.Client, repoPath string) (string, error) {
	// List projects; match by path.
	projects, err := cli.ListProjects(ctx)
	if err != nil {
		return "", err
	}
	abs, _ := filepath.Abs(repoPath)
	for _, p := range projects {
		if p.Path == abs {
			return p.ID, nil
		}
	}
	// Register the project. LOCAL_REVIEW is the default profile.
	p, err := cli.AddProject(ctx, transport.AddProjectRequest{Path: repoPath})
	if err != nil {
		return "", err
	}
	// Start it (DISABLED → IDLE) so it is ready for runs.
	if _, err := cli.StartProject(ctx, p.ID); err != nil {
		// Non-fatal: a project in DISABLED may still work; surface but continue.
		fmt.Fprintf(a.Err, "forge: project start warning: %v\n", err)
	}
	return p.ID, nil
}

// errorClassForValidation maps a validation error to the OUTCOME_CONTRACT.md
// §3.1 error_class string.
func errorClassForValidation(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "mutually exclusive"):
		return "MUTUALLY_EXCLUSIVE"
	case strings.Contains(msg, "description") || strings.Contains(msg, "or --file"):
		return "EMPTY_PROMPT"
	case strings.Contains(msg, "not inside a git repository"):
		return "NOT_A_REPO"
	case strings.Contains(msg, "read --file"):
		return "UNREADABLE_FILE"
	}
	return "VALIDATION_ERROR"
}

// emitJSONError prints a JSON document for a validation/infrastructure error
// (OUTCOME_CONTRACT.md §3.2). All fields are present; null where N/A.
func (a *App) emitJSONError(parsed runCmdArgs, msg, class string) {
	doc := map[string]any{
		"outcome":         "failed",
		"task_id":         nil,
		"workspace_id":    nil,
		"run_id":          nil,
		"workspace_path":  nil,
		"base_sha":        nil,
		"actual_head_sha": nil,
		"engine":          parsed.Engine,
		"model":           parsed.Model,
		"changed_files":   []string{},
		"commit_sha":      nil,
		"result_branch":   nil,
		"next_action":     nil,
		"error":           msg,
		"error_class":     class,
	}
	b, _ := json.Marshal(doc)
	fmt.Fprintln(a.Out, string(b))
}

// emitJSONResult prints a single JSON document for a successful run
// (OUTCOME_CONTRACT.md §3). Returns the exit code per §4.
func (a *App) emitJSONResult(res transport.RunTaskResultDTO) int {
	// The DTO marshals directly; nothing else may be on stdout (I.11).
	b, _ := json.Marshal(res)
	fmt.Fprintln(a.Out, string(b))
	return exitCodeFor(res.Outcome)
}

// emitHumanResult prints the human-readable result block per
// OUTCOME_CONTRACT.md §2 and returns the exit code per §4.
func (a *App) emitHumanResult(res transport.RunTaskResultDTO, verbose bool) int {
	if verbose {
		fmt.Fprintf(a.Out, "Task:        %s\n", res.TaskID)
		fmt.Fprintf(a.Out, "Workspace id:%s\n", res.WorkspaceID)
		if res.RunID != "" {
			fmt.Fprintf(a.Out, "Run:         %s\n", res.RunID)
		}
	}
	switch res.Outcome {
	case "completed-with-commit":
		fmt.Fprintln(a.Out, "Completed")
		fmt.Fprintln(a.Out)
		fmt.Fprintf(a.Out, "Workspace: %s\n", res.WorkspacePath)
		fmt.Fprintf(a.Out, "Commit:    %s\n", shortSHA(res.CommitSHA))
		fmt.Fprintf(a.Out, "Changed:   %d file(s)\n", len(res.ChangedFiles))
		if res.NextAction != "" {
			fmt.Fprintf(a.Out, "Next:      %s\n", res.NextAction)
		}
	case "completed-with-uncommitted-changes":
		fmt.Fprintln(a.Out, "Completed (uncommitted changes — nothing was committed by the agent)")
		fmt.Fprintln(a.Out)
		fmt.Fprintf(a.Out, "Workspace:   %s\n", res.WorkspacePath)
		fmt.Fprintf(a.Out, "Changed:     %d file(s): %s\n", len(res.ChangedFiles), strings.Join(res.ChangedFiles, ", "))
		if res.ResultBranch != "" {
			fmt.Fprintf(a.Out, "Result ref:  %s (at base; changes are in the worktree)\n", res.ResultBranch)
		}
		if res.NextAction != "" {
			fmt.Fprintf(a.Out, "Next:        %s\n", res.NextAction)
		}
	case "completed-no-changes":
		fmt.Fprintln(a.Out, "Agent finished without producing repository changes.")
		fmt.Fprintln(a.Out)
		fmt.Fprintf(a.Out, "Workspace: %s\n", res.WorkspacePath)
		fmt.Fprintln(a.Out, "Next:      rephrase the task and run again")
	case "failed":
		reason := res.Error
		if reason == "" {
			reason = "agent run failed"
		}
		fmt.Fprintf(a.Out, "Failed: %s\n", reason)
		fmt.Fprintln(a.Out)
		fmt.Fprintf(a.Out, "Workspace: %s\n", res.WorkspacePath)
		if verbose && res.RunID != "" {
			fmt.Fprintf(a.Out, "Run:       %s\n", res.RunID)
		}
		fmt.Fprintln(a.Out, "Next:      check the agent output; re-run with a clearer task description")
	case "cancelled":
		fmt.Fprintln(a.Out, "Cancelled.")
		fmt.Fprintln(a.Out)
		fmt.Fprintf(a.Out, "Workspace: %s\n", res.WorkspacePath)
		fmt.Fprintln(a.Out, "Next:      re-run when ready")
	case "interrupted":
		// Produced only by the restart reconciler (STATE_MACHINE.md §5.1) when
		// the daemon died mid-run. Exit code is 130 (SIGINT-like).
		reason := res.Error
		if reason == "" {
			reason = "run interrupted by daemon restart"
		}
		fmt.Fprintf(a.Out, "Interrupted: %s\n", reason)
		fmt.Fprintln(a.Out)
		fmt.Fprintf(a.Out, "Workspace: %s\n", res.WorkspacePath)
		if verbose && res.RunID != "" {
			fmt.Fprintf(a.Out, "Run:       %s\n", res.RunID)
		}
		fmt.Fprintln(a.Out, "Next:      re-run the task; the previous attempt was not preserved")
	case "timed-out":
		fmt.Fprintf(a.Out, "Timed out.\n")
		fmt.Fprintln(a.Out)
		fmt.Fprintf(a.Out, "Workspace: %s\n", res.WorkspacePath)
		if verbose && res.RunID != "" {
			fmt.Fprintf(a.Out, "Run:       %s\n", res.RunID)
		}
		fmt.Fprintln(a.Out, "Next:      raise --timeout, or split the task into smaller steps")
	default:
		fmt.Fprintf(a.Out, "Outcome: %s\n", res.Outcome)
		if res.Error != "" {
			fmt.Fprintf(a.Out, "Error:   %s\n", res.Error)
		}
	}
	return exitCodeFor(res.Outcome)
}

// exitCodeFor maps an outcome string to its exit code (OUTCOME_CONTRACT.md §4).
// `interrupted` is produced only by the restart reconciler and maps to 130
// (SIGINT-like), matching `cancelled`.
func exitCodeFor(outcome string) int {
	switch outcome {
	case "completed-with-commit", "completed-with-uncommitted-changes":
		return ExitOK
	case "failed", "completed-no-changes":
		return ExitErr
	case "cancelled", "interrupted":
		return ExitCancelled
	case "timed-out":
		return ExitTimedOut
	}
	return ExitErr
}

func shortSHA(sha string) string {
	if len(sha) >= 7 {
		return sha[:7]
	}
	return sha
}

// _ keeps the os/exec import alive for future "is git installed" probes.
var _ = exec.LookPath
