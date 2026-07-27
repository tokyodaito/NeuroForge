package task

import (
	"errors"
	"strings"
	"testing"
)

// validSpec is a baseline specification that passes validation. Each table
// case clones it and mutates one field to break exactly one invariant.
func validSpec() Specification {
	return Specification{
		TaskID:    "proj-1",
		Version:   0,
		Objective: "Fix the double progress indicator on the payment screen.",
		AcceptanceCriteria: []AcceptanceCriterion{
			{ID: "AC-1", Statement: "Only one progress indicator is shown at any time."},
			{ID: "AC-2", Statement: "The indicator disappears within 200ms of completion."},
		},
		Risk:          RiskR1,
		Complexity:    ComplexityC1,
		NonGoals:      []string{"redesigning the payment screen"},
		Assumptions:   []string{"the bug reproduces on the current build"},
		Constraints:   []string{"must not change the payment API contract"},
		ProposedScope: []string{"PaymentView.render()"},
		VisualRequirements: VisualRequirements{
			Required: true, Viewport: "390x844", Theme: "dark", Locale: "en", Density: "xxhdpi",
			References: []string{"sha256:abc"},
		},
	}
}

func TestValidateSpecification_TableDriven(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		mutate     func(Specification) Specification
		wantErr    string // substring expected in the joined error
		wantPasses bool
	}{
		{
			name:       "valid baseline passes",
			mutate:     func(s Specification) Specification { return s },
			wantPasses: true,
		},
		{
			name:    "missing task id",
			mutate:  func(s Specification) Specification { s.TaskID = ""; return s },
			wantErr: "task_id is required",
		},
		{
			name:    "blank objective",
			mutate:  func(s Specification) Specification { s.Objective = "   "; return s },
			wantErr: "objective is required",
		},
		{
			name:    "no acceptance criteria",
			mutate:  func(s Specification) Specification { s.AcceptanceCriteria = nil; return s },
			wantErr: "at least one acceptance criterion is required",
		},
		{
			name: "acceptance criterion with empty id",
			mutate: func(s Specification) Specification {
				s.AcceptanceCriteria = []AcceptanceCriterion{{ID: "", Statement: "x"}}
				return s
			},
			wantErr: "has no id",
		},
		{
			name: "acceptance criterion with empty statement",
			mutate: func(s Specification) Specification {
				s.AcceptanceCriteria = []AcceptanceCriterion{{ID: "AC-1", Statement: "  "}}
				return s
			},
			wantErr: `acceptance criterion "AC-1" has no statement`,
		},
		{
			name: "duplicate acceptance criterion id",
			mutate: func(s Specification) Specification {
				s.AcceptanceCriteria = []AcceptanceCriterion{
					{ID: "AC-1", Statement: "a"},
					{ID: "AC-1", Statement: "b"},
				}
				return s
			},
			wantErr: `duplicate acceptance criterion id "AC-1"`,
		},
		{
			name:    "unknown risk class",
			mutate:  func(s Specification) Specification { s.Risk = Risk("R9"); return s },
			wantErr: "unknown risk class",
		},
		{
			name:    "unknown complexity class",
			mutate:  func(s Specification) Specification { s.Complexity = Complexity("C9"); return s },
			wantErr: "unknown complexity class",
		},
		{
			name: "empty risk and complexity are allowed (unspecified)",
			mutate: func(s Specification) Specification {
				s.Risk = ""
				s.Complexity = ""
				return s
			},
			wantPasses: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := ValidateSpecification(tc.mutate(validSpec()))
			if tc.wantPasses {
				if err != nil {
					t.Fatalf("expected pass, got error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
			if !errors.Is(err, ErrInvalidSpecification) {
				t.Fatalf("error is not ErrInvalidSpecification: %v", err)
			}
		})
	}
}

func TestValidateSpecification_NormalisesWhitespace(t *testing.T) {
	t.Parallel()
	s := validSpec()
	s.Objective = "  padded objective  "
	s.AcceptanceCriteria = []AcceptanceCriterion{
		{ID: "  AC-1  ", Statement: "  trimmed statement  "},
	}
	out, err := ValidateSpecification(s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Objective != "padded objective" {
		t.Fatalf("objective not trimmed: %q", out.Objective)
	}
	if out.AcceptanceCriteria[0].ID != "AC-1" {
		t.Fatalf("ac id not trimmed: %q", out.AcceptanceCriteria[0].ID)
	}
	if out.AcceptanceCriteria[0].Statement != "trimmed statement" {
		t.Fatalf("ac statement not trimmed: %q", out.AcceptanceCriteria[0].Statement)
	}
}

func TestValidateSpecification_ReportsAllViolationsAtOnce(t *testing.T) {
	t.Parallel()
	// Break three invariants; the joined error must mention each.
	s := Specification{
		TaskID:             "",
		Objective:          "",
		AcceptanceCriteria: []AcceptanceCriterion{{ID: "AC-1"}, {ID: "AC-1"}},
		Risk:               Risk("RX"),
	}
	_, err := ValidateSpecification(s)
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"task_id", "objective", "duplicate", "unknown risk"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestRisk_IsValid_Table(t *testing.T) {
	t.Parallel()
	for _, r := range []Risk{"", RiskR0, RiskR1, RiskR2, RiskR3, RiskR4} {
		if !r.IsValid() {
			t.Errorf("Risk(%q).IsValid() = false, want true", r)
		}
	}
	for _, r := range []Risk{"R5", "r0", "low", "X"} {
		if r.IsValid() {
			t.Errorf("Risk(%q).IsValid() = true, want false", r)
		}
	}
}

func TestComplexity_IsValid_Table(t *testing.T) {
	t.Parallel()
	for _, c := range []Complexity{"", ComplexityC0, ComplexityC1, ComplexityC2, ComplexityC3} {
		if !c.IsValid() {
			t.Errorf("Complexity(%q).IsValid() = false, want true", c)
		}
	}
	for _, c := range []Complexity{"C9", "c0", "high"} {
		if c.IsValid() {
			t.Errorf("Complexity(%q).IsValid() = true, want false", c)
		}
	}
}
