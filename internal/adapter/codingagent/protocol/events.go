package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// EventType enumerates the normalized event set (spec §12.4). The set is fixed
// for protocol version 1; new additive types may appear in minor versions and
// MUST be ignored — not fatal — by older consumers (see [ParseEventLine]).
type EventType string

const (
	EventRunStarted        EventType = "run.started"
	EventRunResumed        EventType = "run.resumed"
	EventMessageStarted    EventType = "message.started"
	EventMessageDelta      EventType = "message.delta"
	EventMessageCompleted  EventType = "message.completed"
	EventToolStarted       EventType = "tool.started"
	EventToolCompleted     EventType = "tool.completed"
	EventCommandStarted    EventType = "command.started"
	EventCommandCompleted  EventType = "command.completed"
	EventFileChanged       EventType = "file.changed"
	EventUsageUpdated      EventType = "usage.updated"
	EventCheckpointCreated EventType = "checkpoint.created"
	EventApprovalRequested EventType = "approval.requested"
	EventWarning           EventType = "warning"
	EventRunCompleted      EventType = "run.completed"
	EventRunFailed         EventType = "run.failed"
	EventRunCancelled      EventType = "run.cancelled"
)

// IsTerminal reports whether ev is a run-terminal event (after which no further
// events are expected for that run). Used by the conformance suite and
// supervisor to detect a clean end-of-stream.
func (t EventType) IsTerminal() bool {
	switch t {
	case EventRunCompleted, EventRunFailed, EventRunCancelled:
		return true
	}
	return false
}

// IsValid reports whether t is one of the known protocol v1 event types.
func (t EventType) IsValid() bool {
	switch t {
	case EventRunStarted, EventRunResumed,
		EventMessageStarted, EventMessageDelta, EventMessageCompleted,
		EventToolStarted, EventToolCompleted,
		EventCommandStarted, EventCommandCompleted,
		EventFileChanged, EventUsageUpdated, EventCheckpointCreated,
		EventApprovalRequested, EventWarning,
		EventRunCompleted, EventRunFailed, EventRunCancelled:
		return true
	}
	return false
}

// NormalizedEvent is the unit an adapter pushes to an EventSink (spec §12.4).
// Exactly one payload field is populated for typed events; RunStarted/RunResumed
// carry only the top-level Engine/Model/RunID. Raw holds the original bytes for
// events that could not be fully parsed (see [ParseEventLine]).
type NormalizedEvent struct {
	Type      EventType `json:"type"`
	Timestamp time.Time `json:"ts"`
	RunID     string    `json:"run_id,omitempty"`
	Engine    string    `json:"engine,omitempty"`
	Model     string    `json:"model,omitempty"`

	Message    *MessagePayload    `json:"message,omitempty"`
	Tool       *ToolPayload       `json:"tool,omitempty"`
	Command    *CommandPayload    `json:"command,omitempty"`
	FileChange *FileChangePayload `json:"file_change,omitempty"`
	Usage      *UsagePayload      `json:"usage,omitempty"`
	Checkpoint *CheckpointPayload `json:"checkpoint,omitempty"`
	Approval   *ApprovalPayload   `json:"approval,omitempty"`
	Warning    *WarningPayload    `json:"warning,omitempty"`
	Failure    *FailurePayload    `json:"failure,omitempty"`

	// Raw is the original, unparsed JSON for malformed/unknown events. Populated
	// by [ParseEventLine] when parsing fails so the supervisor can persist it as
	// an artifact and classify it (spec: malformed event is saved + classified,
	// it never aborts the whole run).
	Raw json.RawMessage `json:"raw,omitempty"`
}

// MessagePayload covers message.started / message.delta / message.completed.
type MessagePayload struct {
	// MessageID correlates the started/delta/completed triple.
	MessageID string `json:"message_id,omitempty"`
	// Delta is the incremental text fragment (message.delta).
	Delta string `json:"delta,omitempty"`
	// Text is the full message text (message.started/completed).
	Text string `json:"text,omitempty"`
	// Role is the message role (default "user").
	Role string `json:"role,omitempty"`
}

// ToolPayload covers tool.started / tool.completed.
type ToolPayload struct {
	ToolID     string `json:"tool_id,omitempty"`
	Name       string `json:"name,omitempty"`
	Status     string `json:"status,omitempty"`
	Detail     string `json:"detail,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

// CommandPayload covers command.started / command.completed (shell/build/test).
type CommandPayload struct {
	CommandID   string `json:"command_id,omitempty"`
	CommandLine string `json:"command,omitempty"`
	ExitCode    int    `json:"exit_code,omitempty"`
	Success     bool   `json:"success,omitempty"`
	DurationMS  int64  `json:"duration_ms,omitempty"`
}

// FileChangePayload covers file.changed (spec §29.4 audit).
type FileChangePayload struct {
	Path   string `json:"path"`
	Action string `json:"action"` // created | modified | deleted
	// InScope reports whether the change is within the run's [AgentRunRequest.Scope].
	// A change outside scope is a SCOPE_VIOLATION (spec §22.6).
	InScope bool `json:"in_scope"`
}

// UsagePayload covers usage.updated (spec §22 token accounting, §14.4).
type UsagePayload struct {
	InputTokens      int64           `json:"input_tokens,omitempty"`
	OutputTokens     int64           `json:"output_tokens,omitempty"`
	CacheReadTokens  int64           `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64           `json:"cache_write_tokens,omitempty"`
	Cost             float64         `json:"cost,omitempty"`
	Currency         string          `json:"currency,omitempty"`
	Confidence       QuotaConfidence `json:"confidence,omitempty"`
}

