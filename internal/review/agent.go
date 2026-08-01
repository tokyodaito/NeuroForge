package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"text/template"
)

// ErrUnparseableReview is returned (wrapped) when the review agent's output
// contains no valid findings array. The caller classifies this as invalid
// agent output — it must never be silently converted into an approval.
var ErrUnparseableReview = errors.New("review: unparseable agent output")

// DefaultMaxDiffBytes caps the diff embedded in a review prompt. Larger diffs
// are truncated and annotated (see [truncateDiff]).
const DefaultMaxDiffBytes = 200 * 1024

// maxFindings caps how many findings a single review response may produce.
// Anything beyond the cap is discarded; the cap is generous enough that it
// only guards against pathological model output.
const maxFindings = 50

// RunFunc runs one agent invocation with the given prompt and returns the
// agent's stdout. The daemon supplies an implementation that runs an agent
// through the supervisor; this package stays free of adapter imports.
type RunFunc func(ctx context.Context, prompt string) (stdout string, err error)

// AgentReviewerOptions configures an [AgentReviewer].
type AgentReviewerOptions struct {
	// MaxDiffBytes truncates the diff embedded in each prompt. Zero means
	// [DefaultMaxDiffBytes]. A negative value disables truncation.
	MaxDiffBytes int
}

// AgentReviewer implements [Reviewer] by invoking an external agent process
// (via the injected [RunFunc]) once per review role. It renders a focused,
// deterministic prompt per role and parses the model's JSON response back
// into findings. It performs no I/O itself and holds no credentials.
type AgentReviewer struct {
	run          RunFunc
	maxDiffBytes int
}

// NewAgentReviewer creates an AgentReviewer. run must be non-nil.
func NewAgentReviewer(run RunFunc, opts AgentReviewerOptions) *AgentReviewer {
	max := opts.MaxDiffBytes
	if max == 0 {
		max = DefaultMaxDiffBytes
	}
	return &AgentReviewer{run: run, maxDiffBytes: max}
}

// Compile-time check that AgentReviewer satisfies the Reviewer interface.
var _ Reviewer = (*AgentReviewer)(nil)

// Review implements Reviewer. It renders the role-specific prompt, invokes
// the agent, and parses the findings. Empty or whitespace-only agent output
// is an error wrapping [ErrUnparseableReview] — silence must never be
// silently converted into an approval (review finding M8); output with no
// valid JSON array yields the same error.
func (a *AgentReviewer) Review(ctx context.Context, role Role, req ReviewRequest) ([]Finding, error) {
	prompt, err := renderPrompt(role, req, a.maxDiffBytes)
	if err != nil {
		return nil, fmt.Errorf("review: render prompt: %w", err)
	}
	out, err := a.run(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("review: agent run: %w", err)
	}
	if strings.TrimSpace(out) == "" {
		return nil, fmt.Errorf("%w: empty agent output", ErrUnparseableReview)
	}
	findings, err := parseFindings(role, out)
	if err != nil {
		return nil, err
	}
	return findings, nil
}

// roleInstruction is the role-specific part of the review prompt.
var roleInstruction = map[Role]string{
	RoleCorrectness: "Review the change for CORRECTNESS: logic errors, off-by-one mistakes, " +
		"nil/edge-case handling, error paths, concurrency hazards, and behaviour that " +
		"diverges from the stated intent.",
	RoleArchitecture: "Review the change for ARCHITECTURE: package boundary violations, layering " +
		"regressions, misplaced responsibilities, coupling, and drift from the documented design.",
	RoleSecurity: "Review the change for SECURITY: injection, credential or secret exposure, " +
		"unsafe command/process construction, path traversal, unvalidated input, and weakened " +
		"policy enforcement.",
}

// promptTemplate is the deterministic prompt rendered for every review call.
// The model is instructed to review only (never modify files) and to answer
// with exactly one JSON array of findings.
var promptTemplate = template.Must(template.New("review").Parse(
	`You are an independent code reviewer. You did not write this change. Review it only — do not modify any files.

Your role: {{ .RoleInstruction }}

Changed files:
{{ range .ChangedFiles }}- {{ . }}
{{ else }}(none listed)
{{ end }}
{{ if .Context }}Additional context:
{{ .Context }}

{{ end }}Diff under review:
` + "```diff" + `
{{ .Diff }}
` + "```" + `

Respond with ONLY a JSON array of findings — no prose, no markdown fences. Each finding is an object with fields:
- "severity": one of "info", "minor", "major", "blocker"
- "title": short one-line summary
- "description": what is wrong and why it matters
- "file": file path the finding applies to ("" if not file-specific)
- "line": 1-based line number in that file (0 if not line-specific)
- "remediation": concrete suggested fix

If the change is clean, respond with exactly: []
`))

