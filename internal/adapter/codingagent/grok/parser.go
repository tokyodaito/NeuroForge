package grok

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// This file maps Grok's `--output-format streaming-json` items onto the protocol
// v1 normalized event set (spec §12.4). The item schema is ASSUMED (rule §36.25):
// it is derived from the headless streaming behaviour documented for Grok Build
// and from analogous agent CLIs, and is NOT yet confirmed against every build of
// the installed `grok`. The parser is deliberately tolerant:
//
//   - Unknown top-level "type" values → emitted as a recoverable `warning` event
//     and the raw bytes are persisted by the caller; the run never aborts.
//   - Unknown fields inside a known item → ignored (standard JSON decoding).
//   - Malformed JSON → emitted as a recoverable `warning`, raw persisted.
//
// See docs/adapters/grok.md for the full mapping table.

// parseStatus reports how a line was handled.
type parseStatus int

const (
	parseOK        parseStatus = iota // line mapped to 0+ normalized events
	parseUnknown                      // top-level type not recognised → warning, raw saved
	parseMalformed                    // JSON invalid → warning, raw saved
)

// parseGrokLine decodes one streaming-json line into normalized events. It
// returns the events to emit, whether one of them is a run-terminal event
// (run.completed / run.failed), and a status describing the parse outcome so the
// caller can persist the raw bytes as an artifact when appropriate. It never
// panics and never returns an error that should abort a run.
func parseGrokLine(line []byte, scope []string) (events []protocol.NormalizedEvent, terminal bool, status parseStatus) {
	trimmed := trimLine(line)
	if len(trimmed) == 0 {
		return nil, false, parseOK
	}

	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(trimmed, &head); err != nil {
		return []protocol.NormalizedEvent{malformedWarning("malformed.json",
			"failed to decode streaming-json item: "+err.Error(), trimmed)}, false, parseMalformed
	}

	switch head.Type {
	case "", "stream", "heartbeat":
		// Ignore: no-op items some CLIs emit as keep-alives.
		return nil, false, parseOK
	case "system":
		return mapSystem(trimmed)
	case "message", "text":
		return mapMessage(trimmed)
	case "tool":
		return mapTool(trimmed)
	case "command":
		return mapCommand(trimmed)
	case "file", "file_change":
		return mapFile(trimmed, scope)
	case "usage":
		return mapUsageItem(trimmed)
	case "checkpoint":
		return mapCheckpoint(trimmed)
	case "approval":
		return mapApproval(trimmed)
	case "result":
		return mapResult(trimmed)
	case "error":
		return mapErrorItem(trimmed)
	default:
		// Unknown future item type: forward as a warning, keep going.
		return []protocol.NormalizedEvent{malformedWarning("unknown-item-type",
			fmt.Sprintf("unknown streaming-json item type %q; ignored", head.Type), trimmed)}, false, parseUnknown
	}
}

// trimLine strips a single trailing CR/LF (CRLF tolerant).
func trimLine(line []byte) []byte {
	for len(line) > 0 && (line[len(line)-1] == '\n' || line[len(line)-1] == '\r') {
		line = line[:len(line)-1]
	}
	return line
}

func now() time.Time { return time.Now().UTC() }

func malformedWarning(code, msg string, raw []byte) protocol.NormalizedEvent {
	return protocol.NormalizedEvent{
		Type:      protocol.EventWarning,
		Timestamp: now(),
		Warning: &protocol.WarningPayload{
			Code: code, Message: msg, Recoverable: true,
		},
		Raw: append([]byte(nil), raw...),
	}
}

// --- item mappers --------------------------------------------------------

