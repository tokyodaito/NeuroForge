package codex

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// utf8BOM is the UTF-8 byte-order mark a Codex build may prepend to its first
// line.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// lineScanner reads newline-delimited bytes without bufio.Scanner's default
// 64KiB token limit (agent lines can be large — a single file.changed event can
// carry a whole diff). It is the same approach as the declarative adapter's
// scanner, duplicated here so this package depends only on the frozen protocol
// + proctree packages.
type lineScanner struct {
	r     *bufio.Reader
	atEOF bool
}

func newLineScanner(r io.Reader) *lineScanner { return &lineScanner{r: bufio.NewReader(r)} }

// next returns the next line (without the trailing newline) and hasMore reports
// whether more data may follow. A nil line with hasMore=false means EOF. A final
// line lacking a trailing newline is still returned (hasMore=false).
func (s *lineScanner) next() (line []byte, hasMore bool) {
	if s.atEOF {
		return nil, false
	}
	frag, isPrefix, err := s.r.ReadLine()
	if err != nil {
		s.atEOF = true
		return frag, false // frag may be non-nil for a final newline-less line
	}
	if !isPrefix {
		return append([]byte(nil), frag...), true
	}
	// Long line: accumulate until the final fragment (no 64KiB cap).
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

// stripBOM removes a single leading UTF-8 BOM, if present.
func stripBOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == utf8BOM[0] && b[1] == utf8BOM[1] && b[2] == utf8BOM[2] {
		return b[3:]
	}
	return b
}

// parseCodexLine decodes one stdout line into a normalized event. It is layered:
//
//  1. Tolerate CRLF and a leading UTF-8 BOM, and skip blank lines.
//  2. Try protocol.ParseEventLine first. If the line is already a NeuroForge
//     NormalizedEvent (e.g. a Codex build configured to emit our schema, or our
//     own conformance fixtures) it is used verbatim — reusing the shared
//     malformed/unknown handling.
//  3. If ParseEventLine reports an unknown event type, the line is valid JSON
//     whose type is not NeuroForge-native: attempt [mapCodexEvent] to interpret
//     it as a Codex event. If the mapper recognizes it, the mapped event is
//     returned; otherwise the unknown-type warning from step 2 stands (with the
//     raw bytes attached).
//  4. If ParseEventLine reports malformed JSON, that warning stands (the caller
//     persists the raw bytes as an artifact).
//
// A line is never fatal (spec: unknown/malformed events never abort a run).
func parseCodexLine(line []byte, now time.Time) (protocol.NormalizedEvent, error) {
	line = stripBOM(line)
	// Trim a trailing CR/CRLF defensively (ParseEventLine also trims, but the
	// BOM-stripped slice may still carry a stray CR on CRLF streams).
	for len(line) > 0 && (line[len(line)-1] == '\n' || line[len(line)-1] == '\r') {
		line = line[:len(line)-1]
	}
	if len(line) == 0 {
		return protocol.NormalizedEvent{}, protocol.ErrEmptyLine
	}

	ev, err := protocol.ParseEventLine(line)
	if err == nil {
		return ev, nil
	}
	// Only the unknown-event-type case is retried through the Codex mapper.
	// Malformed JSON (not parseable at all) and malformed payloads keep the
	// shared warning from ParseEventLine.
	if !isUnknownType(err) {
		return ev, err
	}
	if mapped, ok := mapCodexEvent(line, now); ok {
		return mapped, nil
	}
	return ev, err
}

func isUnknownType(err error) bool {
	var u protocol.UnknownEventTypeError
	return errors.As(err, &u)
}

// extractSessionID pulls a Codex session/thread id out of a raw JSON line if one
// is present. It inspects the union of field names Codex versions use
// (session_id, thread_id, session, conversation_id). Empty string when absent
// or when the line is not JSON.
func extractSessionID(line []byte) string {
	line = stripBOM(line)
	var obj map[string]any
	if err := json.Unmarshal(line, &obj); err != nil {
		return ""
	}
	if s := strVal(obj, "session_id", "thread_id", "session", "conversation_id"); s != "" {
		return s
	}
	// Some Codex versions nest the id one level deep ({"session":{"id":"..."}}).
	if sess, ok := obj["session"].(map[string]any); ok {
		if s := strVal(sess, "id", "session_id"); s != "" {
			return s
		}
	}
	return ""
}

