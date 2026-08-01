package gemini

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// utf8BOM is the byte-order mark some tools prepend to UTF-8 output.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// bufReader is a type alias for bufio.Reader so the scanner reads line fragments
// without bufio.Scanner's 64KiB token cap.
type bufReader = bufio.Reader

func newBufReader(r io.Reader) *bufReader { return bufio.NewReader(r) }

// frameMeta carries the run identity used to populate synthesized events.
type frameMeta struct {
	runID    string
	engine   string
	model    string
	isResume bool
}

// parseResult is the outcome of parsing one agent stdout stream. Body events
// exclude the frame-open (run.started/resumed) and terminal events, which are
// managed by the supervise loop. Terminal is the stream's own terminal event
// when the output is protocol JSONL (forward-compat), else nil.
type parseResult struct {
	body      []protocol.NormalizedEvent
	terminal  *protocol.NormalizedEvent
	malformed [][]byte
}

// readAll reads an agent stdout stream to EOF using an unbounded line scanner
// (bufio.Scanner's 64KiB cap would truncate large agent payloads), strips a
// leading UTF-8 BOM, and returns the accumulated bytes. CRLF and LF line
// endings are tolerated; a final line without a trailing newline is preserved.
//
// readAll blocks until the reader reaches EOF. The supervise loop calls it from
// a goroutine that is preempted by context cancellation (the group is killed,
// unblocking the read), so the adapter never blocks forever on a stdout read.
func readAll(r io.Reader) ([]byte, error) {
	sc := newStreamScanner(r)
	var buf bytes.Buffer
	for {
		line, hasMore := sc.next()
		if line != nil {
			buf.Write(line)
			buf.WriteByte('\n')
		}
		if !hasMore {
			break
		}
	}
	return buf.Bytes(), nil
}

// stripBOM removes a single leading UTF-8 BOM if present.
func stripBOM(raw []byte) []byte {
	if bytes.HasPrefix(raw, utf8BOM) {
		return raw[len(utf8BOM):]
	}
	return raw
}

// parseStream interprets the agent's stdout bytes as normalized events.
//
// It sniffs the first non-empty line to decide the output shape:
//
//   - Protocol JSONL mode (forward-compatible): the first line is a JSON object
//     carrying a non-empty "type" field. Each line is parsed with
//     [protocol.ParseEventLine]. Unknown event types and malformed JSON become
//     recoverable warnings (never fatal) — exactly the spec's robustness rule.
//   - Gemini document mode (the actual `--output-format json` shape): the whole
//     stdout is one JSON response document. It is decoded and translated into a
//     synthesized sequence (message.completed, usage.updated).
//
// In either mode, malformed input is collected into malformed for the supervise
// loop to persist as artifacts and surface as warning events; parsing never
// aborts the run.
func parseStream(raw []byte, md frameMeta) parseResult {
	raw = stripBOM(raw)
	if looksLikeProtocolJSONL(raw) {
		return parseJSONL(raw, md)
	}
	return parseGeminiDocument(raw, md)
}

// looksLikeProtocolJSONL reports whether the first non-empty line is a JSON
// object carrying a non-empty "type" field (a protocol event discriminator). A
// Gemini response document has no top-level "type", so this cleanly selects the
// document path for real Gemini output and the JSONL path for forward-compat
// protocol streams.
func looksLikeProtocolJSONL(raw []byte) bool {
	for _, line := range splitLines(raw) {
		trim := bytes.TrimSpace(line)
		if len(trim) == 0 {
			continue
		}
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(trim, &head); err != nil {
			return false
		}
		return head.Type != ""
	}
	return false
}

