package grok

import (
	"encoding/json"
	"strings"
	"testing"

	"neuroforge/internal/adapter/codingagent/protocol"
)

func TestParseGrokLineWellFormedItems(t *testing.T) {
	cases := []struct {
		line string
		want protocol.EventType
	}{
		{`{"type":"message","role":"assistant","delta":"hi"}`, protocol.EventMessageDelta},
		{`{"type":"message","role":"assistant","text":"full"}`, protocol.EventMessageCompleted},
		{`{"type":"text","delta":"chunk"}`, protocol.EventMessageDelta},
		{`{"type":"tool","id":"t1","name":"edit","status":"running"}`, protocol.EventToolStarted},
		{`{"type":"tool","id":"t1","name":"edit","status":"completed","duration_ms":12}`, protocol.EventToolCompleted},
		{`{"type":"command","id":"c1","command":"go test","status":"running"}`, protocol.EventCommandStarted},
		{`{"type":"command","id":"c1","command":"go test","status":"completed","exit_code":0,"success":true}`, protocol.EventCommandCompleted},
		{`{"type":"file","path":"a/b.go","action":"modified"}`, protocol.EventFileChanged},
		{`{"type":"usage","input_tokens":10,"output_tokens":5,"cost":0.1}`, protocol.EventUsageUpdated},
		{`{"type":"checkpoint","id":"ck1","path":".neuroforge/ck","reason":"turn limit"}`, protocol.EventCheckpointCreated},
		{`{"type":"approval","action":"run_shell","detail":"rm -rf x"}`, protocol.EventApprovalRequested},
	}
	for _, c := range cases {
		evs, terminal, status := parseGrokLine([]byte(c.line), nil)
		if status != parseOK {
			t.Errorf("line %q: status = %d, want parseOK", c.line, status)
			continue
		}
		if terminal {
			t.Errorf("line %q: unexpected terminal", c.line)
		}
		if len(evs) != 1 || evs[0].Type != c.want {
			t.Errorf("line %q: got %+v, want %s", c.line, typesOf(evs), c.want)
		}
	}
}

func TestParseGrokLineResultTerminal(t *testing.T) {
	evs, terminal, status := parseGrokLine([]byte(`{"type":"result","status":"completed","text":"done"}`), nil)
	if status != parseOK || !terminal {
		t.Fatalf("completed result should be terminal/ok: evs=%+v term=%v status=%d", evs, terminal, status)
	}
	// Expect a final message.completed then run.completed.
	if evs[len(evs)-1].Type != protocol.EventRunCompleted {
		t.Errorf("last event = %s, want run.completed", evs[len(evs)-1].Type)
	}

	evs, terminal, _ = parseGrokLine([]byte(`{"type":"result","status":"failed","error_code":"quota","message":"nope"}`), nil)
	if !terminal || len(evs) != 1 || evs[0].Type != protocol.EventRunFailed {
		t.Fatalf("failed result mapping wrong: %+v", evs)
	}
	if evs[0].Failure == nil || evs[0].Failure.Class != protocol.FailureProviderQuota {
		t.Errorf("failure class wrong: %+v", evs[0].Failure)
	}
}

func TestParseGrokLineErrorCodesMapToTaxonomy(t *testing.T) {
	cases := map[string]protocol.FailureClass{
		"quota":           protocol.FailureProviderQuota,
		"rate_limit":      protocol.FailureProviderRateLimit,
		"capacity":        protocol.FailureProviderCapacity,
		"unauthorized":    protocol.FailureProviderAuth,
		"model_not_found": protocol.FailureModelNotAvailable,
	}
	for code, want := range cases {
		evs, terminal, _ := parseGrokLine([]byte(`{"type":"error","code":"`+code+`","message":"x","fatal":true}`), nil)
		if !terminal {
			t.Errorf("code %q: expected terminal", code)
		}
		if got := evs[0].Failure.Class; got != want {
			t.Errorf("code %q: class = %s, want %s", code, got, want)
		}
	}
}

func TestParseGrokLineMalformedJSON(t *testing.T) {
	evs, terminal, status := parseGrokLine([]byte(`{not valid json`), nil)
	if status != parseMalformed {
		t.Errorf("status = %d, want parseMalformed", status)
	}
	if terminal {
		t.Error("malformed must not be terminal")
	}
	if len(evs) != 1 || evs[0].Type != protocol.EventWarning {
		t.Fatalf("malformed should yield a warning, got %+v", evs)
	}
	if evs[0].Warning == nil || !strings.Contains(evs[0].Warning.Code, "malformed") {
		t.Errorf("warning code wrong: %+v", evs[0].Warning)
	}
	if len(evs[0].Raw) == 0 {
		t.Error("malformed raw bytes not preserved")
	}
}

