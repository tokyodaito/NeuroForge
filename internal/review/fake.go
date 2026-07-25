package review

import (
	"context"
	"sync"
)

// FakeScript describes the deterministic behaviour of a [FakeReviewer].
type FakeScript struct {
	// DefaultFindings are returned for any role if no per-role override.
	DefaultFindings []Finding
	// PerRole overrides the findings for a specific role.
	PerRole map[Role][]Finding
	// Err, if set, is returned instead of findings (simulates infra failure).
	Err error
}

// FakeReviewer is a deterministic reviewer that performs no real AI calls (rule
// §36.5). It produces scripted findings and can be updated at runtime by
// repair-loop tests to simulate a fix landing.
type FakeReviewer struct {
	script FakeScript

	mu      sync.Mutex
	calls   []Role
	updates map[Role][]Finding
}

// NewFakeReviewer creates a FakeReviewer with the given script.
func NewFakeReviewer(script FakeScript) *FakeReviewer {
	return &FakeReviewer{script: script, updates: map[Role][]Finding{}}
}

// Review implements Reviewer.
func (r *FakeReviewer) Review(_ context.Context, role Role, _ ReviewRequest) ([]Finding, error) {
	r.mu.Lock()
	r.calls = append(r.calls, role)
	r.mu.Unlock()

	if r.script.Err != nil {
		return nil, r.script.Err
	}

	r.mu.Lock()
	if findings, ok := r.updates[role]; ok {
		r.mu.Unlock()
		return findings, nil
	}
	r.mu.Unlock()

	if findings, ok := r.script.PerRole[role]; ok {
		return findings, nil
	}
	return r.script.DefaultFindings, nil
}

// Calls returns the ordered list of roles that were reviewed.
func (r *FakeReviewer) Calls() []Role {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Role, len(r.calls))
	copy(out, r.calls)
	return out
}

// CallCount returns how many times a given role was reviewed.
func (r *FakeReviewer) CallCount(role Role) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.calls {
		if c == role {
			n++
		}
	}
	return n
}

// SetFindings overrides the findings for a role at runtime (repair loop test
// helper).
func (r *FakeReviewer) SetFindings(role Role, findings []Finding) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updates[role] = findings
}

// Reset clears the call history and runtime overrides.
func (r *FakeReviewer) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = nil
	r.updates = map[Role][]Finding{}
}
