package kimi

import (
	"bytes"
	"strings"
	"testing"

	"neuroforge/internal/adapter/codingagent/protocol"
)

func TestParseWellFormedKimiItems(t *testing.T) {
	cases := []struct {
		name string
		line string
		want protocol.EventType
	}{
		{"system init", `{"type":"system","event":"init","session_id":"s1","model":"m1"}`, protocol.EventRunStarted},
		{"system resume", `{"type":"system","event":"resume","session_id":"s1"}`, protocol.EventRunResumed},
		{"assistant text", `{"type":"assistant","event":"text","text":"hi"}`, protocol.EventMessageDelta},
		{"assistant text via content", `{"type":"assistant","event":"text","content":"hello"}`, protocol.EventMessageDelta},
		{"assistant content array", `{"type":"assistant","event":"text","content":[{"type":"text","text":"block"}]}`, protocol.EventMessageDelta},
		{"tool use", `{"type":"assistant","event":"tool_use","tool":"editor","tool_use_id":"t1"}`, protocol.EventToolStarted},
		{"tool result", `{"type":"user","event":"tool_result","tool_use_id":"t1"}`, protocol.EventToolCompleted},
		{"command completed", `{"type":"assistant","event":"command","command":"npm test","exit_code":0}`, protocol.EventCommandCompleted},
		{"file changed", `{"type":"file","path":"src/a.go","action":"modified"}`, protocol.EventFileChanged},
		{"usage", `{"type":"usage","input_tokens":10,"output_tokens":5}`, protocol.EventUsageUpdated},
		{"result success", `{"type":"result","event":"success","session_id":"s1"}`, protocol.EventRunCompleted},
		{"result error", `{"type":"result","event":"error","error":"quota exhausted","class":"PROVIDER_QUOTA"}`, protocol.EventRunFailed},
	}
	for _, c := range cases {
		ev, err := parseKimiLine([]byte(c.line))
		if err != nil {
			t.Errorf("%s: unexpected error: %v", c.name, err)
			continue
		}
		if ev.Type != c.want {
			t.Errorf("%s: type = %s, want %s", c.name, ev.Type, c.want)
		}
	}
}

func TestParseNormalizedFastPath(t *testing.T) {
	// A line already speaking the NeuroForge normalized format is accepted as-is.
	line := `{"type":"run.started","ts":"2024-01-01T00:00:00Z","run_id":"r1","engine":"kimi"}`
	ev, err := parseKimiLine([]byte(line))
	if err != nil {
		t.Fatalf("fast path error: %v", err)
	}
	if ev.Type != protocol.EventRunStarted || ev.RunID != "r1" {
		t.Errorf("fast path mis-parsed: %+v", ev)
	}
}

func TestParseMalformedIsRecoverable(t *testing.T) {
	ev, err := parseKimiLine([]byte("{not valid json"))
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	if ev.Type != protocol.EventWarning {
		t.Fatalf("malformed line should yield warning, got %s", ev.Type)
	}
	if ev.Warning == nil || !ev.Warning.Recoverable {
		t.Errorf("warning missing/not recoverable: %+v", ev.Warning)
	}
	if len(ev.Raw) == 0 {
		t.Errorf("malformed line should carry raw bytes for artifact save")
	}
}

func TestParseEmptyLine(t *testing.T) {
	_, err := parseKimiLine([]byte("   "))
	if err == nil {
		t.Fatal("expected error for empty line")
	}
}

func TestParseUnknownShapeYieldsWarning(t *testing.T) {
	// Valid JSON but no recognizable Kimi shape and no normalized type.
	ev, err := parseKimiLine([]byte(`{"type":"frobnicate","weird":true}`))
	if err == nil {
		t.Fatal("expected error for unrecognized shape")
	}
	if ev.Type != protocol.EventWarning {
		t.Errorf("unrecognized shape should yield warning, got %s", ev.Type)
	}
}

func TestParseCRLF(t *testing.T) {
	// CRLF-terminated lines parse correctly (trimLine strips trailing CR/LF).
	ev, err := parseKimiLine([]byte("{\"type\":\"result\",\"event\":\"success\"}\r\n"))
	if err != nil {
		t.Fatalf("CRLF parse error: %v", err)
	}
	if ev.Type != protocol.EventRunCompleted {
		t.Errorf("CRLF type = %s, want run.completed", ev.Type)
	}
}

