package codingagent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// stubAdapter is a minimal Adapter for registry/event-sink tests.
type stubAdapter struct {
	id string
}

func (s *stubAdapter) ID() string { return s.id }
func (s *stubAdapter) Detect(context.Context) protocol.DetectionResult {
	return protocol.DetectionResult{Installed: true}
}
func (s *stubAdapter) Version(context.Context) protocol.VersionResult {
	return protocol.VersionResult{ProtocolVersion: protocol.ProtocolVersion}
}
func (s *stubAdapter) Health(context.Context, protocol.Account) protocol.HealthResult {
	return protocol.HealthResult{Status: protocol.HealthOK}
}
func (s *stubAdapter) Capabilities(context.Context) protocol.AgentCapabilities {
	return protocol.AgentCapabilities{HeadlessMode: true}
}
func (s *stubAdapter) ListModels(context.Context, protocol.Account) ([]protocol.ModelDescriptor, error) {
	return nil, nil
}
func (s *stubAdapter) InspectQuota(context.Context, protocol.Account) protocol.QuotaSnapshot {
	return protocol.QuotaSnapshot{Confidence: protocol.QuotaConfUnknown, State: protocol.QuotaStateUnknown}
}
func (s *stubAdapter) Start(context.Context, protocol.AgentRunRequest, EventSink) (protocol.RunHandle, error) {
	return protocol.RunHandle{Engine: s.id}, nil
}
func (s *stubAdapter) Resume(context.Context, protocol.ResumeRequest, EventSink) (protocol.RunHandle, error) {
	return protocol.RunHandle{Engine: s.id}, nil
}
func (s *stubAdapter) SendMessage(context.Context, protocol.RunHandle, protocol.AgentMessage) error {
	return nil
}
func (s *stubAdapter) Cancel(context.Context, protocol.RunHandle) error { return nil }
func (s *stubAdapter) ClassifyFailure(exitCode int, _ []protocol.NormalizedEvent, _ string) protocol.FailureClassification {
	return DefaultClassify(exitCode, nil, "")
}

func TestRegistryRegisterAndLookup(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&stubAdapter{id: "a"}, 10); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(&stubAdapter{id: "b"}, 20); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Lookup("a"); !err {
		t.Error("a not found")
	}
	if r.Len() != 2 {
		t.Errorf("Len = %d, want 2", r.Len())
	}
	// Display order: priority desc (b before a).
	if got := r.IDs(); len(got) != 2 || got[0] != "b" || got[1] != "a" {
		t.Errorf("IDs = %v, want [b a]", got)
	}
}

func TestRegistryRejectsDuplicateAndEmpty(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(&stubAdapter{id: "x"}, 0)
	if err := r.Register(&stubAdapter{id: "x"}, 0); err == nil {
		t.Error("duplicate register should fail")
	}
	if err := r.Register(&stubAdapter{id: ""}, 0); err == nil {
		t.Error("empty-id register should fail")
	}
	if err := r.Register(nil, 0); err == nil {
		t.Error("nil register should fail")
	}
}

func TestRegistryIDsAlphabeticalOnTie(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(&stubAdapter{id: "zeta"}, 5)
	r.MustRegister(&stubAdapter{id: "alpha"}, 5)
	if got := r.IDs(); got[0] != "alpha" || got[1] != "zeta" {
		t.Errorf("tie order wrong: %v", got)
	}
}

func TestSliceSinkOrder(t *testing.T) {
	s := &SliceSink{}
	ctx := context.Background()
	for _, et := range []protocol.EventType{protocol.EventRunStarted, protocol.EventMessageDelta, protocol.EventRunCompleted} {
		if err := s.OnEvent(ctx, protocol.NormalizedEvent{Type: et}); err != nil {
			t.Fatal(err)
		}
	}
	want := []protocol.EventType{protocol.EventRunStarted, protocol.EventMessageDelta, protocol.EventRunCompleted}
	got := s.Types()
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("idx %d: got %s want %s", i, got[i], want[i])
		}
	}
}

func TestSliceSinkConcurrency(t *testing.T) {
	// SliceSink must be safe under concurrent OnEvent (race detector enforces).
	s := &SliceSink{}
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.OnEvent(ctx, protocol.NormalizedEvent{Type: protocol.EventMessageDelta})
		}()
	}
	wg.Wait()
	if len(s.Events()) != 50 {
		t.Errorf("collected %d, want 50", len(s.Events()))
	}
}

func TestChannelSinkBackpressureAndCancel(t *testing.T) {
	s := NewChannelSink(1)
	ctx, cancel := context.WithCancel(context.Background())
	// Fill the buffer.
	if err := s.OnEvent(ctx, protocol.NormalizedEvent{Type: protocol.EventRunStarted}); err != nil {
		t.Fatal(err)
	}
	// Next send should block; cancel the context to unblock with an error.
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if err := s.OnEvent(ctx, protocol.NormalizedEvent{Type: protocol.EventMessageDelta}); err == nil {
		t.Error("expected context cancellation error")
	}
	s.Close()
	// Drain what was buffered.
	var n int
	for range s.Events() {
		n++
	}
	if n != 1 {
		t.Errorf("drained %d, want 1", n)
	}
}

func TestTeeSinkFanoutAndErrorStop(t *testing.T) {
	a, b := &SliceSink{}, &SliceSink{}
	tee := NewTeeSink(a, b)
	ctx := context.Background()
	_ = tee.OnEvent(ctx, protocol.NormalizedEvent{Type: protocol.EventRunStarted})
	if len(a.Events()) != 1 || len(b.Events()) != 1 {
		t.Fatal("tee did not fan out")
	}
	// A sink that errors should stop the tee and return the error.
	stop := SinkFunc(func(context.Context, protocol.NormalizedEvent) error {
		return errors.New("boom")
	})
	tee2 := NewTeeSink(a, stop, b)
	if err := tee2.OnEvent(ctx, protocol.NormalizedEvent{Type: protocol.EventMessageDelta}); err == nil {
		t.Error("expected error to propagate")
	}
	// b should NOT have received the second event (error short-circuits).
	if len(b.Events()) != 1 {
		t.Errorf("b collected %d, want 1 (error short-circuit)", len(b.Events()))
	}
}

func TestSinkFuncAdapter(t *testing.T) {
	called := false
	f := SinkFunc(func(context.Context, protocol.NormalizedEvent) error {
		called = true
		return nil
	})
	var _ EventSink = f
	_ = f.OnEvent(context.Background(), protocol.NormalizedEvent{})
	if !called {
		t.Error("SinkFunc not invoked")
	}
}
