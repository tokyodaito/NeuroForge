package plugin

import (
	"context"
	"fmt"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/protocol"
)

// Adapter exposes a native JSON-RPC plugin [Client] as a
// [codingagent.Adapter] (spec §13.2). It is the "7th agent via plugin" path
// (AC-6): the plugin is discovered, handshaked and driven entirely through the
// protocol, with no core code changes.
type Adapter struct {
	client *Client
	id     string
	info   protocol.HandshakeResponse
}

// New wraps an already-dialled client. id comes from the handshake agent_id.
func New(client *Client, info protocol.HandshakeResponse) *Adapter {
	id := info.AgentID
	if id == "" {
		id = "plugin"
	}
	return &Adapter{client: client, id: id, info: info}
}

// DialAdapter is a convenience that spawns+handshakes a plugin and returns it as
// a [codingagent.Adapter]. env is the allowlisted subprocess environment.
func DialAdapter(ctx context.Context, path string, args, env []string) (codingagent.Adapter, error) {
	c, info, err := Dial(ctx, path, args, env)
	if err != nil {
		return nil, err
	}
	return New(c, info), nil
}

// ID implements codingagent.Adapter.
func (a *Adapter) ID() string { return a.id }

// Client returns the underlying RPC client (for explicit lifecycle control).
func (a *Adapter) Client() *Client { return a.client }

// Close terminates the plugin process group.
func (a *Adapter) Close() error { return a.client.Close() }

// Detect implements codingagent.Adapter.
func (a *Adapter) Detect(ctx context.Context) protocol.DetectionResult {
	var r protocol.DetectionResult
	if err := a.client.call(ctx, protocol.MethodAgentDetect, nil, &r); err != nil {
		return protocol.DetectionResult{Installed: false, Detail: err.Error()}
	}
	return r
}

// Version implements codingagent.Adapter.
func (a *Adapter) Version(ctx context.Context) protocol.VersionResult {
	return protocol.VersionResult{
		AdapterVersion:  a.info.ServerVersion,
		EngineVersion:   a.info.ServerVersion,
		ProtocolVersion: a.info.ProtocolVersion,
	}
}

// Health implements codingagent.Adapter.
func (a *Adapter) Health(ctx context.Context, account protocol.Account) protocol.HealthResult {
	var r protocol.HealthResult
	if err := a.client.call(ctx, protocol.MethodAgentHealth, account, &r); err != nil {
		return protocol.HealthResult{Status: protocol.HealthDown, Detail: err.Error()}
	}
	return r
}

// Capabilities implements codingagent.Adapter.
func (a *Adapter) Capabilities(ctx context.Context) protocol.AgentCapabilities {
	var r protocol.AgentCapabilities
	_ = a.client.call(ctx, protocol.MethodAgentCapabilities, nil, &r)
	return r
}

// ListModels implements codingagent.Adapter.
func (a *Adapter) ListModels(ctx context.Context, account protocol.Account) ([]protocol.ModelDescriptor, error) {
	var r []protocol.ModelDescriptor
	if err := a.client.call(ctx, protocol.MethodAgentModels, account, &r); err != nil {
		return nil, err
	}
	return r, nil
}

// InspectQuota implements codingagent.Adapter.
func (a *Adapter) InspectQuota(ctx context.Context, account protocol.Account) protocol.QuotaSnapshot {
	var r protocol.QuotaSnapshot
	_ = a.client.call(ctx, protocol.MethodAgentQuota, account, &r)
	return r
}

// Start implements codingagent.Adapter. It calls run.start, registers the sink
// to receive streamed event notifications, and returns the handle. Events flow
// asynchronously to sink until a terminal event arrives.
func (a *Adapter) Start(ctx context.Context, req protocol.AgentRunRequest, sink codingagent.EventSink) (protocol.RunHandle, error) {
	return a.startRun(ctx, req, sink, protocol.MethodRunStart)
}