// mapCodexEvent interprets a raw JSON line as a Codex event. It returns
// (event, true) when it recognizes the event, or (zero, false) when the line is
// not a known Codex event (the caller then keeps the unknown-type warning).
//
// The discriminator is the "type" field; field-name alternatives are tried in
// precedence order to cover multiple Codex releases without pinning one schema.
// Unknown fields are ignored (additive tolerance).
func mapCodexEvent(raw []byte, now time.Time) (protocol.NormalizedEvent, bool) {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return protocol.NormalizedEvent{}, false
	}
	typ, _ := obj["type"].(string)

	// Common envelope: some Codex versions wrap the real payload under "data" or
	// "payload". If present and itself an object, prefer it for field lookup
	// while keeping the outer type as the discriminator.
	payload := obj
	if d, ok := obj["data"].(map[string]any); ok {
		payload = d
		if t, _ := d["type"].(string); t != "" {
			typ = t
		}
	}
	if p, ok := obj["payload"].(map[string]any); ok {
		payload = p
		if t, _ := p["type"].(string); t != "" {
			typ = t
		}
	}

	base := protocol.NormalizedEvent{
		Timestamp: now,
		Raw:       append([]byte(nil), raw...),
		Model:     strVal(payload, "model", "model_id"),
	}

	switch typ {
	case "task_started", "session_started", "session.start", "start", "turn_context":
		ev := base
		ev.Type = protocol.EventRunStarted
		return ev, true

	case "task_complete", "task_completed", "session_completed", "session.end", "completed", "done", "turn_complete":
		ev := base
		ev.Type = protocol.EventRunCompleted
		return ev, true

	case "agent_message", "assistant_message", "agent_message_completed", "message.completed", "message":
		ev := base
		ev.Type = protocol.EventMessageCompleted
		ev.Message = &protocol.MessagePayload{Text: strVal(payload, "message", "text", "content")}
		return ev, true

	case "agent_message_delta", "message.delta", "message_delta", "delta", "stream_event":
		ev := base
		ev.Type = protocol.EventMessageDelta
		ev.Message = &protocol.MessagePayload{Delta: strVal(payload, "delta", "message", "text", "content")}
		return ev, true

	case "token_count", "usage", "tokens", "token_counts", "cost":
		u, ok := mapUsage(payload)
		if !ok {
			return base, false
		}
		ev := base
		ev.Type = protocol.EventUsageUpdated
		ev.Usage = u
		return ev, true

	case "exec_command_begin", "shell_call_begin", "command_begin", "command.started", "exec.begin":
		ev := base
		ev.Type = protocol.EventCommandStarted
		ev.Command = &protocol.CommandPayload{CommandLine: commandLineFrom(payload), CommandID: strVal(payload, "call_id", "id", "command_id")}
		return ev, true

	case "exec_command_end", "shell_call_end", "command_end", "command.completed", "exec.end":
		ev := base
		ev.Type = protocol.EventCommandCompleted
		ev.Command = &protocol.CommandPayload{
			CommandID:   strVal(payload, "call_id", "id", "command_id"),
			CommandLine: commandLineFrom(payload),
			ExitCode:    int(numInt(payload, "exit_code")),
			Success:     boolVal(payload, false, "success", "ok"),
		}
		return ev, true

	case "file_change", "file.changed", "patch", "patch_begin", "patch_end", "write_file", "apply_patch":
		ev := base
		ev.Type = protocol.EventFileChanged
		ev.FileChange = mapFileChange(payload)
		return ev, true

	case "mcp_tool_call_begin", "tool_call_begin", "tool.started":
		ev := base
		ev.Type = protocol.EventToolStarted
		ev.Tool = &protocol.ToolPayload{Name: strVal(payload, "tool", "name", "tool_name"), ToolID: strVal(payload, "call_id", "id")}
		return ev, true

	case "mcp_tool_call_end", "tool_call_end", "tool.completed":
		ev := base
		ev.Type = protocol.EventToolCompleted
		status := strVal(payload, "status")
		if status == "" {
			if boolVal(payload, false, "success", "ok") {
				status = "completed"
			} else {
				status = "failed"
			}
		}
		ev.Tool = &protocol.ToolPayload{
			Name:   strVal(payload, "tool", "name", "tool_name"),
			ToolID: strVal(payload, "call_id", "id"),
			Status: status,
			Detail: strVal(payload, "detail", "error"),
		}
		return ev, true

	case "error", "fatal_error":
		ev := base
		ev.Type = protocol.EventWarning
		msg := strVal(payload, "error", "message", "msg")
		if msg == "" {
			msg = "codex reported an error"
		}
		ev.Warning = &protocol.WarningPayload{
			Code:        "codex.error",
			Message:     msg,
			Recoverable: typ != "fatal_error",
		}
		return ev, true
	}

	// Unrecognized Codex type: signal "not mapped" so the caller keeps the
	// unknown-type warning with the raw bytes.
	return base, false
}

// mapFileChange builds a FileChangePayload from a decoded Codex file/patch event.
// Codex reports the changed path(s) under various names; action defaults to
// "modified". inScope is left true (the workspace manager enforces the real
// scope check; the adapter does not over-claim).
func mapFileChange(obj map[string]any) *protocol.FileChangePayload {
	path := strVal(obj, "path", "file", "file_path", "changed_file")
	if path == "" {
		// Codex patch events sometimes carry a list of paths.
		if paths := anySlice(obj, "paths", "files", "changes"); len(paths) > 0 {
			path = paths[0]
		}
	}
	action := strVal(obj, "action", "change", "op")
	if action == "" {
		switch strVal(obj, "type") {
		case "create", "created":
			action = "created"
		case "delete", "deleted":
			action = "deleted"
		default:
			action = "modified"
		}
	}
	return &protocol.FileChangePayload{Path: path, Action: action, InScope: true}
}

// commandLineFrom reconstructs a display string for a Codex command event. Codex
// carries the argv either as an array under "command"/"args" or as a string.
func commandLineFrom(obj map[string]any) string {
	if s := strVal(obj, "command", "cmd", "command_line"); s != "" {
		return s
	}
	if args := anySlice(obj, "command", "args", "argv"); len(args) > 0 {
		return joinArgs(args)
	}
	return ""
}

// anySlice returns string elements of the first present array field among names.
func anySlice(obj map[string]any, names ...string) []string {
	for _, n := range names {
		v, ok := obj[n]
		if !ok {
			continue
		}
		switch x := v.(type) {
		case []any:
			out := make([]string, 0, len(x))
			for _, e := range x {
				if s, ok := e.(string); ok {
					out = append(out, s)
				}
			}
			if len(out) > 0 {
				return out
			}
		case []string:
			if len(x) > 0 {
				return append([]string(nil), x...)
			}
		}
	}
	return nil
}

func joinArgs(args []string) string {
	out := ""
	for i, a := range args {
		if i > 0 {
			out += " "
		}
		out += a
	}
	return out
}
