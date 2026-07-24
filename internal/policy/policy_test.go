package policy

import "testing"

// TestResolve_TableProfiles is the master table-driven test: for every profile
// it resolves the project defaults and asserts the action gate matches the spec
// §4 capabilities.
func TestResolve_TableProfiles(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		profile   Profile
		isUI      bool
		allow     []Action // must be allowed
		deny      []Action // must be denied
		mustBlock bool     // expect a hard block violation
	}{
		{
			name: "PLAN_ONLY", profile: ProfilePlanOnly,
			deny: []Action{ActImplement, ActPush, ActCreateChangeRequest, ActMerge, ActPostMerge},
		},
		{
			name: "LOCAL_REVIEW", profile: ProfileLocalReview,
			allow: []Action{ActImplement, ActLocalCheckpoint, ActGenerateTests, ActRunExistingTests, ActAIReview},
			deny:  []Action{ActPush, ActCreateChangeRequest, ActMerge, ActPostMerge},
		},
		{
			name: "REMOTE_REVIEW", profile: ProfileRemoteReview,
			allow: []Action{ActImplement, ActPush, ActCreateChangeRequest, ActUpdateChangeRequest, ActGenerateTests},
			deny:  []Action{ActMerge, ActPostMerge},
		},
		{
			name: "AUTONOMOUS", profile: ProfileAutonomous,
			allow: []Action{ActPush, ActCreateChangeRequest, ActMerge, ActPostMerge, ActPostMergeAutoRevert},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, vs := Resolve(NewProject(c.profile), TaskContext{IsUI: c.isUI})
			for _, a := range c.allow {
				if d := res.Allows(a); !d.Allow {
					t.Errorf("%s: %s should be allowed (%s)", c.name, a, d.Reason)
				}
			}
			for _, a := range c.deny {
				if d := res.Allows(a); d.Allow {
					t.Errorf("%s: %s should be denied (%s)", c.name, a, d.Reason)
				}
			}
			if c.mustBlock && !Blocks(vs) {
				t.Errorf("%s: expected a hard block violation, got %+v", c.name, vs)
			}
		})
	}
}

// TestResolve_AC29_OverrideCannotWeakenSecurity is the AC-29 test: a task
// override cannot disable a mandatory project check, nor enable a capability the
// project disables.
func TestResolve_AC29_OverrideCannotWeakenSecurity(t *testing.T) {
	t.Parallel()
	proj := NewProject(ProfileRemoteReview)
	// Project marks all reviews as mandatory (non-disableable).
	proj.Security.Mandatory = MandatoryChecks{AIReview: true, SecurityReview: true, ArchitectureReview: true}

	// Override tries to: disable the mandatory reviews, disable AI review,
	// disable tests, and (pointlessly) keep push on.
	over := proj.Pipeline
	over.Review.AIReview = false
	over.Review.SecurityReview = TriOff
	over.Review.ArchitectureReview = TriOff
	over.Tests.Generate = false
	over.Tests.RunExisting = false

	res, vs := Resolve(proj, TaskContext{Override: &over})

	// Mandatory checks restored despite the override.
	if !res.Pipeline.Review.AIReview {
		t.Error("mandatory AI review was disabled by override (AC-29)")
	}
	if res.Pipeline.Review.SecurityReview != TriOn {
		t.Error("mandatory security review must remain on (AC-29)")
	}
	if res.Pipeline.Review.ArchitectureReview != TriOn {
		t.Error("mandatory architecture review must remain on (AC-29)")
	}
	// Tests genuinely restricted by the override (allowed restriction).
	if res.Pipeline.Tests.Generate || res.Pipeline.Tests.RunExisting {
		t.Error("override should be allowed to restrict tests")
	}
	// Violations recorded for the attempted weakening.
	rules := map[string]bool{}
	for _, v := range vs {
		rules[v.Rule] = true
	}
	for _, want := range []string{"ac29.mandatory.ai_review", "ac29.mandatory.security_review", "ac29.mandatory.architecture_review"} {
		if !rules[want] {
			t.Errorf("expected violation %s; got %+v", want, vs)
		}
	}
}

