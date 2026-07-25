package evidence

import "testing"

func TestSet_IsComplete(t *testing.T) {
	t.Parallel()
	s := Set{}
	s.Add(Evidence{CriterionID: "AC-1", Type: EvidenceTest, Reference: "TestFoo", Status: "passed", Confidence: ConfidenceHigh, TestsWereRun: true})
	s.Add(Evidence{CriterionID: "AC-2", Type: EvidenceStatic, Reference: "scope-check", Status: "passed", Confidence: ConfidenceMedium})
	if !s.IsComplete() {
		t.Error("should be complete")
	}

	s.Add(Evidence{CriterionID: "AC-3", Type: EvidenceTest, Status: "pending"})
	if s.IsComplete() {
		t.Error("should not be complete with pending AC-3")
	}
}

func TestSet_AggregateConfidence_LowestWins(t *testing.T) {
	t.Parallel()
	s := Set{}
	s.Add(Evidence{CriterionID: "AC-1", Confidence: ConfidenceHigh})
	s.Add(Evidence{CriterionID: "AC-2", Confidence: ConfidenceLow})
	if s.AggregateConfidence() != ConfidenceLow {
		t.Errorf("aggregate = %s, want low", s.AggregateConfidence())
	}
}

func TestSet_EmptyIsNone(t *testing.T) {
	t.Parallel()
	s := Set{}
	if s.AggregateConfidence() != ConfidenceNone {
		t.Errorf("empty aggregate = %s, want none", s.AggregateConfidence())
	}
	if s.IsComplete() {
		t.Error("empty set should not be complete")
	}
}

func TestSet_LowerForDisabledTests(t *testing.T) {
	t.Parallel()
	s := Set{}
	s.Add(Evidence{CriterionID: "AC-1", Confidence: ConfidenceHigh, TestsWereRun: true, Status: "passed"})
	s.Add(Evidence{CriterionID: "AC-2", Confidence: ConfidenceHigh, TestsWereRun: false, Status: "passed"})
	s.Add(Evidence{CriterionID: "AC-3", Confidence: ConfidenceMedium, TestsWereRun: false, Status: "passed"})

	lowered := s.LowerForDisabledTests()
	// AC-1 keeps high (tests ran).
	if lowered.Items[0].Confidence != ConfidenceHigh {
		t.Error("AC-1 should keep high confidence (tests ran)")
	}
	// AC-2 downgraded to low.
	if lowered.Items[1].Confidence != ConfidenceLow {
		t.Error("AC-2 should be downgraded to low (no tests)")
	}
	// AC-3 stays medium (was already below high).
	if lowered.Items[2].Confidence != ConfidenceMedium {
		t.Error("AC-3 should keep medium")
	}
}

func TestSet_MissingCriteria(t *testing.T) {
	t.Parallel()
	s := Set{}
	s.Add(Evidence{CriterionID: "AC-1", Status: "passed"})
	s.Add(Evidence{CriterionID: "AC-2", Status: "failed"})
	s.Add(Evidence{CriterionID: "AC-3", Status: "passed"})
	missing := s.MissingCriteria()
	if len(missing) != 1 || missing[0] != "AC-2" {
		t.Errorf("missing = %v, want [AC-2]", missing)
	}
}

func TestSet_HasTestEvidence(t *testing.T) {
	t.Parallel()
	s := Set{}
	if s.HasTestEvidence() {
		t.Error("empty should have no test evidence")
	}
	s.Add(Evidence{CriterionID: "AC-1", Type: EvidenceReview, Status: "passed"})
	if s.HasTestEvidence() {
		t.Error("review-only should have no test evidence")
	}
	s.Add(Evidence{CriterionID: "AC-2", Type: EvidenceTest, Status: "passed"})
	if !s.HasTestEvidence() {
		t.Error("should have test evidence after adding a test")
	}
}
