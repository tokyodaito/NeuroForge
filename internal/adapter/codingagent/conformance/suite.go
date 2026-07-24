package conformance

import (
	"context"
	"fmt"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/fake"
)

// AdapterFactory returns an adapter configured to exhibit the given scenario,
// plus a cleanup that releases its resources. The factory may spawn a process
// (plugin/declarative) or build an in-process adapter. Scenario-aware adapters
// (the fake agent) honour every scenario; others honour what they can.
type AdapterFactory func(ctx context.Context, scenario fake.Scenario) (codingagent.Adapter, func(), error)

// CheckResult is the outcome of one conformance check.
type CheckResult struct {
	Name   string
	Passed bool
	Detail string
}

// Suite is the §13.3 conformance suite. Run executes every check and returns the
// results in a stable order.
type Suite struct {
	Factory AdapterFactory
	// Timeout is the per-check wall-clock budget. Defaults to 10s if zero.
	Timeout time.Duration
}

// checkSpec pairs a check name with its implementation.
type checkSpec struct {
	name string
	fn   checkFunc
}

// checkFunc returns (passed, detail). It is bound to a *Suite method.
type checkFunc func(s *Suite, ctx context.Context) (bool, string)

// checks is the ordered list of conformance checks.
var checks = []checkSpec{
	{"handshake", (*Suite).checkHandshake},
	{"version_compatibility", (*Suite).checkVersionCompatibility},
	{"event_ordering", (*Suite).checkEventOrdering},
	{"malformed_output", (*Suite).checkMalformedOutput},
	{"cancellation", (*Suite).checkCancellation},
	{"timeout", (*Suite).checkTimeout},
	{"quota_failure", (*Suite).checkQuotaFailure},
	{"resume", (*Suite).checkResume},
	{"process_crash", (*Suite).checkProcessCrash},
}

// Names returns the ordered conformance check names (for CLI/JSON output).
func Names() []string {
	out := make([]string, len(checks))
	for i, c := range checks {
		out[i] = c.name
	}
	return out
}

// Run executes all conformance checks sequentially and returns their results in
// a stable order. A check that panics or times out is recorded as failed (the
// suite never aborts the run).
func (s *Suite) Run(ctx context.Context) []CheckResult {
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	results := make([]CheckResult, 0, len(checks))
	for _, c := range checks {
		cctx, cancel := context.WithTimeout(ctx, timeout)
		passed, detail := s.runOne(cctx, c)
		cancel()
		results = append(results, CheckResult{Name: c.name, Passed: passed, Detail: detail})
	}
	return results
}

func (s *Suite) runOne(ctx context.Context, c checkSpec) (passed bool, detail string) {
	defer func() {
		if r := recover(); r != nil {
			passed = false
			detail = fmt.Sprintf("panic: %v", r)
		}
	}()
	return c.fn(s, ctx)
}

// makeAdapter obtains a scenario-configured adapter plus its cleanup.
func (s *Suite) makeAdapter(ctx context.Context, scenario fake.Scenario) (codingagent.Adapter, func(), error) {
	return s.Factory(ctx, scenario)
}

// Summary reports the number of passing checks.
func Summary(results []CheckResult) (passed, total int) {
	for _, r := range results {
		total++
		if r.Passed {
			passed++
		}
	}
	return
}
