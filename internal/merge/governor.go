// Package merge implements the deterministic Merge Governor.
//
// STATUS: implemented for milestone M8 (ADR-0009).
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §28): a deterministic decision engine that
// checks specification lock, scope validity, required checks, acceptance
// evidence, findings, target policy, branch currency, budget and visual policy
// before emitting one of ALLOW_LOCAL_RESULT/ALLOW_PUSH/ALLOW_CHANGE_REQUEST/
// ALLOW_MERGE/REQUIRE_REBASE/REQUIRE_REPAIR/POLICY_BLOCKED/QUARANTINE.
//
// The Governor is pure code — it never calls an LLM (rule §22.6/§36.6) and never
// weakens non-disableable project security policy (AC-29). A task override that
// disables tests cannot bypass the mandatory merge rules (§24.5).
//
// Boundaries: the Governor only AUTHORISES; the actual delivery is performed by
// adapter/vcs (M11), which is the only holder of merge credentials and is
// unreachable without an ALLOW_* decision. This package does not perform Git
// network operations or hold credentials.
package merge

import (
	"fmt"

	"neuroforge/internal/evidence"
	"neuroforge/internal/policy"
	"neuroforge/internal/review"
	"neuroforge/internal/testengine"
)

// Decision is the Governor's verdict (spec §28).
type Decision string

const (
	DecisionAllowLocalResult   Decision = "ALLOW_LOCAL_RESULT"
	DecisionAllowPush          Decision = "ALLOW_PUSH"
	DecisionAllowChangeRequest Decision = "ALLOW_CHANGE_REQUEST"
	DecisionAllowMerge         Decision = "ALLOW_MERGE"
	DecisionRequireRebase      Decision = "REQUIRE_REBASE"
	DecisionRequireRepair      Decision = "REQUIRE_REPAIR"
	DecisionPolicyBlocked      Decision = "POLICY_BLOCKED"
	DecisionQuarantine         Decision = "QUARANTINE"
)

// String returns the decision label.
func (d Decision) String() string { return string(d) }

// IsAllow reports whether the decision permits a delivery action.
func (d Decision) IsAllow() bool {
	switch d {
	case DecisionAllowLocalResult, DecisionAllowPush,
		DecisionAllowChangeRequest, DecisionAllowMerge:
		return true
	}
	return false
}

// Input carries all the §28 gates the Governor checks. Every field maps to a
// spec §28 line.
type Input struct {
	// Resolved policy for the project+task (AC-29 enforcement already applied).
	Policy policy.Resolved

	// --- §28 gates ---
	SpecificationLocked        bool
	ScopeValid                 bool
	RequiredChecksPassed       bool
	AcceptanceEvidenceComplete bool
	BlockerFindings            int
	MajorFindings              int
	TargetAllowed              bool
	BranchCurrent              bool
	BudgetPolicySatisfied      bool
	VisualPolicySatisfied      bool

	// --- supporting evidence (§27) ---
	Evidence evidence.Set

	// --- test + review outcomes ---
	TestSummary  testengine.Summary
	ReviewResult review.Result
}

// Gate is one individual check and its pass/fail state.
type Gate struct {
	Name   string
	Passed bool
	Detail string
}

// Result is the full Governor decision with the per-gate breakdown.
type Result struct {
	Decision Decision
	Gates    []Gate
	// Reason explains the decision.
	Reason string
}