func mapSystem(raw []byte) ([]protocol.NormalizedEvent, bool, parseStatus) {
	// system/init carries session metadata (notably session_id for resume). The
	// adapter synthesizes run.started/resumed itself, so this item emits no
	// event; the session id is decoded opportunistically and may be surfaced by
	// a future continuation-pack integration (spec §21).
	var s struct {
		Subtype   string `json:"subtype"`
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(raw, &s)
	return nil, false, parseOK
}

func mapMessage(raw []byte) ([]protocol.NormalizedEvent, bool, parseStatus) {
	var m struct {
		Role      string `json:"role"`
		Delta     string `json:"delta"`
		Text      string `json:"text"`
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return []protocol.NormalizedEvent{malformedWarning("malformed.payload", err.Error(), raw)}, false, parseMalformed
	}
	role := m.Role
	if role == "" {
		role = "assistant"
	}
	ev := protocol.NormalizedEvent{Timestamp: now()}
	switch {
	case m.Delta != "":
		ev.Type = protocol.EventMessageDelta
		ev.Message = &protocol.MessagePayload{MessageID: m.MessageID, Delta: m.Delta, Role: role}
	default:
		ev.Type = protocol.EventMessageCompleted
		ev.Message = &protocol.MessagePayload{MessageID: m.MessageID, Text: m.Text, Role: role}
	}
	return []protocol.NormalizedEvent{ev}, false, parseOK
}

func mapTool(raw []byte) ([]protocol.NormalizedEvent, bool, parseStatus) {
	var t struct {
		ID         string `json:"id"`
		ToolID     string `json:"tool_id"`
		Name       string `json:"name"`
		Status     string `json:"status"`
		Detail     string `json:"detail"`
		DurationMS int64  `json:"duration_ms"`
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		return []protocol.NormalizedEvent{malformedWarning("malformed.payload", err.Error(), raw)}, false, parseMalformed
	}
	id := t.ID
	if id == "" {
		id = t.ToolID
	}
	status := strings.ToLower(t.Status)
	ev := protocol.NormalizedEvent{Timestamp: now()}
	switch status {
	case "completed", "complete", "success", "ok", "done", "finished":
		ev.Type = protocol.EventToolCompleted
		ev.Tool = &protocol.ToolPayload{ToolID: id, Name: t.Name, Status: "completed", Detail: t.Detail, DurationMS: t.DurationMS}
	default:
		ev.Type = protocol.EventToolStarted
		ev.Tool = &protocol.ToolPayload{ToolID: id, Name: t.Name, Status: "running", Detail: t.Detail, DurationMS: t.DurationMS}
	}
	return []protocol.NormalizedEvent{ev}, false, parseOK
}

func mapCommand(raw []byte) ([]protocol.NormalizedEvent, bool, parseStatus) {
	var c struct {
		ID         string `json:"id"`
		CommandID  string `json:"command_id"`
		Command    string `json:"command"`
		Status     string `json:"status"`
		ExitCode   int    `json:"exit_code"`
		Success    bool   `json:"success"`
		DurationMS int64  `json:"duration_ms"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return []protocol.NormalizedEvent{malformedWarning("malformed.payload", err.Error(), raw)}, false, parseMalformed
	}
	id := c.ID
	if id == "" {
		id = c.CommandID
	}
	status := strings.ToLower(c.Status)
	ev := protocol.NormalizedEvent{Timestamp: now()}
	switch status {
	case "completed", "complete", "done", "finished":
		ev.Type = protocol.EventCommandCompleted
		ev.Command = &protocol.CommandPayload{CommandID: id, CommandLine: c.Command, ExitCode: c.ExitCode, Success: c.Success || c.ExitCode == 0, DurationMS: c.DurationMS}
	default:
		ev.Type = protocol.EventCommandStarted
		ev.Command = &protocol.CommandPayload{CommandID: id, CommandLine: c.Command}
	}
	return []protocol.NormalizedEvent{ev}, false, parseOK
}

func mapFile(raw []byte, scope []string) ([]protocol.NormalizedEvent, bool, parseStatus) {
	var f struct {
		Path   string `json:"path"`
		Action string `json:"action"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return []protocol.NormalizedEvent{malformedWarning("malformed.payload", err.Error(), raw)}, false, parseMalformed
	}
	action := normalizeAction(f.Action)
	ev := protocol.NormalizedEvent{
		Type:      protocol.EventFileChanged,
		Timestamp: now(),
		FileChange: &protocol.FileChangePayload{
			Path:    f.Path,
			Action:  action,
			InScope: inScope(f.Path, scope),
		},
	}
	return []protocol.NormalizedEvent{ev}, false, parseOK
}

func normalizeAction(a string) string {
	switch strings.ToLower(a) {
	case "create", "created", "add", "added", "new":
		return "created"
	case "delete", "deleted", "remove", "removed":
		return "deleted"
	case "modify", "modified", "update", "updated", "change", "changed", "write", "wrote":
		return "modified"
	default:
		if a == "" {
			return "modified"
		}
		return strings.ToLower(a)
	}
}

// inScope reports whether path falls within the run's allowed scope (spec
// §22.6). An empty scope means the whole workspace is allowed.
func inScope(path string, scope []string) bool {
	if len(scope) == 0 {
		return true
	}
	clean := filepath.Clean(path)
	for _, entry := range scope {
		e := filepath.Clean(entry)
		if clean == e || strings.HasPrefix(clean, e+string(filepath.Separator)) {
			return true
		}
		// Tolerate mismatched separators (scope may use "/" on every OS).
		if strings.HasPrefix(clean, strings.ReplaceAll(e, "/", string(filepath.Separator))+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func mapUsageItem(raw []byte) ([]protocol.NormalizedEvent, bool, parseStatus) {
	var u struct {
		InputTokens      int64   `json:"input_tokens"`
		OutputTokens     int64   `json:"output_tokens"`
		CacheReadTokens  int64   `json:"cache_read_tokens"`
		CacheWriteTokens int64   `json:"cache_write_tokens"`
		Cost             float64 `json:"cost"`
		Currency         string  `json:"currency"`
	}
	if err := json.Unmarshal(raw, &u); err != nil {
		return []protocol.NormalizedEvent{malformedWarning("malformed.payload", err.Error(), raw)}, false, parseMalformed
	}
	hasCost := u.Cost > 0
	payload := mapUsage(u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens, u.Cost, hasCost, u.Currency)
	return []protocol.NormalizedEvent{{Type: protocol.EventUsageUpdated, Timestamp: now(), Usage: payload}}, false, parseOK
}

func mapCheckpoint(raw []byte) ([]protocol.NormalizedEvent, bool, parseStatus) {
	var c struct {
		ID           string `json:"id"`
		CheckpointID string `json:"checkpoint_id"`
		Path         string `json:"path"`
		Reason       string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return []protocol.NormalizedEvent{malformedWarning("malformed.payload", err.Error(), raw)}, false, parseMalformed
	}
	id := c.ID
	if id == "" {
		id = c.CheckpointID
	}
	return []protocol.NormalizedEvent{{
		Type: protocol.EventCheckpointCreated, Timestamp: now(),
		Checkpoint: &protocol.CheckpointPayload{CheckpointID: id, Path: c.Path, Reason: c.Reason},
	}}, false, parseOK
}

func mapApproval(raw []byte) ([]protocol.NormalizedEvent, bool, parseStatus) {
	var a struct {
		Action  string `json:"action"`
		Detail  string `json:"detail"`
		Timeout string `json:"timeout"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return []protocol.NormalizedEvent{malformedWarning("malformed.payload", err.Error(), raw)}, false, parseMalformed
	}
	return []protocol.NormalizedEvent{{
		Type: protocol.EventApprovalRequested, Timestamp: now(),
		Approval: &protocol.ApprovalPayload{Action: a.Action, Detail: a.Detail, Timeout: a.Timeout},
	}}, false, parseOK
}

// mapResult maps the terminal `result` item. status "completed" → run.completed;
// "failed" → run.failed with the §32 class derived from error_code/message.
func mapResult(raw []byte) ([]protocol.NormalizedEvent, bool, parseStatus) {
	var r struct {
		Status    string `json:"status"`
		Text      string `json:"text"`
		ErrorCode string `json:"error_code"`
		Code      string `json:"code"`
		Message   string `json:"message"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return []protocol.NormalizedEvent{malformedWarning("malformed.payload", err.Error(), raw)}, false, parseMalformed
	}
	if strings.EqualFold(r.Status, "failed") || r.ErrorCode != "" || r.Code != "" {
		class := errorCodeToClass(r.ErrorCode, r.Code)
		reason := r.Message
		if reason == "" {
			reason = r.Text
		}
		return []protocol.NormalizedEvent{{
			Type:      protocol.EventRunFailed,
			Timestamp: now(),
			Failure:   &protocol.FailurePayload{Class: class, Reason: redactSecrets(reason)},
		}}, true, parseOK
	}
	// Completed: surface the final text as a message then a terminal run.completed.
	var evs []protocol.NormalizedEvent
	if r.Text != "" {
		evs = append(evs, protocol.NormalizedEvent{
			Type: protocol.EventMessageCompleted, Timestamp: now(),
			Message: &protocol.MessagePayload{Text: r.Text, Role: "assistant"},
		})
	}
	evs = append(evs, protocol.NormalizedEvent{Type: protocol.EventRunCompleted, Timestamp: now()})
	return evs, true, parseOK
}

// mapErrorItem maps a non-fatal or fatal `error` item. A fatal error yields
// run.failed; otherwise a warning (the run continues).
func mapErrorItem(raw []byte) ([]protocol.NormalizedEvent, bool, parseStatus) {
	var e struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Fatal   bool   `json:"fatal"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return []protocol.NormalizedEvent{malformedWarning("malformed.payload", err.Error(), raw)}, false, parseMalformed
	}
	if e.Fatal {
		class := errorCodeToClass(e.Code, "")
		return []protocol.NormalizedEvent{{
			Type:      protocol.EventRunFailed,
			Timestamp: now(),
			Failure:   &protocol.FailurePayload{Class: class, Reason: redactSecrets(e.Message)},
		}}, true, parseOK
	}
	return []protocol.NormalizedEvent{{
		Type: protocol.EventWarning, Timestamp: now(),
		Warning: &protocol.WarningPayload{Code: e.Code, Message: redactSecrets(e.Message), Recoverable: true},
	}}, false, parseOK
}

// errorCodeToClass maps a Grok error code/token onto the §32 taxonomy. Unknown
// codes default to INTERNAL_ERROR (bounded retry, never infinite — rule §32).
func errorCodeToClass(codes ...string) protocol.FailureClass {
	for _, c := range codes {
		switch strings.ToLower(c) {
		case "quota", "quota_exhausted", "billing", "credit", "limit_exceeded":
			return protocol.FailureProviderQuota
		case "rate_limit", "rate-limit", "429", "too_many_requests":
			return protocol.FailureProviderRateLimit
		case "capacity", "overloaded", "service_unavailable", "busy":
			return protocol.FailureProviderCapacity
		case "auth", "unauthorized", "401", "invalid_api_key", "forbidden":
			return protocol.FailureProviderAuth
		case "model_not_available", "model_not_found", "model_deprecated":
			return protocol.FailureModelNotAvailable
		case "timeout":
			return protocol.FailureTimeout
		case "scope_violation":
			return protocol.FailureScopeViolation
		}
	}
	return protocol.FailureInternalError
}
