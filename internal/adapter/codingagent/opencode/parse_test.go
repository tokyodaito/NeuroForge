package opencode

import (
	"bytes"
	"strings"
	"testing"

	"neuroforge/internal/adapter/codingagent/protocol"
)

func TestJSONLScannerStripsBOMAndCRLF(t *testing.T) {
	// A UTF-8 BOM, then two CRLF-terminated lines, then a final line with LF.
	in := append([]byte{0xEF, 0xBB, 0xBF},
		[]byte("{\"type\":\"run.started\"}\r\n{\"type\":\"message.delta\"}\r\n{\"type\":\"run.completed\"}\n")...)
	s := newJSONLScanner(bytes.NewReader(in))
	var lines []string
	for {
		line, hasMore := s.next()
		if line != nil {
			lines = append(lines, string(line))
		}
		if !hasMore && line == nil {
			break
		}
	}
	if len(lines) != 3 {
		t.Fatalf("got %d lines: %v", len(lines), lines)
	}
	for _, l := range lines {
		if strings.HasSuffix(l, "\r") || strings.HasSuffix(l, "\n") {
			t.Errorf("line not stripped of CR/LF: %q", l)
		}
	}
}

func TestJSONLScannerNoCapOnLongLine(t *testing.T) {
	// A line far larger than bufio.Scanner's 64KiB default must be read whole.
	big := strings.Repeat("a", 200_000)
	in := "{\"type\":\"message.delta\",\"message\":{\"delta\":\"" + big + "\"}}\n{\"type\":\"run.completed\"}\n"
	s := newJSONLScanner(strings.NewReader(in))
	line1, _ := s.next()
	if !strings.Contains(string(line1), "message.delta") {
		t.Errorf("long line type not parsed: %q...", string(line1[:40]))
	}
	if len(line1) < 200_000 {
		t.Errorf("long line truncated: got %d bytes", len(line1))
	}
}

func TestParseLineWellFormed(t *testing.T) {
	ev, hasContent, err := parseLine([]byte(`{"type":"message.delta","message":{"delta":"hi"}}`))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !hasContent || ev.Type != protocol.EventMessageDelta {
		t.Fatalf("got %+v hasContent=%v", ev, hasContent)
	}
}

func TestParseLineEmptySkipped(t *testing.T) {
	_, hasContent, err := parseLine([]byte("   \r\n"))
	if hasContent || err != nil {
		t.Errorf("blank line should be skipped: hasContent=%v err=%v", hasContent, err)
	}
}

func TestParseLineMalformedYieldsWarning(t *testing.T) {
	ev, hasContent, err := parseLine([]byte("{not valid json"))
	if !hasContent {
		t.Fatal("malformed line should still have content")
	}
	if err == nil {
		t.Fatal("expected a recoverable error for malformed json")
	}
	if ev.Type != protocol.EventWarning {
		t.Errorf("type = %s, want warning", ev.Type)
	}
}

func TestParseLineUnknownEventTypeYieldsWarning(t *testing.T) {
	ev, hasContent, err := parseLine([]byte(`{"type":"spaceship.landed"}`))
	if !hasContent || err == nil {
		t.Fatalf("expected recoverable error for unknown type")
	}
	if ev.Type != protocol.EventWarning {
		t.Errorf("type = %s, want warning", ev.Type)
	}
}
