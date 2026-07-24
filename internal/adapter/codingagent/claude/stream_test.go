package claude

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent/fake"
	"neuroforge/internal/adapter/codingagent/protocol"
)

func newTransCtx() *transCtx {
	return &transCtx{runID: "r1", engine: "claude", model: "sonnet", now: func() time.Time {
		return time.Unix(1700000000, 0)
	}}
}

func adapt() *Adapter {
	a, _ := New(Options{BinaryPath: "claude"})
	return a
}

func translateOne(t *testing.T, line string) ([]protocol.NormalizedEvent, *transCtx) {
	t.Helper()
	a := adapt()
	tc := newTransCtx()
	return a.translate([]byte(line), tc), tc
}

func TestTranslateSystemInitCapturesSession(t *testing.T) {
	evs, tc := translateOne(t, claudeLine(map[string]any{
		"type": "system", "subtype": "init", "session_id": "sess-xyz", "model": "sonnet",
	}))
	if len(evs) != 0 {
		t.Errorf("system/init should emit no events, got %d", len(evs))
	}
	if tc.session != "sess-xyz" {
		t.Errorf("session = %q", tc.session)
	}
}

func TestTranslateAssistantText(t *testing.T) {
	evs, _ := translateOne(t, claudeLine(map[string]any{
		"type": "assistant", "session_id": "s1",
		"message": map[string]any{"content": []map[string]any{{"type": "text", "text": "hello world"}}},
	}))
	if len(evs) != 1 || evs[0].Type != protocol.EventMessageCompleted {
		t.Fatalf("expected one message.completed, got %+v", evs)
	}
	if evs[0].Message == nil || evs[0].Message.Text != "hello world" {
		t.Errorf("text not mapped: %+v", evs[0].Message)
	}
	if evs[0].RunID != "r1" || evs[0].Engine != "claude" {
		t.Errorf("identity not fixed up: %+v", evs[0])
	}
}

func TestTranslateAssistantToolUse(t *testing.T) {
	evs, _ := translateOne(t, claudeLine(map[string]any{
		"type": "assistant", "session_id": "s1",
		"message": map[string]any{"content": []map[string]any{{
			"type": "tool_use", "id": "tu_1", "name": "Bash", "input": map[string]any{"command": "ls"},
		}}},
	}))
	if len(evs) != 1 || evs[0].Type != protocol.EventToolStarted {
		t.Fatalf("expected tool.started, got %+v", evs)
	}
	if evs[0].Tool.Name != "Bash" || evs[0].Tool.ToolID != "tu_1" {
		t.Errorf("tool not mapped: %+v", evs[0].Tool)
	}
}

func TestTranslateUserToolResult(t *testing.T) {
	evs, _ := translateOne(t, claudeLine(map[string]any{
		"type": "user", "session_id": "s1",
		"message": map[string]any{"content": []map[string]any{{
			"type": "tool_result", "id": "tu_1",
		}}},
	}))
	if len(evs) != 1 || evs[0].Type != protocol.EventToolCompleted {
		t.Fatalf("expected tool.completed, got %+v", evs)
	}
}

func TestTranslateResultSuccessUsageAndCompleted(t *testing.T) {
	evs, tc := translateOne(t, claudeLine(map[string]any{
		"type": "result", "subtype": "success", "is_error": false, "session_id": "s1",
		"total_cost_usd": 0.0123,
		"usage": map[string]any{
			"input_tokens": 1234, "output_tokens": 567,
			"cache_creation_input_tokens": 89, "cache_read_input_tokens": 10,
		},
	}))
	if len(evs) != 2 {
		t.Fatalf("expected usage + terminal, got %d", len(evs))
	}
	if evs[0].Type != protocol.EventUsageUpdated {
		t.Fatalf("first should be usage.updated, got %s", evs[0].Type)
	}
	u := evs[0].Usage
	if u.InputTokens != 1234 || u.OutputTokens != 567 {
		t.Errorf("tokens wrong: %+v", u)
	}
	if u.CacheReadTokens != 10 || u.CacheWriteTokens != 89 {
		t.Errorf("cache tokens wrong: %+v", u)
	}
	if u.Cost != 0.0123 || u.Currency != "USD" {
		t.Errorf("cost wrong: %+v", u)
	}
	if u.Confidence != protocol.QuotaConfProviderReported {
		t.Errorf("confidence = %q, want PROVIDER_REPORTED", u.Confidence)
	}
	if evs[1].Type != protocol.EventRunCompleted {
		t.Errorf("second should be run.completed, got %s", evs[1].Type)
	}
	if tc.session != "s1" {
		t.Errorf("session not captured: %q", tc.session)
	}
}

