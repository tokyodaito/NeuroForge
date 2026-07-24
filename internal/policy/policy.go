package policy

import "fmt"

// Security is the non-disableable project security policy (AC-29, §29).
type Security struct {
	// Mandatory checks that must remain on regardless of any task override.
	Mandatory MandatoryChecks
	// NetworkLocked structurally forbids every Git network operation
	// (push/change_request/merge/post_merge). Set from the profile
	// (LOCAL_REVIEW/PLAN_ONLY) — ADR-0008, AC-7.
	NetworkLocked bool
	// AllowVisualVerificationBypass permits auto-merging UI tasks without visual
	// verification (§5.1 exception). False by default: merging a UI task requires
	// visual verification.
	AllowVisualVerificationBypass bool
}

// MandatoryChecks are the review checks a project marks as non-disableable
// (rule §36.15/§36.16, AC-29).
type MandatoryChecks struct {
	AIReview           bool
	SecurityReview     bool
	ArchitectureReview bool
}

// Project is a project's compiled policy: its profile, baseline pipeline and
// security posture. Pipeline should be the profile defaults (possibly with
// project-level tightening); Resolve normalises it.
type Project struct {
	Profile  Profile
	Pipeline Pipeline
	Security Security
}

// TaskContext carries per-task constraints used during resolution.
type TaskContext struct {
	// Override, if non-nil, may only further restrict the project policy
	// (disable a stage or capability). It may never enable something the project
	// disables, nor disable a mandatory check (AC-29).
	Override *Pipeline
	// IsUI marks the task as a UI task (influences the visual-verification rule).
	IsUI bool
}

// Resolved is the final, enforceable policy for one project+task pair.
type Resolved struct {
	Profile  Profile
	Pipeline Pipeline
}

// Action is a capability gated by policy.
type Action string

const (
	ActPush                Action = "git.push"
	ActCreateChangeRequest Action = "change_request.create"
	ActUpdateChangeRequest Action = "change_request.update"
	ActMerge               Action = "merge"
	ActPostMerge           Action = "post_merge"
	ActPostMergeAutoRevert Action = "post_merge.auto_revert"
	ActImplement           Action = "implementation"
	ActGenerateTests       Action = "tests.generate"
	ActRunExistingTests    Action = "tests.run_existing"
	ActGenerateDesign      Action = "design.generate"
	ActAIReview            Action = "review.ai_review"
	ActLocalCheckpoint     Action = "git.local_checkpoint"
)

// Decision is the result of gating an [Action].
type Decision struct {
	Action Action
	Allow  bool
	Reason string
}

// String renders a decision for logs/audit.
func (d Decision) String() string {
	verb := "deny"
	if d.Allow {
		verb = "allow"
	}
	return fmt.Sprintf("%s %s: %s", verb, d.Action, d.Reason)
}

// NewProject builds a Project from a profile, applying profile defaults.
func NewProject(p Profile) Project {
	sec := Security{NetworkLocked: p.IsNetworkLocked()}
	return Project{
		Profile:  p,
		Pipeline: ProfileDefaults(p),
		Security: sec,
	}
}

// Resolve merges the project policy with a task context, applies the §5.1
// dependency rules, enforces the AC-29 invariant (an override cannot weaken
// security or enable a disabled capability), and applies the LOCAL_REVIEW
// structural network lock.
//
// It returns the resolved policy and any violations. Hard (block) violations
// mean the offending intent was refused and the safer project value was kept.
func Resolve(project Project, task TaskContext) (Resolved, []Violation) {
	var vs []Violation

	eff := project.Pipeline

	// 1. Merge task override with "only restrict" + "cannot disable mandatory".
	if task.Override != nil {
		eff, vs = mergeRestrict(eff, *task.Override, vs)
	}

	// 2. Enforce mandatory checks (AC-29) — kept on regardless of override.
	eff, vs = enforceMandatory(eff, project.Security, vs)

	// 3. Apply §5.1 dependency normalisation; surface the forced adjustments as
	// informational violations so callers/audit can see what was normalised.
	var adj []Adjustment
	eff, adj = Normalize(eff)
	for _, a := range adj {
		vs = append(vs, Violation{
			Rule:     "§5.1.normalize",
			Severity: SeverityInfo,
			Detail:   fmt.Sprintf("%s: %s -> %s (%s)", a.Field, a.From, a.To, a.Reason),
		})
	}

	// 4. Structural network lock (LOCAL_REVIEW / PLAN_ONLY).
	if project.Security.NetworkLocked || project.Profile.IsNetworkLocked() {
		eff, vs = applyNetworkLock(eff, vs)
	}

	// 5. Visual-verification requirement for auto-merged UI tasks (§5.1).
	if task.IsUI && eff.Merge && !eff.Design.VisualVerification &&
		!project.Security.AllowVisualVerificationBypass {
		vs = append(vs, Violation{
			Rule:     "§5.1.vv-required-for-ui-merge",
			Severity: SeverityBlock,
			Detail:   "auto-merging a UI task requires visual verification enabled (or an explicit project exemption)",
		})
	}

	return Resolved{Profile: project.Profile, Pipeline: eff}, vs
}

