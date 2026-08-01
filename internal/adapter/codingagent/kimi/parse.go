package kimi

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// utf8BOM is the byte-order mark some shells prefix on output.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// bomStripper removes a single leading UTF-8 BOM from the first read of r. It
// transparently passes through everything else, so CRLF and long lines are
// handled downstream by the line scanner.
type bomStripper struct {
	r       *bufio.Reader
	checked bool
}

func newBomStripper(r io.Reader) *bomStripper {
	return &bomStripper{r: bufio.NewReader(r)}
}

func (b *bomStripper) Read(p []byte) (int, error) {
	if !b.checked {
		b.checked = true
		// Peek up to 3 bytes; if they are the BOM, discard them.
		if peek, _ := b.r.Peek(3); len(peek) >= 3 && peek[0] == utf8BOM[0] && peek[1] == utf8BOM[1] && peek[2] == utf8BOM[2] {
			_, _ = b.r.Discard(3)
		}
	}
	return b.r.Read(p)
}

// lineScanner reads newline-delimited bytes without bufio.Scanner's default
// 64KiB token limit (agent lines can be large — spec requires no cap). Next
// returns the next line without its trailing newline/CRLF and hasMore reports
// whether more data may follow. At EOF with no pending line it returns
// (nil, false).
type lineScanner struct {
	r     *bufio.Reader
	atEOF bool
}

func newLineScanner(r io.Reader) *lineScanner { return &lineScanner{r: bufio.NewReader(r)} }

