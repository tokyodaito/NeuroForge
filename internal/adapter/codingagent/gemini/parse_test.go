package gemini

import (
	"encoding/json"
	"strings"
	"testing"

	"neuroforge/internal/adapter/codingagent/protocol"
)

func md() frameMeta { return frameMeta{runID: "r1", engine: "gemini", model: "m"} }

func TestParseGeminiDocumentSuccess(t *testing.T) {
	doc := `{"response":{"text":"hello world"},"usage":{"metadata":{"promptTokenCount":10,"candidatesTokenCount":20,"totalTokenCount":30,"cachedContentTokenCount":2,"thoughtsTokenCount":1,"toolUseTokenCount":1}}}`
	res := parseStream([]byte(doc), md())
	if len(res.malformed) != 0 {
		t.Errorf("unexpected malformed: %d", len(res.malformed))
	}
	if res.terminal != nil {
		t.Errorf("document mode should not set terminal: %+v", res.terminal)
	}
	var msg *protocol.NormalizedEvent
	var usage *protocol.NormalizedEvent
	for i := range res.body {
		if res.body[i].Type == protocol.EventMessageCompleted {
			msg = &res.body[i]
		}
		if res.body[i].Type == protocol.EventUsageUpdated {
			usage = &res.body[i]
		}
	}
	if msg == nil || msg.Message == nil || msg.Message.Text != "hello world" {
		t.Errorf("message.completed missing/wrong: %+v", msg)
	}
	if usage == nil || usage.Usage == nil {
		t.Fatalf("usage.updated missing: %+v", res.body)
	}
	if usage.Usage.InputTokens != 10 {
		t.Errorf("input tokens = %d, want 10", usage.Usage.InputTokens)
	}
	if usage.Usage.OutputTokens != 20 {
		t.Errorf("output tokens = %d, want 20", usage.Usage.OutputTokens)
	}
	if usage.Usage.CacheReadTokens != 2 {
		t.Errorf("cache read tokens = %d, want 2", usage.Usage.CacheReadTokens)
	}
	if usage.Usage.Confidence != protocol.QuotaConfProviderReported {
		t.Errorf("confidence = %s, want PROVIDER_REPORTED", usage.Usage.Confidence)
	}
}

func TestParseGeminiDocumentNoUsage(t *testing.T) {
	// Absent usage → no usage.updated event (never fabricated).
	doc := `{"response":{"text":"hi"}}`
	res := parseStream([]byte(doc), md())
	for _, ev := range res.body {
		if ev.Type == protocol.EventUsageUpdated {
			t.Errorf("usage event should not be emitted when usage absent")
		}
	}
}

func TestParseGeminiDocumentMalformedRecovered(t *testing.T) {
	// Garbage document → malformed artifact; the run continues (supervise
	// synthesizes the terminal). parseStream never aborts.
	res := parseStream([]byte("{not valid json"), md())
	if len(res.malformed) != 1 {
		t.Fatalf("want 1 malformed entry, got %d", len(res.malformed))
	}
	if len(res.body) != 0 {
		t.Errorf("want no body events for malformed doc, got %d", len(res.body))
	}
}

func TestParseStreamEmptyOutput(t *testing.T) {
	res := parseStream(nil, md())
	if len(res.body) != 0 || len(res.malformed) != 0 || res.terminal != nil {
		t.Errorf("empty output should produce nothing: %+v", res)
	}
}