// mergeRestrict applies override onto base with only-restrict semantics: a bool
// capability stays on only if both base and override allow it. An override that
// tries to enable a capability the base disables is recorded as a violation and
// clamped.
func mergeRestrict(base, over Pipeline, vs []Violation) (Pipeline, []Violation) {
	type b struct {
		name             string
		baseVal, overVal bool
	}
	caps := []b{
		{"specification.enabled", base.Specification, over.Specification},
		{"planning.enabled", base.Planning, over.Planning},
		{"implementation.enabled", base.Implementation, over.Implementation},
		{"design.generate", base.Design.Generate, over.Design.Generate},
		{"design.human_selection", base.Design.HumanSelection, over.Design.HumanSelection},
		{"design.visual_verification", base.Design.VisualVerification, over.Design.VisualVerification},
		{"tests.generate", base.Tests.Generate, over.Tests.Generate},
		{"tests.run_existing", base.Tests.RunExisting, over.Tests.RunExisting},
		{"tests.run_generated", base.Tests.RunGenerated, over.Tests.RunGenerated},
		{"tests.required_for_completion", base.Tests.RequiredForCompletion, over.Tests.RequiredForCompletion},
		{"review.ai_review", base.Review.AIReview, over.Review.AIReview},
		{"git.local_checkpoint_commits", base.Git.LocalCheckpointCommits, over.Git.LocalCheckpointCommits},
		{"git.final_local_commit", base.Git.FinalLocalCommit, over.Git.FinalLocalCommit},
		{"git.push", base.Git.Push, over.Git.Push},
		{"change_request.create", base.ChangeRequest.Create, over.ChangeRequest.Create},
		{"change_request.update_existing", base.ChangeRequest.UpdateExisting, over.ChangeRequest.UpdateExisting},
		{"merge.enabled", base.Merge, over.Merge},
		{"post_merge.enabled", base.PostMerge.Enabled, over.PostMerge.Enabled},
		{"post_merge.auto_revert", base.PostMerge.AutoRevert, over.PostMerge.AutoRevert},
	}

	out := base
	set := func(name string, on bool) {
		switch name {
		case "specification.enabled":
			out.Specification = on
		case "planning.enabled":
			out.Planning = on
		case "implementation.enabled":
			out.Implementation = on
		case "design.generate":
			out.Design.Generate = on
		case "design.human_selection":
			out.Design.HumanSelection = on
		case "design.visual_verification":
			out.Design.VisualVerification = on
		case "tests.generate":
			out.Tests.Generate = on
		case "tests.run_existing":
			out.Tests.RunExisting = on
		case "tests.run_generated":
			out.Tests.RunGenerated = on
		case "tests.required_for_completion":
			out.Tests.RequiredForCompletion = on
		case "review.ai_review":
			out.Review.AIReview = on
		case "git.local_checkpoint_commits":
			out.Git.LocalCheckpointCommits = on
		case "git.final_local_commit":
			out.Git.FinalLocalCommit = on
		case "git.push":
			out.Git.Push = on
		case "change_request.create":
			out.ChangeRequest.Create = on
		case "change_request.update_existing":
			out.ChangeRequest.UpdateExisting = on
		case "merge.enabled":
			out.Merge = on
		case "post_merge.enabled":
			out.PostMerge.Enabled = on
		case "post_merge.auto_revert":
			out.PostMerge.AutoRevert = on
		}
	}

	for _, c := range caps {
		effective := c.baseVal && c.overVal
		set(c.name, effective)
		if c.overVal && !c.baseVal {
			vs = append(vs, Violation{
				Rule:     "ac29.override-clamp",
				Severity: SeverityWarn,
				Detail:   fmt.Sprintf("task override tried to enable %s which the project disables; kept disabled", c.name),
			})
		}
	}

	// TriState review switches: override may only further restrict (toward Off)
	// relative to the project; an override toward On where the project is Off is
	// clamped. TriAuto defers.
	out.Review.SecurityReview = mergeTri(base.Review.SecurityReview, over.Review.SecurityReview, "review.security_review", &vs)
	out.Review.ArchitectureReview = mergeTri(base.Review.ArchitectureReview, over.Review.ArchitectureReview, "review.architecture_review", &vs)

	return out, vs
}

// mergeTri models "only restrict": On > Auto > Off in restrictiveness. The
// effective value is the more restrictive of base/override. An override that is
// less restrictive than base is clamped and reported.
func mergeTri(base, over TriState, name string, vs *[]Violation) TriState {
	if over == TriAuto || over == base {
		return base
	}
	if moreRestrictive(over, base) {
		return over
	}
	// override is less restrictive than base → clamp to base.
	*vs = append(*vs, Violation{
		Rule:     "ac29.override-clamp",
		Severity: SeverityWarn,
		Detail:   fmt.Sprintf("task override tried to relax %s (%s -> %s); kept %s", name, base, over, base),
	})
	return base
}

