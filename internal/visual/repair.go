package visual

import (
	"context"
	"fmt"
)

// RepairLoopConfig configures the §16.5 visual repair loop.
type RepairLoopConfig struct {
	// MaximumIterations caps the repair attempts (§16.5 maximum_iterations).
	// Must be > 0 (rule §32: no infinite retry).
	MaximumIterations int
	// MinimumScore is the §16.5 minimum_score threshold.
	MinimumScore float64
}

// DefaultRepairLoopConfig returns sane defaults (§16.5 example: 3 / 0.90).
func DefaultRepairLoopConfig() RepairLoopConfig {
	return RepairLoopConfig{MaximumIterations: 3, MinimumScore: 0.9}
}

// RepairFunc attempts to fix visual findings by re-running the coding agent
// with a targeted prompt. It returns an error only if the repair itself fails
// (infrastructure); the loop re-verifies regardless.
type RepairFunc func(ctx context.Context, findings []Finding, iteration int) error

// VerifyFunc re-captures and re-verifies the UI, returning a fresh [Result].
type VerifyFunc func(ctx context.Context) (Result, error)

// RepairOutcome is the final result of the repair loop (§16.5).
type RepairOutcome struct {
	// Resolved reports whether the final score met the threshold with no
	// blocker/major findings.
	Resolved bool
	// FinalResult is the last verification result.
	FinalResult Result
	// IterationsRun is the number of repair iterations executed.
	IterationsRun int
	// History records the verification result at each step (initial + after
	// each repair), for audit (§29.4).
	History []Result
}

// RunRepairLoop runs the §16.5 visual repair loop:
//
//	Screenshot → Visual findings → Targeted UI repair → Rebuild → New screenshot
//
// It stops when:
//   - The verification result is StatusPassed with score >= MinimumScore; or
//   - MaximumIterations is reached; or
//   - A repair/verify call returns a hard error.
//
// The loop NEVER retries indefinitely (rule §32). MaximumIterations must be > 0.
//
// AC-24: the loop NEVER claims "verified" without a passing verification. If
// the loop ends unresolved, FinalResult.Status reflects that (Failed or
// NotVerified), and the task layer MUST present it accordingly.
func RunRepairLoop(ctx context.Context, cfg RepairLoopConfig, initial Result, repair RepairFunc, verify VerifyFunc) (RepairOutcome, error) {
	if cfg.MaximumIterations <= 0 {
		panic("visual: MaximumIterations must be > 0 (rule §32: no infinite retry)")
	}
	out := RepairOutcome{FinalResult: initial, History: []Result{initial}}

	// Short-circuit: already passing.
	if initial.Status == StatusPassed && initial.Score >= cfg.MinimumScore {
		out.Resolved = true
		return out, nil
	}
	// Short-circuit: skipped/not-verified → no repair (the loop can't help if
	// verification is disabled or impossible). AC-24: never claim verified.
	if initial.Status == StatusSkipped || initial.Status == StatusNotVerified {
		return out, nil
	}

	actionable := filterActionable(initial.Findings)
	current := initial
	for iter := 1; iter <= cfg.MaximumIterations; iter++ {
		if len(actionable) == 0 && current.Score >= cfg.MinimumScore {
			out.Resolved = true
			out.IterationsRun = iter - 1
			return out, nil
		}
		if err := repair(ctx, actionable, iter); err != nil {
			out.IterationsRun = iter
			return out, fmt.Errorf("visual repair: iteration %d: %w", iter, err)
		}
		out.IterationsRun = iter
		verified, err := verify(ctx)
		if err != nil {
			return out, fmt.Errorf("visual repair: verify iteration %d: %w", iter, err)
		}
		out.FinalResult = verified
		out.History = append(out.History, verified)
		if verified.Status == StatusPassed && verified.Score >= cfg.MinimumScore {
			out.Resolved = true
			return out, nil
		}
		current = verified
		actionable = filterActionable(verified.Findings)
	}
	return out, nil
}
