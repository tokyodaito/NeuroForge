package codingagent

import (
	"context"
	"errors"
	"sync"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// SliceSink collects every event into a slice. Useful for tests and
// deterministic replay. It never returns an error from OnEvent.
type SliceSink struct {
	mu     sync.Mutex
	events []protocol.NormalizedEvent
}

// OnEvent implements [EventSink].
func (s *SliceSink) OnEvent(_ context.Context, ev protocol.NormalizedEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
	return nil
}

// Events returns a copy of the collected events in arrival order.
func (s *SliceSink) Events() []protocol.NormalizedEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]protocol.NormalizedEvent, len(s.events))
	copy(out, s.events)
	return out
}

// Types returns the event types in arrival order (convenience for assertions).
func (s *SliceSink) Types() []protocol.EventType {
	evs := s.Events()
	out := make([]protocol.EventType, len(evs))
	for i, e := range evs {
		out[i] = e.Type
	}
	return out
}

// ChannelSink forwards events to a buffered channel. It allows callers to
// consume the stream concurrently. The channel is closed by [ChannelSink.Close]
// (typically after the run finishes). OnEvent blocks when the buffer is full,
// providing natural backpressure.
type ChannelSink struct {
	ch chan protocol.NormalizedEvent
}

// NewChannelSink returns a ChannelSink with a buffer of the given size (must be
// >= 1).
func NewChannelSink(buffer int) *ChannelSink {
	if buffer < 1 {
		buffer = 1
	}
	return &ChannelSink{ch: make(chan protocol.NormalizedEvent, buffer)}
}

// OnEvent implements [EventSink]. It blocks if the buffer is full until the
// context is cancelled (then returns ctx.Err()).
func (s *ChannelSink) OnEvent(ctx context.Context, ev protocol.NormalizedEvent) error {
	select {
	case s.ch <- ev:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Events returns the receive channel.
func (s *ChannelSink) Events() <-chan protocol.NormalizedEvent { return s.ch }

// Close closes the channel. It must be called exactly once when the run ends.
func (s *ChannelSink) Close() { close(s.ch) }

// TeeSink fans out each event to multiple sinks in order. If any sink returns an
// error, TeeSink stops and returns that error (the remaining sinks for that
// event are skipped). This mirrors the supervisor's "abort on consumer error"
// contract.
type TeeSink struct {
	sinks []EventSink
}

// NewTeeSink returns a TeeSink over the given sinks (at least one).
func NewTeeSink(sinks ...EventSink) *TeeSink {
	return &TeeSink{sinks: sinks}
}

// OnEvent implements [EventSink].
func (t *TeeSink) OnEvent(ctx context.Context, ev protocol.NormalizedEvent) error {
	for _, s := range t.sinks {
		if err := s.OnEvent(ctx, ev); err != nil {
			return err
		}
	}
	return nil
}

// ErrSinkClosed is returned by a sink wrapper when the run has been aborted and
// no further events should be accepted.
var ErrSinkClosed = errors.New("codingagent: event sink closed")