// moreRestrictive reports whether a is more restrictive than b (Off > Auto > On).
func moreRestrictive(a, b TriState) bool {
	rank := func(t TriState) int {
		switch t {
		case TriOff:
			return 2
		case TriAuto:
			return 1
		default:
			return 0
		}
	}
	return rank(a) > rank(b)
}

// enforceMandatory forces non-disableable checks on (AC-29).
func enforceMandatory(p Pipeline, sec Security, vs []Violation) (Pipeline, []Violation) {
	out := p
	if sec.Mandatory.AIReview && !out.Review.AIReview {
		out.Review.AIReview = true
		vs = append(vs, Violation{
			Rule:     "ac29.mandatory.ai_review",
			Severity: SeverityWarn,
			Detail:   "task override cannot disable the mandatory AI review; restored",
		})
	}
	if sec.Mandatory.SecurityReview && out.Review.SecurityReview == TriOff {
		out.Review.SecurityReview = TriOn
		vs = append(vs, Violation{
			Rule:     "ac29.mandatory.security_review",
			Severity: SeverityWarn,
			Detail:   "task override cannot disable the mandatory security review; restored",
		})
	}
	if sec.Mandatory.ArchitectureReview && out.Review.ArchitectureReview == TriOff {
		out.Review.ArchitectureReview = TriOn
		vs = append(vs, Violation{
			Rule:     "ac29.mandatory.architecture_review",
			Severity: SeverityWarn,
			Detail:   "task override cannot disable the mandatory architecture review; restored",
		})
	}
	return out, vs
}

// applyNetworkLock structurally disables every Git network capability.
func applyNetworkLock(p Pipeline, vs []Violation) (Pipeline, []Violation) {
	out := p
	if p.Git.Push {
		vs = append(vs, Violation{
			Rule: "ac7.network-locked", Severity: SeverityBlock,
			Detail: "git.push is structurally disabled in a network-locked profile (LOCAL_REVIEW/PLAN_ONLY)",
		})
		out.Git.Push = false
	}
	if p.ChangeRequest.Create {
		vs = append(vs, Violation{
			Rule: "ac7.network-locked", Severity: SeverityBlock,
			Detail: "change_request.create is structurally disabled in a network-locked profile",
		})
		out.ChangeRequest.Create = false
	}
	if p.ChangeRequest.UpdateExisting {
		out.ChangeRequest.UpdateExisting = false
	}
	if p.Merge {
		vs = append(vs, Violation{
			Rule: "ac7.network-locked", Severity: SeverityBlock,
			Detail: "merge is structurally disabled in a network-locked profile",
		})
		out.Merge = false
	}
	if p.PostMerge.Enabled {
		out.PostMerge.Enabled = false
	}
	return out, vs
}

// Allows gates a single [Action] against the resolved policy.
func (r Resolved) Allows(a Action) Decision {
	p := r.Pipeline
	switch a {
	case ActPush:
		return Decision{a, p.Git.Push, "git.push=" + b(p.Git.Push)}
	case ActCreateChangeRequest:
		return Decision{a, p.Git.Push && p.ChangeRequest.Create, "git.push=" + b(p.Git.Push) + " and change_request.create=" + b(p.ChangeRequest.Create)}
	case ActUpdateChangeRequest:
		return Decision{a, p.Git.Push && p.ChangeRequest.UpdateExisting, "git.push=" + b(p.Git.Push) + " and change_request.update_existing=" + b(p.ChangeRequest.UpdateExisting)}
	case ActMerge:
		return Decision{a, p.Merge, "merge=" + b(p.Merge)}
	case ActPostMerge:
		return Decision{a, p.Merge && p.PostMerge.Enabled, "merge=" + b(p.Merge) + " and post_merge.enabled=" + b(p.PostMerge.Enabled)}
	case ActPostMergeAutoRevert:
		return Decision{a, p.Merge && p.PostMerge.Enabled && p.PostMerge.AutoRevert, "post_merge.auto_revert=" + b(p.PostMerge.AutoRevert)}
	case ActImplement:
		return Decision{a, p.Implementation, "implementation=" + b(p.Implementation)}
	case ActGenerateTests:
		return Decision{a, p.Tests.Generate, "tests.generate=" + b(p.Tests.Generate)}
	case ActRunExistingTests:
		return Decision{a, p.Tests.RunExisting, "tests.run_existing=" + b(p.Tests.RunExisting)}
	case ActGenerateDesign:
		return Decision{a, p.Design.Generate, "design.generate=" + b(p.Design.Generate)}
	case ActAIReview:
		return Decision{a, p.Review.AIReview, "review.ai_review=" + b(p.Review.AIReview)}
	case ActLocalCheckpoint:
		// Checkpoint commits are allowed even without push (§5.2).
		return Decision{a, p.Git.LocalCheckpointCommits, "git.local_checkpoint_commits=" + b(p.Git.LocalCheckpointCommits)}
	default:
		return Decision{a, false, "unknown action"}
	}
}

func b(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
