package protocol

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestProtocolVersion(t *testing.T) {
	if ProtocolVersion != 1 {
		t.Fatalf("ProtocolVersion = %d, want 1 (fixed in M2)", ProtocolVersion)
	}
	if !ForgeRange.Supports(ProtocolVersion) {
		t.Fatalf("ForgeRange %s does not support ProtocolVersion %d", ForgeRange, ProtocolVersion)
	}
}

func TestNegotiate(t *testing.T) {
	cases := []struct {
		name    string
		client  ProtocolVersionRange
		server  ProtocolVersionRange
		wantVer int
		wantOK  bool
	}{
		{"exact match", ForgeRange, ProtocolVersionRange{1, 1}, 1, true},
		{"server supports newer", ProtocolVersionRange{1, 1}, ProtocolVersionRange{1, 3}, 1, true},
		{"both support 2-3", ProtocolVersionRange{1, 3}, ProtocolVersionRange{2, 4}, 3, true},
		{"no overlap", ProtocolVersionRange{1, 1}, ProtocolVersionRange{2, 3}, 0, false},
		{"server too old", ProtocolVersionRange{2, 2}, ProtocolVersionRange{1, 1}, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, ok := Negotiate(c.client, c.server)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if ok && v != c.wantVer {
				t.Fatalf("version = %d, want %d", v, c.wantVer)
			}
		})
	}
}

func TestEventTypeIsValidAndTerminal(t *testing.T) {
	// Every §12.4 event must be valid.
	all := []EventType{
		EventRunStarted, EventRunResumed,
		EventMessageStarted, EventMessageDelta, EventMessageCompleted,
		EventToolStarted, EventToolCompleted,
		EventCommandStarted, EventCommandCompleted,
		EventFileChanged, EventUsageUpdated, EventCheckpointCreated,
		EventApprovalRequested, EventWarning,
		EventRunCompleted, EventRunFailed, EventRunCancelled,
	}
	seen := map[EventType]bool{}
	for _, e := range all {
		if !e.IsValid() {
			t.Errorf("event %q not valid", e)
		}
		seen[e] = true
	}
	if len(seen) != len(all) {
		t.Errorf("duplicate event constants in §12.4 set")
	}
	for _, e := range []EventType{EventRunCompleted, EventRunFailed, EventRunCancelled} {
		if !e.IsTerminal() {
			t.Errorf("event %q should be terminal", e)
		}
	}
	for _, e := range []EventType{EventRunStarted, EventMessageDelta, EventFileChanged} {
		if e.IsTerminal() {
			t.Errorf("event %q should not be terminal", e)
		}
	}
	if EventType("bogus").IsValid() {
		t.Errorf("bogus event reported valid")
	}
}

func TestParseEventLineTypedEvents(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	lines := []string{
		`{"type":"run.started","ts":"2026-07-24T12:00:00Z","run_id":"r1","engine":"fake","model":"m/x"}`,
		`{"type":"message.delta","ts":"2026-07-24T12:00:00Z","message":{"message_id":"m1","delta":"hel"}}`,
		`{"type":"message.delta","ts":"2026-07-24T12:00:00Z","message":{"message_id":"m1","delta":"lo"}}`,
		`{"type":"file.changed","ts":"2026-07-24T12:00:00Z","file_change":{"path":"a/b.go","action":"modified","in_scope":true}}`,
		`{"type":"usage.updated","ts":"2026-07-24T12:00:00Z","usage":{"input_tokens":100,"output_tokens":50,"confidence":"PROVIDER_REPORTED"}}`,
		`{"type":"run.completed","ts":"2026-07-24T12:00:00Z","run_id":"r1"}`,
	}
	var got []EventType
	for _, l := range lines {
		ev, err := ParseEventLine([]byte(l))
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", l, err)
		}
		got = append(got, ev.Type)
		if !ev.Timestamp.Equal(now) {
			t.Errorf("timestamp not parsed for %s: got %v", ev.Type, ev.Timestamp)
		}
	}
	want := []EventType{EventRunStarted, EventMessageDelta, EventMessageDelta, EventFileChanged, EventUsageUpdated, EventRunCompleted}
	if !equalSlice(got, want) {
		t.Errorf("types = %v, want %v", got, want)
	}
}

