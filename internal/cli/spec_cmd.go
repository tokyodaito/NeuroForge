package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"strconv"
	"strings"

	"neuroforge/internal/task"
)

// runSpec dispatches `forge spec <subcommand>`. M14-02 implements the
// deterministic task compiler surface (§18.1) via `forge spec compile`. Spec
// CRUD (create/get/lock/versions) over the daemon transport is a follow-up:
// here the compiler is exercised directly so the command works without a
// running daemon (the compiler is pure: no I/O, no clock).
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
  compile [flags] <description>   Deterministically compile free-form task text
                                  into a structured task.Specification (§18.1).

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

The compiler is pure: it never calls an external model (§18.2 cascade:
deterministic parsing -> cheap classifier). Output is the compiled
specification + confidence + clarifications. No daemon is required.`

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
