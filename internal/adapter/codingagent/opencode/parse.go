package opencode

import (
	"bufio"
	"io"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// utf8BOM is the UTF-8 byte-order mark, tolerated at the very start of a stream
// (some Windows shells and toolchains emit it).
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// jsonlScanner reads newline-delimited bytes WITHOUT bufio.Scanner's default
// 64KiB token limit (agent lines can be large) and tolerates a leading UTF-8
// BOM and CRLF line endings. next returns the next line (without the trailing
// newline/CRLF) and whether more data may follow. A nil line with hasMore=false
// means EOF.
type jsonlScanner struct {
	r          *bufio.Reader
	bomChecked bool
}

func newJSONLScanner(r io.Reader) *jsonlScanner {
	return &jsonlScanner{r: bufio.NewReader(r)}
}

// stripBOMOnce discards a leading UTF-8 BOM if present at the start of the
// stream.
func (s *jsonlScanner) stripBOMOnce() {
	if s.bomChecked {
		return
	}
	s.bomChecked = true
	// Peek does not consume; only discard if it really is a BOM.
	if b, err := s.r.Peek(3); err == nil && len(b) == 3 && b[0] == utf8BOM[0] && b[1] == utf8BOM[1] && b[2] == utf8BOM[2] {
		_, _ = s.r.Discard(3)
	}
}

// next returns the next line. bufio.Reader.ReadLine returns the line without the
// trailing newline (it consumes both "\n" and "\r\n"), and reports isPrefix when
// the line exceeds the internal buffer so it can be accumulated without a 64KiB
// cap.
func (s *jsonlScanner) next() (line []byte, hasMore bool) {
	s.stripBOMOnce()
	frag, isPrefix, err := s.r.ReadLine()
	if err != nil {
		// EOF or read error: frag may hold a final line without a terminator.
		return frag, false
	}
	if !isPrefix {
		return append([]byte(nil), frag...), true
	}
	acc := append([]byte(nil), frag...)
	for isPrefix {
		frag, isPrefix, err = s.r.ReadLine()
		if err != nil {
			break
		}
		acc = append(acc, frag...)
	}
	return acc, true
}

// parseLine decodes one JSONL line via the canonical protocol parser. On a
// recoverable parse problem (malformed JSON, unknown event type, bad payload) it
// returns the non-nil warning event produced by [protocol.ParseEventLine] plus a
// non-nil error so the caller can persist the (redacted) raw bytes as an
// artifact (spec: malformed events are saved + classified, never fatal). Only a
// truly empty line is skipped silently.
func parseLine(line []byte) (protocol.NormalizedEvent, bool, error) {
	// Tolerate stray CRLF whitespace after bufio already stripped the newline.
	trimmed := trimSpaceAndCR(line)
	if len(trimmed) == 0 {
		return protocol.NormalizedEvent{}, false, nil
	}
	ev, err := protocol.ParseEventLine(trimmed)
	return ev, true, err
}

func trimSpaceAndCR(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == ' ' || b[len(b)-1] == '\t') {
		b = b[:len(b)-1]
	}
	return b
}
