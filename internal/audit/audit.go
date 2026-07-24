package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"neuroforge/internal/storage"
)

// Well-known scopes. scope/scope_id identify what an event belongs to.
const (
	ScopeSystem  = "system"
	ScopeProject = "project"
	ScopeTask    = "task"

	ScopeGlobal = "global" // scope_id for system-wide events

	ActorUser   = "user"
	ActorDaemon = "daemon"
	ActorSystem = "system"
)

// Event is the domain-level audit event. The Recorder converts it to a durable
// [storage.AuditEvent] row.
type Event struct {
	Type      string         // e.g. "daemon.started"
	Scope     string         // ScopeSystem | ScopeProject | ScopeTask
	ScopeID   string         // "global" | project id | task id
	Actor     string         // ActorUser | ActorDaemon | ActorSystem
	Payload   map[string]any // structured detail; serialized to JSON
	Timestamp time.Time      // zero -> time.Now()
}

// Recorder appends audit events to durable storage and reads them back. It is
// the only legitimate writer of the audit trail.
type Recorder struct {
	store  AuditStore
	logger *slog.Logger
}

// AuditAppender is the write capability of an [AuditStore]. Both *storage.DB
// and *storage.Tx satisfy it, so an audit event can be appended as part of a
// caller's storage transaction (spec §11.4, ADR-0003).
type AuditAppender interface {
	AppendAuditEvent(ctx context.Context, e storage.AuditEvent) (int64, error)
}

// AuditStore is the subset of storage that the recorder depends on. *storage.DB
// satisfies it.
type AuditStore interface {
	AuditAppender
	ListAuditEvents(ctx context.Context, f storage.AuditFilter) ([]storage.AuditEvent, error)
}

// NewRecorder wraps an AuditStore.
func NewRecorder(store AuditStore, logger *slog.Logger) *Recorder {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	}
	return &Recorder{store: store, logger: logger}
}

// Record appends one event, filling defaults for scope/scope_id/actor/ts. It
// returns the assigned sequence id.
func (r *Recorder) Record(ctx context.Context, e Event) (int64, error) {
	return r.recordInto(ctx, r.store, e)
}

// RecordTx appends one event into the provided [AuditAppender] (typically a
// *storage.Tx) instead of the recorder's own store, so the audit append shares
// the caller's SQLite transaction. This keeps a state mutation and the audit
// event that records it atomic: both commit together or roll back together
// (spec §11.4, ADR-0003).
func (r *Recorder) RecordTx(ctx context.Context, a AuditAppender, e Event) (int64, error) {
	if a == nil {
		a = r.store
	}
	return r.recordInto(ctx, a, e)
}

func (r *Recorder) recordInto(ctx context.Context, a AuditAppender, e Event) (int64, error) {
	if e.Type == "" {
		return 0, fmt.Errorf("audit: event type is required")
	}
	if e.Scope == "" {
		e.Scope = ScopeSystem
	}
	if e.ScopeID == "" {
		e.ScopeID = ScopeGlobal
	}
	if e.Actor == "" {
		e.Actor = ActorDaemon
	}
	ts := e.Timestamp
	if ts.IsZero() {
		ts = time.Now().UTC()
	}

	payload := "{}"
	if e.Payload != nil {
		b, err := json.Marshal(e.Payload)
		if err != nil {
			return 0, fmt.Errorf("audit: marshal payload for %q: %w", e.Type, err)
		}
		payload = string(b)
	}

	id, err := a.AppendAuditEvent(ctx, storage.AuditEvent{
		Timestamp: ts.Format(time.RFC3339Nano),
		Scope:     e.Scope,
		ScopeID:   e.ScopeID,
		Type:      e.Type,
		Actor:     e.Actor,
		Payload:   payload,
	})
	if err != nil {
		return 0, err
	}
	r.logger.Debug("audit event recorded", "type", e.Type, "scope", e.Scope, "scope_id", e.ScopeID, "id", id)
	return id, nil
}

// History reconstructs the chronological event history for a scope id (e.g. a
// task id or "global"). limit<=0 uses a default cap.
func (r *Recorder) History(ctx context.Context, scopeID string, limit int) ([]storage.AuditEvent, error) {
	return r.store.ListAuditEvents(ctx, storage.AuditFilter{ScopeID: scopeID, Limit: limit})
}

// Filter returns events matching the given filter (passed through to storage).
func (r *Recorder) Filter(ctx context.Context, f storage.AuditFilter) ([]storage.AuditEvent, error) {
	return r.store.ListAuditEvents(ctx, f)
}

// Payload is a small helper to build a payload map from key/value pairs.
func Payload(kvs ...any) map[string]any {
	m := make(map[string]any, len(kvs)/2)
	for i := 0; i+1 < len(kvs); i += 2 {
		key, _ := kvs[i].(string)
		m[key] = kvs[i+1]
	}
	return m
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
