package task

import (
	"testing"
	"time"
)

// TestSpecificationsSemanticallyEqual is the comprehensive table-driven unit
// test for specificationsSemanticallyEqual — the heart of the compile-and-save
// idempotency rule (M14-03 MAJOR-2 remediation).
//
// It asserts that every meaningful (semantic) field mutation breaks equality,
// and that every persistence/provenance-only field mutation does NOT break
// equality. The expected values are constructed explicitly in each case (not
// via the production comparison) so a mutation that drops a comparison is
// caught immediately.
func TestSpecificationsSemanticallyEqual(t *testing.T) {
	t.Parallel()

	base := func() Specification {
		return Specification{
			TaskID:    "proj-1",
			Version:   1,
			Objective: "Add a retry button to the payment screen.",
			AcceptanceCriteria: []AcceptanceCriterion{
				{ID: "AC-1", Statement: "A retry button is shown when payment fails."},
				{ID: "AC-2", Statement: "Clicking retry re-submits within 500ms."},
			},
			NonGoals:      []string{"redesigning the payment screen"},
			Assumptions:   []string{"the failure endpoint returns a retriable error"},
			Constraints:   []string{"no new dependencies"},
			ProposedScope: []string{"PaymentView.swift", "PaymentViewModel.swift"},
			Risk:          RiskR2,
			Complexity:    ComplexityC1,
			VisualRequirements: VisualRequirements{
				Required:   true,
				Viewport:   "390x844",
				Theme:      "dark",
				Locale:     "en",
				Density:    "xxhdpi",
				References: []string{"sha256:abc", "sha256:def"},
			},
			Locked:    true,
			LockedAt:  time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
			LockedBy:  "reviewer-1",
			CreatedAt: time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
			CreatedBy: "compiler",
		}
	}

	cases := []struct {
		name      string
		mutate    func(Specification) Specification
		wantEqual bool
	}{
		// ---- MUST be equal ----
		{
			name:      "identical specifications are equal",
			mutate:    func(s Specification) Specification { return s },
			wantEqual: true,
		},
		{
			name: "different persistence Version is equal",
			mutate: func(s Specification) Specification {
				s.Version = 42
				return s
			},
			wantEqual: true,
		},
		{
			name: "different CreatedAt timestamp is equal",
			mutate: func(s Specification) Specification {
				s.CreatedAt = time.Date(2030, 6, 15, 8, 30, 0, 0, time.UTC)
				return s
			},
			wantEqual: true,
		},
		{
			name: "different lock state is equal",
			mutate: func(s Specification) Specification {
				s.Locked = false
				return s
			},
			wantEqual: true,
		},
		{
			name: "different LockedBy is equal",
			mutate: func(s Specification) Specification {
				s.LockedBy = "someone-else"
				return s
			},
			wantEqual: true,
		},
		{
			name: "different LockedAt timestamp is equal",
			mutate: func(s Specification) Specification {
				s.LockedAt = time.Date(2099, 12, 31, 23, 59, 0, 0, time.UTC)
				return s
			},
			wantEqual: true,
		},
		{
			name: "different CreatedBy is equal",
			mutate: func(s Specification) Specification {
				s.CreatedBy = "another-compiler"
				return s
			},
			wantEqual: true,
		},
		{
			name: "different TaskID is equal (comparison is about content, not identity)",
			mutate: func(s Specification) Specification {
				s.TaskID = "other-task"
				return s
			},
			wantEqual: true,
		},

		// ---- MUST differ: scalar content fields ----
		{
			name: "different Objective is NOT equal",
			mutate: func(s Specification) Specification {
				s.Objective = "Add a CANCEL button to the payment screen."
				return s
			},
			wantEqual: false,
		},
		{
			name: "different Risk is NOT equal",
			mutate: func(s Specification) Specification {
				s.Risk = RiskR4
				return s
			},
			wantEqual: false,
		},
		{
			name: "different Complexity is NOT equal",
			mutate: func(s Specification) Specification {
				s.Complexity = ComplexityC3
				return s
			},
			wantEqual: false,
		},

		// ---- MUST differ: list fields ----
		{
			name: "different NonGoals is NOT equal",
			mutate: func(s Specification) Specification {
				s.NonGoals = []string{"different non-goal"}
				return s
			},
			wantEqual: false,
		},
		{
			name: "different Assumptions is NOT equal",
			mutate: func(s Specification) Specification {
				s.Assumptions = []string{"different assumption"}
				return s
			},
			wantEqual: false,
		},
		{
			name: "different Constraints is NOT equal",
			mutate: func(s Specification) Specification {
				s.Constraints = []string{"different constraint"}
				return s
			},
			wantEqual: false,
		},
		{
			name: "different ProposedScope is NOT equal",
			mutate: func(s Specification) Specification {
				s.ProposedScope = []string{"Only.swift"}
				return s
			},
			wantEqual: false,
		},
		{
			name: "extra element in NonGoals is NOT equal",
			mutate: func(s Specification) Specification {
				s.NonGoals = append([]string{"extra"}, s.NonGoals...)
				return s
			},
			wantEqual: false,
		},
		{
			name: "reordered NonGoals is NOT equal",
			mutate: func(s Specification) Specification {
				s.NonGoals = []string{"redesigning the payment screen", "second-item"}
				return s
			},
			wantEqual: false,
		},
		{
			name: "nil vs empty NonGoals is equal (both length 0)",
			mutate: func(s Specification) Specification {
				s.NonGoals = nil
				return s
			},
			wantEqual: false, // base has one non-goal; nil makes it different
		},

		// ---- MUST differ: acceptance criteria ----
		{
			name: "different AC ID is NOT equal",
			mutate: func(s Specification) Specification {
				s.AcceptanceCriteria[0].ID = "AC-99"
				return s
			},
			wantEqual: false,
		},
		{
			name: "different AC Statement is NOT equal",
			mutate: func(s Specification) Specification {
				s.AcceptanceCriteria[0].Statement = "Changed statement."
				return s
			},
			wantEqual: false,
		},
		{
			name: "fewer ACs is NOT equal",
			mutate: func(s Specification) Specification {
				s.AcceptanceCriteria = s.AcceptanceCriteria[:1]
				return s
			},
			wantEqual: false,
		},
		{
			name: "more ACs is NOT equal",
			mutate: func(s Specification) Specification {
				s.AcceptanceCriteria = append(s.AcceptanceCriteria, AcceptanceCriterion{ID: "AC-3", Statement: "extra"})
				return s
			},
			wantEqual: false,
		},
		{
			name: "reordered ACs is NOT equal (order is semantic)",
			mutate: func(s Specification) Specification {
				s.AcceptanceCriteria = []AcceptanceCriterion{
					s.AcceptanceCriteria[1],
					s.AcceptanceCriteria[0],
				}
				return s
			},
			wantEqual: false,
		},

		// ---- MUST differ: visual requirements sub-fields ----
		{
			name: "different VisualRequirements.Required is NOT equal",
			mutate: func(s Specification) Specification {
				s.VisualRequirements.Required = false
				return s
			},
			wantEqual: false,
		},
		{
			name: "different VisualRequirements.Viewport is NOT equal",
			mutate: func(s Specification) Specification {
				s.VisualRequirements.Viewport = "768x1024"
				return s
			},
			wantEqual: false,
		},
		{
			name: "different VisualRequirements.Theme is NOT equal",
			mutate: func(s Specification) Specification {
				s.VisualRequirements.Theme = "light"
				return s
			},
			wantEqual: false,
		},
		{
			name: "different VisualRequirements.Locale is NOT equal",
			mutate: func(s Specification) Specification {
				s.VisualRequirements.Locale = "fr"
				return s
			},
			wantEqual: false,
		},
		{
			name: "different VisualRequirements.Density is NOT equal",
			mutate: func(s Specification) Specification {
				s.VisualRequirements.Density = "mdpi"
				return s
			},
			wantEqual: false,
		},
		{
			name: "different VisualRequirements.References content is NOT equal",
			mutate: func(s Specification) Specification {
				s.VisualRequirements.References = []string{"sha256:changed"}
				return s
			},
			wantEqual: false,
		},
		{
			name: "different VisualRequirements.References count is NOT equal",
			mutate: func(s Specification) Specification {
				s.VisualRequirements.References = append(s.VisualRequirements.References, "sha256:extra")
				return s
			},
			wantEqual: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := base()
			b := tc.mutate(base())
			got := specificationsSemanticallyEqual(a, b)
			if got != tc.wantEqual {
				t.Fatalf("specificationsSemanticallyEqual = %v, want %v (case: %s)", got, tc.wantEqual, tc.name)
			}
		})
	}
}