func TestTranslateResultErrorClasses(t *testing.T) {
	cases := []struct {
		subtype string
		errs    []string
		want    protocol.FailureClass
	}{
		{"error_during_execution", []string{"billing_error: quota exhausted"}, protocol.FailureProviderQuota},
		{"error_during_execution", []string{"rate_limit: 429 too many requests"}, protocol.FailureProviderRateLimit},
		{"error_during_execution", []string{"authentication_failed"}, protocol.FailureProviderAuth},
		{"error_during_execution", []string{"model_not_found"}, protocol.FailureModelNotAvailable},
		{"error_during_execution", []string{"overloaded"}, protocol.FailureProviderCapacity},
		{"error_during_execution", []string{"invalid_request"}, protocol.FailureMalformedOutput},
		{"error_max_turns", nil, protocol.FailureInternalError},
		{"error_max_budget_usd", nil, protocol.FailureBudgetExceeded},
	}
	for _, c := range cases {
		m := map[string]any{"type": "result", "subtype": c.subtype, "is_error": true,
			"session_id": "s1", "total_cost_usd": 0.0, "usage": map[string]any{}}
		if c.errs != nil {
			m["errors"] = c.errs
		}
		evs, _ := translateOne(t, claudeLine(m))
		// last event is terminal run.failed
		var last protocol.NormalizedEvent
		for _, e := range evs {
			if e.Type.IsTerminal() {
				last = e
			}
		}
		if last.Type != protocol.EventRunFailed {
			t.Errorf("%s: expected run.failed, got %s", c.subtype, last.Type)
			continue
		}
		if last.Failure == nil || last.Failure.Class != c.want {
			got := protocol.FailureClass("")
			if last.Failure != nil {
				got = last.Failure.Class
			}
			t.Errorf("%s: class = %q, want %q", c.subtype, got, c.want)
		}
	}
}

func TestTranslateStreamEventTextDelta(t *testing.T) {
	evs, _ := translateOne(t, claudeLine(map[string]any{
		"type": "stream_event",
		"event": map[string]any{
			"type":  "content_block_delta",
			"delta": map[string]any{"type": "text_delta", "text": "Hi"},
		},
	}))
	if len(evs) != 1 || evs[0].Type != protocol.EventMessageDelta {
		t.Fatalf("expected message.delta, got %+v", evs)
	}
	if evs[0].Message.Delta != "Hi" {
		t.Errorf("delta = %q", evs[0].Message.Delta)
	}
}

func TestTranslateMalformedJSONWarning(t *testing.T) {
	evs, _ := translateOne(t, "{not valid json")
	if len(evs) != 1 || evs[0].Type != protocol.EventWarning {
		t.Fatalf("expected one warning, got %+v", evs)
	}
	if evs[0].Warning == nil || !evs[0].Warning.Recoverable {
		t.Errorf("warning not recoverable: %+v", evs[0].Warning)
	}
	if !strings.Contains(evs[0].Warning.Code, "malformed") {
		t.Errorf("code = %q", evs[0].Warning.Code)
	}
	if len(evs[0].Raw) == 0 {
		t.Errorf("Raw payload not preserved for artifact persistence")
	}
}

func TestTranslateUnknownTypeWarning(t *testing.T) {
	evs, _ := translateOne(t, claudeLine(map[string]any{"type": "future_event", "data": 1}))
	if len(evs) != 1 || evs[0].Type != protocol.EventWarning {
		t.Fatalf("expected warning for unknown type, got %+v", evs)
	}
	if evs[0].Warning.Code != "unknown-claude-event" {
		t.Errorf("code = %q", evs[0].Warning.Code)
	}
}

