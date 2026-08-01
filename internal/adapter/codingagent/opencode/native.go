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
	// Part is the event payload; only text-bearing fields are modelled.
	Part NativePart `json:"part"`
}

// NativePart is the payload of a [NativeEvent]. For "text" events, Text holds
// one chunk of assistant output.
type NativePart struct {
	// Type mirrors the event type in hyphenated form ("step-start", "text",
	// "step-finish"). Informational only — NativeEvent.Type is authoritative.
	Type string `json:"type"`
	// Text is the assistant text chunk (present only on text events).
	Text string `json:"text"`
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
