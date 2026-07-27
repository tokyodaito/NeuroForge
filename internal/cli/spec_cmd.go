package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"time"

	"neuroforge/internal/task"
	"neuroforge/internal/transport"
)

// runSpec dispatches `forge spec <subcommand>`. M14-02 implements the
// deterministic task compiler surface (§18.1) via `forge spec compile` (the
// offline pure compiler probe). M14-03 adds the daemon-mediated,
// durable, idempotent compile-and-save path plus the spec CRUD commands
// (`save`/`show`/`lock`/`versions`) that reach the same store through the
// loopback transport.
func (a *App) runSpec(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(a.Err, specUsage)
		return ExitErr
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "compile":
		return a.specCompile(rest)
	case "save":
		return a.specSave(rest)
	case "show":
		return a.specShow(rest)
	case "lock":
		return a.specLock(rest)
	case "versions":
		return a.specVersions(rest)
	case "-h", "--help":
		fmt.Fprintln(a.Out, specUsage)
		return ExitOK
	default:
		fmt.Fprintf(a.Err, "%s: unknown spec subcommand %q\n\n", a.Name, sub)
		fmt.Fprintln(a.Err, specUsage)
		return ExitErr
	}
}

const specUsage = `Usage: forge spec <subcommand> [flags]

Subcommands:
  compile [flags] <description>
                            Deterministically compile free-form task text into a
                            structured task.Specification (§18.1) OFFLINE. The
                            compiler is pure (no I/O, no clock, no daemon).
  save -t <id> [--by <actor>] [--json]
                            Daemon-mediated compile-and-save: read the task from
                            the backlog, run the deterministic compiler, and
                            durably persist the resulting specification
                            (idempotent: a re-compile of an unchanged task
                            returns the existing version without minting a new
                            one). Survives daemon restart.
  show -t <id> [-v <ver>] [--json]
                            Read the latest (or a specific) persisted
                            specification for a task.
  lock -t <id> -v <ver> [--by <actor>] [--json]
                            Mark a specification version immutable (§28).
  versions -t <id> [--json]
                            List every persisted specification version for a
                            task.

Spec compile flags:
  -p, --project <id>     Project ID (required; becomes the spec's TaskID prefix)
  --task <id>            Full task ID (overrides --project)
  --title <title>        Optional task title
  --priority <level>     LOW | NORMAL | HIGH | URGENT (default NORMAL; validated)
  --attach <spec>        Attachment metadata, repeatable. Spec grammar:
                             hash=ROLE
                             hash=ROLE:filename
                             hash=ROLE:filename:mimeType
                             hash=ROLE:filename:mimeType:size
                          ROLE: DESIGN_REFERENCE|BUG_SCREENSHOT|REQUIREMENTS|
                                LOG|API_SPECIFICATION|EXAMPLE|GENERAL_CONTEXT
                          filename/mimeType/size are optional metadata the
                          compiler consumes (it never reads attachment content).
                          Filenames containing ':' are not supported by the
                          inline form (drop the colon or pipe-escape elsewhere).
  --json                 Emit machine-readable JSON (opt-in; default is text)

Spec save / show / lock / versions flags:
  -t, --task <id>        Task ID (required)
  -v, --version <n>      Specification version (show/lock); <=0 means "latest"
                          (show only)
      --by <actor>       Provenance actor recorded on the audit trail
                          (save/lock; default: "daemon")
      --json             Emit machine-readable JSON (opt-in; default is text)

The compile subcommand is offline; save/show/lock/versions talk to the daemon.`