func TestTranslateMalformedNeverFatal(t *testing.T) {
	// A malformed line never panics and always returns a recoverable warning.
	for _, bad := range []string{"", "   ", "{", `{"type":`, `[]`, "null"} {
		a := adapt()
		tc := newTransCtx()
		// Empty line returns nil (no event) — also acceptable (non-fatal).
		_ = a.translate([]byte(bad), tc)
	}
}

// ---- BOM / CRLF / long-line robustness ----

func TestStripBOM(t *testing.T) {
	in := append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"type":"system"}`)...)
	r := stripBOM(bytes.NewReader(in))
	buf := make([]byte, len(in))
	n, _ := r.Read(buf)
	got := string(buf[:n])
	if strings.HasPrefix(got, "\uFEFF") {
		t.Errorf("BOM not stripped: %q", got)
	}
	if !strings.Contains(got, "system") {
		t.Errorf("content lost: %q", got)
	}
}

func TestStripBOMAbsent(t *testing.T) {
	in := []byte(`{"type":"system"}`)
	r := stripBOM(bytes.NewReader(in))
	buf := make([]byte, len(in))
	n, _ := r.Read(buf)
	if string(buf[:n]) != `{"type":"system"}` {
		t.Errorf("content altered: %q", buf[:n])
	}
}

func TestLineReaderHandlesCRLF(t *testing.T) {
	in := []byte("{\"a\":1}\r\n{\"b\":2}\r\n")
	r := newLineReader(bytes.NewReader(in))
	l1, more := r.Next()
	if !more || string(l1) != `{"a":1}` {
		t.Errorf("l1 = %q, more=%v", l1, more)
	}
	l2, more := r.Next()
	if !more || string(l2) != `{"b":2}` {
		t.Errorf("l2 = %q, more=%v", l2, more)
	}
	l3, more := r.Next()
	if more || l3 != nil {
		t.Errorf("expected EOF, got %q more=%v", l3, more)
	}
}

func TestLineReaderNo64KiBCap(t *testing.T) {
	// Build a single line well over the 64KiB bufio.Scanner default cap.
	big := make([]byte, 200*1024)
	for i := range big {
		big[i] = 'a'
	}
	big = append(big, '\n')
	r := newLineReader(bytes.NewReader(big))
	line, hasMore := r.Next()
	if !hasMore {
		t.Fatal("expected hasMore")
	}
	if len(line) != 200*1024 {
		t.Errorf("line truncated: got %d bytes, want %d", len(line), 200*1024)
	}
}

func TestLineReaderFinalLineWithoutNewline(t *testing.T) {
	// bufio.Reader.ReadLine returns the trailing data with a nil error on the
	// first call (it cannot know it is the final line without a newline); the
	// following call reports EOF. The reader must not lose the bytes.
	r := newLineReader(bytes.NewReader([]byte(`{"x":1}`)))
	line, _ := r.Next()
	if string(line) != `{"x":1}` {
		t.Fatalf("line = %q", line)
	}
	line2, hasMore := r.Next()
	if hasMore || line2 != nil {
		t.Errorf("expected EOF, got %q hasMore=%v", line2, hasMore)
	}
}

// TestTranslateRoundtripThroughAdapter ensures a full success fixture
// translates to the expected ordered event set (minus run.started which the
// supervisor emits). Sanity check against the recorded fixture shape.
func TestTranslateFullSuccessFixture(t *testing.T) {
	spec := fixtureForScenario(fake.ScenarioSuccess) // default success
	a := adapt()
	tc := newTransCtx()
	var got []protocol.NormalizedEvent
	for _, line := range spec.lines {
		evs := a.translate([]byte(line), tc)
		got = append(got, evs...)
	}
	types := typesOf(got)
	// Expect: message.completed, usage.updated, run.completed (system/init → none).
	want := []string{"message.completed", "usage.updated", "run.completed"}
	if len(types) != len(want) {
		t.Fatalf("types = %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Errorf("types[%d] = %s, want %s", i, types[i], want[i])
		}
	}
}