// CheckpointPayload covers checkpoint.created (spec §21.3, §5.2).
type CheckpointPayload struct {
	CheckpointID string `json:"checkpoint_id,omitempty"`
	Path         string `json:"path,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// ApprovalPayload covers approval.requested (spec §6.5, destructive_commands ask).
type ApprovalPayload struct {
	Action  string `json:"action"`
	Detail  string `json:"detail,omitempty"`
	Timeout string `json:"timeout,omitempty"`
}

// WarningPayload covers the warning event, including malformed-output notices.
type WarningPayload struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
	// Recoverable reports whether the adapter continued after the condition.
	Recoverable bool `json:"recoverable"`
}

// FailurePayload is attached to run.failed (spec §32).
type FailurePayload struct {
	Class    FailureClass `json:"class"`
	Reason   string       `json:"reason"`
	ExitCode int          `json:"exit_code,omitempty"`
}

// ParseEventLine decodes one JSONL line into a NormalizedEvent. It is the
// canonical entry point for declarative-adapter output (spec §13.1 --output
// jsonl) and the JSON-RPC event notification body.
//
// Robustness contract (spec): unknown event types and malformed JSON MUST NOT
// break the run. On a recoverable parse problem the function returns a non-nil
// event of type [EventWarning] carrying the offending raw bytes, plus a
// [MalformedEventError] so the caller can classify/save the artifact. Only
// truly unrecoverable I/O/decode errors are returned as the second value.
func ParseEventLine(line []byte) (NormalizedEvent, error) {
	line = trimJSONL(line)
	if len(line) == 0 {
		return NormalizedEvent{}, ErrEmptyLine
	}

	// Decode just the type discriminator first, tolerating unknown fields.
	var head struct {
		Type string          `json:"type"`
		Raw  json.RawMessage `json:"-"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		// Hard malformed JSON: wrap the raw bytes in a warning event and report
		// a MalformedEventError. The caller persists raw as an artifact.
		ev := NormalizedEvent{
			Type:      EventWarning,
			Timestamp: time.Now(),
			Warning: &WarningPayload{
				Code:        "malformed.json",
				Message:     "failed to decode event JSON: " + err.Error(),
				Recoverable: true,
			},
			Raw: append([]byte(nil), line...),
		}
		return ev, MalformedEventError{Raw: line, Err: err}
	}

	raw := append(json.RawMessage(nil), line...)
	if head.Type == "" {
		ev := NormalizedEvent{
			Type:      EventWarning,
			Timestamp: time.Now(),
			Warning: &WarningPayload{
				Code:        "malformed.missing-type",
				Message:     "event object has no \"type\" field",
				Recoverable: true,
			},
			Raw: raw,
		}
		return ev, MalformedEventError{Raw: line, Err: errors.New("missing type field")}
	}

	et := EventType(head.Type)
	if !et.IsValid() {
		// Unknown event type: forward as a warning but keep going.
		ev := NormalizedEvent{
			Type:      EventWarning,
			Timestamp: time.Now(),
			Warning: &WarningPayload{
				Code:        "unknown-event-type",
				Message:     fmt.Sprintf("unknown event type %q; ignored", head.Type),
				Recoverable: true,
			},
			Raw: raw,
		}
		return ev, MalformedEventError{Raw: line, Err: UnknownEventTypeError{Type: head.Type}}
	}

	// Fully decode the typed event.
	ev := NormalizedEvent{Type: et, Raw: raw}
	if err := json.Unmarshal(line, &ev); err != nil {
		ev = NormalizedEvent{
			Type:      EventWarning,
			Timestamp: time.Now(),
			Warning: &WarningPayload{
				Code:        "malformed.payload",
				Message:     "failed to decode event payload: " + err.Error(),
				Recoverable: true,
			},
			Raw: raw,
		}
		return ev, MalformedEventError{Raw: line, Err: err}
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now()
	}
	return ev, nil
}

// trimJSONL removes a single trailing newline/carriage-return from a JSONL line.
func trimJSONL(line []byte) []byte {
	for len(line) > 0 && (line[len(line)-1] == '\n' || line[len(line)-1] == '\r') {
		line = line[:len(line)-1]
	}
	return line
}

// ErrEmptyLine is returned by [ParseEventLine] for a blank line.
var ErrEmptyLine = errors.New("empty event line")

// MalformedEventError wraps a recoverable parse failure with the offending raw
// bytes. The accompanying event is always a non-nil [EventWarning].
type MalformedEventError struct {
	Raw []byte
	Err error
}

func (e MalformedEventError) Error() string { return "malformed event: " + e.Err.Error() }
func (e MalformedEventError) Unwrap() error { return e.Err }

// IsMalformedEvent reports whether err is (or wraps) a [MalformedEventError].
func IsMalformedEvent(err error) bool {
	var m MalformedEventError
	return errors.As(err, &m)
}

// UnknownEventTypeError indicates an event whose "type" is not part of protocol
// v1. Consumers must ignore it, not fail (spec: unknown events do not break a
// run).
type UnknownEventTypeError struct {
	Type string
}

func (e UnknownEventTypeError) Error() string { return "unknown event type: " + e.Type }