func TestParseGrokLineUnknownTypeWarning(t *testing.T) {
	evs, terminal, status := parseGrokLine([]byte(`{"type":"agent.vibes","feeling":"ok"}`), nil)
	if status != parseUnknown {
		t.Errorf("status = %d, want parseUnknown", status)
	}
	if terminal {
		t.Error("unknown must not be terminal")
	}
	if len(evs) != 1 || evs[0].Type != protocol.EventWarning {
		t.Fatalf("unknown should yield a warning, got %+v", evs)
	}
	if evs[0].Warning == nil || evs[0].Warning.Code != "unknown-item-type" {
		t.Errorf("warning code wrong: %+v", evs[0].Warning)
	}
}

func TestParseGrokLineCRLF(t *testing.T) {
	// A trailing \r\n (Windows line ending) must not corrupt parsing.
	evs, _, status := parseGrokLine([]byte(`{"type":"message","delta":"hi"}`+"\r\n"), nil)
	if status != parseOK {
		t.Fatalf("CRLF line status = %d", status)
	}
	if len(evs) != 1 || evs[0].Type != protocol.EventMessageDelta {
		t.Errorf("CRLF parse wrong: %+v", evs)
	}
}

func TestParseGrokLinePartialJSONAcrossLines(t *testing.T) {
	// A single line that is split across two physical reads (large delta) must
	// be reassembled by the scanner, not parsed as two malformed lines. This
	// exercises the lineScanner, not parseGrokLine directly.
	large := strings.Repeat("x", 80*1024) // > 64KiB (bufio.Scanner cap)
	full := `{"type":"message","delta":"` + large + `"}`
	scanner := newLineScanner(strings.NewReader(full + "\n"))
	line, hasMore := scanner.Next()
	if !hasMore || string(line) != full {
		t.Fatalf("scanner did not reassemble the long line (got %d bytes, hasMore=%v)", len(line), hasMore)
	}
	evs, _, status := parseGrokLine(line, nil)
	if status != parseOK || len(evs) != 1 {
		t.Errorf("long line parse failed: %+v (status %d)", evs, status)
	}
}

func TestLineScannerStripsBOM(t *testing.T) {
	bom := []byte{0xEF, 0xBB, 0xBF}
	input := string(bom) + `{"type":"message","delta":"hi"}` + "\n"
	scanner := newLineScanner(strings.NewReader(input))
	line, hasMore := scanner.Next()
	if !hasMore {
		t.Fatal("hasMore=false")
	}
	if len(line) > 0 && (line[0] == bom[0]) {
		t.Errorf("BOM not stripped: %x", line[:min(3, len(line))])
	}
	evs, _, status := parseGrokLine(line, nil)
	if status != parseOK || len(evs) != 1 {
		t.Errorf("BOM-prefixed line parse failed: %+v", evs)
	}
}

func TestLineScannerEOF(t *testing.T) {
	scanner := newLineScanner(strings.NewReader(""))
	line, hasMore := scanner.Next()
	if line != nil || hasMore {
		t.Errorf("empty stream should be EOF, got line=%v hasMore=%v", line, hasMore)
	}
}

func TestParseGrokLineFileScope(t *testing.T) {
	// Empty scope → in_scope true (whole workspace).
	evs, _, _ := parseGrokLine([]byte(`{"type":"file","path":"src/a.go","action":"modified"}`), nil)
	if !evs[0].FileChange.InScope {
		t.Error("empty scope should be in-scope")
	}
	// Scope restricts: outside path → in_scope false.
	evs, _, _ = parseGrokLine([]byte(`{"type":"file","path":"OUTSIDE/secret.go","action":"created"}`), []string{"src", "docs"})
	if evs[0].FileChange.InScope {
		t.Error("OUTSIDE/secret.go should be out-of-scope")
	}
	// Inside scope prefix → true.
	evs, _, _ = parseGrokLine([]byte(`{"type":"file","path":"src/a.go","action":"modified"}`), []string{"src"})
	if !evs[0].FileChange.InScope {
		t.Error("src/a.go should be in-scope")
	}
}

func TestNormalizeAction(t *testing.T) {
	cases := map[string]string{
		"created": "created", "added": "created", "deleted": "deleted",
		"modified": "modified", "updated": "modified", "wrote": "modified", "": "modified",
	}
	for in, want := range cases {
		if got := normalizeAction(in); got != want {
			t.Errorf("normalizeAction(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseGrokLineRoundTripsJSON(t *testing.T) {
	// Every emitted event must be JSON-marshallable (the sink may serialize it).
	evs, _, _ := parseGrokLine([]byte(`{"type":"usage","input_tokens":1,"output_tokens":2,"cost":0.5}`), nil)
	for _, ev := range evs {
		if _, err := json.Marshal(ev); err != nil {
			t.Errorf("event not marshallable: %v", err)
		}
	}
}

func typesOf(evs []protocol.NormalizedEvent) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = string(e.Type)
	}
	return out
}
