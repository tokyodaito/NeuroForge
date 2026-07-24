package router

import "strings"

// ComplexitySignals is the structured input to the deterministic complexity
// classifier (spec §18.2 economic cascade). The classifier never calls an LLM
// (rule §22.6): callers extract the numeric and lexical signals first.
type ComplexitySignals struct {
	Description string

	// EstimatedFileCount — how many files the task is expected to touch.
	EstimatedFileCount int
	// EstimatedTurns — agent turns the task is expected to need.
	EstimatedTurns int
	// ContextTokens — size of the context the model must hold.
	ContextTokens int

	// Role hints (e.g. "docs", "refactor", "feature", "arch", "migration").
	Role string

	// CrossPackageChange touches multiple packages/modules at once.
	CrossPackageChange bool
	// ArchitecturalDecision signals an architecture-level decision (§19.4).
	ArchitecturalDecision bool
	// ConflictingCheapResults signals two cheap agents disagreed (§19.4).
	ConflictingCheapResults bool
}

// ComplexityResult is the classification outcome.
type ComplexityResult struct {
	Complexity Complexity
	Reasons    []string
}

// ClassifyComplexity maps signals onto the §18.2/§19.3 bands deterministically.
// The economic cascade (§18.2) is: deterministic parsing -> cheap classifier ->
// standard model at low confidence -> heavy model only for complex tasks.
//
// Scoring is additive: each signal contributes points; bands are thresholds.
// The cheapest applicable band wins ties (so a simple task gets a cheap route,
// AC-16) but structural escalators (architecture, conflicting cheap results,
// cross-package) push toward C3/C4 (AC-17).
func ClassifyComplexity(s ComplexitySignals) ComplexityResult {
	score := 0
	var reasons []string

	// Mechanical parsing floor.
	low := strings.ToLower(s.Description)
	if containsAny(low, "typo", "rename", "docs", "changelog", "comment", "format", "lint rule") {
		score += 0
		reasons = append(reasons, "mechanical/docs change")
	}

	// File-count sizing.
	switch {
	case s.EstimatedFileCount >= 20:
		score += 3
		reasons = append(reasons, "touches many files (>=20)")
	case s.EstimatedFileCount >= 8:
		score += 2
		reasons = append(reasons, "touches several files (>=8)")
	case s.EstimatedFileCount >= 3:
		score += 1
		reasons = append(reasons, "touches a few files (>=3)")
	case s.EstimatedFileCount >= 1:
		score += 0
	}

	// Turn sizing (spec §22.7 turn limits hint complexity).
	switch {
	case s.EstimatedTurns >= 28:
		score += 3
		reasons = append(reasons, "high turn estimate (>=28)")
	case s.EstimatedTurns >= 16:
		score += 2
		reasons = append(reasons, "moderate turn estimate (>=16)")
	case s.EstimatedTurns >= 8:
		score += 1
		reasons = append(reasons, "modest turn estimate (>=8)")
	}

	// Context sizing.
	switch {
	case s.ContextTokens >= 200_000:
		score += 2
		reasons = append(reasons, "large context (>=200k tokens)")
	case s.ContextTokens >= 50_000:
		score += 1
		reasons = append(reasons, "medium context (>=50k tokens)")
	}

	// Role / keyword hints.
	if containsAny(low, "refactor", "feature", "implement") {
		score += 1
		reasons = append(reasons, "role: implementation/refactor")
	}
	if containsAny(low, "migration", "migrate", "schema") {
		score += 2
		reasons = append(reasons, "role: migration/schema")
	}
	if containsAny(low, "architect", "design system", "rewrite") {
		score += 2
		reasons = append(reasons, "role: architecture")
	}

	// Structural escalators (§19.4 escalation triggers).
	if s.CrossPackageChange {
		score += 2
		reasons = append(reasons, "cross-package change")
	}
	if s.ArchitecturalDecision {
		score += 3
		reasons = append(reasons, "architectural decision required")
	}
	if s.ConflictingCheapResults {
		score += 2
		reasons = append(reasons, "cheap agents disagreed")
	}

	band := scoreToBand(score)
	if len(reasons) == 0 {
		reasons = []string{"no complexity signals; default to standard"}
	}
	return ComplexityResult{Complexity: band, Reasons: reasons}
}

func scoreToBand(score int) Complexity {
	switch {
	case score >= 8:
		return C4
	case score >= 5:
		return C3
	case score >= 3:
		return C2
	case score >= 1:
		return C1
	default:
		return C0
	}
}

// Escalate returns a strictly more complex band (capped at C4). Used by the
// supervisor when escalation triggers fire (§19.4): planner not confident,
// scope exceeded forecast, two repairs failed, etc.
func Escalate(c Complexity) Complexity {
	if c < C4 {
		return c + 1
	}
	return C4
}

// Deescalate returns a strictly cheaper band (floored at C0). Used after heavy
// planning so a cheaper model can do mechanical implementation (§19.5).
func Deescalate(c Complexity) Complexity {
	if c > C0 {
		return c - 1
	}
	return C0
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
