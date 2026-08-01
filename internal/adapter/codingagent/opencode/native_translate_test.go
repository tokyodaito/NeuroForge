package opencode

import (
	"os"
	"strings"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// nativeTranscript is a full native `opencode run --format json` session in the
// v1.x schema: step_start, three assistant text chunks, step_finish carrying
// token accounting + cost, and NO terminal event (completion must come from
// exit-code synthesis).
const nativeTranscript = `{"type":"step_start","timestamp":1785589490597,"sessionID":"ses_X","part":{"id":"prt_1","messageID":"msg_1","sessionID":"ses_X","type":"step-start"}}
{"type":"text","timestamp":1785589490600,"sessionID":"ses_X","part":{"id":"prt_2","messageID":"msg_1","sessionID":"ses_X","type":"text","text":"Hello, ","time":{"start":1785589490598,"end":1785589490599}}}
{"type":"text","timestamp":1785589490601,"sessionID":"ses_X","part":{"id":"prt_3","messageID":"msg_1","sessionID":"ses_X","type":"text","text":"native","time":{"start":1785589490599,"end":1785589490600}}}
{"type":"text","timestamp":1785589490602,"sessionID":"ses_X","part":{"id":"prt_4","messageID":"msg_1","sessionID":"ses_X","type":"text","text":" world","time":{"start":1785589490600,"end":1785589490601}}}
{"type":"step_finish","timestamp":1785589490610,"sessionID":"ses_X","part":{"id":"prt_5","reason":"stop","messageID":"msg_1","sessionID":"ses_X","type":"step-finish","tokens":{"total":7097,"input":273,"output":6,"reasoning":34,"cache":{"write":0,"read":6784}},"cost":0.0012}}
`

func collectByType(evs []protocol.NormalizedEvent, t protocol.EventType) []protocol.NormalizedEvent {
	var out []protocol.NormalizedEvent
	for _, e := range evs {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// TestRunNativeTranscriptTranslated drives a full native-schema transcript
// through the adapter's run machinery and asserts the normalized projection:
// assistant text arrives as ordered message.delta events, step_finish arrives
// as one usage.updated with the token figures, NO warnings are emitted for
// native lines, no malformed artifacts are written, and the terminal
// run.completed is still synthesized from the exit code.
func TestRunNativeTranscriptTranslated(t *testing.T) {
	artDir := t.TempDir()
	a := stubAdapter(nativeTranscript, "", 0, false, artDir)
	sink, _ := startRun(t, a, baseReq())
	evs := waitTerminal(t, sink, 3*time.Second)

	if len(evs) == 0 || evs[0].Type != protocol.EventRunStarted {
		t.Fatalf("first event = %v, want run.started", typesOf(evs))
	}
	if lastType(evs) != protocol.EventRunCompleted {
		t.Fatalf("last = %s, want run.completed (exit-code synthesis)", lastType(evs))
	}

	// Assistant text: exactly three deltas, concatenated in stream order.
	deltas := collectByType(evs, protocol.EventMessageDelta)
	if len(deltas) != 3 {
		t.Fatalf("message.delta count = %d, want 3 (%v)", len(deltas), typesOf(evs))
	}
	var sb strings.Builder
	for _, d := range deltas {
		if d.Message == nil {
			t.Fatal("message.delta without message payload")
		}
		if d.Message.Role != "assistant" {
			t.Errorf("delta role = %q, want assistant", d.Message.Role)
		}
		if d.Message.MessageID != "msg_1" {
			t.Errorf("delta message id = %q, want msg_1", d.Message.MessageID)
		}
		sb.WriteString(d.Message.Delta)
	}
	if got := sb.String(); got != "Hello, native world" {
		t.Errorf("concatenated text = %q, want %q", got, "Hello, native world")
	}

	// Usage: exactly one usage.updated carrying the step_finish figures.
	usages := collectByType(evs, protocol.EventUsageUpdated)
	if len(usages) != 1 {
		t.Fatalf("usage.updated count = %d, want 1 (%v)", len(usages), typesOf(evs))
	}
	u := usages[0].Usage
	if u == nil {
		t.Fatal("usage.updated without usage payload")
	}
	if u.InputTokens != 273 || u.OutputTokens != 6 || u.CacheReadTokens != 6784 || u.CacheWriteTokens != 0 {
		t.Errorf("usage tokens = %+v, want input=273 output=6 cache_read=6784 cache_write=0", u)
	}
	if u.Cost != 0.0012 {
		t.Errorf("usage cost = %v, want 0.0012", u.Cost)
	}
	if u.Confidence != protocol.QuotaConfProviderReported {
		t.Errorf("usage confidence = %s, want PROVIDER_REPORTED", u.Confidence)
	}

	// Native lines must not be counted as malformed.
	if n := len(collectByType(evs, protocol.EventWarning)); n != 0 {
		t.Errorf("warning events = %d, want 0 for native lines", n)
	}
	entries, _ := os.ReadDir(artDir)
	if len(entries) != 0 {
		t.Errorf("malformed artifacts = %d, want 0 for native lines", len(entries))
	}
}

// TestRunNativeRealFixture runs the captured real-CLI fixture (native_test.go)
// through the full run pipeline.
func TestRunNativeRealFixture(t *testing.T) {
	artDir := t.TempDir()
	a := stubAdapter(realFixture, "", 0, false, artDir)
	sink, _ := startRun(t, a, baseReq())
	evs := waitTerminal(t, sink, 3*time.Second)

	if lastType(evs) != protocol.EventRunCompleted {
		t.Fatalf("last = %s, want run.completed", lastType(evs))
	}
	deltas := collectByType(evs, protocol.EventMessageDelta)
	if len(deltas) != 1 || deltas[0].Message == nil || deltas[0].Message.Delta != "SCHEMA_PROBE" {
		t.Fatalf("deltas = %+v, want one delta carrying SCHEMA_PROBE", deltas)
	}
	usages := collectByType(evs, protocol.EventUsageUpdated)
	if len(usages) != 1 || usages[0].Usage == nil {
		t.Fatalf("usage events = %+v, want one usage.updated", usages)
	}
	if usages[0].Usage.InputTokens != 273 || usages[0].Usage.OutputTokens != 6 {
		t.Errorf("usage tokens = %+v, want input=273 output=6", usages[0].Usage)
	}
	if n := len(collectByType(evs, protocol.EventWarning)); n != 0 {
		t.Errorf("warning events = %d, want 0", n)
	}
	entries, _ := os.ReadDir(artDir)
	if len(entries) != 0 {
		t.Errorf("malformed artifacts = %d, want 0", len(entries))
	}
}

// TestRunNativeMalformedLineStillWarned pins requirement 2: a line that fails
// BOTH the normalized and the native parser keeps the warning+artifact
// behaviour, exactly once, inside an otherwise clean native transcript.
func TestRunNativeMalformedLineStillWarned(t *testing.T) {
	artDir := t.TempDir()
	stream := jline(`{"type":"step_start","timestamp":1,"sessionID":"s","part":{"id":"p1","type":"step-start"}}`) +
		jline(`{"type":"text","timestamp":2,"sessionID":"s","part":{"id":"p2","type":"text","text":"ok"}}`) +
		"{this is neither schema\n" +
		jline(`{"type":"step_finish","timestamp":3,"sessionID":"s","part":{"id":"p3","type":"step-finish","reason":"stop","tokens":{"input":10,"output":4},"cost":0}}`)
	a := stubAdapter(stream, "", 0, false, artDir)
	sink, _ := startRun(t, a, baseReq())
	evs := waitTerminal(t, sink, 3*time.Second)

	if lastType(evs) != protocol.EventRunCompleted {
		t.Fatalf("malformed line broke the run; last = %s", lastType(evs))
	}
	warnings := collectByType(evs, protocol.EventWarning)
	if len(warnings) != 1 {
		t.Errorf("warning events = %d, want exactly 1 (%v)", len(warnings), typesOf(evs))
	}
	entries, _ := os.ReadDir(artDir)
	if len(entries) != 1 {
		t.Errorf("malformed artifacts = %d, want exactly 1", len(entries))
	}
	// The surrounding native content is still captured.
	if n := len(collectByType(evs, protocol.EventMessageDelta)); n != 1 {
		t.Errorf("message.delta count = %d, want 1", n)
	}
	if n := len(collectByType(evs, protocol.EventUsageUpdated)); n != 1 {
		t.Errorf("usage.updated count = %d, want 1", n)
	}
}

// TestParseLineNativeTranslation unit-tests the parseLine fallback directly.
func TestParseLineNativeTranslation(t *testing.T) {
	t.Run("text becomes message.delta", func(t *testing.T) {
		ev, hasContent, err := parseLine([]byte(`{"type":"text","timestamp":1785589490598,"sessionID":"s","part":{"type":"text","messageID":"m1","text":"chunk"}}`))
		if err != nil || !hasContent {
			t.Fatalf("err=%v hasContent=%v", err, hasContent)
		}
		if ev.Type != protocol.EventMessageDelta || ev.Message == nil || ev.Message.Delta != "chunk" {
			t.Fatalf("ev = %+v, want message.delta carrying %q", ev, "chunk")
		}
		if ev.Message.Role != "assistant" {
			t.Errorf("role = %q, want assistant", ev.Message.Role)
		}
		wantTS := time.UnixMilli(1785589490598)
		if !ev.Timestamp.Equal(wantTS) {
			t.Errorf("timestamp = %v, want %v (native ms timestamp)", ev.Timestamp, wantTS)
		}
	})
	t.Run("step_finish becomes usage.updated", func(t *testing.T) {
		ev, hasContent, err := parseLine([]byte(`{"type":"step_finish","timestamp":5,"sessionID":"s","part":{"type":"step-finish","reason":"stop","tokens":{"total":20,"input":12,"output":3,"cache":{"read":7,"write":2}},"cost":0.5}}`))
		if err != nil || !hasContent {
			t.Fatalf("err=%v hasContent=%v", err, hasContent)
		}
		if ev.Type != protocol.EventUsageUpdated || ev.Usage == nil {
			t.Fatalf("ev = %+v, want usage.updated", ev)
		}
		u := ev.Usage
		if u.InputTokens != 12 || u.OutputTokens != 3 || u.CacheReadTokens != 7 || u.CacheWriteTokens != 2 || u.Cost != 0.5 {
			t.Errorf("usage = %+v, want input=12 output=3 cache_read=7 cache_write=2 cost=0.5", u)
		}
	})
	t.Run("step_start is consumed silently", func(t *testing.T) {
		ev, hasContent, err := parseLine([]byte(`{"type":"step_start","timestamp":1,"sessionID":"s","part":{"type":"step-start"}}`))
		if err != nil || hasContent {
			t.Errorf("step_start: err=%v hasContent=%v ev=%+v, want silent skip", err, hasContent, ev)
		}
	})
	t.Run("empty text chunk is consumed silently", func(t *testing.T) {
		if _, hasContent, err := parseLine([]byte(`{"type":"text","part":{"type":"text","text":""}}`)); err != nil || hasContent {
			t.Errorf("empty text: err=%v hasContent=%v, want silent skip", err, hasContent)
		}
	})
	t.Run("unknown native type keeps warning behaviour", func(t *testing.T) {
		ev, hasContent, err := parseLine([]byte(`{"type":"tool_use","part":{"type":"tool_use","tool":"bash"}}`))
		if err == nil || !hasContent || ev.Type != protocol.EventWarning {
			t.Errorf("unknown native type: err=%v hasContent=%v type=%s, want warning+error", err, hasContent, ev.Type)
		}
	})
}
