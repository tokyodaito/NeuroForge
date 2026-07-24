// Package transport implements the local loopback API between TUI and daemon.
//
// STATUS: scaffold — not implemented (planned for milestone M0).
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §11.3): commands over HTTP+JSON, live
// events over Server-Sent Events, a random local auth token, bind exclusively to
// the loopback interface. See ADR-0004.
//
// Boundaries: must never bind to a non-loopback address and must never transmit
// the auth token over an unencrypted channel.
package transport