func TestParseStreamBOMStripped(t *testing.T) {
	// Leading UTF-8 BOM must be tolerated.
	raw := append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"response":{"text":"bom ok"}}`)...)
	res := parseStream(raw, md())
	if len(res.malformed) != 0 {
		t.Fatalf("BOM not stripped: malformed=%d", len(res.malformed))
	}
	found := false
	for _, ev := range res.body {
		if ev.Type == protocol.EventMessageCompleted && ev.Message != nil && ev.Message.Text == "bom ok" {
			found = true
		}
	}
	if !found {
		t.Errorf("BOM document not parsed: %+v", res.body)
	}
}

func TestParseStreamCRLFTolerated(t *testing.T) {
	// Pretty-printed document with CRLF line endings must parse.
	doc := "{\r\n  \"response\": {\r\n    \"text\": \"crlf\"\r\n  }\r\n}\r\n"
	res := parseStream([]byte(doc), md())
	if len(res.malformed) != 0 {
		t.Fatalf("CRLF doc produced malformed: %d", len(res.malformed))
	}
}

// --- protocol-JSONL forward-compat path (parse with ParseEventLine) ---

func TestParseJSONLWellFormed(t *testing.T) {
	raw := []byte(`{"type":"run.started","ts":"2026-01-01T00:00:00Z","run_id":"r1"}` + "\n" +
		`{"type":"message.delta","ts":"2026-01-01T00:00:00Z","message":{"delta":"hi"}}` + "\n" +
		`{"type":"run.completed","ts":"2026-01-01T00:00:00Z"}` + "\n")
	res := parseStream(raw, md())
	// run.started is the frame-open (dropped); message.delta is body; run.completed
	// is the terminal.
	if len(res.malformed) != 0 {
		t.Errorf("unexpected malformed: %d", len(res.malformed))
	}
	if res.terminal == nil || res.terminal.Type != protocol.EventRunCompleted {
		t.Errorf("terminal missing/wrong: %+v", res.terminal)
	}
	hasDelta := false
	for _, ev := range res.body {
		if ev.Type == protocol.EventMessageDelta {
			hasDelta = true
		}
		if ev.Type == protocol.EventRunStarted {
			t.Errorf("frame-open run.started should be dropped from body in JSONL mode")
		}
	}
	if !hasDelta {
		t.Errorf("message.delta not in body: %+v", res.body)
	}
}

func TestParseJSONLMalformedRecovered(t *testing.T) {
	raw := []byte(`{"type":"run.started"}` + "\n" +
		`{not valid json` + "\n" +
		`{"type":"run.completed"}` + "\n")
	res := parseStream(raw, md())
	if len(res.malformed) != 1 {
		t.Errorf("want 1 malformed, got %d", len(res.malformed))
	}
	if res.terminal == nil || res.terminal.Type != protocol.EventRunCompleted {
		t.Errorf("terminal missing: %+v", res.terminal)
	}
}

func TestParseJSONLUnknownEventType(t *testing.T) {
	// Unknown future event type → recoverable, collected as malformed (ParseEventLine
	// returns a warning event + error). The run is not aborted.
	raw := []byte(`{"type":"robot.upgraded","ts":"2026-01-01T00:00:00Z"}` + "\n")
	res := parseStream(raw, md())
	if len(res.malformed) == 0 {
		t.Errorf("unknown event should be collected as malformed (recoverable)")
	}
}

func TestParseJSONLPartialLineAccumulated(t *testing.T) {
	// A single line carrying a full event object (no trailing newline) must
	// still be parsed (the scanner returns a final fragment without newline).
	raw := []byte(`{"type":"message.delta","message":{"delta":"partial"}}`)
	res := parseStream(raw, md())
	if len(res.malformed) != 0 {
		t.Errorf("unexpected malformed: %d", len(res.malformed))
	}
	if len(res.body) != 1 || res.body[0].Type != protocol.EventMessageDelta {
		t.Errorf("partial-line event not parsed: %+v", res.body)
	}
}

func TestLooksLikeProtocolJSONL(t *testing.T) {
	if !looksLikeProtocolJSONL([]byte(`{"type":"run.started"}`)) {
		t.Error("protocol JSONL not detected")
	}
	if looksLikeProtocolJSONL([]byte(`{"response":{"text":"x"}}`)) {
		t.Error("gemini doc misclassified as JSONL")
	}
	if looksLikeProtocolJSONL([]byte("\n\n")) {
		t.Error("blank output misclassified as JSONL")
	}
}

// --- usage mapping edge cases ---

func TestMapUsageZeroWhenAbsent(t *testing.T) {
	u := mapUsage(geminiTokenMetadata{})
	if u.InputTokens != 0 || u.OutputTokens != 0 || u.CacheReadTokens != 0 {
		t.Errorf("absent fields must be 0, never fabricated: %+v", u)
	}
	if u.Confidence != protocol.QuotaConfProviderReported {
		t.Errorf("confidence = %s", u.Confidence)
	}
}

func TestMergeMetadataTopLevelFallback(t *testing.T) {
	// Older/alternate shape: counts at usage top-level, no nested metadata.
	intp := int64(7)
	intc := int64(8)
	u := &geminiUsage{PromptTokenCount: &intp, CandidatesTokenCount: &intc}
	m, any := u.mergeMetadata()
	if !any {
		t.Error("expected any=true")
	}
	if m.PromptTokenCount != 7 || m.CandidatesTokenCount != 8 {
		t.Errorf("fallback merge wrong: %+v", m)
	}
}

func TestDecodeGeminiResponseIgnoresUnknownFields(t *testing.T) {
	// Additive future fields must be ignored, not fatal.
	raw := []byte(`{"response":{"text":"x"},"futureField":{"nested":42},"another":123}`)
	r, ok := decodeGeminiResponse(raw)
	if !ok {
		t.Fatal("decode failed on unknown fields")
	}
	if r.responseText() != "x" {
		t.Errorf("text = %q", r.responseText())
	}
}

func TestSplitLinesCRLF(t *testing.T) {
	// splitLines splits on LF only; a single trailing CR per line is stripped.
	// A lone CR mid-line is preserved (it is not a line separator).
	lines := splitLines([]byte("a\r\nb\rc\nd"))
	if len(lines) != 3 {
		t.Fatalf("want 3 lines (LF-split), got %d (%v)", len(lines), lines)
	}
	if string(lines[0]) != "a" {
		t.Errorf("line0 = %q (trailing CR not stripped)", lines[0])
	}
	if string(lines[1]) != "b\rc" {
		t.Errorf("line1 = %q (lone CR should be preserved)", lines[1])
	}
	if string(lines[2]) != "d" {
		t.Errorf("line2 = %q", lines[2])
	}
}

func TestParseEventRoundTripJSON(t *testing.T) {
	// Ensure the synthesized message.completed event round-trips through JSON
	// with the expected shape (guards against payload struct drift).
	doc := `{"response":{"text":"roundtrip"}}`
	res := parseStream([]byte(doc), md())
	var ev protocol.NormalizedEvent
	for _, e := range res.body {
		if e.Type == protocol.EventMessageCompleted {
			ev = e
		}
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), "roundtrip") {
		t.Errorf("text lost in round-trip: %s", b)
	}
}
