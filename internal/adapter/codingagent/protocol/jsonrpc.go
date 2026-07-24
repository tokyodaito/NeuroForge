package protocol

import "encoding/json"

// JSON-RPC 2.0 wire envelope (spec §13.2). The native plugin protocol exchanges
// these over the plugin process's stdin/stdout: requests/responses carry an id,
// notifications (events) omit it.
type JSONRPCMessage struct {
	// JSONRPC is always "2.0".
	JSONRPC string `json:"jsonrpc"`
	// ID is the correlation id for requests/responses. Nil/omitted for
	// notifications (events streamed from plugin to forge).
	ID *RequestID `json:"id,omitempty"`
	// Method names the RPC method (only on requests/notifications).
	Method string `json:"method,omitempty"`
	// Params is the method parameters (any JSON value).
	Params json.RawMessage `json:"params,omitempty"`
	// Result is the successful response payload (only on responses).
	Result json.RawMessage `json:"result,omitempty"`
	// Error carries a JSON-RPC error (only on responses).
	Error *JSONRPCError `json:"error,omitempty"`
}

// RequestID is a JSON-RPC id (number or string). Marshalled as the underlying
// type; nil means "absent" (notification).
type RequestID struct {
	num *int64
	str *string
	raw json.RawMessage
}

// NewNumberID builds a numeric request id.
func NewNumberID(n int64) RequestID { return RequestID{num: &n} }

// NewStringID builds a string request id.
func NewStringID(s string) RequestID { return RequestID{str: &s} }

// MarshalJSON implements json.Marshaler.
func (r RequestID) MarshalJSON() ([]byte, error) {
	switch {
	case r.num != nil:
		return json.Marshal(*r.num)
	case r.str != nil:
		return json.Marshal(*r.str)
	case r.raw != nil:
		return r.raw, nil
	default:
		return []byte("null"), nil
	}
}

// UnmarshalJSON implements json.Unmarshaler.
func (r *RequestID) UnmarshalJSON(b []byte) error {
	r.raw = append(json.RawMessage(nil), b...)
	return nil
}

// JSONRPCError is the standard JSON-RPC 2.0 error object.
type JSONRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Standard JSON-RPC error codes plus protocol-specific codes.
const (
	JSONRPCParseError     = -32700
	JSONRPCInvalidRequest = -32600
	JSONRPCMethodNotFound = -32601
	JSONRPCInvalidParams  = -32602
	JSONRPCInternalError  = -32603
	// JSONRPCProtocolError is a coding-agent-protocol-specific code for version
	// negotiation/handshake failures and malformed protocol messages.
	JSONRPCProtocolError = -32000
)

// RPC method names defined by spec §13.2 (mandatory for a native plugin).
const (
	MethodPluginHandshake   = "plugin.handshake"
	MethodAgentDetect       = "agent.detect"
	MethodAgentHealth       = "agent.health"
	MethodAgentCapabilities = "agent.capabilities"
	MethodAgentModels       = "agent.models"
	MethodAgentQuota        = "agent.quota"
	MethodRunStart          = "run.start"
	MethodRunResume         = "run.resume"
	MethodRunMessage        = "run.message"
	MethodRunCancel         = "run.cancel"
	MethodFailureClassify   = "failure.classify"
	// MethodRunEvent is the notification method a plugin uses to stream
	// normalized events back to forge during a run (additive to §13.2; carries a
	// [NormalizedEvent] as params).
	MethodRunEvent = "run.event"
)

// HandshakeRequest is the params of [MethodPluginHandshake]. The client (forge)
// advertises the protocol range it supports.
type HandshakeRequest struct {
	ProtocolMin   int    `json:"protocol_min"`
	ProtocolMax   int    `json:"protocol_max"`
	Client        string `json:"client"`
	ClientVersion string `json:"client_version"`
}

// HandshakeResponse is the result of [MethodPluginHandshake]. The server
// (plugin) confirms the chosen protocol version and identifies itself.
type HandshakeResponse struct {
	// ProtocolVersion is the version the plugin will speak; must fall inside the
	// client's requested range.
	ProtocolVersion int    `json:"protocol_version"`
	Server          string `json:"server"`
	ServerVersion   string `json:"server_version"`
	AgentID         string `json:"agent_id"`
	// ProtocolMin/Max describe the range the plugin can support (informational).
	ProtocolMin int `json:"protocol_min"`
	ProtocolMax int `json:"protocol_max"`
}
