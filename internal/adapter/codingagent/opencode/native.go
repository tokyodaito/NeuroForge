package opencode

import (
	"encoding/json"
	"strings"
)

// Native event type names emitted by real `opencode run --format json` output
// (observed against the installed opencode CLI). Each line is a JSON object
// with a "type" discriminator and a "part" payload.
const (
	nativeTypeStepStart  = "step_start"
	nativeTypeText       = "text"
	nativeTypeStepFinish = "step_finish"
)

// NativeEvent is one event from opencode's native `--format json` JSONL
// stream. Unknown fields are ignored; unknown event types parse fine but carry
// no text.
type NativeEvent struct {
	// Type is the event discriminator ("step_start", "text", "step_finish", ...).
	Type string `json:"type"`
	// Timestamp is the engine-side wall clock in milliseconds since the epoch
	// (0 when absent).
	Timestamp int64 `json:"timestamp"`
	// Part is the event payload; only text/usage-bearing fields are modelled.
	Part NativePart `json:"part"`
}

// NativePart is the payload of a [NativeEvent]. For "text" events, Text holds
// one chunk of assistant output; for "step_finish" events, Tokens and Cost
// carry the step's usage accounting.
type NativePart struct {
	// Type mirrors the event type in hyphenated form ("step-start", "text",
	// "step-finish"). Informational only — NativeEvent.Type is authoritative.
	Type string `json:"type"`
	// MessageID correlates the parts of one assistant message.
	MessageID string `json:"messageID"`
	// Text is the assistant text chunk (present only on text events).
	Text string `json:"text"`
	// Reason is the step finish reason ("stop", "tool-calls", ...).
	Reason string `json:"reason"`
	// Tokens is the step's token accounting (present only on step-finish).
	Tokens NativeTokens `json:"tokens"`
	// Cost is the step's cost in USD (present only on step-finish).
	Cost float64 `json:"cost"`
}

// NativeTokens is the token accounting of a native step-finish part.
type NativeTokens struct {
	Total     int64 `json:"total"`
	Input     int64 `json:"input"`
	Output    int64 `json:"output"`
	Reasoning int64 `json:"reasoning"`
	Cache     struct {
		Write int64 `json:"write"`
		Read  int64 `json:"read"`
	} `json:"cache"`
}

// ParseNativeEvent parses one line of opencode's native JSONL output. It
// returns ok=false for empty, malformed, or non-object lines — callers skip
// those (tolerant parsing: the stream may interleave warnings or tool noise).
func ParseNativeEvent(line []byte) (NativeEvent, bool) {
	trimmed := trimSpaceAndCR(line)
	if len(trimmed) == 0 {
		return NativeEvent{}, false
	}
	var ev NativeEvent
	if err := json.Unmarshal(trimmed, &ev); err != nil {
		return NativeEvent{}, false
	}
	return ev, true
}

// IsText reports whether the event carries assistant text.
func (e NativeEvent) IsText() bool {
	return e.Type == nativeTypeText
}

// ExtractAssistantText parses real `opencode run --format json` JSONL output
// and returns the concatenated assistant text chunks in stream order.
// Malformed or unknown lines are skipped; events without text contribute
// nothing.
func ExtractAssistantText(jsonl string) string {
	var b strings.Builder
	sc := newJSONLScanner(strings.NewReader(jsonl))
	for {
		line, hasMore := sc.next()
		if len(line) > 0 {
			if ev, ok := ParseNativeEvent(line); ok && ev.IsText() {
				b.WriteString(ev.Part.Text)
			}
		}
		if !hasMore {
			break
		}
	}
	return b.String()
}
