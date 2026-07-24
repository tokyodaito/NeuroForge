package claude

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// transCtx is the per-run translation context shared across lines: it carries
// the run identity, the timestamp source and the session id discovered from the
// stream (set by the system/init or result line).
type transCtx struct {
	runID   string
	engine  string
	model   string
	now     func() time.Time
	session string
}

// ---- line reader (no 64KiB cap, CRLF-tolerant) ----

// lineReader reads newline-delimited bytes without bufio.Scanner's default 64KiB
// token limit (Claude assistant/result lines can be large). Next returns the
// next line without the trailing newline, plus hasMore. A nil line with
// hasMore=false means EOF. CR (from CRLF) is stripped from the end of each
// returned line.
type lineReader struct {
	r     *bufio.Reader
	out   []byte
	atEOF bool
}

func newLineReader(r io.Reader) *lineReader { return &lineReader{r: bufio.NewReader(r)} }

// Next reads the next line. Returns (line, hasMore). A nil line with hasMore
// false means EOF.
func (s *lineReader) Next() (line []byte, hasMore bool) {
	if s.atEOF {
		return nil, false
	}
	frag, isPrefix, err := s.r.ReadLine()
	if err != nil {
		s.atEOF = true
		return trimCR(frag), false // frag may be non-nil for a final line without newline
	}
	if !isPrefix {
		return trimCR(frag), true
	}
	// Long line: accumulate until the final fragment.
	acc := append([]byte(nil), frag...)
	for isPrefix {
		frag, isPrefix, err = s.r.ReadLine()
		if err != nil {
			break
		}
		acc = append(acc, frag...)
	}
	return trimCR(acc), true
}

// trimCR removes a single trailing carriage return (defensive against any CRLF
// not already consumed by bufio). bufio.ReadLine already drops \n and the \r
// preceding \n, but this keeps partial/edge cases clean.
func trimCR(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\r' || b[len(b)-1] == '\n') {
		b = b[:len(b)-1]
	}
	if len(b) == 0 {
		return nil
	}
	return append([]byte(nil), b...)
}

// stripBOM returns a reader that skips a single leading UTF-8 BOM (EF BB BF)
// if present. Claude Code does not emit a BOM, but Windows shells and some
// proxies do; tolerating it keeps the run from mis-parsing the first line.
func stripBOM(r io.Reader) io.Reader {
	br := bufio.NewReader(r)
	b, err := br.Peek(3)
	if err == nil && len(b) == 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		_, _ = br.Discard(3)
	}
	return br
}

// ---- Claude SDK message → protocol.NormalizedEvent translation ----

// envelope is the common discriminator of every Claude SDK stream message.
type envelope struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
}

// claudeResult is the shape of the terminal `result` message.
type claudeResult struct {
	Subtype        string      `json:"subtype"`
	SessionID      string      `json:"session_id"`
	IsError        bool        `json:"is_error"`
	NumTurns       int         `json:"num_turns"`
	Result         string      `json:"result"`
	TotalCostUSD   float64     `json:"total_cost_usd"`
	APIErrorStatus *int        `json:"api_error_status"`
	Usage          claudeUsage `json:"usage"`
	Errors         []string    `json:"errors"`
}

// claudeUsage maps the result.usage object (all token fields present on the
// result; cache fields may be absent on assistant messages, which we ignore).
type claudeUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

// claudeSystem is the shape of the `system` (init) message we read.
type claudeSystem struct {
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	Model     string `json:"model"`
}

// claudeAssistant is the shape of an `assistant` message: message.content is a
// list of blocks (text / tool_use). We extract text and tool_use signals.
type claudeAssistant struct {
	SessionID string             `json:"session_id"`
	Message   claudeAnthropicMsg `json:"message"`
}

type claudeAnthropicMsg struct {
	Content []claudeContentBlock `json:"content"`
	Usage   *claudeUsage         `json:"usage"`
}

type claudeContentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Name  string          `json:"name"`  // tool_use
	ID    string          `json:"id"`    // tool_use
	Input json.RawMessage `json:"input"` // tool_use
}

// claudeUser carries tool_result blocks back from tool execution.
type claudeUser struct {
	Message claudeAnthropicMsg `json:"message"`
}

