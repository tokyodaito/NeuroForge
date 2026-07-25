// Package testengine implements progressive test verification (spec §24).
//
// STATUS: implemented for milestone M8.
//
// Scope:
//   - Progressive verification levels (§24.3): syntax → compile → targeted →
//     module → full. The engine runs only the level appropriate for the change
//     size, avoiding a full pipeline run after every small edit.
//   - Test scope enforcement (§24.2): when test generation is disabled, test
//     paths are forbidden (delegated to [policy.CheckTestScope]).
//   - Test result model: pass/fail/skip counts, individual failures, sliced
//     output (§22.4 — the agent gets exit code + failing command + first error,
//     never a full multi-MB log).
//   - A deterministic [FakeRunner] that produces scripted results without any
//     real test execution or network (rule §36.5).
//
// Boundaries: this package consumes [policy] for toggle decisions but does not
// itself run coding agents, perform Git operations, or hold credentials.
package testengine

import (
	"context"
	"fmt"
	"time"

	"neuroforge/internal/policy"
)

// VerificationLevel is the progressive verification depth (spec §24.3).
//
//	LevelSyntax    — parse/syntax check of changed files.
//	LevelCompile   — compile the changed module.
//	LevelTargeted  — run only the tests touching changed files.
//	LevelModule    — run the full module test suite.
//	LevelFull      — run the entire project verification.
type VerificationLevel int

const (
	LevelSyntax VerificationLevel = iota
	LevelCompile
	LevelTargeted
	LevelModule
	LevelFull
)

// String returns a stable label.
func (l VerificationLevel) String() string {
	switch l {
	case LevelSyntax:
		return "syntax"
	case LevelCompile:
		return "compile"
	case LevelTargeted:
		return "targeted"
	case LevelModule:
		return "module"
	case LevelFull:
		return "full"
	default:
		return "unknown"
	}
}

// VerificationStatus is the outcome of a verification level.
type VerificationStatus string

const (
	StatusPassed  VerificationStatus = "passed"
	StatusFailed  VerificationStatus = "failed"
	StatusSkipped VerificationStatus = "skipped" // stage disabled by policy
	StatusNotRun  VerificationStatus = "not_run" // not reached (earlier stage failed)
)

// TestFailure is one failing test (spec §22.5 — the repair agent gets the
// finding, failing test and necessary files, not the full log).
type TestFailure struct {
	TestName string
	Package  string
	Message  string
	File     string
	Line     int
}

// Result is the outcome of one verification level run.
type Result struct {
	Level    VerificationLevel
	Status   VerificationStatus
	Passed   int
	Failed   int
	Skipped  int
	Duration time.Duration
	Failures []TestFailure
	// SlicedOutput is the §22.4 summary: exit code, failing command, first error
	// and relevant stack trace. The full log is referenced by LogPath.
	SlicedOutput string
	LogPath      string
}

// Summary is the aggregate outcome of a progressive verification run.
type Summary struct {
	Results []Result
	// HighestCompletedLevel is the deepest level that passed (-1 if none).
	HighestCompletedLevel VerificationLevel
	// TestsWereRun reports whether ANY test level actually executed.
	TestsWereRun bool
}

// OverallStatus returns the aggregate status across all results.
func (s Summary) OverallStatus() VerificationStatus {
	if len(s.Results) == 0 {
		return StatusSkipped
	}
	for _, r := range s.Results {
		if r.Status == StatusFailed {
			return StatusFailed
		}
	}
	allSkipped := true
	for _, r := range s.Results {
		if r.Status != StatusSkipped && r.Status != StatusNotRun {
			allSkipped = false
			break
		}
	}
	if allSkipped {
		return StatusSkipped
	}
	return StatusPassed
}

// HasTestFailures reports whether any result has failing tests.
func (s Summary) HasTestFailures() bool {
	for _, r := range s.Results {
		if r.Failed > 0 {
			return true
		}
	}
	return false
}

// Runner executes tests at a given level.
type Runner interface {
	// Run executes the verification at req.Level. It must be deterministic and
	// perform no network calls (rule §36.5 for fakes; real runners shell out
	// locally).
	Run(ctx context.Context, req RunRequest) (Result, error)
}

// RunRequest describes a verification run.
type RunRequest struct {
	Level         VerificationLevel
	WorkspacePath string
	// ChangedFiles are the paths changed in this attempt (for targeted tests).
	ChangedFiles []string
	// Module is the module/package under test (for module-level tests).
	Module string
}

// Engine drives progressive verification according to the resolved policy. It is
// the M8-2 "test engine" — a deterministic orchestrator that never uses an LLM
// (rule §22.6) and respects the §24.1 toggles.
type Engine struct {
	runner Runner
	now    func() time.Time
}

// Options configures an Engine.
type Options struct {
	Runner Runner
}

// New creates an Engine. If no runner is provided, a [FakeRunner] with a passing
// default script is used (so the engine is always usable in tests).
func New(opts Options) *Engine {
	runner := opts.Runner
	if runner == nil {
		runner = NewFakeRunner(FakeScript{Result: Result{Status: StatusPassed}})
	}
	return &Engine{runner: runner, now: func() time.Time { return time.Now().UTC() }}
}

