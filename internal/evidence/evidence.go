// Package evidence implements Verification Evidence linking (spec §27).
//
// STATUS: implemented for milestone M8.
//
// Scope:
//   - Each acceptance criterion is linked to one or more pieces of evidence
//     (§27): a test reference, a visual report, a static scope-check, etc.
//   - When tests are disabled, alternative evidence is allowed but the system
//     lowers the evidence confidence (§27: "при отключённых тестах допустимы
//     другие evidence, но система должна снижать confidence").
//   - The Merge Governor consumes evidence completeness (§28:
//     acceptance_evidence_complete gate).
//
// Boundaries: pure domain model. Does not run tests, call agents, or touch Git.
package evidence

import (
	"fmt"
	"sort"
)

// EvidenceType is the kind of verification evidence (spec §27).
type EvidenceType string

const (
	EvidenceTest   EvidenceType = "test"
	EvidenceVisual EvidenceType = "visual"
	EvidenceStatic EvidenceType = "static"
	EvidenceManual EvidenceType = "manual"
	EvidenceReview EvidenceType = "review"
)

// Confidence is the reliability of a piece of evidence.
type Confidence string

const (
	// ConfidenceHigh: automated test evidence that passed.
	ConfidenceHigh Confidence = "high"
	// ConfidenceMedium: static check or review evidence.
	ConfidenceMedium Confidence = "medium"
	// ConfidenceLow: manual evidence or evidence collected with tests disabled.
	ConfidenceLow Confidence = "low"
	// ConfidenceNone: no evidence linked.
	ConfidenceNone Confidence = "none"
)

// Evidence links one acceptance criterion to a verification artifact (spec §27).
type Evidence struct {
	// CriterionID is the acceptance-criterion identifier (e.g. "AC-1" or a
	// task-local "accept-1").
	CriterionID string
	// Type is the evidence kind.
	Type EvidenceType
	// Reference points to the artifact (test name, report path, check id).
	Reference string
	// Status: passed | failed | pending | not_collected.
	Status string
	// Confidence of this evidence. Lowered when tests are disabled (§27).
	Confidence Confidence
	// TestsWereRun reports whether automated tests backed this evidence.
	TestsWereRun bool
}

// Set is the complete evidence collection for one task/attempt.
type Set struct {
	Items []Evidence
}

// Add appends a piece of evidence.
func (s *Set) Add(e Evidence) {
	s.Items = append(s.Items, e)
}

// IsComplete reports whether every criterion has at least one piece of evidence
// with status "passed". An empty set is never complete.
func (s Set) IsComplete() bool {
	if len(s.Items) == 0 {
		return false
	}
	byCriterion := s.byCriterion()
	for _, items := range byCriterion {
		hasPassed := false
		for _, e := range items {
			if e.Status == "passed" {
				hasPassed = true
				break
			}
		}
		if !hasPassed {
			return false
		}
	}
	return true
}

// AggregateConfidence returns the lowest confidence across all evidence (§27:
// the system must lower confidence when tests are disabled). If any criterion
// has only low/none confidence, the aggregate is low.
func (s Set) AggregateConfidence() Confidence {
	if len(s.Items) == 0 {
		return ConfidenceNone
	}
	lowest := ConfidenceHigh
	for _, e := range s.Items {
		c := rankConfidence(e.Confidence)
		current := rankConfidence(lowest)
		if c < current {
			lowest = e.Confidence
		}
	}
	return lowest
}

// HasTestEvidence reports whether any criterion has automated test evidence.
func (s Set) HasTestEvidence() bool {
	for _, e := range s.Items {
		if e.Type == EvidenceTest && e.Status == "passed" {
			return true
		}
	}
	return false
}

// MissingCriteria returns criterion IDs that have no passing evidence.
func (s Set) MissingCriteria() []string {
	byCriterion := s.byCriterion()
	var missing []string
	for id, items := range byCriterion {
		hasPassed := false
		for _, e := range items {
			if e.Status == "passed" {
				hasPassed = true
				break
			}
		}
		if !hasPassed {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	return missing
}

func (s Set) byCriterion() map[string][]Evidence {
	m := map[string][]Evidence{}
	for _, e := range s.Items {
		m[e.CriterionID] = append(m[e.CriterionID], e)
	}
	return m
}

func rankConfidence(c Confidence) int {
	switch c {
	case ConfidenceHigh:
		return 4
	case ConfidenceMedium:
		return 3
	case ConfidenceLow:
		return 2
	case ConfidenceNone:
		return 1
	default:
		return 0
	}
}

// LowerForDisabledTests adjusts evidence confidence when automated tests were
// not run (§27). Evidence that previously claimed high confidence is downgraded
// to low because it was not backed by a passing automated test.
func (s Set) LowerForDisabledTests() Set {
	out := Set{}
	for _, e := range s.Items {
		clone := e
		if !e.TestsWereRun && e.Confidence == ConfidenceHigh {
			clone.Confidence = ConfidenceLow
		}
		out.Add(clone)
	}
	return out
}

// String renders the evidence set for logs/audit.
func (s Set) String() string {
	if len(s.Items) == 0 {
		return "evidence: (none)"
	}
	str := fmt.Sprintf("evidence: %d items, confidence=%s, complete=%v\n",
		len(s.Items), s.AggregateConfidence(), s.IsComplete())
	for _, e := range s.Items {
		str += fmt.Sprintf("  %s [%s] %s: %s (conf=%s)\n",
			e.CriterionID, e.Type, e.Reference, e.Status, e.Confidence)
	}
	return str
}
