// Package transport implements the local loopback API between TUI/CLI and the
// daemon (spec §11.3, ADR-0004).
//
// STATUS: foundation implemented for milestone M0.
//
// Implemented in M0:
//   - An in-memory broadcast event Bus (the internal event bus) that publishes
//     live normalized events to subscribers.
//   - A loopback-only HTTP server exposing a JSON command API (/healthz,
//     /status, /shutdown) and a Server-Sent Events stream (/events).
//   - A random bearer token that gates every endpoint; the token is never
//     logged and never exposed to agent processes (spec §29.2).
//   - A Client used by the CLI/TUI to talk to the daemon over the same API.
//
// Boundaries: the server MUST refuse any non-loopback bind and MUST reject any
// request without the correct token.
package transport