// translate converts one raw Claude stream line into zero or more normalized
// events. Malformed JSON is delegated to protocol.ParseEventLine so the
// canonical malformed-warning shape (and persistence contract) is reused
// exactly; unknown future Claude types become recoverable warnings. Neither
// case is fatal (spec: malformed/unknown events never break a run).
func (a *Adapter) translate(line []byte, tc *transCtx) []protocol.NormalizedEvent {
	if len(line) == 0 {
		return nil
	}
	var head envelope
	if err := json.Unmarshal(line, &head); err != nil {
		// Malformed JSON: reuse protocol.ParseEventLine for the canonical
		// recoverable warning + Raw payload. Never fatal.
		ev, _ := protocol.ParseEventLine(line)
		if ev.Type == "" {
			ev = warningEvent("malformed.json", "failed to decode claude line", line, tc)
		}
		fixup(&ev, tc)
		return []protocol.NormalizedEvent{ev}
	}

	switch head.Type {
	case "system":
		return a.translateSystem(line, tc)
	case "assistant":
		return a.translateAssistant(line, tc)
	case "user":
		return a.translateUser(line, tc)
	case "result":
		return a.translateResult(line, tc)
	case "stream_event":
		// Incremental deltas (only with --include-partial-messages, which we do
		// not enable by default). Map text deltas to message.delta when present;
		// otherwise ignore silently (non-fatal, non-warning: these are expected).
		return a.translateStreamEvent(line, tc)
	default:
		// Unknown future Claude type → recoverable warning, persisted as an
		// artifact via Raw. Never fatal.
		ev := warningEvent("unknown-claude-event", "unknown claude stream type "+head.Type+"; ignored", line, tc)
		return []protocol.NormalizedEvent{ev}
	}
}

func (a *Adapter) translateSystem(line []byte, tc *transCtx) []protocol.NormalizedEvent {
	var s claudeSystem
	_ = json.Unmarshal(line, &s)
	if s.SessionID != "" {
		tc.session = s.SessionID
	}
	// The system/init line carries no user-facing content; run.started is
	// emitted by supervise. Capture model if the request did not pin one.
	if tc.model == "" && s.Model != "" {
		tc.model = s.Model
	}
	return nil
}

func (a *Adapter) translateAssistant(line []byte, tc *transCtx) []protocol.NormalizedEvent {
	var as claudeAssistant
	if err := json.Unmarshal(line, &as); err != nil {
		ev := warningEvent("malformed.payload", "failed to decode assistant message: "+err.Error(), line, tc)
		return []protocol.NormalizedEvent{ev}
	}
	if as.SessionID != "" {
		tc.session = as.SessionID
	}
	var out []protocol.NormalizedEvent
	for _, blk := range as.Message.Content {
		switch blk.Type {
		case "text":
			if strings.TrimSpace(blk.Text) != "" {
				out = append(out, protocol.NormalizedEvent{
					Type:    protocol.EventMessageCompleted,
					Message: &protocol.MessagePayload{Text: blk.Text, Role: "assistant"},
				})
			}
		case "tool_use":
			out = append(out, protocol.NormalizedEvent{
				Type: protocol.EventToolStarted,
				Tool: &protocol.ToolPayload{ToolID: blk.ID, Name: blk.Name, Status: "started"},
			})
		}
	}
	for i := range out {
		fixup(&out[i], tc)
	}
	return out
}

func (a *Adapter) translateUser(line []byte, tc *transCtx) []protocol.NormalizedEvent {
	var u claudeUser
	if err := json.Unmarshal(line, &u); err != nil {
		ev := warningEvent("malformed.payload", "failed to decode user message: "+err.Error(), line, tc)
		return []protocol.NormalizedEvent{ev}
	}
	var out []protocol.NormalizedEvent
	for _, blk := range u.Message.Content {
		if blk.Type == "tool_result" {
			// tool_result blocks carry an id matching the tool_use and an
			// optional is_error flag (not modelled here in detail).
			out = append(out, protocol.NormalizedEvent{
				Type: protocol.EventToolCompleted,
				Tool: &protocol.ToolPayload{ToolID: blk.ID, Status: "completed"},
			})
		}
	}
	for i := range out {
		fixup(&out[i], tc)
	}
	return out
}

func (a *Adapter) translateResult(line []byte, tc *transCtx) []protocol.NormalizedEvent {
	var r claudeResult
	if err := json.Unmarshal(line, &r); err != nil {
		ev := warningEvent("malformed.payload", "failed to decode result message: "+err.Error(), line, tc)
		return []protocol.NormalizedEvent{ev}
	}
	if r.SessionID != "" {
		tc.session = r.SessionID
	}
	var out []protocol.NormalizedEvent

	// Map authoritative usage from the result (spec §22, §14.4). Cache tokens
	// are reported by Claude → CachedUsageReporting is honoured. Confidence is
	// PROVIDER_REPORTED: the engine computes and reports it authoritatively,
	// but we do not overstate to EXACT (rule §36.10).
	out = append(out, protocol.NormalizedEvent{
		Type: protocol.EventUsageUpdated,
		Usage: &protocol.UsagePayload{
			InputTokens:      r.Usage.InputTokens,
			OutputTokens:     r.Usage.OutputTokens,
			CacheReadTokens:  r.Usage.CacheReadInputTokens,
			CacheWriteTokens: r.Usage.CacheCreationInputTokens,
			Cost:             r.TotalCostUSD,
			Currency:         "USD",
			Confidence:       protocol.QuotaConfProviderReported,
		},
	})

	// Terminal event derived from subtype. error_max_turns is an agentic-turn
	// limit reached (a policy/terminal outcome), not a provider failure.
	switch r.Subtype {
	case "success":
		out = append(out, protocol.NormalizedEvent{Type: protocol.EventRunCompleted})
	case "error_max_turns":
		out = append(out, terminalFailure(protocol.FailureInternalError, "claude: max agentic turns reached", 0))
	case "error_max_budget_usd":
		out = append(out, terminalFailure(protocol.FailureBudgetExceeded, "claude: run budget cap reached", 0))
	case "error_during_execution", "error_max_structured_output_retries", "":
		cls, reason := classifyResultError(r, "")
		out = append(out, terminalFailure(cls, reason, 0))
	default:
		cls, reason := classifyResultError(r, "")
		out = append(out, terminalFailure(cls, reason, 0))
	}
	for i := range out {
		fixup(&out[i], tc)
	}
	return out
}

