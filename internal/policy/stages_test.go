package policy

import (
	"strings"
	"testing"
)

func TestStageStatus_LocalReview(t *testing.T) {
	t.Parallel()
	res, _ := Resolve(NewProject(ProfileLocalReview), TaskContext{})
	st := res.StageStatus()

	if st.IsSkipped(StageRunTests) {
		t.Error("LOCAL_REVIEW should have tests enabled")
	}
	if !st.IsSkipped(StagePush) {
		t.Error("LOCAL_REVIEW push should be skipped/locked")
	}
	pushStage := findStage(st, StagePush)
	if pushStage.Status != StageLocked {
		t.Errorf("LOCAL_REVIEW push should be locked, got %s", pushStage.Status)
	}
}

func TestStageStatus_Autonomous(t *testing.T) {
	t.Parallel()
	res, _ := Resolve(NewProject(ProfileAutonomous), TaskContext{})
	st := res.StageStatus()
	for _, stage := range []StageID{StageImplementation, StageTestGenerate, StageRunTests, StagePush, StageMerge} {
		if st.IsSkipped(stage) {
			t.Errorf("AUTONOMOUS %s should be active", stage)
		}
	}
}

func TestStageStatus_NoTestTask(t *testing.T) {
	t.Parallel()
	proj := NewProject(ProfileLocalReview)
	over := proj.Pipeline
	over.Tests.Generate = false
	over.Tests.RunExisting = false
	over.Tests.RunGenerated = false
	over.Review.AIReview = false
	over.Review.SecurityReview = TriOff
	over.Review.ArchitectureReview = TriOff

	res, _ := Resolve(proj, TaskContext{Override: &over})
	st := res.StageStatus()

	if !st.IsSkipped(StageTestGenerate) {
		t.Error("test generation should be skipped")
	}
	if !st.IsSkipped(StageRunTests) {
		t.Error("run tests should be skipped")
	}
	if !st.IsSkipped(StageAIReview) {
		t.Error("AI review should be skipped")
	}

	labels := st.LocalResultLabels()
	wantLabels := []string{"IMPLEMENTED", "NOT TESTED", "NOT REVIEWED", "LOCAL BRANCH ONLY"}
	for i, want := range wantLabels {
		if i >= len(labels) || labels[i] != want {
			t.Errorf("label[%d] = %q, want %q (all: %v)", i, labelOrEmpty(labels, i), want, labels)
		}
	}
}

func TestStageStatus_StringContainsSkippedStages(t *testing.T) {
	t.Parallel()
	proj := NewProject(ProfileLocalReview)
	over := proj.Pipeline
	over.Tests.Generate = false
	res, _ := Resolve(proj, TaskContext{Override: &over})
	st := res.StageStatus()
	s := st.String()
	if !strings.Contains(s, "test_generation") {
		t.Errorf("status string should mention test_generation:\n%s", s)
	}
	if !strings.Contains(s, "skipped") {
		t.Errorf("status string should show skipped:\n%s", s)
	}
}

func TestAllows_NewActions(t *testing.T) {
	t.Parallel()
	res, _ := Resolve(NewProject(ProfileLocalReview), TaskContext{})
	p := res.Pipeline

	// LOCAL_REVIEW defaults.
	if d := res.Allows(ActModifyExistingTests); !d.Allow {
		t.Errorf("modify existing should be allowed: %s", d.Reason)
	}
	_ = p

	// With generate off.
	proj := NewProject(ProfileLocalReview)
	over := proj.Pipeline
	over.Tests.Generate = false
	res2, _ := Resolve(proj, TaskContext{Override: &over})
	if d := res2.Allows(ActModifyExistingTests); d.Allow {
		t.Error("modify existing must be denied when generate is off")
	}
	if d := res2.Allows(ActRunGeneratedTests); d.Allow {
		t.Error("run generated must be denied when generate is off")
	}
}

func findStage(s PipelineStatus, id StageID) StageReport {
	for _, r := range s.Stages {
		if r.Stage == id {
			return r
		}
	}
	return StageReport{}
}

func labelOrEmpty(labels []string, i int) string {
	if i < len(labels) {
		return labels[i]
	}
	return ""
}
