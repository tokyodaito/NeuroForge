package daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
	"neuroforge/internal/runapp"
	"neuroforge/internal/transport"
)

// usageRecorder is a test UsageSink capturing every recorded event.
type usageRecorder struct {
	mu     sync.Mutex
	events []runapp.UsageEvent
}

func (u *usageRecorder) RecordUsage(_ context.Context, e runapp.UsageEvent) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.events = append(u.events, e)
	return nil
}

func (u *usageRecorder) all() []runapp.UsageEvent {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]runapp.UsageEvent(nil), u.events...)
}

// TestPipelineUsage_RecordedFromAgentEvents proves (M2) that usage.updated
// events emitted by the engine on the pipeline path are persisted via the
// UsageSink — they were silently discarded before (PipelineDeps.Usage was
// dead wiring).
func TestPipelineUsage_RecordedFromAgentEvents(t *testing.T) {
	sink := &usageRecorder{}
	adapter := newScriptedCodingAdapter(func(_ context.Context, _ int, req protocol.AgentRunRequest, emit func(protocol.NormalizedEvent)) {
		emit(protocol.NormalizedEvent{Type: protocol.EventRunStarted})
		emit(protocol.NormalizedEvent{Type: protocol.EventUsageUpdated, Usage: &protocol.UsagePayload{
			InputTokens: 120, OutputTokens: 45, CacheReadTokens: 12, Cost: 0.0042,
		}})
		writeCommitBehavior(map[string]string{"RESULT.md": "usage\n"})(context.Background(), 1, req, emit)
	})
	env := newFaultEnv(t, faultDeps{adapter: adapter, usage: sink})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	dto, err := env.svc.RunPipeline(ctx, transport.PipelineRunRequest{
		ProjectID:   env.projID,
		Description: "usage recording",
		Engine:      "fake",
		Model:       "fake/standard",
	})
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if dto.RunState != "completed" {
		t.Fatalf("run_state = %s, want completed", dto.RunState)
	}

	events := sink.all()
	if len(events) == 0 {
		t.Fatal("no usage events recorded on the pipeline path (M2 regression)")
	}
	var ev runapp.UsageEvent
	found := false
	for _, e := range events {
		if e.InputTokens == 120 {
			ev = e
			found = true
		}
	}
	if !found {
		t.Fatalf("scripted usage event (120 input tokens) not recorded: %+v", events)
	}
	if ev.TaskID != dto.TaskID {
		t.Errorf("usage task id = %q, want %q", ev.TaskID, dto.TaskID)
	}
	if ev.ProjectID != env.projID {
		t.Errorf("usage project id = %q, want %q", ev.ProjectID, env.projID)
	}
	if ev.OutputTokens != 45 || ev.CachedInputTokens != 12 {
		t.Errorf("usage tokens = in:%d out:%d cached:%d, want 120/45/12",
			ev.InputTokens, ev.OutputTokens, ev.CachedInputTokens)
	}
	if ev.CostUSD != 0.0042 {
		t.Errorf("usage cost = %v, want 0.0042", ev.CostUSD)
	}
	if ev.Provider != "fake" || ev.Model != "fake/standard" {
		t.Errorf("usage provider/model = %s/%s, want fake/fake-standard", ev.Provider, ev.Model)
	}
}