// Decide is the deterministic Merge Governor decision function (spec §28,
// ADR-0009). It is pure: same inputs always yield the same decision.
//
// The decision logic:
//  1. Build the gate list from the §28 inputs + policy enforcement.
//  2. If any hard gate fails, the decision is POLICY_BLOCKED (or REQUIRE_REPAIR
//     for findings, REQUIRE_REBASE for branch staleness).
//  3. If all gates pass, the decision is the highest delivery action permitted by
//     the resolved policy (merge > CR > push > local result).
//  4. Mandatory project checks (§24.5, AC-29) are enforced regardless of any task
//     override — the Governor re-checks them against the project's mandatory
//     policy, not just the resolved (possibly-overridden) pipeline.
func Decide(in Input) Result {
	gates := buildGates(in)

	failedGates := []Gate{}
	for _, g := range gates {
		if !g.Passed {
			failedGates = append(failedGates, g)
		}
	}

	// Branch not current → REQUIRE_REBASE (before other failures).
	for _, g := range failedGates {
		if g.Name == "branch_current" {
			return Result{Decision: DecisionRequireRebase, Gates: gates, Reason: "branch is not current; rebase required"}
		}
	}

	// Blocker/major findings → REQUIRE_REPAIR.
	for _, g := range failedGates {
		if g.Name == "blocker_findings" || g.Name == "major_findings" || g.Name == "tests_failed_repair" {
			return Result{Decision: DecisionRequireRepair, Gates: gates,
				Reason: "findings or test failures require repair before delivery"}
		}
	}

	// Mandatory check failures (tests disabled for merge, mandatory review) →
	// POLICY_BLOCKED. This is the §24.5 enforcement: a task override disabling
	// tests cannot bypass the mandatory merge rule.
	for _, g := range failedGates {
		if g.Name == "tests_disabled_blocked_merge" {
			return Result{Decision: DecisionPolicyBlocked, Gates: gates,
				Reason: "tests were disabled and are mandatory for merge (§24.5): task override cannot bypass this"}
		}
		if g.Name == "tests_required_for_merge" {
			return Result{Decision: DecisionPolicyBlocked, Gates: gates,
				Reason: "tests are mandatory for merge (§24.5): task override cannot bypass this"}
		}
		if g.Name == "mandatory_review" {
			return Result{Decision: DecisionPolicyBlocked, Gates: gates,
				Reason: "a mandatory project review did not pass (AC-29): task override cannot disable it"}
		}
	}

	// Any other failed hard gate → POLICY_BLOCKED.
	if len(failedGates) > 0 {
		var details []string
		for _, g := range failedGates {
			details = append(details, g.Name)
		}
		return Result{Decision: DecisionPolicyBlocked, Gates: gates,
			Reason: fmt.Sprintf("blocked by gates: %v", details)}
	}

	// All gates passed. Return the highest delivery action permitted by policy.
	p := in.Policy.Pipeline
	switch {
	case p.Merge:
		return Result{Decision: DecisionAllowMerge, Gates: gates, Reason: "all gates passed; merge permitted"}
	case p.ChangeRequest.Create && p.Git.Push:
		return Result{Decision: DecisionAllowChangeRequest, Gates: gates, Reason: "all gates passed; PR/MR permitted"}
	case p.Git.Push:
		return Result{Decision: DecisionAllowPush, Gates: gates, Reason: "all gates passed; push permitted"}
	default:
		return Result{Decision: DecisionAllowLocalResult, Gates: gates, Reason: "all gates passed; local result permitted"}
	}
}

