package fake

import (
	"context"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/protocol"
)

func runFake(t *testing.T, scenario Scenario) (protocol.RunHandle, []protocol.NormalizedEvent, protocol.FailureClassification, protocol.RunHandle) {
	t.Helper()
	a := New(AdapterOptions{Scenario: scenario, Installed: true})
	sink := &codingagent.SliceSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req := protocol.AgentRunRequest{RunID: "r", Engine: "fake", Model: "fake/standard", Workspace: t.TempDir()}
	handle, err := a.Start(ctx, req, sink)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Wait for the run to reach a terminal state (or timeout).
	terminal := waitForTerminal(sink, 2*time.Second)
	fc := protocol.FailureClassification{}
	if len(terminal) > 0 {
		last := terminal[len(terminal)-1]
		if last.Type == protocol.EventRunFailed && last.Failure != nil {
			fc = a.ClassifyFailure(last.Failure.ExitCode, terminal, "")
		}
	}
	return handle, sink.Events(), fc, handle
}

func waitForTerminal(s *codingagent.SliceSink, timeout time.Duration) []protocol.NormalizedEvent {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		evs := s.Events()
		if len(evs) > 0 && evs[len(evs)-1].Type.IsTerminal() {
			return evs
		}
		time.Sleep(2 * time.Millisecond)
	}
	return s.Events()
}

func TestScenarioSuccess(t *testing.T) {
	handle, evs, _, _ := runFake(t, ScenarioSuccess)
	types := eventTypes(evs)
	if len(types) == 0 || types[0] != protocol.EventRunStarted {
		t.Fatalf("first event = %v, want run.started", types)
	}
	if types[len(types)-1] != protocol.EventRunCompleted {
		t.Fatalf("last event = %v, want run.completed", types)
	}
	if handle.Engine != "fake" || handle.Model != "fake/standard" {
		t.Errorf("engine/model not distinct: %+v", handle)
	}
	// Expect a file.changed and a usage event for success.
	if !hasType(evs, protocol.EventFileChanged) || !hasType(evs, protocol.EventUsageUpdated) {
		t.Errorf("success scenario missing file/usage event: %v", types)
	}
}

func TestScenarioQuotaBeforeAndAfterEdits(t *testing.T) {
	for _, sc := range []Scenario{ScenarioQuotaBeforeEdits, ScenarioQuotaAfterEdits} {
		t.Run(string(sc), func(t *testing.T) {
			_, evs, fc, _ := runFake(t, sc)
			if fc.Class != protocol.FailureProviderQuota {
				t.Errorf("class = %s, want PROVIDER_QUOTA", fc.Class)
			}
			if !fc.Failover {
				t.Error("quota should suggest failover")
			}
			if lastType(evs) != protocol.EventRunFailed {
				t.Errorf("last = %s, want run.failed", lastType(evs))
			}
		})
	}
}

func TestScenarioRateLimitAndAuth(t *testing.T) {
	cases := []struct {
		sc   Scenario
		want protocol.FailureClass
	}{
		{ScenarioRateLimit, protocol.FailureProviderRateLimit},
		{ScenarioAuthFailure, protocol.FailureProviderAuth},
	}
	for _, c := range cases {
		t.Run(string(c.sc), func(t *testing.T) {
			_, _, fc, _ := runFake(t, c.sc)
			if fc.Class != c.want {
				t.Errorf("class = %s, want %s", fc.Class, c.want)
			}
		})
	}
}

func TestScenarioMalformedDoesNotBreakRun(t *testing.T) {
	_, evs, _, _ := runFake(t, ScenarioMalformedJSON)
	// Malformed scenario emits a warning (in-process) but still completes.
	if lastType(evs) != protocol.EventRunCompleted {
		t.Errorf("last = %s, want run.completed (malformed must not break run)", lastType(evs))
	}
	if !hasType(evs, protocol.EventWarning) {
		t.Errorf("expected a warning for malformed output")
	}
}

func TestScenarioPartialOutput(t *testing.T) {
	_, evs, _, _ := runFake(t, ScenarioPartialOutput)
	// partial-output exits before a terminal event: last event is not terminal.
	if lastType(evs).IsTerminal() {
		t.Errorf("partial-output should have no terminal event, got %s", lastType(evs))
	}
}

func TestScenarioScopeViolation(t *testing.T) {
	_, _, fc, _ := runFake(t, ScenarioScopeViolation)
	if fc.Class != protocol.FailureScopeViolation {
		t.Errorf("class = %s, want SCOPE_VIOLATION", fc.Class)
	}
	if fc.Retryable {
		t.Error("scope violation should not be retryable")
	}
}

