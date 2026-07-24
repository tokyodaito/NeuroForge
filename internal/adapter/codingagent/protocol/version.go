package protocol

import "fmt"

// ProtocolVersion is the coding-agent protocol major version implemented by this
// package (spec §13.2 plugin.handshake, ADR-0005). It is the stability boundary:
// adapters negotiate against this constant.
//
// Versioning rules:
//
//   - A bump of the major version is a breaking change that requires a new
//     adapter implementation (or a compatibility shim).
//   - Additive, backwards-compatible changes (new optional fields, new event
//     types) do NOT bump the major version; consumers must ignore unknown event
//     types rather than fail (see "unknown events do not break a run").
//
// Current: 1 (stabilised in milestone M2).
const ProtocolVersion = 1

// ProtocolVersionRange describes the protocol versions a party can speak.
// [Min, Max] is inclusive.
type ProtocolVersionRange struct {
	Min int
	Max int
}

// Supports reports whether version v falls inside the range (inclusive).
func (r ProtocolVersionRange) Supports(v int) bool { return v >= r.Min && v <= r.Max }

// String renders the range for diagnostics/handshake logs.
func (r ProtocolVersionRange) String() string {
	if r.Min == r.Max {
		return fmt.Sprintf("v%d", r.Min)
	}
	return fmt.Sprintf("v%d..v%d", r.Min, r.Max)
}

// ForgeRange is the protocol-version range the daemon (forge) supports. It
// always includes [ProtocolVersion].
var ForgeRange = ProtocolVersionRange{Min: ProtocolVersion, Max: ProtocolVersion}

// Negotiate picks the highest mutually-supported protocol version, or returns
// ok=false when there is no overlap. When ok, the chosen version is guaranteed
// to be inside both ranges.
func Negotiate(client, server ProtocolVersionRange) (version int, ok bool) {
	v := min(client.Max, server.Max)
	if v < max(client.Min, server.Min) {
		return 0, false
	}
	return v, true
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
