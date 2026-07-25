package policy

import "fmt"

// StageID identifies a pipeline stage (§5).
type StageID string

const (
	StageSpecification  StageID = "specification"
	StagePlanning       StageID = "planning"
	StageDesign         StageID = "design"
	StageImplementation StageID = "implementation"
	StageTestGenerate   StageID = "test_generation"
	StageRunTests       StageID = "run_tests"
	StageAIReview       StageID = "ai_review"
	StageArchReview     StageID = "architecture_review"
	StageSecurityReview StageID = "security_review"
	StagePush           StageID = "push"
	StageChangeRequest  StageID = "change_request"
	StageMerge          StageID = "merge"
	StagePostMerge      StageID = "post_merge"
)

// StageStatus is the per-stage state in a resolved pipeline.
type StageStatus string

const (
	// StageActive: the stage is enabled and will run.
	StageActive StageStatus = "active"
	// StageSkipped: the stage is disabled by policy; it will not run.
	StageSkipped StageStatus = "skipped"
	// StageLocked: the stage is structurally unreachable (network-locked
	// profile, dependency forced off). It cannot be enabled by any override.
	StageLocked StageStatus = "locked"
)

// StageReport describes one stage's resolved status.
type StageReport struct {
	Stage   StageID
	Status  StageStatus
	Reason  string // why it is skipped/locked (empty when active)
	Actions []Action
}

// PipelineStatus is the full, human-readable stage-by-stage breakdown of a
// resolved pipeline (spec: "pipeline status явно показывает пропущенные стадии").
type PipelineStatus struct {
	Profile Profile
	Stages  []StageReport
}

// StageStatus computes the per-stage status for a resolved pipeline, making
// skipped and locked stages explicit. This is the canonical rendering source for
// CLI/TUI ("IMPLEMENTED / NOT TESTED / NOT REVIEWED" labels derive from which
// stages ran vs were skipped).
func (r Resolved) StageStatus() PipelineStatus {
	p := r.Pipeline
	networkLocked := r.Profile.IsNetworkLocked()

	stages := []StageReport{
		{Stage: StageSpecification, Status: stageBool(p.Specification), Actions: nil},
		{Stage: StagePlanning, Status: stageBool(p.Planning), Actions: nil},
		{Stage: StageDesign, Status: stageBool(p.Design.Generate || p.Design.VisualVerification),
			Actions: []Action{ActGenerateDesign}},
		{Stage: StageImplementation, Status: stageBool(p.Implementation), Actions: []Action{ActImplement}},
	}

	// Test stages.
	testGen := StageReport{Stage: StageTestGenerate, Actions: []Action{ActGenerateTests, ActModifyExistingTests}}
	if p.Tests.Generate {
		testGen.Status = StageActive
	} else {
		testGen.Status = StageSkipped
		testGen.Reason = "tests.generate=false (test paths forbidden, §24.2)"
	}
	stages = append(stages, testGen)

	runTests := StageReport{Stage: StageRunTests, Actions: []Action{ActRunExistingTests, ActRunGeneratedTests}}
	switch {
	case p.Tests.RunExisting && p.Tests.RunGenerated:
		runTests.Status = StageActive
	case p.Tests.RunExisting && !p.Tests.RunGenerated:
		runTests.Status = StageActive
		runTests.Reason = "run_generated disabled; only existing tests run"
	case !p.Tests.RunExisting && p.Tests.RunGenerated:
		runTests.Status = StageActive
		runTests.Reason = "run_existing disabled; only generated tests run"
	default:
		runTests.Status = StageSkipped
		runTests.Reason = "tests.run_existing=false and run_generated=false"
	}
	if !p.Tests.Generate {
		runTests.Status = StageSkipped
		runTests.Reason = "tests.generate=false: no tests run (§24.2)"
	}
	stages = append(stages, runTests)

	// Review stages.
	aiReview := StageReport{Stage: StageAIReview, Actions: []Action{ActAIReview}}
	if p.Review.AIReview {
		aiReview.Status = StageActive
	} else {
		aiReview.Status = StageSkipped
		aiReview.Reason = "review.ai_review=false"
	}
	stages = append(stages, aiReview)

	archReview := StageReport{Stage: StageArchReview, Actions: []Action{ActArchReview}}
	if triToBool(p.Review.ArchitectureReview) {
		archReview.Status = StageActive
		archReview.Reason = triLabel(p.Review.ArchitectureReview)
	} else {
		archReview.Status = StageSkipped
		archReview.Reason = "review.architecture_review=off"
	}
	stages = append(stages, archReview)

	secReview := StageReport{Stage: StageSecurityReview, Actions: []Action{ActSecurityReview}}
	if triToBool(p.Review.SecurityReview) {
		secReview.Status = StageActive
		secReview.Reason = triLabel(p.Review.SecurityReview)
	} else {
		secReview.Status = StageSkipped
		secReview.Reason = "review.security_review=off"
	}
	stages = append(stages, secReview)

	// Delivery stages — locked under a network-locked profile.
	push := StageReport{Stage: StagePush, Actions: []Action{ActPush}}
	if p.Git.Push {
		push.Status = StageActive
	} else if networkLocked {
		push.Status = StageLocked
		push.Reason = "network-locked profile (LOCAL_REVIEW/PLAN_ONLY)"
	} else {
		push.Status = StageSkipped
		push.Reason = "git.push=false"
	}
	stages = append(stages, push)

	cr := StageReport{Stage: StageChangeRequest, Actions: []Action{ActCreateChangeRequest, ActUpdateChangeRequest}}
	if p.ChangeRequest.Create {
		cr.Status = StageActive
	} else if networkLocked {
		cr.Status = StageLocked
		cr.Reason = "network-locked profile"
	} else {
		cr.Status = StageSkipped
		cr.Reason = "change_request.create=false"
	}
	stages = append(stages, cr)

	merge := StageReport{Stage: StageMerge, Actions: []Action{ActMerge}}
	if p.Merge {
		merge.Status = StageActive
	} else if networkLocked {
		merge.Status = StageLocked
		merge.Reason = "network-locked profile"
	} else {
		merge.Status = StageSkipped
		merge.Reason = "merge=false"
	}
	stages = append(stages, merge)

	post := StageReport{Stage: StagePostMerge, Actions: []Action{ActPostMerge, ActPostMergeAutoRevert}}
	if p.PostMerge.Enabled {
		post.Status = StageActive
	} else if networkLocked {
		post.Status = StageLocked
		post.Reason = "network-locked profile"
	} else {
		post.Status = StageSkipped
		post.Reason = "post_merge.enabled=false"
	}
	stages = append(stages, post)

	return PipelineStatus{Profile: r.Profile, Stages: stages}
}