// promptData carries the rendered values into promptTemplate.
type promptData struct {
	RoleInstruction string
	ChangedFiles    []string
	Context         string
	Diff            string
}

// renderPrompt builds the deterministic prompt text for one review role.
func renderPrompt(role Role, req ReviewRequest, maxDiffBytes int) (string, error) {
	instr, ok := roleInstruction[role]
	if !ok {
		instr = fmt.Sprintf("Review the change for %s issues.", strings.ToUpper(string(role)))
	}
	var b strings.Builder
	err := promptTemplate.Execute(&b, promptData{
		RoleInstruction: instr,
		ChangedFiles:    req.ChangedFiles,
		Context:         req.Context,
		Diff:            truncateDiff(req.Diff, maxDiffBytes),
	})
	return b.String(), err
}

// truncateDiff caps the diff at maxBytes, appending a notice line when it had
// to cut. A negative maxBytes disables truncation.
func truncateDiff(diff string, maxBytes int) string {
	if maxBytes < 0 || len(diff) <= maxBytes {
		return diff
	}
	// Cut at a newline boundary so the truncated diff does not end mid-line.
	cut := strings.LastIndexByte(diff[:maxBytes], '\n')
	if cut < 0 {
		cut = maxBytes
	}
	return diff[:cut] + fmt.Sprintf("\n[... diff truncated at %d bytes; %d bytes omitted ...]\n", maxBytes, len(diff)-cut)
}

// rawFinding mirrors the model's JSON output before normalization.
type rawFinding struct {
	Severity    string `json:"severity"`
	Title       string `json:"title"`
	Description string `json:"description"`
	File        string `json:"file"`
	Line        int    `json:"line"`
	Remediation string `json:"remediation"`
}

// parseFindings extracts the last JSON array from the agent output (tolerating
// ```json fences and surrounding prose), validates it, and normalizes each
// entry into a Finding for the given role.
func parseFindings(role Role, out string) ([]Finding, error) {
	raw, err := extractLastJSONArray(out)
	if err != nil {
		return nil, err
	}
	var items []rawFinding
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("%w: invalid findings JSON: %v", ErrUnparseableReview, err)
	}
	findings := make([]Finding, 0, len(items))
	for _, it := range items {
		if len(findings) >= maxFindings {
			break
		}
		findings = append(findings, normalizeFinding(role, it))
	}
	return findings, nil
}

// normalizeFinding maps a raw model finding onto the Finding model: unknown
// severities fall back to info, and negative line numbers clamp to 0.
func normalizeFinding(role Role, it rawFinding) Finding {
	line := it.Line
	if line < 0 {
		line = 0
	}
	return Finding{
		Role:        role,
		Severity:    normalizeSeverity(it.Severity),
		Title:       strings.TrimSpace(it.Title),
		Description: strings.TrimSpace(it.Description),
		File:        strings.TrimSpace(it.File),
		Line:        line,
		Remediation: strings.TrimSpace(it.Remediation),
	}
}

// normalizeSeverity lower-cases and validates a severity; unknown values map
// to info so a noisy model cannot manufacture merge gates.
func normalizeSeverity(s string) Severity {
	switch Severity(strings.ToLower(strings.TrimSpace(s))) {
	case SeverityMinor:
		return SeverityMinor
	case SeverityMajor:
		return SeverityMajor
	case SeverityBlocker:
		return SeverityBlocker
	default:
		return SeverityInfo
	}
}

// extractLastJSONArray returns the bytes of the LAST complete top-level
// balanced [...] array in out. The last array is used so a model that quotes
// an example array in prose before its real answer still parses. Fences
// (```json ... ```) need no special handling: the brackets are located
// directly. Brackets inside JSON strings are ignored; the forward scan keeps
// escape handling unambiguous.
func extractLastJSONArray(out string) ([]byte, error) {
	depth := 0
	inString := false
	escaped := false
	start := -1
	lastStart, lastEnd := -1, -1
	for i := 0; i < len(out); i++ {
		c := out[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			// Only treat as a string delimiter inside an array (or prose quotes
			// are harmless either way: they come in pairs).
			inString = true
		case '[':
			if depth == 0 {
				start = i
			}
			depth++
		case ']':
			if depth == 0 {
				continue // stray ']' in prose
			}
			depth--
			if depth == 0 {
				lastStart, lastEnd = start, i
			}
		}
	}
	if lastStart < 0 {
		return nil, fmt.Errorf("%w: no JSON array in output", ErrUnparseableReview)
	}
	return []byte(out[lastStart : lastEnd+1]), nil
}