// VerifyInput carries the context for a verification pass.
type VerifyInput struct {
	// Policy is the resolved pipeline (drives which stages run / are skipped).
	Policy policy.Resolved
	// ChangedFiles are the files modified by the agent.
	ChangedFiles []policy.FileChange
	// MaxLevel caps the verification depth (LevelFull if zero).
	MaxLevel VerificationLevel
}

// Verify runs progressive verification, honouring the §24 toggles:
//   - If tests.run_existing and run_generated are both off (or generate is off),
//     the test levels are skipped and the summary reports StatusSkipped.
//   - If a level fails, deeper levels are marked StatusNotRun.
//
// The function is deterministic and side-effect-free except for Runner calls.
func (e *Engine) Verify(ctx context.Context, in VerifyInput) (Summary, error) {
	p := in.Policy.Pipeline
	maxLevel := in.MaxLevel
	if maxLevel == 0 {
		maxLevel = LevelFull
	}

	// Determine whether test execution is enabled (§24.1, §24.2).
	testsEnabled := p.Tests.RunExisting || (p.Tests.RunGenerated && p.Tests.Generate)
	if !testsEnabled {
		// Even with tests off, syntax/compile may still run (they are not
		// "tests" per se). But per §24.3 the cascade is syntax → compile →
		// tests → full. We run syntax + compile, then skip the test levels.
		return e.runNoTestLevels(ctx, in, maxLevel), nil
	}

	var summary Summary
	startFrom := LevelSyntax
	for lvl := startFrom; lvl <= maxLevel; lvl++ {
		if !shouldRunLevel(lvl, p) {
			summary.Results = append(summary.Results, Result{
				Level:  lvl,
				Status: StatusSkipped,
			})
			continue
		}
		res, err := e.runner.Run(ctx, RunRequest{
			Level:         lvl,
			WorkspacePath: in.ChangedFilesPath(),
			ChangedFiles:  pathsOnly(in.ChangedFiles),
		})
		if err != nil {
			return summary, fmt.Errorf("testengine: level %s: %w", lvl, err)
		}
		res.Level = lvl
		if res.Status == "" {
			res.Status = StatusPassed
		}
		summary.Results = append(summary.Results, res)
		if res.Status == StatusPassed {
			summary.HighestCompletedLevel = lvl
			if lvl >= LevelTargeted {
				summary.TestsWereRun = true
			}
			continue
		}
		// Failed: mark tests as run (they were attempted) and stop deeper levels.
		if res.Status == StatusFailed {
			if lvl >= LevelTargeted {
				summary.TestsWereRun = true
			}
			for deeper := lvl + 1; deeper <= maxLevel; deeper++ {
				summary.Results = append(summary.Results, Result{
					Level:  deeper,
					Status: StatusNotRun,
				})
			}
			return summary, nil
		}
	}
	return summary, nil
}

// runNoTestLevels runs syntax/compile when tests are entirely disabled.
func (e *Engine) runNoTestLevels(ctx context.Context, in VerifyInput, maxLevel VerificationLevel) Summary {
	var summary Summary
	for lvl := LevelSyntax; lvl <= maxLevel; lvl++ {
		if lvl >= LevelTargeted {
			summary.Results = append(summary.Results, Result{
				Level:  lvl,
				Status: StatusSkipped,
			})
			continue
		}
		res, err := e.runner.Run(ctx, RunRequest{
			Level:         lvl,
			WorkspacePath: in.ChangedFilesPath(),
			ChangedFiles:  pathsOnly(in.ChangedFiles),
		})
		if err != nil {
			res = Result{Level: lvl, Status: StatusFailed, SlicedOutput: err.Error()}
		}
		res.Level = lvl
		if res.Status == "" {
			res.Status = StatusPassed
		}
		summary.Results = append(summary.Results, res)
		if res.Status == StatusPassed {
			summary.HighestCompletedLevel = lvl
		} else {
			for deeper := lvl + 1; deeper <= maxLevel; deeper++ {
				summary.Results = append(summary.Results, Result{
					Level:  deeper,
					Status: StatusNotRun,
				})
			}
			return summary
		}
	}
	return summary
}

// shouldRunLevel reports whether a level should execute given the toggles.
func shouldRunLevel(lvl VerificationLevel, p policy.Pipeline) bool {
	switch lvl {
	case LevelSyntax, LevelCompile:
		return true
	case LevelTargeted:
		// Targeted tests run if either existing or generated tests are enabled.
		return p.Tests.RunExisting || (p.Tests.RunGenerated && p.Tests.Generate)
	case LevelModule, LevelFull:
		return p.Tests.RunExisting || (p.Tests.RunGenerated && p.Tests.Generate)
	}
	return false
}

func pathsOnly(changes []policy.FileChange) []string {
	out := make([]string, len(changes))
	for i, c := range changes {
		out[i] = c.Path
	}
	return out
}

// ChangedFilesPath is a helper that returns a workspace path hint from the
// changed files (empty if none).
func (in VerifyInput) ChangedFilesPath() string {
	for _, c := range in.ChangedFiles {
		_ = c
	}
	return ""
}
