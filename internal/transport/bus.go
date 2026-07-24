package transport

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"sync/atomic"
	"time"
)

// MinTokenLen is the minimum acceptable length for an API auth token. Tokens
// shorter than this are rejected by NewServer to avoid weak credentials.
const MinTokenLen = 32

// GenerateToken returns a cryptographically random hex token suitable for use
// as the per-daemon loopback API credential. It never leaves loopback and is
// never transmitted over an unencrypted channel (ADR-0004).
func GenerateToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Event is a single normalized wire event delivered to subscribers (and streamed
// over SSE). Seq is a per-bus monotonic sequence assigned at publish time.
type Event struct {
	Seq  int64  `json:"seq"`
	Type string `json:"type"`
	Ts   string `json:"ts"` // RFC3339Nano UTC
	Data any    `json:"data,omitempty"`
}

// Bus is the internal event bus: an in-memory, non-blocking, broadcast pub/sub.
// It delivers live events to subscribers (e.g. the SSE stream). Historical
// durability is the job of the audit log, not the bus — the bus is ephemeral.
type Bus struct {
	mu     sync.RWMutex
	subs   map[uint64]chan Event
	nextID uint64
	seq    atomic.Int64
	closed atomic.Bool
}

// NewBus returns an empty event bus.
func NewBus() *Bus {
	return &Bus{subs: make(map[uint64]chan Event)}
}

// Publish assigns the next sequence number and timestamp, then broadcasts the
// event to all subscribers. Sends are non-blocking: a slow subscriber whose
// buffer is full misses the event (live-event semantics; this never blocks the
// publisher or other subscribers).
func (b *Bus) Publish(typ string, data any) Event {
	evt := Event{
		Seq:  b.seq.Add(1),
		Type: typ,
		Ts:   time.Now().UTC().Format(time.RFC3339Nano),
		Data: data,
	}
	if b.closed.Load() {
		return evt
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- evt:
		default:
			// subscriber buffer full; drop to keep the bus non-blocking
		}
	}
	return evt
}

// Subscribe returns a receive channel for events and a cancel function that
// removes the subscription. The channel is buffered with buf slots (a default
// is used if buf<=0). Cancel must be called to avoid leaking the subscription.
func (b *Bus) Subscribe(buf int) (<-chan Event, func()) {
	if buf <= 0 {
		buf = defaultSubscribeBuf
	}
	ch := make(chan Event, buf)
	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.subs[id] = ch
	b.mu.Unlock()

	cancel := func() {
		b.mu.Lock()
		if c, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(c)
		}
		b.mu.Unlock()
	}
	return ch, cancel
}

// Len returns the number of active subscribers (for diagnostics/tests).
func (b *Bus) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

// Close drops all subscribers. Further Publish calls are no-ops.
func (b *Bus) Close() {
	if !b.closed.CompareAndSwap(false, true) {
		return
	}
	b.mu.Lock()
	for id, ch := range b.subs {
		close(ch)
		delete(b.subs, id)
	}
	b.mu.Unlock()
}

const defaultSubscribeBuf = 64
