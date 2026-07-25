// Package review implements the AI review engine (spec §25).
//
// STATUS: implemented for milestone M8.
//
// Scope:
//   - Three independent review roles (§25): correctness/AI-review, architecture,
//     security. Each is independently toggleable (AC-13 + the task's explicit
//     list). A task override cannot disable a mandatory project review (AC-29 —
//     enforced by [policy.Resolve]).
//   - The [Finding] model (severity, role, location, remediation). Findings are
//     consumed by the Merge Governor (§28: blocker/major counts) and the repair
//     loop (§25/§16.5).
//   - A deterministic [FakeReviewer] that produces scripted findings without any
//     real AI calls (rule §36.5).
//   - The "review disabled" path (§25.1): when all reviews are off, the result is
//     labelled NOT AI-REVIEWED and may still be a valid local result.
//
// Boundaries: consumes [policy] for toggle decisions. Does not itself call
// coding agents, perform Git, or hold credentials.
package review

import (
	"context"
	"fmt"

	"neuroforge/internal/policy"
)

// Role is a review dimension (spec §25.roles).
type Role string

const (
	RoleCorrectness  Role = "correctness"
	RoleArchitecture Role = "architecture"
	RoleSecurity     Role = "security"
)

// Severity of a finding (maps to the §28 merge-governor gates).
type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityMinor   Severity = "minor"
	SeverityMajor   Severity = "major"
	SeverityBlocker Severity = "blocker"
)

// Finding is one review issue. The Merge Governor counts blocker + major
// findings (§28: blocker_findings==0 and major_findings==0 gates).
type Finding struct {
	Role        Role
	Severity    Severity
	Title       string
	Description string
	File        string
	Line        int
	Remediation string
}

// IsBlocker reports whether this finding blocks delivery.
func (f Finding) IsBlocker() bool {
	return f.Severity == SeverityBlocker
}

// IsMajorOrWorse reports whether this finding is major or blocker.
func (f Finding) IsMajorOrWorse() bool {
	return f.Severity == SeverityMajor || f.Severity == SeverityBlocker
}

// ReviewRequest describes what the reviewer examines.
type ReviewRequest struct {
	// Diff is the patch/changes under review.
	Diff string
	// ChangedFiles lists the changed paths.
	ChangedFiles []string
	// Context is optional supplementary material (spec, architecture notes).
	Context string
}

// Reviewer examines a change for one review role.
type Reviewer interface {
	// Review performs the review and returns findings (possibly empty). An error
	// means the review itself could not complete (infrastructure failure).
	Review(ctx context.Context, role Role, req ReviewRequest) ([]Finding, error)
}

// Result is the aggregate outcome of a multi-role review pass.
type Result struct {
	// Findings by role.
	Findings []Finding
	// Which roles ran.
	RolesRun []Role
	// Which roles were skipped (disabled by policy).
	RolesSkipped []Role
}

// Status is the aggregate review verdict.
type Status string

const (
	StatusApproved         Status = "approved"
	StatusChangesRequested Status = "changes_requested"
	StatusSkipped          Status = "skipped"
)

// OverallStatus computes the aggregate verdict from the findings.
func (r Result) OverallStatus() Status {
	if len(r.RolesRun) == 0 {
		return StatusSkipped
	}
	for _, f := range r.Findings {
		if f.IsMajorOrWorse() {
			return StatusChangesRequested
		}
	}
	return StatusApproved
}

// BlockerCount returns the number of blocker findings (§28 gate).
func (r Result) BlockerCount() int {
	n := 0
	for _, f := range r.Findings {
		if f.IsBlocker() {
			n++
		}
	}
	return n
}

// MajorCount returns the number of major (non-blocker) findings (§28 gate).
func (r Result) MajorCount() int {
	n := 0
	for _, f := range r.Findings {
		if f.Severity == SeverityMajor {
			n++
		}
	}
	return n
}

// HasFindings reports whether any role produced findings.
func (r Result) HasFindings() bool {
	return len(r.Findings) > 0
}

// Engine runs the configured review roles according to the resolved policy. It
// is the M8-4 "review engine" — deterministic orchestration that never uses an
// LLM for the gate decision (rule §22.6/§36.6). The individual [Reviewer]
// implementations may be AI-backed in production, but the gate is always code.
type Engine struct {
	reviewer Reviewer
}

// Options configures an Engine.
type Options struct {
	Reviewer Reviewer
}

// New creates an Engine. If no reviewer is provided, a [FakeReviewer] with an
// approve-by-default script is used.
func New(opts Options) *Engine {
	r := opts.Reviewer
	if r == nil {
		r = NewFakeReviewer(FakeScript{})
	}
	return &Engine{reviewer: r}
}

// RunInput carries the context for a review pass.
type RunInput struct {
	Policy policy.Resolved
	Req    ReviewRequest
}

// Run executes the enabled review roles, honouring the §25 toggles and the
// AC-29 mandatory enforcement (already applied by policy.Resolve). Disabled
// roles are recorded as skipped, not run.
func (e *Engine) Run(ctx context.Context, in RunInput) (Result, error) {
	p := in.Policy.Pipeline
	var res Result

	roles := []struct {
		role Role
		on   bool
	}{
		{RoleCorrectness, p.Review.AIReview},
		{RoleArchitecture, triEnabled(p.Review.ArchitectureReview)},
		{RoleSecurity, triEnabled(p.Review.SecurityReview)},
	}

	for _, r := range roles {
		if !r.on {
			res.RolesSkipped = append(res.RolesSkipped, r.role)
			continue
		}
		findings, err := e.reviewer.Review(ctx, r.role, in.Req)
		if err != nil {
			return res, fmt.Errorf("review: role %s: %w", r.role, err)
		}
		res.RolesRun = append(res.RolesRun, r.role)
		res.Findings = append(res.Findings, findings...)
	}
	return res, nil
}

// IsReviewed reports whether any review role ran.
func (r Result) IsReviewed() bool {
	return len(r.RolesRun) > 0
}

// Label returns the §25.1 status label for a local result.
func (r Result) Label() string {
	if r.IsReviewed() {
		return "REVIEWED"
	}
	return "NOT AI-REVIEWED"
}

// triEnabled resolves a TriState the same way the policy gate does: On and Auto
// mean "enabled"; only Off disables.
func triEnabled(t policy.TriState) bool {
	return t != policy.TriOff
}