func TestParseEventLineUnknownTypeDoesNotBreak(t *testing.T) {
	// An unknown event type must parse into a warning, not an aborting error.
	ev, err := ParseEventLine([]byte(`{"type":"agent.vibes","ts":"2026-07-24T12:00:00Z"}`))
	if err == nil {
		t.Fatalf("expected a MalformedEventError for unknown type")
	}
	if !IsMalformedEvent(err) {
		t.Fatalf("expected MalformedEventError, got %T %v", err, err)
	}
	var unknown UnknownEventTypeError
	if !errors.As(err, &unknown) {
		t.Fatalf("expected wrapped UnknownEventTypeError, got %v", err)
	}
	if unknown.Type != "agent.vibes" {
		t.Errorf("unknown type = %q", unknown.Type)
	}
	if ev.Type != EventWarning {
		t.Fatalf("event type = %s, want warning", ev.Type)
	}
	if ev.Warning == nil || ev.Warning.Code != "unknown-event-type" {
		t.Fatalf("missing/ wrong warning payload: %+v", ev.Warning)
	}
	if len(ev.Raw) == 0 {
		t.Errorf("raw bytes not preserved for unknown event")
	}
}

func TestParseEventLineMalformedJSON(t *testing.T) {
	ev, err := ParseEventLine([]byte(`{not json`))
	if err == nil || !IsMalformedEvent(err) {
		t.Fatalf("expected MalformedEventError, got %v", err)
	}
	if ev.Type != EventWarning {
		t.Fatalf("type = %s, want warning", ev.Type)
	}
	if ev.Warning == nil || !strings.Contains(ev.Warning.Message, "decode") {
		t.Errorf("unexpected warning: %+v", ev.Warning)
	}
	if string(ev.Raw) != "{not json" {
		t.Errorf("raw not preserved: %q", string(ev.Raw))
	}
}

func TestParseEventLineMissingType(t *testing.T) {
	ev, err := ParseEventLine([]byte(`{"foo":"bar"}`))
	if err == nil || !IsMalformedEvent(err) {
		t.Fatalf("expected MalformedEventError, got %v", err)
	}
	if ev.Type != EventWarning || ev.Warning == nil || ev.Warning.Code != "malformed.missing-type" {
		t.Fatalf("unexpected event: %+v", ev)
	}
}

func TestParseEventLineEmptyAndBlank(t *testing.T) {
	if _, err := ParseEventLine(nil); !errors.Is(err, ErrEmptyLine) {
		t.Errorf("nil line: err = %v, want ErrEmptyLine", err)
	}
	if _, err := ParseEventLine([]byte("\n\r\n")); !errors.Is(err, ErrEmptyLine) {
		t.Errorf("blank line: err = %v, want ErrEmptyLine", err)
	}
}

func TestParseEventLineTrailingNewlineStripped(t *testing.T) {
	ev, err := ParseEventLine([]byte(`{"type":"run.started"}` + "\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != EventRunStarted {
		t.Fatalf("type = %s", ev.Type)
	}
}

func TestNormalizedEventRoundTrip(t *testing.T) {
	src := NormalizedEvent{
		Type:      EventRunFailed,
		Timestamp: time.Now(),
		RunID:     "r1",
		Engine:    "fake",
		Model:     "m/x",
		Failure:   &FailurePayload{Class: FailureProviderQuota, Reason: "nope", ExitCode: 2},
	}
	b, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	ev, err := ParseEventLine(b)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ev.Type != EventRunFailed || ev.Failure == nil || ev.Failure.Class != FailureProviderQuota {
		t.Fatalf("round-trip lost data: %+v", ev)
	}
}

func equalSlice[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
