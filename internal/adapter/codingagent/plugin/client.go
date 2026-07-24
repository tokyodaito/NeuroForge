package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"neuroforge/internal/adapter/codingagent/proctree"
	"neuroforge/internal/adapter/codingagent/protocol"
)

// Client is the JSON-RPC 2.0 client side of the native plugin protocol (spec
// §13.2). It owns one plugin subprocess, multiplexes requests over its
// stdin/stdout, and routes streamed event notifications to per-run sinks.
//
// A Client is safe for concurrent use. Use [Dial] to spawn + handshake a plugin
// and [Client.Close] to terminate its process group.
type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *atomicWriter // buffered stderr for diagnostics

	writeMu sync.Mutex
	encMu   sync.Mutex

	id      atomic.Int64
	pending sync.Map // int64 -> chan rpcResponse

	runMu sync.RWMutex
	runs  map[string]runSink // runID -> sink + done

	closed atomic.Bool
	done   chan struct{}
}

type runSink struct {
	sink eventForwarder
}

// eventForwarder is the minimal sink surface the Client needs (avoids a hard
// dependency cycle with the parent package's EventSink type at the transport
// level; [Adapter] adapts a codingagent.EventSink to it).
type eventForwarder interface {
	onEvent(ctx context.Context, ev protocol.NormalizedEvent)
}

type rpcResponse struct {
	result json.RawMessage
	err    *protocol.JSONRPCError
}

// Dial spawns the plugin executable, starts the response reader, and performs
// the [protocol.MethodPluginHandshake] with version negotiation. env is the
// allowlisted environment for the subprocess (AC-28: never merge credentials or
// the daemon token). Returns the negotiated handshake response and the client.
func Dial(ctx context.Context, path string, args, env []string) (*Client, protocol.HandshakeResponse, error) {
	cmd := proctree.NewGroupCommand(path, args...)
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, protocol.HandshakeResponse{}, fmt.Errorf("plugin: stdin pipe: %w", err)
	}
	stderr := &atomicWriter{}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, protocol.HandshakeResponse{}, fmt.Errorf("plugin: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, protocol.HandshakeResponse{}, fmt.Errorf("plugin: start %q: %w", path, err)
	}

	c := &Client{
		cmd:    cmd,
		stdin:  stdin,
		stderr: stderr,
		runs:   map[string]runSink{},
		done:   make(chan struct{}),
	}
	go c.readLoop(stdout)

	// Handshake with version negotiation.
	var resp protocol.HandshakeResponse
	req := protocol.HandshakeRequest{
		ProtocolMin: protocol.ProtocolVersion, ProtocolMax: protocol.ProtocolVersion,
		Client: "forge", ClientVersion: "1",
	}
	if err := c.call(ctx, protocol.MethodPluginHandshake, req, &resp); err != nil {
		c.hardClose()
		return nil, protocol.HandshakeResponse{}, fmt.Errorf("plugin: handshake: %w", err)
	}
	if resp.ProtocolVersion != protocol.ProtocolVersion {
		c.hardClose()
		return nil, protocol.HandshakeResponse{}, fmt.Errorf("plugin: negotiated protocol v%d but daemon speaks v%d", resp.ProtocolVersion, protocol.ProtocolVersion)
	}
	return c, resp, nil
}

// Stderr returns any captured plugin stderr (best-effort diagnostics).
func (c *Client) Stderr() string { return c.stderr.String() }

// call sends a request and unmarshals the result into out (which may be nil).
func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	if c.closed.Load() {
		return errClientClosed
	}
	id := c.id.Add(1)
	var rawParams json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		rawParams = b
	}
	rid := protocol.NewNumberID(id)
	msg := protocol.JSONRPCMessage{JSONRPC: "2.0", ID: &rid, Method: method, Params: rawParams}

	key := strconv.FormatInt(id, 10)
	ch := make(chan rpcResponse, 1)
	c.pending.Store(key, ch)
	defer c.pending.Delete(key)

	if err := c.send(msg); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return errClientClosed
	case r := <-ch:
		if r.err != nil {
			return rpcError{code: r.err.Code, message: r.err.Message}
		}
		if out != nil && len(r.result) > 0 {
			if err := json.Unmarshal(r.result, out); err != nil {
				return fmt.Errorf("plugin: decode %s result: %w", method, err)
			}
		}
		return nil
	}
}