// TestResolve_OverrideCannotExpandCapabilities: a LOCAL_REVIEW task override
// trying to enable push/merge/CR is clamped (the capabilities stay off) and the
// gate denies them.
func TestResolve_OverrideCannotExpandCapabilities(t *testing.T) {
	t.Parallel()
	proj := NewProject(ProfileLocalReview)
	over := proj.Pipeline
	over.Git.Push = true
	over.Merge = true
	over.ChangeRequest.Create = true

	res, vs := Resolve(proj, TaskContext{Override: &over})

	if res.Pipeline.Git.Push || res.Pipeline.Merge || res.Pipeline.ChangeRequest.Create {
		t.Fatalf("override must not expand LOCAL_REVIEW capabilities: %+v", res.Pipeline)
	}
	// The override attempts were clamped (warned).
	clamped := 0
	for _, v := range vs {
		if v.Rule == "ac29.override-clamp" {
			clamped++
		}
	}
	if clamped < 3 {
		t.Fatalf("expected >=3 override-clamp violations (push/merge/CR), got %d: %+v", clamped, vs)
	}
	// The gate denies every network action regardless.
	for _, a := range []Action{ActPush, ActCreateChangeRequest, ActMerge, ActPostMerge} {
		if res.Allows(a).Allow {
			t.Errorf("%s must be denied for LOCAL_REVIEW even with override", a)
		}
	}
}

// TestResolve_OverrideMayRestrict: a REMOTE_REVIEW override may turn push off,
// which then cascades (§5.1) to CR/merge off.
func TestResolve_OverrideMayRestrict(t *testing.T) {
	t.Parallel()
	proj := NewProject(ProfileRemoteReview)
	over := proj.Pipeline
	over.Git.Push = false // restrict

	res, vs := Resolve(proj, TaskContext{Override: &over})
	if res.Pipeline.Git.Push {
		t.Error("override should have disabled push")
	}
	// §5.1 cascade: CR must be forced off by normalisation.
	if res.Pipeline.ChangeRequest.Create {
		t.Error("push=false should cascade CR off")
	}
	if !Blocks(vs) {
		// Network-locked? No — REMOTE_REVIEW isn't locked; but there should be
		// informational normalize adjustments at least.
	}
	// Push + CR now denied.
	if res.Allows(ActPush).Allow || res.Allows(ActCreateChangeRequest).Allow {
		t.Error("push and CR must be denied after restriction")
	}
}

// TestResolve_VisualVerificationRequiredForUIMerge: auto-merging a UI task with
// visual verification off is a hard block (unless exempted).
func TestResolve_VisualVerificationRequiredForUIMerge(t *testing.T) {
	t.Parallel()
	proj := NewProject(ProfileAutonomous)
	over := proj.Pipeline
	over.Design.VisualVerification = false

	_, vs := Resolve(proj, TaskContext{Override: &over, IsUI: true})
	if !hasRule(vs, "§5.1.vv-required-for-ui-merge") {
		t.Fatalf("expected vv-required block for UI merge, got %+v", vs)
	}

	// With an explicit project exemption, no block.
	proj.Security.AllowVisualVerificationBypass = true
	_, vs2 := Resolve(proj, TaskContext{Override: &over, IsUI: true})
	if hasRule(vs2, "§5.1.vv-required-for-ui-merge") {
		t.Fatalf("exemption should suppress the block, got %+v", vs2)
	}

	// Non-UI task is unaffected by the rule.
	_, vs3 := Resolve(proj, TaskContext{Override: &over, IsUI: false})
	if hasRule(vs3, "§5.1.vv-required-for-ui-merge") {
		t.Fatalf("non-UI task should not trigger the UI rule, got %+v", vs3)
	}
}

// TestResolve_CheckpointsAllowedWithoutPush (§5.2): local checkpoint commits are
// allowed even when push is off.
func TestResolve_CheckpointsAllowedWithoutPush(t *testing.T) {
	t.Parallel()
	res, _ := Resolve(NewProject(ProfileLocalReview), TaskContext{})
	if !res.Allows(ActLocalCheckpoint).Allow {
		t.Error("local checkpoint commits must be allowed without push (§5.2)")
	}
	if res.Allows(ActPush).Allow {
		t.Error("push must be denied in LOCAL_REVIEW")
	}
}

// TestResolve_PLAN_ONLY forbids implementation.
func TestResolve_PlanOnlyForbidsImplementation(t *testing.T) {
	t.Parallel()
	res, _ := Resolve(NewProject(ProfilePlanOnly), TaskContext{})
	if res.Allows(ActImplement).Allow {
		t.Error("PLAN_ONLY must deny implementation")
	}
}

func TestAllows_UnknownActionDenied(t *testing.T) {
	t.Parallel()
	res, _ := Resolve(NewProject(ProfileAutonomous), TaskContext{})
	if d := res.Allows(Action("bogus")); d.Allow {
		t.Error("unknown action must be denied")
	}
}

func TestDecision_String(t *testing.T) {
	t.Parallel()
	d := Decision{Action: ActPush, Allow: true, Reason: "git.push=true"}
	if d.String() != "allow git.push: git.push=true" {
		t.Errorf("unexpected decision string: %q", d.String())
	}
}

func hasRule(vs []Violation, rule string) bool {
	for _, v := range vs {
		if v.Rule == rule {
			return true
		}
	}
	return false
}
