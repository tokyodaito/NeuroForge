package conformance

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/fake"
	"neuroforge/internal/adapter/codingagent/protocol"
)

// workspace returns a fresh temp directory for an agent run so simulated file
// writes never pollute the working directory or a shared path. Leaked temp dirs
// are harmless (they live in the system temp tree).
func workspace() string {
	if d, err := os.MkdirTemp("", "neuroforge-conf-*"); err == nil {
		return d
	}
	return ""
}

func waitTerminal(s *codingagent.SliceSink, timeout time.Duration) []protocol.NormalizedEvent {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		evs := s.Events()
		if len(evs) > 0 && evs[len(evs)-1].Type.IsTerminal() {
			return evs
		}
		time.Sleep(3 * time.Millisecond)
	}
	return s.Events()
}

func (s *Suite) checkHandshake(ctx context.Context) (bool, string) {
	a, cleanup, err := s.makeAdapter(ctx, fake.ScenarioSuccess)
	if err != nil {
		return false, "factory error: " + err.Error()
	}
	defer cleanup()
	d := a.Detect(ctx)
	if !d.Installed {
		return false, "detect reported not installed: " + d.Detail
	}
	caps := a.Capabilities(ctx)
	if !caps.HeadlessMode {
		return false, "adapter must support headless mode"
	}
	return true, "detect installed, capabilities present"
}

func (s *Suite) checkVersionCompatibility(ctx context.Context) (bool, string) {
	a, cleanup, err := s.makeAdapter(ctx, fake.ScenarioSuccess)
	if err != nil {
		return false, "factory error: " + err.Error()
	}
	defer cleanup()
	v := a.Version(ctx)
	if v.ProtocolVersion != protocol.ProtocolVersion {
		return false, fmt.Sprintf("adapter speaks protocol v%d, daemon requires v%d", v.ProtocolVersion, protocol.ProtocolVersion)
	}
	return true, fmt.Sprintf("protocol v%d", v.ProtocolVersion)
}

func (s *Suite) checkEventOrdering(ctx context.Context) (bool, string) {
	a, cleanup, err := s.makeAdapter(ctx, fake.ScenarioSuccess)
	if err != nil {
		return false, "factory error: " + err.Error()
	}
	defer cleanup()
	sink := &codingagent.SliceSink{}
	if _, err := a.Start(ctx, protocol.AgentRunRequest{RunID: "order", Engine: a.ID(), Model: "fake/standard", Workspace: workspace()}, sink); err != nil {
		return false, "start: " + err.Error()
	}
	evs := waitTerminal(sink, 6*time.Second)
	if len(evs) == 0 {
		return false, "no events emitted"
	}
	if evs[0].Type != protocol.EventRunStarted {
		return false, "first event must be run.started, got " + string(evs[0].Type)
	}
	if !evs[len(evs)-1].Type.IsTerminal() {
		return false, "run did not reach a terminal event; last was " + string(evs[len(evs)-1].Type)
	}
	return true, "ordered: " + joinTypes(evs)
}

func (s *Suite) checkMalformedOutput(ctx context.Context) (bool, string) {
	a, cleanup, err := s.makeAdapter(ctx, fake.ScenarioMalformedJSON)
	if err != nil {
		return false, "factory error: " + err.Error()
	}
	defer cleanup()
	sink := &codingagent.SliceSink{}
	if _, err := a.Start(ctx, protocol.AgentRunRequest{RunID: "mal", Engine: a.ID(), Model: "fake/standard", Workspace: workspace()}, sink); err != nil {
		return false, "start: " + err.Error()
	}
	evs := waitTerminal(sink, 6*time.Second)
	if len(evs) == 0 || !evs[len(evs)-1].Type.IsTerminal() {
		return false, "malformed output broke the run (no terminal event)"
	}
	// A malformed-output warning must be present (unknown/malformed events do
	// not break the run; they are surfaced as warnings).
	hasWarning := false
	for _, e := range evs {
		if e.Type == protocol.EventWarning {
			hasWarning = true
			break
		}
	}
	if !hasWarning {
		return false, "malformed output produced no warning event"
	}
	return true, "run completed despite malformed output; warning emitted"
}

func (s *Suite) checkCancellation(ctx context.Context) (bool, string) {
	a, cleanup, err := s.makeAdapter(ctx, fake.ScenarioCancellation)
	if err != nil {
		return false, "factory error: " + err.Error()
	}
	defer cleanup()
	sink := &codingagent.SliceSink{}
	handle, err := a.Start(ctx, protocol.AgentRunRequest{RunID: "cancel", Engine: a.ID(), Model: "fake/standard", Workspace: workspace()}, sink)
	if err != nil {
		return false, "start: " + err.Error()
	}
	time.Sleep(150 * time.Millisecond) // let the run reach its hang point
	if err := a.Cancel(ctx, handle); err != nil {
		return false, "cancel: " + err.Error()
	}
	evs := waitTerminal(sink, 3*time.Second)
	if len(evs) == 0 || evs[len(evs)-1].Type != protocol.EventRunCancelled {
		return false, "cancel did not produce run.cancelled (last=" + lastType(evs) + ")"
	}
	return true, "run cancelled; process group terminated"
}

