package imageprovider

import (
	"context"
	"sync"

	"neuroforge/internal/adapter/imageprovider/protocol"
)

// SliceSink collects every image event into a slice. Useful for tests and
// deterministic replay. It never returns an error from OnEvent.
type SliceSink struct {
	mu     sync.Mutex
	events []protocol.Event
}

// OnEvent implements [EventSink].
func (s *SliceSink) OnEvent(_ context.Context, ev protocol.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
	return nil
}

// Events returns a copy of the collected events in arrival order.
func (s *SliceSink) Events() []protocol.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]protocol.Event, len(s.events))
	copy(out, s.events)
	return out
}

// Kinds returns the event kinds in arrival order (convenience for assertions).
func (s *SliceSink) Kinds() []protocol.EventKind {
	evs := s.Events()
	out := make([]protocol.EventKind, len(evs))
	for i, e := range evs {
		out[i] = e.Kind
	}
	return out
}

// ChannelSink forwards events to a buffered channel for concurrent consumers.
// OnEvent blocks when the buffer is full, providing natural backpressure.
type ChannelSink struct {
	ch chan protocol.Event
}

// NewChannelSink returns a ChannelSink with a buffer of the given size (>=1).
func NewChannelSink(buffer int) *ChannelSink {
	if buffer < 1 {
		buffer = 1
	}
	return &ChannelSink{ch: make(chan protocol.Event, buffer)}
}

// OnEvent implements [EventSink]. Blocks if buffer full until ctx is cancelled.
func (s *ChannelSink) OnEvent(ctx context.Context, ev protocol.Event) error {
	select {
	case s.ch <- ev:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Events returns the receive channel.
func (s *ChannelSink) Events() <-chan protocol.Event { return s.ch }

// Close closes the channel. Must be called exactly once when the run ends.
func (s *ChannelSink) Close() { close(s.ch) }

// TeeSink fans out each event to multiple sinks in order. If any sink returns
// an error, TeeSink returns that error and the remaining sinks for that event
// are skipped.
type TeeSink struct {
	sinks []EventSink
}

// NewTeeSink returns a TeeSink over the given sinks (at least one).
func NewTeeSink(sinks ...EventSink) *TeeSink { return &TeeSink{sinks: sinks} }

// OnEvent implements [EventSink].
func (t *TeeSink) OnEvent(ctx context.Context, ev protocol.Event) error {
	for _, s := range t.sinks {
		if err := s.OnEvent(ctx, ev); err != nil {
			return err
		}
	}
	return nil
}