func TestParseLeadingBOM(t *testing.T) {
	// A line-level UTF-8 BOM is stripped before parsing.
	bommed := append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"type":"result","event":"success"}`)...)
	ev, err := parseKimiLine(bommed)
	if err != nil {
		t.Fatalf("BOM parse error: %v", err)
	}
	if ev.Type != protocol.EventRunCompleted {
		t.Errorf("BOM type = %s, want run.completed", ev.Type)
	}
}

func TestBomStripperRemovesStreamBOM(t *testing.T) {
	// A BOM at the very start of the stream is removed; the rest passes through.
	raw := append([]byte{0xEF, 0xBB, 0xBF}, []byte("{\"type\":\"result\",\"event\":\"success\"}\n")...)
	bs := newBomStripper(bytes.NewReader(raw))
	scan := newLineScanner(bs)
	line, hasMore := scan.Next()
	if !hasMore {
		t.Fatal("expected a line")
	}
	// The first byte must NOT be a BOM byte.
	if len(line) > 0 && (line[0] == 0xEF) {
		t.Errorf("stream BOM not stripped: % x", line[:3])
	}
	ev, err := parseKimiLine(line)
	if err != nil || ev.Type != protocol.EventRunCompleted {
		t.Errorf("post-BOM parse: ev=%+v err=%v", ev, err)
	}
}

func TestLineScannerNoCap(t *testing.T) {
	// A single line far larger than bufio.Scanner's 64KiB cap is read whole.
	big := strings.Repeat("a", 200_000)
	var buf bytes.Buffer
	buf.WriteString(`{"type":"assistant","event":"text","text":"`)
	buf.WriteString(big)
	buf.WriteString(`"}`)
	buf.WriteByte('\n')
	scan := newLineScanner(&buf)
	line, hasMore := scan.Next()
	if !hasMore {
		t.Fatal("scanner reported EOF on a long line")
	}
	if len(line) < 200_000 {
		t.Errorf("scanner truncated long line: got %d bytes", len(line))
	}
}

func TestUsageMappingConfidence(t *testing.T) {
	// Reported counts → PROVIDER_REPORTED.
	in, out := int64(120), int64(80)
	u := &kimiUsage{InputTokens: &in, OutputTokens: &out, Cost: ptrFloat(0.001)}
	p := usagePayload(u, kimiItem{}, "USD")
	if p.Confidence != protocol.QuotaConfProviderReported {
		t.Errorf("reported usage confidence = %s, want PROVIDER_REPORTED", p.Confidence)
	}
	if p.InputTokens != 120 || p.OutputTokens != 80 || p.Cost != 0.001 {
		t.Errorf("usage values wrong: %+v", p)
	}

	// Nothing reported → UNKNOWN, no fabricated numbers.
	p = usagePayload(nil, kimiItem{}, "")
	if p.Confidence != protocol.QuotaConfUnknown {
		t.Errorf("empty usage confidence = %s, want UNKNOWN", p.Confidence)
	}

	// Cached tokens mapped.
	cr, cw := int64(40), int64(10)
	u = &kimiUsage{CacheReadTokens: &cr, CacheWriteTokens: &cw}
	p = usagePayload(u, kimiItem{}, "USD")
	if p.CacheReadTokens != 40 || p.CacheWriteTokens != 10 {
		t.Errorf("cached tokens wrong: %+v", p)
	}
	if p.Confidence != protocol.QuotaConfProviderReported {
		t.Errorf("cached-only usage should be PROVIDER_REPORTED: %s", p.Confidence)
	}
}

func TestUsageDropsReasoningTokens(t *testing.T) {
	// Reasoning/thinking tokens are reported by Kimi but have no field in
	// UsagePayload; they are dropped (never fabricated) per §36.10.
	var rt int64 = 99
	u := &kimiUsage{ReasoningTokens: &rt}
	p := usagePayload(u, kimiItem{}, "USD")
	// Reasoning tokens alone do not populate input/output; confidence is still
	// PROVIDER_REPORTED because the engine DID report usage, but no fabricated
	// reasoning axis exists.
	if p.InputTokens != 0 || p.OutputTokens != 0 {
		t.Errorf("reasoning tokens should not leak into input/output: %+v", p)
	}
}

func TestContentArrayExtraction(t *testing.T) {
	if got := contentText([]byte(`"plain"`)); got != "plain" {
		t.Errorf("string content = %q", got)
	}
	if got := contentText([]byte(`[{"type":"text","text":"a"},{"type":"text","text":"b"}]`)); got != "ab" {
		t.Errorf("array content = %q", got)
	}
	if got := contentText(nil); got != "" {
		t.Errorf("nil content = %q", got)
	}
}

func TestResultErrorClassInference(t *testing.T) {
	cases := map[string]protocol.FailureClass{
		`{"type":"result","event":"error","error":"quota exhausted"}`:     protocol.FailureProviderQuota,
		`{"type":"result","event":"error","error":"HTTP 429 rate limit"}`: protocol.FailureProviderRateLimit,
		`{"type":"result","event":"error","error":"401 unauthorized"}`:    protocol.FailureProviderAuth,
		`{"type":"result","event":"error","error":"server overloaded"}`:   protocol.FailureProviderCapacity,
		`{"type":"result","event":"error","error":"model not available"}`: protocol.FailureModelNotAvailable,
		`{"type":"result","event":"error","class":"PROVIDER_QUOTA"}`:      protocol.FailureProviderQuota,
	}
	for line, want := range cases {
		ev, err := parseKimiLine([]byte(line))
		if err != nil {
			t.Errorf("parse error for %s: %v", line, err)
			continue
		}
		if ev.Type != protocol.EventRunFailed || ev.Failure == nil {
			t.Errorf("expected run.failed with payload: %+v", ev)
			continue
		}
		if ev.Failure.Class != want {
			t.Errorf("class for %s = %s, want %s", line, ev.Failure.Class, want)
		}
	}
}

func ptrFloat(f float64) *float64 { return &f }