func (c *Client) send(msg protocol.JSONRPCMessage) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed.Load() {
		return errClientClosed
	}
	_, err = c.stdin.Write(b)
	return err
}

// readLoop reads newline-delimited JSON-RPC messages until the pipe closes,
// dispatching responses to pending callers and event notifications to run sinks.
func (c *Client) readLoop(r io.Reader) {
	defer close(c.done)
	sc := bufio.NewReader(r)
	for {
		line, err := sc.ReadBytes('\n')
		if len(line) > 0 {
			c.handleLine(line)
		}
		if err != nil {
			return
		}
	}
}

func (c *Client) handleLine(line []byte) {
	var msg protocol.JSONRPCMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		return
	}
	if msg.ID != nil {
		// Response to a request: match by the JSON id representation.
		key := string(mustJSON(msg.ID))
		if ch, ok := c.pending.LoadAndDelete(key); ok {
			ch.(chan rpcResponse) <- rpcResponse{result: msg.Result, err: msg.Error}
		}
		return
	}
	// Notification: route event to the run sink.
	if msg.Method == protocol.MethodRunEvent {
		var ev protocol.NormalizedEvent
		if err := json.Unmarshal(msg.Params, &ev); err == nil {
			c.routeEvent(ev)
		}
	}
}

// mustJSON marshals v, returning "null" on error (used for id correlation).
func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("null")
	}
	return b
}

func (c *Client) routeEvent(ev protocol.NormalizedEvent) {
	c.runMu.RLock()
	rs, ok := c.runs[ev.RunID]
	c.runMu.RUnlock()
	if !ok {
		// No registered sink (e.g. late event after close); drop it.
		return
	}
	rs.sink.onEvent(context.Background(), ev)
	// Terminal events end the run: unregister so subsequent late events are
	// dropped and the sink can be released. This MUST happen after delivery so
	// the caller observes run.completed/failed/cancelled.
	if ev.Type.IsTerminal() {
		c.unregisterRun(ev.RunID)
	}
}

func (c *Client) registerRun(runID string, sink eventForwarder) {
	c.runMu.Lock()
	c.runs[runID] = runSink{sink: sink}
	c.runMu.Unlock()
}

func (c *Client) unregisterRun(runID string) {
	c.runMu.Lock()
	delete(c.runs, runID)
	c.runMu.Unlock()
}

// hardClose kills the plugin process group (spec: cancellation ends the whole
// process group) and closes stdin. The killed process is reaped (cmd.Wait) so it
// does not linger as a zombie. Safe to call multiple times.
func (c *Client) hardClose() {
	if !c.closed.CompareAndSwap(false, true) {
		return
	}
	_ = proctree.KillGroup(c.cmd, proctree.SigKill)
	_ = c.stdin.Close()
	// Reap the process in a goroutine: Wait blocks until the now-killed process
	// and its stdout pipe are torn down, releasing the zombie.
	go func() {
		if c.cmd != nil && c.cmd.Process != nil {
			_ = c.cmd.Wait()
		}
	}()
}

// Close terminates the plugin: it cancels any in-flight run, then kills the
// process group. It is the normal shutdown path.
func (c *Client) Close() error {
	c.hardClose()
	return nil
}

// callCancel sends run.cancel for a run (best-effort); a hard KillGroup follows
// via Close when the client is discarded.
func (c *Client) callCancel(ctx context.Context, runID string) error {
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return c.call(callCtx, protocol.MethodRunCancel, protocol.RunHandle{RunID: runID}, nil)
}

// cmdExited reports whether the plugin process has exited.
func (c *Client) cmdExited() bool {
	if c.cmd == nil || c.cmd.Process == nil {
		return true
	}
	return c.closed.Load()
}

// ---- helpers ----

// rpcError wraps a JSON-RPC error with the standard error interface.
type rpcError struct {
	code    int
	message string
}

func (e rpcError) Error() string { return fmt.Sprintf("plugin rpc error %d: %s", e.code, e.message) }

// Code returns the JSON-RPC error code.
func (e rpcError) Code() int { return e.code }

var errClientClosed = errors.New("plugin: client closed")

// reqTimeout is the default per-RPC timeout for non-streaming methods.
const reqTimeout = 5 * time.Second

// atomicWriter is a concurrency-safe bytes.Buffer for capturing plugin stderr.
type atomicWriter struct {
	mu  sync.Mutex
	buf []byte
}

func (w *atomicWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	return len(p), nil
}

func (w *atomicWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.buf)
}
