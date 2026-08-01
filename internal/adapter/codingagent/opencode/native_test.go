package opencode

import (
	"strings"
	"testing"
)

// realFixture is captured (and redacted) from an actual
// `opencode run --format json` invocation against the installed CLI:
// event types step_start / text / step_finish, text carried in part.text.
const realFixture = `{"type":"step_start","timestamp":1785589490597,"sessionID":"ses_REDACTED","part":{"id":"prt_REDACTED","messageID":"msg_REDACTED","sessionID":"ses_REDACTED","type":"step-start"}}
{"type":"text","timestamp":1785589490598,"sessionID":"ses_REDACTED","part":{"id":"prt_REDACTED","messageID":"msg_REDACTED","sessionID":"ses_REDACTED","type":"text","text":"SCHEMA_PROBE","time":{"start":1785589490571,"end":1785589490575}}}
{"type":"step_finish","timestamp":1785589490598,"sessionID":"ses_REDACTED","part":{"id":"prt_REDACTED","reason":"stop","messageID":"msg_REDACTED","sessionID":"ses_REDACTED","type":"step-finish","tokens":{"total":7097,"input":273,"output":6,"reasoning":34,"cache":{"write":0,"read":6784}},"cost":0}}
`

func TestExtractAssistantText_RealFixture(t *testing.T) {
	got := ExtractAssistantText(realFixture)
	if got != "SCHEMA_PROBE" {
		t.Fatalf("ExtractAssistantText = %q, want %q", got, "SCHEMA_PROBE")
	}
}

func TestExtractAssistantText_MultipleChunksConcatenatedInOrder(t *testing.T) {
	in := `{"type":"step_start","timestamp":1,"sessionID":"s","part":{"id":"p1","type":"step-start"}}
{"type":"text","timestamp":2,"sessionID":"s","part":{"id":"p2","type":"text","text":"Hello, "}}
{"type":"text","timestamp":3,"sessionID":"s","part":{"id":"p3","type":"text","text":"world"}}
{"type":"text","timestamp":4,"sessionID":"s","part":{"id":"p4","type":"text","text":"!"}}
{"type":"step_finish","timestamp":5,"sessionID":"s","part":{"id":"p5","type":"step-finish","reason":"stop"}}
`
	got := ExtractAssistantText(in)
	if got != "Hello, world!" {
		t.Fatalf("ExtractAssistantText = %q, want %q", got, "Hello, world!")
	}
}

func TestExtractAssistantText_ToleratesGarbageAndUnknownEvents(t *testing.T) {
	in := strings.Join([]string{
		``,                                   // empty line
		`not json at all`,                    // malformed
		`{"type":"text","part":{"text":"A"}`, // truncated JSON
		`[1,2,3]`,                            // valid JSON, wrong shape
		`{"type":"tool_use","part":{"type":"tool_use","tool":"bash"}}`, // unknown event
		`{"type":"text","part":{"type":"text","text":"B"}}`,
		`{"type":"step_finish","part":{"type":"step-finish"}}`,
	}, "\n")
	got := ExtractAssistantText(in)
	if got != "B" {
		t.Fatalf("ExtractAssistantText = %q, want %q", got, "B")
	}
}

func TestExtractAssistantText_EmptyStream(t *testing.T) {
	if got := ExtractAssistantText(""); got != "" {
		t.Fatalf("ExtractAssistantText(\"\") = %q, want \"\"", got)
	}
	if got := ExtractAssistantText("\n\n  \n"); got != "" {
		t.Fatalf("ExtractAssistantText(blank lines) = %q, want \"\"", got)
	}
}

func TestExtractAssistantText_NoTrailingNewline(t *testing.T) {
	got := ExtractAssistantText(`{"type":"text","part":{"text":"tail"}}`)
	if got != "tail" {
		t.Fatalf("ExtractAssistantText = %q, want %q", got, "tail")
	}
}

func TestExtractAssistantText_CRLFAndBOM(t *testing.T) {
	in := "\xEF\xBB\xBF{\"type\":\"text\",\"part\":{\"text\":\"boom\"}}\r\n"
	if got := ExtractAssistantText(in); got != "boom" {
		t.Fatalf("ExtractAssistantText = %q, want %q", got, "boom")
	}
}

func TestParseNativeEvent(t *testing.T) {
	ev, ok := ParseNativeEvent([]byte(`{"type":"step_finish","part":{"type":"step-finish","reason":"stop"}}`))
	if !ok {
		t.Fatal("ParseNativeEvent returned ok=false for a valid event")
	}
	if ev.Type != "step_finish" {
		t.Fatalf("ev.Type = %q, want %q", ev.Type, "step_finish")
	}
	if ev.IsText() {
		t.Fatal("step_finish must not be a text event")
	}
}

func TestParseNativeEvent_RejectsBadLines(t *testing.T) {
	for _, line := range []string{"", "   ", "nope", "{", `["text"]`} {
		if _, ok := ParseNativeEvent([]byte(line)); ok {
			t.Fatalf("ParseNativeEvent(%q) = ok, want not-ok", line)
		}
	}
}