// parseJSONL parses protocol-JSONL output line-by-line via
// [protocol.ParseEventLine]. Frame-open and terminal events are separated so the
// supervise loop owns run.started/resumed and terminal synthesis. Malformed and
// unknown-future events are recoverable: they are collected as malformed
// artifacts and surfaced as warnings, never fatal.
func parseJSONL(raw []byte, _ frameMeta) parseResult {
	var res parseResult
	for _, line := range splitLines(raw) {
		trim := bytes.TrimSpace(line)
		if len(trim) == 0 {
			continue
		}
		ev, perr := protocol.ParseEventLine(trim)
		if perr != nil {
			res.malformed = append(res.malformed, append([]byte(nil), trim...))
			if ev.Type != "" {
				res.body = append(res.body, ev)
			}
			continue
		}
		switch ev.Type {
		case protocol.EventRunStarted, protocol.EventRunResumed:
			// Frame-open owned by the supervise loop; drop.
		case protocol.EventRunCompleted, protocol.EventRunFailed, protocol.EventRunCancelled:
			term := ev
			res.terminal = &term
		default:
			res.body = append(res.body, ev)
		}
	}
	return res
}

// parseGeminiDocument decodes the stdout as a single Gemini JSON response and
// translates it into body events (message.completed for the response text,
// usage.updated for reported tokens). The terminal is left to the supervise
// loop, which synthesizes run.completed/run.failed from the process exit code.
func parseGeminiDocument(raw []byte, md frameMeta) parseResult {
	trim := bytes.TrimSpace(raw)
	if len(trim) == 0 {
		// No output at all: nothing to translate; the supervise loop synthesizes
		// the terminal from the exit code.
		return parseResult{}
	}
	resp, ok := decodeGeminiResponse(trim)
	if !ok {
		return parseResult{malformed: [][]byte{append([]byte(nil), trim...)}}
	}

	var body []protocol.NormalizedEvent
	now := time.Now()
	if text := resp.responseText(); text != "" {
		body = append(body, protocol.NormalizedEvent{
			Type:      protocol.EventMessageCompleted,
			Timestamp: now,
			RunID:     md.runID,
			Engine:    md.engine,
			Model:     md.model,
			Message:   &protocol.MessagePayload{Text: text, Role: "assistant"},
		})
	}
	if resp.hasUsage() {
		meta, _ := resp.Usage.mergeMetadata()
		body = append(body, protocol.NormalizedEvent{
			Type:      protocol.EventUsageUpdated,
			Timestamp: now,
			RunID:     md.runID,
			Engine:    md.engine,
			Model:     md.model,
			Usage:     usagePtr(mapUsage(meta)),
		})
	}
	return parseResult{body: body}
}

func usagePtr(u protocol.UsagePayload) *protocol.UsagePayload { return &u }

// splitLines splits raw on LF, trimming a single trailing CR per line (tolerates
// CRLF and lone CR). Empty trailing elements are kept only when non-empty so the
// caller can skip blanks uniformly.
func splitLines(raw []byte) [][]byte {
	if len(raw) == 0 {
		return nil
	}
	var lines [][]byte
	start := 0
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\n' {
			lines = append(lines, copyLine(raw[start:i]))
			start = i + 1
		}
	}
	if start <= len(raw) {
		lines = append(lines, copyLine(raw[start:]))
	}
	return lines
}

// copyLine returns a copy of line with a single trailing CR removed.
func copyLine(line []byte) []byte {
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	out := make([]byte, len(line))
	copy(out, line)
	return out
}

// streamScanner reads newline-delimited bytes without bufio.Scanner's 64KiB
// token limit (agent lines can be large). It accumulates long lines across
// reads. Mirrors the declarative adapter's scanner contract but is local to this
// package (the declarative one is unexported and must not be depended on).
type streamScanner struct {
	r     *bufReader
	atEOF bool
}

func newStreamScanner(r io.Reader) *streamScanner {
	return &streamScanner{r: newBufReader(r)}
}

// next returns the next line (without the trailing newline) and whether more
// data may follow. A nil line with hasMore=false means EOF.
func (s *streamScanner) next() (line []byte, hasMore bool) {
	if s.atEOF {
		return nil, false
	}
	frag, isPrefix, err := s.r.ReadLine()
	if err != nil {
		s.atEOF = true
		return frag, false // frag may be non-nil for a final line w/o newline.
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
