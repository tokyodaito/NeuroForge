package testengine

import (
	"fmt"

	"neuroforge/internal/policy"
)

// ScopeValidator enforces the §24.2 test-path rule against agent file changes.
// When test generation is disabled, test paths become forbidden — the validator
// rejects any change to a test file.
type ScopeValidator struct {
	policy policy.Pipeline
	convs  []policy.TestPathConvention
}

// NewScopeValidator creates a validator for the given pipeline. If no
// conventions are supplied, the policy package defaults are used.
func NewScopeValidator(p policy.Pipeline, convs ...policy.TestPathConvention) *ScopeValidator {
	return &ScopeValidator{policy: p, convs: convs}
}

// ScopeViolation is returned when a file change violates the test scope rule.
type ScopeViolation struct {
	Path    string
	Code    string
	Reason  string
	Changes int // how many paths were denied
}

// Error implements error.
func (v ScopeViolation) Error() string {
	return fmt.Sprintf("scope violation: %s (%s): %s", v.Path, v.Code, v.Reason)
}

// Validate checks a batch of file changes and returns a ScopeViolation if any
// test path is denied. When generation is enabled and all test-path changes are
// permitted, it returns nil.
func (sv *ScopeValidator) Validate(changes []policy.FileChange) error {
	r, denied := policy.CheckFileChanges(sv.policy, changes, sv.convs...)
	if denied == 0 {
		return nil
	}
	return ScopeViolation{
		Path:    r.Path,
		Code:    r.DenialCode,
		Reason:  r.Reason,
		Changes: denied,
	}
}

// AllowedTestPaths returns the set of explicitly-allowed test paths for the
// current task. When generation is off, this is always empty (§24.2 exception:
// the user may explicitly allow a specific file, but that is handled at the task
// level, not here).
func (sv *ScopeValidator) AllowedTestPaths() []string {
	return nil
}