// IsSkipped reports whether a stage is disabled (skipped or locked).
func (s PipelineStatus) IsSkipped(stage StageID) bool {
	for _, r := range s.Stages {
		if r.Stage == stage {
			return r.Status != StageActive
		}
	}
	return true
}

// LocalResultLabels renders the §24.4/§25.1 status labels for a local-only
// result based on which stages were skipped. This is the explicit "pipeline
// status shows skipped stages" surface.
func (s PipelineStatus) LocalResultLabels() []string {
	var labels []string
	labels = append(labels, "IMPLEMENTED")
	if s.IsSkipped(StageRunTests) {
		labels = append(labels, "NOT TESTED")
	} else {
		labels = append(labels, "TESTED")
	}
	if s.IsSkipped(StageAIReview) && s.IsSkipped(StageArchReview) && s.IsSkipped(StageSecurityReview) {
		labels = append(labels, "NOT REVIEWED")
	} else {
		labels = append(labels, "REVIEWED")
	}
	labels = append(labels, "LOCAL BRANCH ONLY")
	return labels
}

// String renders the full status report for logs/CLI.
func (s PipelineStatus) String() string {
	var b []byte
	b = append(b, fmt.Sprintf("Pipeline status [%s]:\n", s.Profile)...)
	for _, st := range s.Stages {
		extra := ""
		if st.Reason != "" {
			extra = " (" + st.Reason + ")"
		}
		b = append(b, fmt.Sprintf("  %-22s %s%s\n", st.Stage, st.Status, extra)...)
	}
	return string(b)
}

func stageBool(on bool) StageStatus {
	if on {
		return StageActive
	}
	return StageSkipped
}

func triLabel(t TriState) string {
	if t == TriAuto {
		return "auto"
	}
	return ""
}