// Resume implements codingagent.Adapter.
func (a *Adapter) Resume(ctx context.Context, req protocol.ResumeRequest, sink codingagent.EventSink) (protocol.RunHandle, error) {
	return a.startRun(ctx, protocol.AgentRunRequest{
		RunID: req.RunID, Engine: req.Engine, Model: req.Model, Account: req.Account,
		Workspace: req.Workspace, Scope: req.Scope, AllowlistEnv: req.AllowlistEnv,
		TurnLimit: req.TurnLimit, Timeout: req.Timeout, SessionID: req.SessionID,
	}, sink, protocol.MethodRunResume)
}

func (a *Adapter) startRun(ctx context.Context, req protocol.AgentRunRequest, sink codingagent.EventSink, method string) (protocol.RunHandle, error) {
	// Register the sink BEFORE sending run.start/resume so events emitted by a
	// fast plugin (between the response and registration) are not dropped. The
	// server echoes the request's RunID on events, so keying on req.RunID first
	// captures them; if the server assigns a different RunID we re-key after.
	fwd := &sinkForwarder{sink: sink}
	preKey := req.RunID
	if preKey != "" {
		a.client.registerRun(preKey, fwd)
	}

	var handle protocol.RunHandle
	if err := a.client.call(ctx, method, req, &handle); err != nil {
		if preKey != "" {
			a.client.unregisterRun(preKey)
		}
		return protocol.RunHandle{}, fmt.Errorf("plugin: %s: %w", method, err)
	}
	if handle.RunID == "" {
		handle.RunID = req.RunID
	}
	if handle.Engine == "" {
		handle.Engine = a.id
	}
	// Re-key if the server assigned its own RunID.
	if handle.RunID != "" && handle.RunID != preKey {
		a.client.registerRun(handle.RunID, fwd)
		if preKey != "" {
			a.client.unregisterRun(preKey)
		}
	}
	return handle, nil
}

// SendMessage implements codingagent.Adapter.
func (a *Adapter) SendMessage(ctx context.Context, handle protocol.RunHandle, msg protocol.AgentMessage) error {
	payload := struct {
		protocol.RunHandle
		Message protocol.AgentMessage
	}{handle, msg}
	if err := a.client.call(ctx, protocol.MethodRunMessage, payload, nil); err != nil {
		return fmt.Errorf("plugin: run.message: %w", err)
	}
	return nil
}

// Cancel implements codingagent.Adapter. It sends run.cancel; the plugin is
// expected to stop the run and emit a terminal run.cancelled event. The run
// stays registered until that terminal event is observed (so the caller
// receives run.cancelled), then the client unregisters it automatically.
func (a *Adapter) Cancel(ctx context.Context, handle protocol.RunHandle) error {
	if err := a.client.callCancel(ctx, handle.RunID); err != nil && err != errClientClosed {
		return fmt.Errorf("plugin: run.cancel: %w", err)
	}
	return nil
}

// ClassifyFailure implements codingagent.Adapter via the failure.classify RPC.
func (a *Adapter) ClassifyFailure(exitCode int, events []protocol.NormalizedEvent, stderr string) protocol.FailureClassification {
	payload := struct {
		ExitCode int                        `json:"exit_code"`
		Events   []protocol.NormalizedEvent `json:"events"`
		Stderr   string                     `json:"stderr"`
	}{exitCode, events, stderr}
	var r protocol.FailureClassification
	ctx, cancel := context.WithTimeout(context.Background(), reqTimeout)
	defer cancel()
	if err := a.client.call(ctx, protocol.MethodFailureClassify, payload, &r); err != nil {
		// Fallback to the shared classifier if the plugin can't classify.
		return codingagent.DefaultClassify(exitCode, events, stderr)
	}
	return r
}

// sinkForwarder adapts a codingagent.EventSink to the client's eventForwarder.
type sinkForwarder struct {
	sink codingagent.EventSink
}

func (f *sinkForwarder) onEvent(ctx context.Context, ev protocol.NormalizedEvent) {
	if f.sink == nil {
		return
	}
	_ = f.sink.OnEvent(ctx, ev)
}
