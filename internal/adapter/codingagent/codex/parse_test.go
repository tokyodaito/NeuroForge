package codex

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"neuroforge/internal/adapter/codingagent/protocol"
)

var parseNow = fixedNow()

func TestParseCodexLineWellFormedNativeNF(t *testing.T) {
	// A line that is already a NeuroForge NormalizedEvent is used verbatim
	// (reuses protocol.ParseEventLine).
	line := []byte(`{"type":"run.started","ts":"2023-01-01T00:00:00Z","run_id":"r1","engine":"codex"}`)
	ev, err := parseCodexLine(line, parseNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != protocol.EventRunStarted {
		t.Errorf("type = %s", ev.Type)
	}
}

func TestParseCodexLineTaskStartedMapsToRunStarted(t *testing.T) {
	line := []byte(`{"type":"task_started","session_id":"sess-1","model":"some-model"}`)
	ev, err := parseCodexLine(line, parseNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != protocol.EventRunStarted {
		t.Errorf("type = %s, want run.started", ev.Type)
	}
	if ev.Model != "some-model" {
		t.Errorf("model = %q", ev.Model)
	}
}

func TestParseCodexLineTaskCompleteMapsToRunCompleted(t *testing.T) {
	for _, typ := range []string{"task_complete", "task_completed", "turn_complete"} {
		line := []byte(`{"type":"` + typ + `"}`)
		ev, err := parseCodexLine(line, parseNow)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", typ, err)
			continue
		}
		if ev.Type != protocol.EventRunCompleted {
			t.Errorf("%s: type = %s, want run.completed", typ, ev.Type)
		}
	}
}

func TestParseCodexLineAgentMessageDelta(t *testing.T) {
	line := []byte(`{"type":"agent_message_delta","delta":"hello "}`)
	ev, err := parseCodexLine(line, parseNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != protocol.EventMessageDelta || ev.Message == nil || ev.Message.Delta != "hello " {
		t.Errorf("delta not mapped: %+v", ev)
	}
}

func TestParseCodexLineAgentMessageFull(t *testing.T) {
	line := []byte(`{"type":"agent_message","message":"full reply"}`)
	ev, err := parseCodexLine(line, parseNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != protocol.EventMessageCompleted || ev.Message == nil || ev.Message.Text != "full reply" {
		t.Errorf("message not mapped: %+v", ev)
	}
}

func TestParseCodexLineUsageEvent(t *testing.T) {
	line := []byte(`{"type":"token_count","input_tokens":10,"output_tokens":5,"cached_input_tokens":2}`)
	ev, err := parseCodexLine(line, parseNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != protocol.EventUsageUpdated || ev.Usage == nil {
		t.Fatalf("usage not mapped: %+v", ev)
	}
	if ev.Usage.InputTokens != 10 || ev.Usage.CacheReadTokens != 2 {
		t.Errorf("usage fields wrong: %+v", ev.Usage)
	}
	if ev.Usage.Confidence != protocol.QuotaConfProviderReported {
		t.Errorf("confidence = %q", ev.Usage.Confidence)
	}
}

func TestParseCodexLineCommandEvents(t *testing.T) {
	begin := []byte(`{"type":"exec_command_begin","command":["go","test","./..."],"cwd":"/ws"}`)
	ev, err := parseCodexLine(begin, parseNow)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if ev.Type != protocol.EventCommandStarted || ev.Command == nil || ev.Command.CommandLine != "go test ./..." {
		t.Errorf("begin mapped wrong: %+v", ev)
	}

	end := []byte(`{"type":"exec_command_end","command":["go","test"],"exit_code":0,"success":true}`)
	ev, err = parseCodexLine(end, parseNow)
	if err != nil {
		t.Fatalf("end: %v", err)
	}
	if ev.Type != protocol.EventCommandCompleted || ev.Command == nil || ev.Command.ExitCode != 0 {
		t.Errorf("end mapped wrong: %+v", ev)
	}
}

func TestParseCodexLineFileChange(t *testing.T) {
	line := []byte(`{"type":"file_change","path":"src/main.go","action":"modified"}`)
	ev, err := parseCodexLine(line, parseNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != protocol.EventFileChanged || ev.FileChange == nil {
		t.Fatalf("file change not mapped: %+v", ev)
	}
	if ev.FileChange.Path != "src/main.go" || ev.FileChange.Action != "modified" {
		t.Errorf("file change wrong: %+v", ev.FileChange)
	}
}

func TestParseCodexLineErrorEventIsWarning(t *testing.T) {
	line := []byte(`{"type":"error","message":"something went wrong"}`)
	ev, err := parseCodexLine(line, parseNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != protocol.EventWarning || ev.Warning == nil {
		t.Fatalf("error not mapped to warning: %+v", ev)
	}
	if !ev.Warning.Recoverable {
		t.Error("non-fatal error should be recoverable")
	}
}

func TestParseCodexLineFatalErrorIsNonRecoverableWarning(t *testing.T) {
	line := []byte(`{"type":"fatal_error","error":"boom"}`)
	ev, err := parseCodexLine(line, parseNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Warning == nil || ev.Warning.Recoverable {
		t.Errorf("fatal error should be non-recoverable: %+v", ev)
	}
}

func TestParseCodexLineMalformedJSONIsWarning(t *testing.T) {
	line := []byte(`{not valid json`)
	ev, err := parseCodexLine(line, parseNow)
	if err == nil {
		t.Fatal("expected malformed error")
	}
	if ev.Type != protocol.EventWarning {
		t.Errorf("type = %s, want warning", ev.Type)
	}
	if ev.Warning == nil || !strings.Contains(ev.Warning.Code, "malformed") {
		t.Errorf("expected malformed warning, got %+v", ev.Warning)
	}
	if len(ev.Raw) == 0 {
		t.Error("malformed event should carry raw bytes")
	}
}

func TestParseCodexLineUnknownCodexTypeIsWarningWithRaw(t *testing.T) {
	// A valid-JSON event whose type neither NeuroForge nor the Codex mapper
	// recognizes is forwarded as a warning with the raw bytes; it never aborts.
	line := []byte(`{"type":"some_future_codex_event","payload":{"x":1}}`)
	ev, err := parseCodexLine(line, parseNow)
	if err == nil {
		t.Fatal("expected unknown-type error")
	}
	if ev.Type != protocol.EventWarning {
		t.Errorf("type = %s, want warning", ev.Type)
	}
	if len(ev.Raw) == 0 {
		t.Error("unknown event should carry raw bytes")
	}
}

func TestParseCodexLineEmptyIsErrEmptyLine(t *testing.T) {
	_, err := parseCodexLine([]byte(""), parseNow)
	if !errors.Is(err, protocol.ErrEmptyLine) {
		t.Errorf("expected ErrEmptyLine, got %v", err)
	}
}

func TestParseCodexLineCRLF(t *testing.T) {
	// CRLF line endings must be tolerated.
	line := []byte("{\"type\":\"task_complete\"}\r\n")
	ev, err := parseCodexLine(line, parseNow)
	if err != nil {
		t.Fatalf("CRLF: unexpected error: %v", err)
	}
	if ev.Type != protocol.EventRunCompleted {
		t.Errorf("type = %s", ev.Type)
	}
}

func TestParseCodexLineUTF8BOM(t *testing.T) {
	// A leading UTF-8 BOM must be stripped before parsing.
	line := append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"type":"task_complete"}`)...)
	ev, err := parseCodexLine(line, parseNow)
	if err != nil {
		t.Fatalf("BOM: unexpected error: %v", err)
	}
	if ev.Type != protocol.EventRunCompleted {
		t.Errorf("type = %s", ev.Type)
	}
}

func TestParseCodexLineBOMAndCRLF(t *testing.T) {
	line := append([]byte{0xEF, 0xBB, 0xBF}, []byte("{\"type\":\"task_started\"}\r\n")...)
	ev, err := parseCodexLine(line, parseNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != protocol.EventRunStarted {
		t.Errorf("type = %s", ev.Type)
	}
}

func TestLineScannerNoCap(t *testing.T) {
	// A line far larger than bufio.Scanner's 64KiB cap must be read whole.
	big := bytes.Repeat([]byte("a"), 200_000)
	big = append(big, '\n')
	sc := newLineScanner(bytes.NewReader(big))
	line, hasMore := sc.next()
	if !hasMore {
		t.Fatal("expected hasMore")
	}
	if len(line) != 200_000 {
		t.Errorf("line truncated to %d bytes", len(line))
	}
	// EOF after the long line.
	line2, hasMore2 := sc.next()
	if line2 != nil || hasMore2 {
		t.Errorf("expected EOF, got line=%d hasMore=%v", len(line2), hasMore2)
	}
}

func TestLineScannerFinalLineWithoutNewline(t *testing.T) {
	// A final line lacking a trailing newline is still returned.
	sc := newLineReader("one\ntwo\nthree")
	got := []string{}
	for {
		line, hasMore := sc.next()
		if line != nil {
			got = append(got, string(line))
		}
		if !hasMore {
			break
		}
	}
	want := []string{"one", "two", "three"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestExtractSessionID(t *testing.T) {
	cases := map[string]string{
		`{"type":"task_started","session_id":"s1"}`: "s1",
		`{"type":"x","thread_id":"t1"}`:             "t1",
		`{"session":{"id":"nested-id"}}`:            "nested-id",
		`{"conversation_id":"c1"}`:                  "c1",
		`{"type":"x","no_id":true}`:                 "",
		`not json`:                                  "",
	}
	for in, want := range cases {
		if got := extractSessionID([]byte(in)); got != want {
			t.Errorf("extractSessionID(%s) = %q, want %q", in, got, want)
		}
	}
}

// newLineReader wraps newLineScanner behind a strings.Reader for compactness.
func newLineReader(s string) *lineScanner { return newLineScanner(strings.NewReader(s)) }