// buildGates constructs the §28 gate list. Each gate is evaluated against the
// input and the resolved policy. The mandatory-checks gate (§24.5, AC-29)
// consults the project's mandatory policy independently of any task override.
func buildGates(in Input) []Gate {
	var gates []Gate

	gates = append(gates,
		Gate{Name: "specification_locked", Passed: in.SpecificationLocked,
			Detail: "specification must be locked before delivery"},
		Gate{Name: "scope_valid", Passed: in.ScopeValid,
			Detail: "all changes must be within the allowed scope"},
		Gate{Name: "required_checks_passed", Passed: in.RequiredChecksPassed,
			Detail: "required CI/build checks must pass"},
		Gate{Name: "acceptance_evidence_complete", Passed: in.AcceptanceEvidenceComplete && in.Evidence.IsComplete(),
			Detail: "every acceptance criterion must have passing evidence (§27)"},
		Gate{Name: "blocker_findings", Passed: in.BlockerFindings == 0,
			Detail: fmt.Sprintf("%d blocker findings (must be 0)", in.BlockerFindings)},
		Gate{Name: "major_findings", Passed: in.MajorFindings == 0,
			Detail: fmt.Sprintf("%d major findings (must be 0)", in.MajorFindings)},
		Gate{Name: "target_allowed", Passed: in.TargetAllowed,
			Detail: "the merge target must be an allowed branch"},
		Gate{Name: "branch_current", Passed: in.BranchCurrent,
			Detail: "the branch must be current with the target (no rebase needed)"},
		Gate{Name: "budget_policy_satisfied", Passed: in.BudgetPolicySatisfied,
			Detail: "budget policy must be satisfied"},
		Gate{Name: "visual_policy_satisfied", Passed: in.VisualPolicySatisfied,
			Detail: "visual verification policy must be satisfied for UI tasks"},
	)

	// §24.5: tests required for merge. This gate consults the
	// RequireForRemoteMerge setting — which the policy normalisation keeps
	// independent of the test-run toggles. A task override that disables test
	// GENERATION or RUNNING cannot bypass this gate (§24.5: "Отключение тестов в
	// task override не должно автоматически обходить обязательные merge rules").
	//
	// Two failure modes:
	//   - tests ran but FAILED → the code needs fixing (handled as REQUIRE_REPAIR
	//     via the test-failure gate below).
	//   - tests were NOT RUN at all (disabled) → POLICY_BLOCKED (§24.5: override
	//     cannot bypass the mandatory requirement).
	if in.Policy.Pipeline.Merge && in.Policy.Pipeline.Tests.RequireForRemoteMerge {
		testsRan := in.TestSummary.TestsWereRun
		testsPassed := testsRan && in.TestSummary.OverallStatus() == testengine.StatusPassed
		gates = append(gates, Gate{
			Name:   "tests_required_for_merge",
			Passed: testsPassed,
			Detail: "tests are mandatory for merge (§24.5); disabling tests via override does not bypass this",
		})
		// Distinguish "tests disabled" (policy block) from "tests failed" (repair).
		if !testsRan {
			gates = append(gates, Gate{
				Name:   "tests_disabled_blocked_merge",
				Passed: false,
				Detail: "tests were not run; a task override disabling tests cannot bypass mandatory merge rules (§24.5/AC-29)",
			})
		} else if !testsPassed {
			gates = append(gates, Gate{
				Name:   "tests_failed_repair",
				Passed: false,
				Detail: "tests ran but failed; repair required before merge",
			})
		}
	}

	// AC-29 / §25.2: a mandatory review producing a blocker is an explicit
	// policy block (redundant with blocker_findings but labelled for AC-29
	// audit clarity).
	if in.Policy.Pipeline.Merge {
		for _, f := range in.ReviewResult.Findings {
			if f.IsBlocker() {
				gates = append(gates, Gate{
					Name:   "mandatory_review",
					Passed: false,
					Detail: "a mandatory review produced a blocker finding (AC-29)",
				})
				break
			}
		}
	}

	return gates
}

// LocalResultLabels renders the §24.4/§25.1 labels for a local-only result. This
// is the explicit status surface ("IMPLEMENTED / NOT TESTED / NOT REVIEWED") that
// depends on whether tests and reviews ran.
func LocalResultLabels(in Input) []string {
	labels := []string{"IMPLEMENTED"}

	// Tests.
	if in.TestSummary.TestsWereRun && in.TestSummary.OverallStatus() == testengine.StatusPassed {
		labels = append(labels, "TESTED")
	} else {
		labels = append(labels, "NOT TESTED")
	}

	// Reviews.
	if in.ReviewResult.IsReviewed() && in.ReviewResult.OverallStatus() == review.StatusApproved {
		labels = append(labels, "REVIEWED")
	} else {
		labels = append(labels, "NOT REVIEWED")
	}

	labels = append(labels, "LOCAL BRANCH ONLY")
	return labels
}
