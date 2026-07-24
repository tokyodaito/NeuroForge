package fake

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/protocol"
)

// ServeJSONRPC runs the fake coding agent as a native JSON-RPC 2.0 plugin server
// (spec §13.2). It reads requests/notifications from stdin (one message per
// line) and writes responses/notifications to stdout; diagnostics go to stderr.
// It blocks until stdin closes or a fatal decode error occurs. scenario selects
// the run behaviour for every run.start/run.resume.
//
// This is the reference implementation of the server side of the protocol and is
// the target the plugin client (and conformance suite) exercises. It implements
// every mandatory method of §13.2 plus the run.event notification for streaming.
func ServeJSONRPC(stdin io.Reader, stdout, stderr io.Writer, scenario Scenario) error {
	srv := &rpcServer{
		in:       bufio.NewReader(stdin),
		out:      stdout,
		errw:     stderr,
		scenario: scenario,
		runs:     map[string]chan struct{}{},
	}
	return srv.serve()
}

type rpcServer struct {
	in    *bufio.Reader
	out   io.Writer
	outMu sync.Mutex
	errw  io.Writer

	scenario Scenario

	mu   sync.Mutex
	runs map[string]chan struct{} // runID -> cancel signal
}

func (s *rpcServer) serve() error {
	for {
		line, err := s.in.ReadBytes('\n')
		if len(line) > 0 {
			// Tolerate trailing whitespace; ignore blank lines.
			if hasContent(line) {
				if e := s.handleLine(line); e != nil {
					fmt.Fprintf(s.errw, "fake-jsonrpc: handle error: %v\n", e)
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func hasContent(b []byte) bool {
	for _, c := range b {
		if c != '\n' && c != '\r' && c != ' ' && c != '\t' {
			return true
		}
	}
	return false
}

func (s *rpcServer) handleLine(line []byte) error {
	var msg protocol.JSONRPCMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		// Malformed incoming message: respond with a parse error if we can't
		// even tell if it was a request.
		s.write(protocol.JSONRPCMessage{
			JSONRPC: "2.0",
			Error:   &protocol.JSONRPCError{Code: protocol.JSONRPCParseError, Message: err.Error()},
		})
		return nil
	}
	// Notifications (no id) are not expected from the client in v1.
	if msg.ID == nil {
		return nil
	}

	result, rpcErr := s.dispatch(context.Background(), msg.Method, msg.Params)
	resp := protocol.JSONRPCMessage{JSONRPC: "2.0", ID: msg.ID}
	if rpcErr != nil {
		resp.Error = rpcErr
	} else {
		resp.Result = result
	}
	s.write(resp)
	return nil
}

func (s *rpcServer) write(msg protocol.JSONRPCMessage) {
	b, err := json.Marshal(msg)
	if err != nil {
		return
	}
	b = append(b, '\n')
	s.outMu.Lock()
	defer s.outMu.Unlock()
	_, _ = s.out.Write(b)
}

func (s *rpcServer) writeNotification(method string, params any) {
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err == nil {
			raw = b
		}
	}
	s.write(protocol.JSONRPCMessage{JSONRPC: "2.0", Method: method, Params: raw})
}

// dispatch routes a request to its handler and returns the result payload or a
// JSON-RPC error.
func (s *rpcServer) dispatch(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *protocol.JSONRPCError) {
	switch method {
	case protocol.MethodPluginHandshake:
		return s.handleHandshake(params)
	case protocol.MethodAgentDetect:
		return marshalResult(protocol.DetectionResult{Installed: true, Path: "fake", Version: "1.0.0-fake", Detail: "fake coding agent"})
	case protocol.MethodAgentHealth:
		return marshalResult(protocol.HealthResult{Status: protocol.HealthOK, Detail: "fake agent healthy"})
	case protocol.MethodAgentCapabilities:
		return marshalResult((&Adapter{}).Capabilities(ctx))
	case protocol.MethodAgentModels:
		return marshalResult(defaultModels)
	case protocol.MethodAgentQuota:
		return marshalResult(protocol.QuotaSnapshot{Confidence: protocol.QuotaConfProviderReported, State: protocol.QuotaStateAvailable, Reason: "fake unlimited"})
	case protocol.MethodRunStart:
		return s.handleRun(params, false)
	case protocol.MethodRunResume:
		return s.handleRun(params, true)
	case protocol.MethodRunMessage:
		return marshalResult(map[string]any{"ok": true})
	case protocol.MethodRunCancel:
		return s.handleCancel(params)
	case protocol.MethodFailureClassify:
		return s.handleClassify(params)
	default:
		return nil, &protocol.JSONRPCError{Code: protocol.JSONRPCMethodNotFound, Message: "unknown method: " + method}
	}
}

func (s *rpcServer) handleHandshake(params json.RawMessage) (json.RawMessage, *protocol.JSONRPCError) {
	var hr protocol.HandshakeRequest
	if len(params) > 0 {
		if err := json.Unmarshal(params, &hr); err != nil {
			return nil, &protocol.JSONRPCError{Code: protocol.JSONRPCInvalidParams, Message: err.Error()}
		}
	}
	v, ok := protocol.Negotiate(
		protocol.ProtocolVersionRange{Min: hr.ProtocolMin, Max: hr.ProtocolMax},
		protocol.ForgeRange,
	)
	if !ok {
		return nil, &protocol.JSONRPCError{
			Code:    protocol.JSONRPCProtocolError,
			Message: fmt.Sprintf("protocol version negotiation failed: client=%d..%d server=%s", hr.ProtocolMin, hr.ProtocolMax, protocol.ForgeRange),
		}
	}
	resp := protocol.HandshakeResponse{
		ProtocolVersion: v,
		Server:          "fake-coding-agent",
		ServerVersion:   "1.0.0-fake",
		AgentID:         "fake",
		ProtocolMin:     protocol.ProtocolVersion,
		ProtocolMax:     protocol.ProtocolVersion,
	}
	return marshalResult(resp)
}

// runStartParams is the run.start/run.resume parameter envelope. It embeds the
// protocol request plus the fake-specific scenario (resolved from the plugin
// startup env, not the core).
type runStartParams struct {
	protocol.AgentRunRequest
}

func (s *rpcServer) handleRun(params json.RawMessage, isResume bool) (json.RawMessage, *protocol.JSONRPCError) {
	var rp runStartParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &rp); err != nil {
			return nil, &protocol.JSONRPCError{Code: protocol.JSONRPCInvalidParams, Message: err.Error()}
		}
	}
	runID := rp.RunID
	if runID == "" {
		runID = "fake-run"
	}
	engine := rp.Engine
	if engine == "" {
		engine = "fake"
	}
	sessionID := rp.SessionID
	if sessionID == "" {
		sessionID = "fake-session-1"
	}
	handle := protocol.RunHandle{RunID: runID, Engine: engine, Model: rp.Model, SessionID: sessionID}

	cancel := make(chan struct{})
	s.mu.Lock()
	s.runs[runID] = cancel
	s.mu.Unlock()

	p := runParams{
		workspace:     rp.Workspace,
		engine:        engine,
		model:         rp.Model,
		runID:         runID,
		sessionID:     sessionID,
		scenario:      s.scenario,
		startIsResume: isResume,
	}
	sc := resolveScenario(s.scenario, p)
	if isResume && len(sc.steps) > 0 && sc.steps[0].event != nil {
		sc.steps[0].event.kind = "run.resumed"
	}

	go s.runReplay(runID, cancel, sc, p)

	return marshalResult(handle)
}

// runReplay drives the scenario, streaming events as run.event notifications,
// and honours cancellation (run.cancel). It is the server-side counterpart of
// the in-process replay.
func (s *rpcServer) runReplay(runID string, cancel chan struct{}, sc script, p runParams) {
	ctx, cancelReplay := context.WithCancel(context.Background())
	defer cancelReplay()

	em := &rpcEmitter{server: s, runID: runID, engine: p.engine, model: p.model}
	done := make(chan error, 1)
	go func() {
		_, e := replay(ctx, sc, p, em)
		done <- e
	}()

	select {
	case <-done:
	case <-cancel:
		cancelReplay()
		<-done
	}

	s.mu.Lock()
	delete(s.runs, runID)
	s.mu.Unlock()
}

func (s *rpcServer) handleCancel(params json.RawMessage) (json.RawMessage, *protocol.JSONRPCError) {
	var handle protocol.RunHandle
	if len(params) > 0 {
		_ = json.Unmarshal(params, &handle)
	}
	s.mu.Lock()
	ch, ok := s.runs[handle.RunID]
	s.mu.Unlock()
	if ok {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	return marshalResult(map[string]any{"cancelled": true})
}

func (s *rpcServer) handleClassify(params json.RawMessage) (json.RawMessage, *protocol.JSONRPCError) {
	var fc struct {
		ExitCode int                        `json:"exit_code"`
		Events   []protocol.NormalizedEvent `json:"events"`
		Stderr   string                     `json:"stderr"`
	}
	if len(params) > 0 {
		_ = json.Unmarshal(params, &fc)
	}
	class := classify(fc.ExitCode, fc.Events, fc.Stderr)
	return marshalResult(class)
}

func classify(exitCode int, events []protocol.NormalizedEvent, stderr string) protocol.FailureClassification {
	return codingagent.DefaultClassify(exitCode, events, stderr)
}

// marshalResult wraps any value as a JSON-RPC result.
func marshalResult(v any) (json.RawMessage, *protocol.JSONRPCError) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, &protocol.JSONRPCError{Code: protocol.JSONRPCInternalError, Message: err.Error()}
	}
	return b, nil
}