// specCompile implements `forge spec compile` — the deterministic task compiler
// (M14-02). It exercises the production task.Compile function directly so
// black-box observers can validate the compiler through the compiled binary
// (engineering baseline §2).
func (a *App) specCompile(args []string) int {
	fs := flag.NewFlagSet("spec compile", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	fs.Usage = func() { fmt.Fprintln(a.Err, specUsage) }
	projectID := fs.String("project", "", "project ID (required)")
	fs.StringVar(projectID, "p", "", "shortcut for --project")
	taskID := fs.String("task", "", "full task ID (overrides --project)")
	title := fs.String("title", "", "optional task title")
	priority := fs.String("priority", "NORMAL", "LOW | NORMAL | HIGH | URGENT")
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")

	var attachments []string
	fs.Func("attach", "attachment metadata as hash=ROLE[:filename[:mimeType[:size]]] (repeatable)", func(s string) error {
		attachments = append(attachments, s)
		return nil
	})

	if err := fs.Parse(args); err != nil {
		return ExitErr
	}

	// Validate --priority against the known set so a typo is reported, not
	// silently propagated into CompileInput.Priority (MINOR-3 review fix;
	// matches the --attach role-validation behaviour).
	switch task.Priority(*priority) {
	case task.PriorityLow, task.PriorityNormal, task.PriorityHigh, task.PriorityUrgent:
	default:
		fmt.Fprintf(a.Err, "Error: --priority must be one of LOW|NORMAL|HIGH|URGENT, got %q\n", *priority)
		return ExitErr
	}

	id := *taskID
	if id == "" {
		if *projectID == "" {
			fmt.Fprintln(a.Err, "Error: --project (-p) or --task is required")
			return ExitErr
		}
		// The compiler does not allocate sequence numbers; use a stable,
		// human-readable TaskID derived from --project so the output is a
		// valid Specification the caller can later persist against a real task.
		id = *projectID + "-compiled"
	}

	description := strings.Join(fs.Args(), " ")
	if description == "" && len(attachments) == 0 {
		fmt.Fprintln(a.Err, "Error: description or attachment is required")
		return ExitErr
	}

	in := task.CompileInput{
		TaskID:      id,
		Title:       *title,
		Description: description,
		Priority:    task.Priority(*priority),
	}
	for _, raw := range attachments {
		att, ok := splitAttachFlag(raw)
		if !ok {
			fmt.Fprintf(a.Err, "Error: --attach expects hash=ROLE[:filename[:mimeType[:size]]], got %q\n", raw)
			return ExitErr
		}
		in.Attachments = append(in.Attachments, att)
	}

	res, err := task.Compile(in)
	if err != nil {
		fmt.Fprintf(a.Err, "%s: spec compile: %v\n", a.Name, err)
		return ExitErr
	}

	if !*jsonOut {
		writeSpecCompileText(a, res)
		return ExitOK
	}

	b, err := json.MarshalIndent(specCompileJSON{Result: res}, "", "  ")
	if err != nil {
		fmt.Fprintf(a.Err, "%s: spec compile: marshal: %v\n", a.Name, err)
		return ExitErr
	}
	fmt.Fprintln(a.Out, string(b))
	return ExitOK
}

// splitAttachFlag parses the `--attach` flag value into a task.Attachment.
//
// Accepted grammar (MAJOR-1 review fix — the CLI now carries the metadata the
// compiler is documented to consume, spec §9.5):
//
//	hash=ROLE
//	hash=ROLE:filename
//	hash=ROLE:filename:mimeType
//	hash=ROLE:filename:mimeType:size
//
// ROLE is validated against the known attachment-role set so a typo is
// reported, not silently swallowed. Filename, mimeType and size are optional;
// when absent the corresponding task.Attachment field stays zero-valued. A
// filename that itself contains ':' is not representable in this inline form
// (documented in --help); callers needing such filenames should pass them via
// the daemon transport's task-add path, which content-addresses files.
//
// The legacy `hash=ROLE` form remains valid (backward compatible).
func splitAttachFlag(s string) (task.Attachment, bool) {
	idx := strings.IndexByte(s, '=')
	if idx <= 0 || idx == len(s)-1 {
		return task.Attachment{}, false
	}
	hash := s[:idx]
	rest := s[idx+1:]
	if hash == "" || strings.TrimSpace(rest) == "" {
		return task.Attachment{}, false
	}
	// Split on ':' into role / filename / mimeType / size. Filenames with ':'
	// are out of scope (see docstring).
	parts := strings.Split(rest, ":")
	role := strings.ToUpper(strings.TrimSpace(parts[0]))
	if role == "" {
		return task.Attachment{}, false
	}
	switch task.AttachmentRole(role) {
	case task.RoleDesignReference, task.RoleBugScreenshot, task.RoleRequirements,
		task.RoleLog, task.RoleAPISpec, task.RoleExample, task.RoleGeneralContext:
	default:
		return task.Attachment{}, false
	}
	att := task.Attachment{Hash: hash, Role: task.AttachmentRole(role)}
	if len(parts) > 1 {
		att.Filename = strings.TrimSpace(parts[1])
	}
	if len(parts) > 2 {
		att.MimeType = strings.TrimSpace(parts[2])
	}
	if len(parts) > 3 {
		sizeStr := strings.TrimSpace(parts[3])
		if sizeStr == "" {
			return att, true
		}
		n, err := strconv.ParseInt(sizeStr, 10, 64)
		if err != nil || n < 0 {
			return task.Attachment{}, false
		}
		att.Size = n
	}
	return att, true
}

// specCompileJSON wraps the CompileResult so the marshalled output is a stable
// JSON object even when the result carries nil maps/slices (json's default
// handling would emit null, which is awkward for downstream parsers).
type specCompileJSON struct {
	Result task.CompileResult `json:"result"`
}

func writeSpecCompileText(a *App, res task.CompileResult) {
	spec := res.Specification
	fmt.Fprintf(a.Out, "TaskID:     %s\n", spec.TaskID)
	fmt.Fprintf(a.Out, "Objective:  %s\n", spec.Objective)
	fmt.Fprintf(a.Out, "Risk:       %s\n", spec.Risk)
	fmt.Fprintf(a.Out, "Complexity: %s\n", spec.Complexity)
	fmt.Fprintf(a.Out, "Confidence: %s\n", res.Confidence)
	for _, ac := range spec.AcceptanceCriteria {
		fmt.Fprintf(a.Out, "  %s: %s\n", ac.ID, ac.Statement)
	}
	if len(spec.NonGoals) > 0 {
		fmt.Fprintln(a.Out, "Non-goals:")
		for _, ng := range spec.NonGoals {
			fmt.Fprintf(a.Out, "  - %s\n", ng)
		}
	}
	if len(spec.Constraints) > 0 {
		fmt.Fprintln(a.Out, "Constraints:")
		for _, c := range spec.Constraints {
			fmt.Fprintf(a.Out, "  - %s\n", c)
		}
	}
	if len(res.Clarifications) > 0 {
		fmt.Fprintln(a.Out, "Clarifications:")
		for _, c := range res.Clarifications {
			fmt.Fprintf(a.Out, "  - %s (%s)\n", c.Question, c.Reason)
		}
	}
}

// ---- daemon-mediated spec subcommands (M14-03) ----
//
// specSave implements `forge spec save -t <id>`: compile the task's
// description through the daemon and durably persist the resulting
// specification. Idempotent — a re-compile of an unchanged task returns the
// existing version without minting a new one.
func (a *App) specSave(args []string) int {
	fs := flag.NewFlagSet("spec save", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	fs.Usage = func() { fmt.Fprintln(a.Err, specUsage) }
	taskID := fs.String("task", "", "task ID (required)")
	fs.StringVar(taskID, "t", "", "shortcut for --task")
	lockedBy := fs.String("by", "", "provenance actor (default: daemon)")
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := cli.CompileSpec(ctx, *taskID, transport.CompileSpecRequest{LockedBy: *lockedBy})
	if err != nil {
		if *jsonOut {
			fmt.Fprintln(a.Out, jsonError(err))
		} else {
			a.errf("spec save: %v", err)
		}
		return ExitErr
	}
	if *jsonOut {
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Fprintln(a.Out, string(b))
		return ExitOK
	}
	writeSpecDTOText(a, res.Specification)
	createdTag := "returned (idempotent)"
	if res.Created {
		createdTag = "created"
	}
	fmt.Fprintf(a.Out, "Confidence: %s\n", res.Confidence)
	fmt.Fprintf(a.Out, "Saved:      %s\n", createdTag)
	if len(res.Clarifications) > 0 {
		fmt.Fprintln(a.Out, "Clarifications:")
		for _, c := range res.Clarifications {
			fmt.Fprintf(a.Out, "  - %s (%s)\n", c.Question, c.Reason)
		}
	}
	return ExitOK
}

// specShow implements `forge spec show -t <id> [-v <ver>]`: read the latest
// (or a specific) persisted specification for a task.
func (a *App) specShow(args []string) int {
	fs := flag.NewFlagSet("spec show", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	fs.Usage = func() { fmt.Fprintln(a.Err, specUsage) }
	taskID := fs.String("task", "", "task ID (required)")
	fs.StringVar(taskID, "t", "", "shortcut for --task")
	version := fs.Int("version", 0, "specification version (<=0 means latest)")
	fs.IntVar(version, "v", 0, "shortcut for --version")
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

	spec, err := cli.GetSpecification(ctx, *taskID, *version)
	if err != nil {
		if *jsonOut {
			fmt.Fprintln(a.Out, jsonError(err))
		} else {
			a.errf("spec show: %v", err)
		}
		return ExitErr
	}
	if *jsonOut {
		b, _ := json.MarshalIndent(spec, "", "  ")
		fmt.Fprintln(a.Out, string(b))
		return ExitOK
	}
	writeSpecDTOText(a, spec)
	return ExitOK
}

// specLock implements `forge spec lock -t <id> -v <ver>`: mark a specification
// version immutable (§28). Idempotent.
func (a *App) specLock(args []string) int {
	fs := flag.NewFlagSet("spec lock", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	fs.Usage = func() { fmt.Fprintln(a.Err, specUsage) }
	taskID := fs.String("task", "", "task ID (required)")
	fs.StringVar(taskID, "t", "", "shortcut for --task")
	version := fs.Int("version", 0, "specification version to lock (required, >0)")
	fs.IntVar(version, "v", 0, "shortcut for --version")
	lockedBy := fs.String("by", "", "provenance actor (default: daemon)")
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return ExitErr
	}
	if *taskID == "" {
		fmt.Fprintln(a.Err, "Error: --task (-t) is required")
		return ExitErr
	}
	if *version <= 0 {
		fmt.Fprintln(a.Err, "Error: --version (-v) is required and must be > 0")
		return ExitErr
	}

	cli, err := a.ensureDaemon()
	if err != nil {
		a.errf("connect to daemon: %v", err)
		return ExitErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	spec, err := cli.LockSpecification(ctx, *taskID, transport.LockSpecRequest{
		Version:  *version,
		LockedBy: *lockedBy,
	})
	if err != nil {
		if *jsonOut {
			fmt.Fprintln(a.Out, jsonError(err))
		} else {
			a.errf("spec lock: %v", err)
		}
		return ExitErr
	}
	if *jsonOut {
		b, _ := json.MarshalIndent(spec, "", "  ")
		fmt.Fprintln(a.Out, string(b))
		return ExitOK
	}
	writeSpecDTOText(a, spec)
	fmt.Fprintf(a.Out, "Locked:     %v\n", spec.Locked)
	return ExitOK
}

// specVersions implements `forge spec versions -t <id>`: list every persisted
// specification version for a task, ascending.
func (a *App) specVersions(args []string) int {
	fs := flag.NewFlagSet("spec versions", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	fs.Usage = func() { fmt.Fprintln(a.Err, specUsage) }
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

	versions, err := cli.ListSpecificationVersions(ctx, *taskID)
	if err != nil {
		if *jsonOut {
			fmt.Fprintln(a.Out, jsonError(err))
		} else {
			a.errf("spec versions: %v", err)
		}
		return ExitErr
	}
	if *jsonOut {
		b, _ := json.MarshalIndent(versions, "", "  ")
		fmt.Fprintln(a.Out, string(b))
		return ExitOK
	}
	if len(versions) == 0 {
		fmt.Fprintln(a.Out, "No persisted specification versions.")
		return ExitOK
	}
	fmt.Fprintf(a.Out, "Versions for %s:\n", *taskID)
	for _, v := range versions {
		fmt.Fprintf(a.Out, "  v%d\n", v)
	}
	return ExitOK
}

// writeSpecDTOText renders a transport.SpecificationDTO as human-readable text.
// Mirrors writeSpecCompileText's shape so `forge spec compile` and
// `forge spec save`/`show`/`lock` share a consistent text format.
func writeSpecDTOText(a *App, spec transport.SpecificationDTO) {
	fmt.Fprintf(a.Out, "TaskID:     %s\n", spec.TaskID)
	fmt.Fprintf(a.Out, "Version:    %d\n", spec.Version)
	fmt.Fprintf(a.Out, "Objective:  %s\n", spec.Objective)
	fmt.Fprintf(a.Out, "Risk:       %s\n", spec.Risk)
	fmt.Fprintf(a.Out, "Complexity: %s\n", spec.Complexity)
	for _, ac := range spec.AcceptanceCriteria {
		fmt.Fprintf(a.Out, "  %s: %s\n", ac.ID, ac.Statement)
	}
	if len(spec.NonGoals) > 0 {
		fmt.Fprintln(a.Out, "Non-goals:")
		for _, ng := range spec.NonGoals {
			fmt.Fprintf(a.Out, "  - %s\n", ng)
		}
	}
	if len(spec.Constraints) > 0 {
		fmt.Fprintln(a.Out, "Constraints:")
		for _, c := range spec.Constraints {
			fmt.Fprintf(a.Out, "  - %s\n", c)
		}
	}
	if spec.Locked {
		fmt.Fprintf(a.Out, "Locked:     true (by %s at %s)\n", spec.LockedBy, spec.LockedAt)
	}
}