func (a *Adapter) translateStreamEvent(line []byte, tc *transCtx) []protocol.NormalizedEvent {
	// Best-effort extraction of text deltas for streaming UX. Shape (from the
	// SDK): {"type":"stream_event","event":{"type":"content_block_delta",
	// "delta":{"type":"text_delta","text":"..."}}}. Malformed → ignore.
	var se struct {
		Event struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		} `json:"event"`
	}
	if err := json.Unmarshal(line, &se); err != nil {
		return nil
	}
	if se.Event.Type == "content_block_delta" && se.Event.Delta.Type == "text_delta" && se.Event.Delta.Text != "" {
		ev := protocol.NormalizedEvent{
			Type:    protocol.EventMessageDelta,
			Message: &protocol.MessagePayload{Delta: se.Event.Delta.Text, Role: "assistant"},
		}
		fixup(&ev, tc)
		return []protocol.NormalizedEvent{ev}
	}
	return nil
}

// ---- translation helpers ----

func warningEvent(code, msg string, raw []byte, tc *transCtx) protocol.NormalizedEvent {
	ev := protocol.NormalizedEvent{
		Type:      protocol.EventWarning,
		Timestamp: tc.now(),
		RunID:     tc.runID,
		Engine:    tc.engine,
		Warning: &protocol.WarningPayload{
			Code: code, Message: msg, Recoverable: true,
		},
		Raw: append([]byte(nil), raw...),
	}
	return ev
}

func terminalFailure(cls protocol.FailureClass, reason string, exitCode int) protocol.NormalizedEvent {
	return protocol.NormalizedEvent{
		Type:    protocol.EventRunFailed,
		Failure: &protocol.FailurePayload{Class: cls, Reason: reason, ExitCode: exitCode},
	}
}

func fixup(ev *protocol.NormalizedEvent, tc *transCtx) {
	if ev.Timestamp.IsZero() {
		ev.Timestamp = tc.now()
	}
	if ev.RunID == "" {
		ev.RunID = tc.runID
	}
	if ev.Engine == "" {
		ev.Engine = tc.engine
	}
	if ev.Model == "" {
		ev.Model = tc.model
	}
}

// classifyResultError maps a non-success result subtype + errors[] onto a §32
// class. See classify.go for the full classifier (this is the result-specific
// fast path used during translation).
func classifyResultError(r claudeResult, stderr string) (protocol.FailureClass, string) {
	joined := strings.ToLower(strings.Join(r.Errors, " ") + " " + stderr)
	switch {
	case strings.Contains(joined, "rate_limit") || strings.Contains(joined, "429") || strings.Contains(joined, "too many requests"):
		return protocol.FailureProviderRateLimit, firstLine("provider rate limited")
	case strings.Contains(joined, "billing") || strings.Contains(joined, "quota") || strings.Contains(joined, "exhausted") || strings.Contains(joined, "limit exceeded"):
		return protocol.FailureProviderQuota, firstLine("provider subscription quota exhausted")
	case strings.Contains(joined, "authentication_failed") || strings.Contains(joined, "auth") || strings.Contains(joined, "401") || strings.Contains(joined, "unauthorized"):
		return protocol.FailureProviderAuth, firstLine("authentication failed")
	case strings.Contains(joined, "model_not_found") || strings.Contains(joined, "model not found"):
		return protocol.FailureModelNotAvailable, firstLine("model not available")
	case strings.Contains(joined, "overloaded") || strings.Contains(joined, "capacity") || strings.Contains(joined, "529"):
		return protocol.FailureProviderCapacity, firstLine("provider capacity/overloaded")
	case strings.Contains(joined, "invalid_request"):
		return protocol.FailureMalformedOutput, firstLine("invalid request / malformed")
	case strings.Contains(joined, "server_error") || strings.Contains(joined, "500"):
		return protocol.FailureProviderCapacity, firstLine("provider server error")
	}
	return protocol.FailureInternalError, firstLine("claude run ended with error subtype " + r.Subtype)
}
