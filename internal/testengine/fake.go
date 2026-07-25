package testengine

import (
	"context"
	"sync"
)

// FakeScript describes the deterministic behaviour of a [FakeRunner] for one
// level. If PerLevel is set, it takes precedence over Result.
type FakeScript struct {
	// Result is the default result returned for any level.
	Result Result
	// PerLevel overrides the result for a specific level.
	PerLevel map[VerificationLevel]Result
	// CallCount tracks how many times Run was called per level (for repair-loop
	// tests that want to observe re-runs).
}

// FakeRunner is a deterministic test runner that performs no real test execution
// or network calls (rule §36.5). It is the test-only runner used by M8
// orchestration and integration tests.
type FakeRunner struct {
	script FakeScript

	mu      sync.Mutex
	calls   []VerificationLevel          // ordered record of Run invocations
	updates map[VerificationLevel]Result // runtime overrides for repair-loop re-runs
}

// NewFakeRunner creates a FakeRunner with the given script.
func NewFakeRunner(script FakeScript) *FakeRunner {
	return &FakeRunner{
		script:  script,
		updates: map[VerificationLevel]Result{},
	}
}

// Run implements Runner.
func (r *FakeRunner) Run(_ context.Context, req RunRequest) (Result, error) {
	r.mu.Lock()
	r.calls = append(r.calls, req.Level)
	r.mu.Unlock()

	// Check for a runtime update first (repair loop may change the outcome).
	r.mu.Lock()
	if res, ok := r.updates[req.Level]; ok {
		r.mu.Unlock()
		res.Level = req.Level
		return res, nil
	}
	r.mu.Unlock()

	// Per-level override.
	if res, ok := r.script.PerLevel[req.Level]; ok {
		res.Level = req.Level
		return res, nil
	}

	res := r.script.Result
	res.Level = req.Level
	if res.Status == "" {
		res.Status = StatusPassed
	}
	return res, nil
}

// Calls returns the ordered list of levels that were run.
func (r *FakeRunner) Calls() []VerificationLevel {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]VerificationLevel, len(r.calls))
	copy(out, r.calls)
	return out
}

// CallCount returns how many times a given level was run.
func (r *FakeRunner) CallCount(level VerificationLevel) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.calls {
		if c == level {
			n++
		}
	}
	return n
}

// SetResult overrides the result for a level at runtime. Used by repair-loop
// tests to simulate a fix landing.
func (r *FakeRunner) SetResult(level VerificationLevel, res Result) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updates[level] = res
}

// Reset clears the call history and runtime overrides.
func (r *FakeRunner) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = nil
	r.updates = map[VerificationLevel]Result{}
}