func TestScenarioUsageEvents(t *testing.T) {
	_, evs, _, _ := runFake(t, ScenarioUsageEvents)
	count := 0
	for _, e := range evs {
		if e.Type == protocol.EventUsageUpdated {
			count++
		}
	}
	if count != 3 {
		t.Errorf("usage events = %d, want 3", count)
	}
	if lastType(evs) != protocol.EventRunCompleted {
		t.Errorf("last = %s, want run.completed", lastType(evs))
	}
}

func TestScenarioCancellation(t *testing.T) {
	a := New(AdapterOptions{Scenario: ScenarioCancellation, Installed: true})
	sink := &codingagent.SliceSink{}
	ctx := context.Background()
	handle, err := a.Start(ctx, protocol.AgentRunRequest{RunID: "c", Engine: "fake", Model: "fake/standard", Workspace: t.TempDir()}, sink)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Give it a moment to reach the hang point.
	time.Sleep(2 * hangGrace)
	if err := a.Cancel(context.Background(), handle); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	evs := waitForTerminal(sink, 2*time.Second)
	if lastType(evs) != protocol.EventRunCancelled {
		t.Errorf("last = %s, want run.cancelled", lastType(evs))
	}
}

func TestScenarioSuccessfulResume(t *testing.T) {
	a := New(AdapterOptions{Scenario: ScenarioResume, Installed: true})
	sink := &codingagent.SliceSink{}
	ctx := context.Background()
	handle, err := a.Resume(ctx, protocol.ResumeRequest{
		RunID: "rr", Engine: "fake", Model: "fake/standard", Workspace: t.TempDir(), SessionID: "fake-session-1",
	}, sink)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	evs := waitForTerminal(sink, 2*time.Second)
	types := eventTypes(evs)
	if len(types) == 0 || types[0] != protocol.EventRunResumed {
		t.Fatalf("first event = %v, want run.resumed", types)
	}
	if lastType(evs) != protocol.EventRunCompleted {
		t.Errorf("last = %s, want run.completed", lastType(evs))
	}
	if handle.SessionID == "" {
		t.Error("resume handle missing session id")
	}
}

func TestScenarioTimeoutHangsUntilCancelled(t *testing.T) {
	// Timeout scenario hangs; with a short context deadline it should be
	// terminated by context cancellation (simulating the supervisor timeout).
	a := New(AdapterOptions{Scenario: ScenarioTimeout, Installed: true})
	sink := &codingagent.SliceSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := a.Start(ctx, protocol.AgentRunRequest{RunID: "t", Engine: "fake", Model: "fake/standard", Workspace: t.TempDir()}, sink)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// No terminal event should arrive within a short window (it hangs).
	time.Sleep(hangGrace * 2)
	if lastType(sink.Events()).IsTerminal() {
		t.Errorf("timeout scenario terminated unexpectedly: %s", lastType(sink.Events()))
	}
}

func TestFakeCapabilitiesAndModels(t *testing.T) {
	a := New(AdapterOptions{Installed: true})
	caps := a.Capabilities(context.Background())
	if !caps.SessionResume || !caps.UsageReporting {
		t.Errorf("fake caps missing resume/usage: %+v", caps)
	}
	models, err := a.ListModels(context.Background(), protocol.Account{})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) == 0 {
		t.Fatal("no models reported")
	}
	for _, m := range models {
		if m.Engine != "fake" {
			t.Errorf("model %s has engine %s, want fake", m.ID, m.Engine)
		}
	}
}

func TestAllScenariosValid(t *testing.T) {
	for _, s := range AllScenarios {
		if !IsValidScenario(s) {
			t.Errorf("scenario %q not valid", s)
		}
	}
}

func TestUnknownScenarioClassified(t *testing.T) {
	a := New(AdapterOptions{Scenario: "bogus", Installed: true})
	sink := &codingagent.SliceSink{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _ = a.Start(ctx, protocol.AgentRunRequest{RunID: "u", Engine: "fake"}, sink)
	evs := waitForTerminal(sink, time.Second)
	if lastType(evs) != protocol.EventRunFailed {
		t.Fatalf("last = %s, want run.failed", lastType(evs))
	}
}

// helpers

func eventTypes(evs []protocol.NormalizedEvent) []protocol.EventType {
	out := make([]protocol.EventType, len(evs))
	for i, e := range evs {
		out[i] = e.Type
	}
	return out
}

func lastType(evs []protocol.NormalizedEvent) protocol.EventType {
	if len(evs) == 0 {
		return ""
	}
	return evs[len(evs)-1].Type
}

func hasType(evs []protocol.NormalizedEvent, t protocol.EventType) bool {
	for _, e := range evs {
		if e.Type == t {
			return true
		}
	}
	return false
}