// rpcEmitter streams events from the replay loop to the client as run.event
// notifications.
type rpcEmitter struct {
	server *rpcServer
	runID  string
	engine string
	model  string
}

func (e *rpcEmitter) emit(_ context.Context, ev protocol.NormalizedEvent) error {
	if ev.RunID == "" {
		ev.RunID = e.runID
	}
	if ev.Engine == "" {
		ev.Engine = e.engine
	}
	e.server.writeNotification(protocol.MethodRunEvent, ev)
	return nil
}

func (e *rpcEmitter) emitRaw(_ context.Context, line string) error {
	// A malformed raw line becomes a warning notification. The line is carried
	// as a valid JSON string in Raw (a json.RawMessage must itself be valid
	// JSON so the notification survives transport) and echoed in Message.
	rawStr, _ := json.Marshal(line)
	ev := protocol.NormalizedEvent{
		Type:    protocol.EventWarning,
		RunID:   e.runID,
		Engine:  e.engine,
		Warning: &protocol.WarningPayload{Code: "malformed.json", Message: "malformed agent output: " + line, Recoverable: true},
		Raw:     json.RawMessage(rawStr),
	}
	e.server.writeNotification(protocol.MethodRunEvent, ev)
	return nil
}

func (e *rpcEmitter) write(_ context.Context, rel, content string) error {
	return nil
}