func (s *Suite) checkTimeout(ctx context.Context) (bool, string) {
	// A timeout-scenario run must not hang the suite forever. We run it under a
	// short budget; it must either be cancellable or terminate. We verify that a
	// Cancel ends it within budget.
	a, cleanup, err := s.makeAdapter(ctx, fake.ScenarioTimeout)
	if err != nil {
		return false, "factory error: " + err.Error()
	}
	defer cleanup()
	sink := &codingagent.SliceSink{}
	handle, err := a.Start(ctx, protocol.AgentRunRequest{RunID: "to", Engine: a.ID(), Model: "fake/standard", Workspace: workspace()}, sink)
	if err != nil {
		return false, "start: " + err.Error()
	}
	time.Sleep(120 * time.Millisecond)
	if err := a.Cancel(ctx, handle); err != nil {
		return false, "cancel: " + err.Error()
	}
	// The run must not still be hanging after cancel.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		evs := sink.Events()
		if len(evs) > 0 && evs[len(evs)-1].Type.IsTerminal() {
			return true, "timeout run terminated after cancel"
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false, "timeout run did not terminate after cancel"
}

func (s *Suite) checkQuotaFailure(ctx context.Context) (bool, string) {
	a, cleanup, err := s.makeAdapter(ctx, fake.ScenarioQuotaBeforeEdits)
	if err != nil {
		return false, "factory error: " + err.Error()
	}
	defer cleanup()
	sink := &codingagent.SliceSink{}
	if _, err := a.Start(ctx, protocol.AgentRunRequest{RunID: "quota", Engine: a.ID(), Model: "fake/standard", Workspace: workspace()}, sink); err != nil {
		return false, "start: " + err.Error()
	}
	evs := waitTerminal(sink, 6*time.Second)
	if len(evs) == 0 || evs[len(evs)-1].Type != protocol.EventRunFailed {
		return false, "quota failure did not produce run.failed (last=" + lastType(evs) + ")"
	}
	last := evs[len(evs)-1]
	if last.Failure == nil {
		return false, "run.failed missing failure payload"
	}
	fc := a.ClassifyFailure(last.Failure.ExitCode, evs, "")
	if fc.Class != protocol.FailureProviderQuota {
		return false, "classified as " + string(fc.Class) + ", want PROVIDER_QUOTA"
	}
	if !fc.Failover {
		return false, "quota failure should suggest failover"
	}
	return true, "quota failure classified as PROVIDER_QUOTA (failover)"
}

func (s *Suite) checkResume(ctx context.Context) (bool, string) {
	a, cleanup, err := s.makeAdapter(ctx, fake.ScenarioResume)
	if err != nil {
		return false, "factory error: " + err.Error()
	}
	defer cleanup()
	sink := &codingagent.SliceSink{}
	if _, err := a.Resume(ctx, protocol.ResumeRequest{
		RunID: "resume", Engine: a.ID(), Model: "fake/standard", Workspace: workspace(), SessionID: "fake-session-1",
	}, sink); err != nil {
		return false, "resume: " + err.Error()
	}
	evs := waitTerminal(sink, 6*time.Second)
	if len(evs) == 0 || evs[0].Type != protocol.EventRunResumed {
		return false, "resume must emit run.resumed first, got " + firstType(evs)
	}
	if !evs[len(evs)-1].Type.IsTerminal() {
		return false, "resume did not reach a terminal event"
	}
	return true, "successful resume"
}

func (s *Suite) checkProcessCrash(ctx context.Context) (bool, string) {
	a, cleanup, err := s.makeAdapter(ctx, fake.ScenarioCrash)
	if err != nil {
		return false, "factory error: " + err.Error()
	}
	defer cleanup()
	sink := &codingagent.SliceSink{}
	if _, err := a.Start(ctx, protocol.AgentRunRequest{RunID: "crash", Engine: a.ID(), Model: "fake/standard", Workspace: workspace()}, sink); err != nil {
		return false, "start: " + err.Error()
	}
	evs := waitTerminal(sink, 6*time.Second)
	if len(evs) == 0 || evs[len(evs)-1].Type != protocol.EventRunFailed {
		return false, "crash must produce run.failed (last=" + lastType(evs) + ")"
	}
	last := evs[len(evs)-1]
	if last.Failure == nil {
		return false, "crash run.failed missing failure payload"
	}
	fc := a.ClassifyFailure(last.Failure.ExitCode, evs, "")
	if fc.Class != protocol.FailureEngineCrash {
		return false, "crash classified as " + string(fc.Class) + ", want ENGINE_CRASH"
	}
	return true, "crash classified as ENGINE_CRASH"
}

// helpers

func joinTypes(evs []protocol.NormalizedEvent) string {
	parts := make([]string, len(evs))
	for i, e := range evs {
		parts[i] = string(e.Type)
	}
	return strings.Join(parts, " -> ")
}

func lastType(evs []protocol.NormalizedEvent) string {
	if len(evs) == 0 {
		return "(none)"
	}
	return string(evs[len(evs)-1].Type)
}

func firstType(evs []protocol.NormalizedEvent) string {
	if len(evs) == 0 {
		return "(none)"
	}
	return string(evs[0].Type)
}
