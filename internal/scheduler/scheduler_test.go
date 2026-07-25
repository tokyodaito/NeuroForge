package scheduler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"neuroforge/internal/policy"
	"neuroforge/internal/quality"
)

// fakeRuntime is a deterministic Runtime for unit-testing the scheduler without
// the daemon (rule §33: no real paid providers).
type fakeRuntime struct {
	mu        sync.Mutex
	created   int
	outcome   string
	events    []AgentEvent
	createErr error
}

func (f *fakeRuntime) CreateAttempt(ctx context.Context, taskID, wpID, base string) (string, string, error) {
	if f.createErr != nil {
		return "", "", f.createErr
	}
	f.mu.Lock()
	f.created++
	id := "ws-" + taskID
	f.mu.Unlock()
	return id, "/tmp/" + id, nil
}

func (f *fakeRuntime) RunAgent(ctx context.Context, wsID, wsPath, engine, model, prompt string, timeout time.Duration) (string, []AgentEvent, error) {
	return f.outcome, f.events, nil
}

type fakeResolver struct {
	pctx ProjectContext
	err  error
}

func (r *fakeResolver) Resolve(ctx context.Context, taskID string) (ProjectContext, error) {
	if r.err != nil {
		return ProjectContext{}, r.err
	}
	r.pctx.TaskID = taskID
	return r.pctx, nil
}

type fakeUsageSink struct {
	mu     sync.Mutex
	events []quality.UsageEvent
}

func (s *fakeUsageSink) RecordUsage(ctx context.Context, e quality.UsageEvent) error {
	s.mu.Lock()
	s.events = append(s.events, e)
	s.mu.Unlock()
	return nil
}

type fakeMemorySink struct {
	rules  []string
	learnF map[string]bool
}

func (m *fakeMemorySink) Learn(ctx context.Context, projectID, category, key, value, confidence string) error {
	if m.learnF == nil {
		m.learnF = map[string]bool{}
	}
	m.learnF[key] = true
	return nil
}
func (m *fakeMemorySink) Rules(ctx context.Context, projectID string) []string { return m.rules }

type fakePostMergeSink struct {
	count int
}

func (s *fakePostMergeSink) RecordPostMerge(ctx context.Context, r PostMergeRecord) error {
	s.count++
	return nil
}

func newTestScheduler(t *testing.T) (*Scheduler, *fakeRuntime, *fakeUsageSink) {
	t.Helper()
	rt := &fakeRuntime{outcome: "completed", events: []AgentEvent{
		{Type: "usage.updated", InputTokens: 100, OutputTokens: 50, CacheRead: 30, CostUSD: 0.01},
	}}
	res := &fakeResolver{pctx: ProjectContext{
		ProjectID: "p1", ProjectPath: "/tmp/repo",
		Profile:  policy.ProfileLocalReview,
		Resolved: policy.Resolved{Profile: policy.ProfileLocalReview},
	}}
	usage := &fakeUsageSink{}
	mem := &fakeMemorySink{rules: []string{"rule-one"}}
	pm := &fakePostMergeSink{}
	s, err := New(Deps{
		Runtime: rt, Resolver: res, Usage: usage, Memory: mem, PostMerge: pm,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, rt, usage
}

// TestDispatch_RecordsUsageAndMemory proves the production execution path
// records usage events + quality stats + memory durably.
func TestDispatch_RecordsUsageAndMemory(t *testing.T) {
	s, _, usage := newTestScheduler(t)

	res, err := s.Dispatch(context.Background(), "T1", DispatchOptions{Engine: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != "completed" {
		t.Errorf("outcome=%s want completed", res.Outcome)
	}
	if res.UsageEvents != 1 {
		t.Errorf("usage events=%d want 1", res.UsageEvents)
	}
	if res.EstimatedTokens != 180 {
		t.Errorf("tokens=%d want 180 (100+50+30)", res.EstimatedTokens)
	}
	if !res.MemoryLearned {
		t.Error("memory should be learned on dispatch")
	}
	if len(usage.events) != 1 {
		t.Errorf("durable usage sink got %d events want 1", len(usage.events))
	}
	if s.Statistics().OverallSuccessRate() != 1.0 {
		t.Errorf("success rate=%v want 1.0", s.Statistics().OverallSuccessRate())
	}
}

// TestDispatch_FailedOutcome proves a failed agent run records a failure in the
// quality statistics.
func TestDispatch_FailedOutcome(t *testing.T) {
	s, rt, _ := newTestScheduler(t)
	rt.outcome = "failed"

	_, err := s.Dispatch(context.Background(), "T2", DispatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Statistics().OverallSuccessRate() != 0.0 {
		t.Errorf("success rate=%v want 0.0 after failure", s.Statistics().OverallSuccessRate())
	}
}

// TestDispatch_CreateAttemptError proves a dispatcher error is surfaced and no
// usage/memory is recorded.
func TestDispatch_CreateAttemptError(t *testing.T) {
	s, rt, _ := newTestScheduler(t)
	rt.createErr = errors.New("workspace unavailable")

	_, err := s.Dispatch(context.Background(), "T3", DispatchOptions{})
	if err == nil {
		t.Fatal("expected error when CreateAttempt fails")
	}
}

// TestPostMerge_SkippedOutsideAutonomous proves the sentinel is a structural
// no-op when post_merge is not enabled (every profile except AUTONOMOUS).
func TestPostMerge_SkippedOutsideAutonomous(t *testing.T) {
	s, _, _ := newTestScheduler(t)
	// The fake resolver returns LOCAL_REVIEW (post_merge.enabled=false).
	rec, err := s.RunPostMerge(context.Background(), MergeOutcome{TaskID: "T4"},
		PostMergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Decision != "SKIPPED" {
		t.Errorf("decision=%s want SKIPPED outside AUTONOMOUS", rec.Decision)
	}
	if rec.Reverted {
		t.Error("revert must never fire when post-merge is disabled (§4.4)")
	}
}

// TestNew_RequiresRuntime proves the scheduler cannot be constructed without
// the required dependencies.
func TestNew_RequiresRuntime(t *testing.T) {
	_, err := New(Deps{})
	if err == nil {
		t.Error("expected error when Runtime is nil")
	}
}