func (s *lineScanner) Next() (line []byte, hasMore bool) {
	if s.atEOF {
		return nil, false
	}
	frag, isPrefix, err := s.r.ReadLine()
	if err != nil {
		s.atEOF = true
		return frag, false // frag may be non-nil for a final line without newline
	}
	if !isPrefix {
		return append([]byte(nil), frag...), true
	}
	// Long line: accumulate until the final fragment (no length cap).
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

// ErrEmptyLine is returned by parseKimiLine for a blank line.
var ErrEmptyLine = errors.New("kimi: empty event line")

// parseKimiLine decodes one stream-json line into a normalized event. It first
// accepts lines that already speak the NeuroForge normalized event format (so
// the same adapter is robust to engines that emit normalized JSONL), then
// translates Kimi's native stream-json item shapes. Unknown/malformed lines are
// returned as a recoverable warning event carrying the raw bytes plus a
// non-nil error so the caller can persist the artifact (spec: malformed output
// is saved + classified, never fatal).
func parseKimiLine(line []byte) (protocol.NormalizedEvent, error) {
	line = trimLine(line)
	if len(line) == 0 {
		return protocol.NormalizedEvent{}, ErrEmptyLine
	}

	// Fast path: the line is already a normalized protocol event.
	if ev, err := protocol.ParseEventLine(line); err == nil {
		return ev, nil
	}

	// Translate a native Kimi stream-json item.
	if ev, ok := translateKimiLine(line); ok {
		return ev, nil
	}

	// Could not translate: return a recoverable warning carrying the raw bytes.
	raw := append(json.RawMessage(nil), line...)
	return protocol.NormalizedEvent{
		Type:      protocol.EventWarning,
		Timestamp: time.Now(),
		Warning: &protocol.WarningPayload{
			Code:        "malformed.json",
			Message:     "kimi: unrecognized stream-json line",
			Recoverable: true,
		},
		Raw: raw,
	}, malformedLineError{raw: line}
}

// trimLine strips a leading UTF-8 BOM (defensive; the stream wrapper already
// removes the first one) and trailing CR/LF.
func trimLine(line []byte) []byte {
	for len(line) >= 3 && line[0] == utf8BOM[0] && line[1] == utf8BOM[1] && line[2] == utf8BOM[2] {
		line = line[3:]
	}
	for len(line) > 0 && (line[len(line)-1] == '\n' || line[len(line)-1] == '\r') {
		line = line[:len(line)-1]
	}
	return line
}

type malformedLineError struct{ raw []byte }

func (e malformedLineError) Error() string { return "kimi: malformed stream-json line" }

// kimiItem is a permissive decode of a Kimi stream-json object: every field is
// optional so unknown/future fields are ignored (spec: unknown events do not
// break a run) and only the recognized shape is translated.
type kimiItem struct {
	Type      string          `json:"type"`
	Event     string          `json:"event"`
	Subtype   string          `json:"subtype"`
	Role      string          `json:"role"`
	SessionID string          `json:"session_id"`
	Model     string          `json:"model"`
	Text      string          `json:"text"`
	Content   json.RawMessage `json:"content"`
	Tool      string          `json:"tool"`
	Name      string          `json:"name"`
	ToolUseID string          `json:"tool_use_id"`
	Input     json.RawMessage `json:"input"`
	Command   string          `json:"command"`
	ExitCode  *int            `json:"exit_code"`
	Path      string          `json:"path"`
	Action    string          `json:"action"`
	Error     string          `json:"error"`
	Class     string          `json:"class"`
	Reason    string          `json:"reason"`
	Usage     *kimiUsage      `json:"usage"`
	Cost      *float64        `json:"cost"`
	Currency  string          `json:"currency"`
	IsResume  *bool           `json:"is_resume"`
	Success   *bool           `json:"success"`
}

// kimiUsage mirrors the usage object Kimi embeds in usage/result items.
type kimiUsage struct {
	InputTokens      *int64   `json:"input_tokens"`
	OutputTokens     *int64   `json:"output_tokens"`
	CacheReadTokens  *int64   `json:"cache_read_tokens"`
	CacheWriteTokens *int64   `json:"cache_write_tokens"`
	ReasoningTokens  *int64   `json:"reasoning_tokens"`
	Cost             *float64 `json:"cost"`
	Currency         string   `json:"currency"`
}

func translateKimiLine(line []byte) (protocol.NormalizedEvent, bool) {
	var it kimiItem
	if err := json.Unmarshal(line, &it); err != nil {
		return protocol.NormalizedEvent{}, false
	}
	ev, ok := translateKimiItem(it)
	if !ok {
		return protocol.NormalizedEvent{}, false
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now()
	}
	// Keep the raw bytes so the supervisor can extract metadata (e.g. the
	// session id on the init item) and persist it for forensics.
	ev.Raw = append(json.RawMessage(nil), line...)
	return ev, true
}

// translateKimiItem maps a recognized Kimi stream-json shape onto a normalized
// event. Returns ok=false for shapes the adapter does not understand (the
// caller surfaces them as recoverable warnings).
func translateKimiItem(it kimiItem) (protocol.NormalizedEvent, bool) {
	ts := time.Now()
	sub := it.Event
	if sub == "" {
		sub = it.Subtype
	}
	switch it.Type {
	case "system":
		// Session init / resume. The session id and model are carried in Raw;
		// the supervisor extracts them. Emit run.resumed when the engine says
		// this is a resumed session.
		evType := protocol.EventRunStarted
		if sub == "resume" || boolVal(it.IsResume) {
			evType = protocol.EventRunResumed
		}
		return protocol.NormalizedEvent{Type: evType, Timestamp: ts, Model: it.Model}, true

	case "assistant", "message", "text":
		switch sub {
		case "text", "delta", "message", "":
			text := it.Text
			if text == "" {
				text = contentText(it.Content)
			}
			role := it.Role
			if role == "" {
				role = "assistant"
			}
			return protocol.NormalizedEvent{
				Type: protocol.EventMessageDelta, Timestamp: ts,
				Message: &protocol.MessagePayload{Delta: text, Role: role},
			}, true
		case "tool_use", "tool_start":
			return protocol.NormalizedEvent{
				Type: protocol.EventToolStarted, Timestamp: ts,
				Tool: &protocol.ToolPayload{ToolID: it.ToolUseID, Name: firstNonEmpty(it.Tool, it.Name), Status: "running"},
			}, true
		case "command":
			return commandEvent(it, ts, true), true
		}

	case "user":
		if sub == "tool_result" {
			status := "completed"
			if it.Success != nil && !*it.Success {
				status = "failed"
			}
			return protocol.NormalizedEvent{
				Type: protocol.EventToolCompleted, Timestamp: ts,
				Tool: &protocol.ToolPayload{ToolID: it.ToolUseID, Name: firstNonEmpty(it.Tool, it.Name), Status: status, Detail: contentText(it.Content)},
			}, true
		}

	case "command":
		return commandEvent(it, ts, false), true

	case "file":
		return protocol.NormalizedEvent{
			Type: protocol.EventFileChanged, Timestamp: ts,
			FileChange: &protocol.FileChangePayload{Path: it.Path, Action: normalizeAction(it.Action), InScope: true},
		}, true

	case "usage":
		return protocol.NormalizedEvent{Type: protocol.EventUsageUpdated, Timestamp: ts, Usage: usagePayload(it.Usage, it, it.Currency)}, true

	case "result":
		switch sub {
		case "success", "completed", "":
			return protocol.NormalizedEvent{
				Type: protocol.EventRunCompleted, Timestamp: ts, Model: it.Model,
				Usage: usagePayload(it.Usage, it, it.Currency),
			}, true
		case "error", "failure":
			fc := classifyResult(it)
			return protocol.NormalizedEvent{
				Type: protocol.EventRunFailed, Timestamp: ts, Model: it.Model,
				Failure: &protocol.FailurePayload{Class: fc, Reason: firstNonEmpty(it.Error, it.Reason)},
			}, true
		}
	}

	// A usage object attached to any item type is still useful usage telemetry.
	if it.Usage != nil || it.Cost != nil || it.Type == "" && hasUsage(&it) {
		return protocol.NormalizedEvent{Type: protocol.EventUsageUpdated, Timestamp: ts, Usage: usagePayload(it.Usage, it, it.Currency)}, true
	}
	return protocol.NormalizedEvent{}, false
}

func commandEvent(it kimiItem, ts time.Time, startedOnly bool) protocol.NormalizedEvent {
	exit := 0
	success := true
	if it.ExitCode != nil {
		exit = *it.ExitCode
		success = exit == 0
	}
	if startedOnly {
		// We only get a completed-shaped command from Kimi; emit completed with
		// the exit code when present, otherwise a started.
		if it.ExitCode == nil {
			return protocol.NormalizedEvent{
				Type: protocol.EventCommandStarted, Timestamp: ts,
				Command: &protocol.CommandPayload{CommandLine: it.Command},
			}
		}
	}
	return protocol.NormalizedEvent{
		Type: protocol.EventCommandCompleted, Timestamp: ts,
		Command: &protocol.CommandPayload{CommandLine: it.Command, ExitCode: exit, Success: success},
	}
}

// classifyResult maps a Kimi result/error item onto a §32 class, using the
// engine-supplied `class` hint when present and otherwise inferring from the
// error text.
func classifyResult(it kimiItem) protocol.FailureClass {
	if c := protocol.FailureClass(it.Class); c.IsValid() {
		return c
	}
	return classFromText(it.Error + " " + it.Reason)
}

// contentText extracts a textual payload from a Kimi content field, which may be
// a plain string or an array of content blocks ({"type":"text","text":"..."}).
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Plain string?
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Array of blocks?
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var b strings.Builder
		for _, blk := range blocks {
			if blk.Type == "" || blk.Type == "text" {
				b.WriteString(blk.Text)
			}
		}
		return b.String()
	}
	return ""
}

func hasUsage(it *kimiItem) bool {
	return it.Usage != nil || it.Cost != nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func boolVal(p *bool) bool { return p != nil && *p }

func normalizeAction(a string) string {
	switch a {
	case "created", "modified", "deleted":
		return a
	case "create", "new":
		return "created"
	case "modify", "update", "updated", "write", "wrote":
		return "modified"
	case "delete", "remove", "removed":
		return "deleted"
	case "":
		return "modified"
	default:
		return a
	}
}
