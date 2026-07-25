// Package repair implements the bounded repair loop (spec §22.5, §25, §16.5).
//
// STATUS: implemented for milestone M8.
//
// Scope:
//   - Collects findings from the test engine (test failures) and review engine
//     (review findings).
//   - Builds a targeted repair context per §22.5 (the repair agent receives the
//     finding, the current diff, the failing test and the necessary files — NOT
//     the full conversation history of the failed run).
//   - Invokes a pluggable repair function (the coding agent with a targeted
//     prompt), then re-verifies, up to a configurable maximum iterations.
//   - Bounded: the loop NEVER retries indefinitely (rule §32). If the max is
//     reached without resolution, the loop returns the remaining findings.
//
// Boundaries: the loop is deterministic orchestration. The repair function and
// verifiers are injected; this package never calls an LLM directly (§22.6) and
// holds no credentials.
package repair

import (
	"context"
	"fmt"

	"neuroforge/internal/review"
	"neuroforge/internal/testengine"
)

// Finding is a unified finding from either the test engine or the review engine.
type Finding struct {
	Source      string // "test" | "review"
	Severity    string // mirrors review.Severity or "fail" for tests
	Title       string
	Description string
	File        string
	Line        int
}

// FromTestFailures converts testengine failures into unified findings.
func FromTestFailures(results []testengine.Result) []Finding {
	var out []Finding
	for _, r := range results {
		for _, f := range r.Failures {
			out = append(out, Finding{
				Source:      "test",
				Severity:    "fail",
				Title:       f.TestName,
				Description: f.Message,
				File:        f.File,
				Line:        f.Line,
			})
		}
	}
	return out
}

// FromReviewFindings converts review findings into unified findings.
func FromReviewFindings(findings []review.Finding) []Finding {
	out := make([]Finding, len(findings))
	for i, f := range findings {
		out[i] = Finding{
			Source:      "review",
			Severity:    string(f.Severity),
			Title:       f.Title,
			Description: f.Description,
			File:        f.File,
			Line:        f.Line,
		}
	}
	return out
}

// IsActionable reports whether a finding should trigger a repair (only test
// failures and major/blocker review findings warrant repair; minor/info can be
// left for the human).
func (f Finding) IsActionable() bool {
	if f.Source == "test" {
		return true
	}
	return f.Severity == string(review.SeverityMajor) || f.Severity == string(review.SeverityBlocker)
}

// RepairContext is the targeted context handed to the repair function (§22.5).
// It deliberately excludes the full conversation history of the failed run.
type RepairContext struct {
	Findings []Finding
	// Diff is the current change set under repair.
	Diff string
	// ChangedFiles are the paths to focus on.
	ChangedFiles []string
	// Iteration is the current loop iteration (1-based).
	Iteration int
}

// Prompt renders the targeted repair prompt (§22.5). The repair agent gets the
// findings and the files it needs, not the entire prior run transcript.
func (c RepairContext) Prompt() string {
	if len(c.Findings) == 0 {
		return "No actionable findings."
	}
	p := fmt.Sprintf("Repair the following issues (iteration %d):\n\n", c.Iteration)
	for i, f := range c.Findings {
		p += fmt.Sprintf("%d. [%s] %s\n", i+1, f.Source, f.Title)
		if f.Description != "" {
			p += fmt.Sprintf("   %s\n", f.Description)
		}
		if f.File != "" {
			loc := f.File
			if f.Line > 0 {
				loc = fmt.Sprintf("%s:%d", f.File, f.Line)
			}
			p += fmt.Sprintf("   at %s\n", loc)
		}
	}
	return p
}

// RepairFunc attempts to fix the findings. It returns an error if the repair
// itself fails (infrastructure); returning nil means the repair ran and the loop
// should re-verify.
type RepairFunc func(ctx context.Context, rc RepairContext) error

// VerifyFunc re-runs verification after a repair attempt. It returns the
// remaining findings (test failures + review findings) and an error only if
// verification itself failed.
type VerifyFunc func(ctx context.Context) ([]Finding, error)

// Outcome is the final result of a repair loop.
type Outcome struct {
	// Resolved reports whether no actionable findings remain.
	Resolved bool
	// IterationsRun is the number of repair iterations executed.
	IterationsRun int
	// RemainingFindings are the findings still open after the loop ended.
	RemainingFindings []Finding
	// History records the findings at each iteration (for audit/§29.4).
	History [][]Finding
}

// Loop runs a bounded repair loop. It stops when:
//   - No actionable findings remain (Resolved=true); or
//   - MaxIterations is reached; or
//   - A repair/verify call returns a hard error.
//
// The loop NEVER retries indefinitely (rule §32). MaxIterations must be > 0.
type Loop struct {
	maxIterations int
	repair        RepairFunc
	verify        VerifyFunc
}

// Options configures a Loop.
type Options struct {
	MaxIterations int
	Repair        RepairFunc
	Verify        VerifyFunc
}

// New creates a Loop. Panics if MaxIterations <= 0 (an unbounded loop is
// forbidden by rule §32).
func New(opts Options) *Loop {
	if opts.MaxIterations <= 0 {
		panic("repair: MaxIterations must be > 0 (rule §32: no infinite retry)")
	}
	return &Loop{
		maxIterations: opts.MaxIterations,
		repair:        opts.Repair,
		verify:        opts.Verify,
	}
}

// Run executes the repair loop starting from the given initial findings.
func (l *Loop) Run(ctx context.Context, initial []Finding, diff string, changedFiles []string) (Outcome, error) {
	current := filterActionable(initial)
	out := Outcome{History: [][]Finding{current}}

	if len(current) == 0 {
		out.Resolved = true
		return out, nil
	}

	for iter := 1; iter <= l.maxIterations; iter++ {
		rc := RepairContext{
			Findings:     current,
			Diff:         diff,
			ChangedFiles: changedFiles,
			Iteration:    iter,
		}
		if err := l.repair(ctx, rc); err != nil {
			out.RemainingFindings = current
			out.IterationsRun = iter
			return out, fmt.Errorf("repair: iteration %d: %w", iter, err)
		}
		out.IterationsRun = iter

		remaining, err := l.verify(ctx)
		if err != nil {
			out.RemainingFindings = current
			return out, fmt.Errorf("repair: verify iteration %d: %w", iter, err)
		}
		remaining = filterActionable(remaining)
		out.History = append(out.History, remaining)

		if len(remaining) == 0 {
			out.Resolved = true
			return out, nil
		}
		current = remaining
	}

	out.RemainingFindings = current
	return out, nil
}

// filterActionable keeps only findings that warrant a repair.
func filterActionable(findings []Finding) []Finding {
	var out []Finding
	for _, f := range findings {
		if f.IsActionable() {
			out = append(out, f)
		}
	}
	return out
}
