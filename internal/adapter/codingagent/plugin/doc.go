// Package plugin implements the native JSON-RPC 2.0 coding-agent plugin protocol
// (spec §13.2) on the client side. A [Client] spawns the plugin executable as a
// subprocess, performs the [protocol.MethodPluginHandshake] with version
// negotiation, and exposes the plugin as a [codingagent.Adapter].
//
// The plugin speaks newline-delimited JSON-RPC 2.0 over its stdin/stdout.
// Requests carry an id; the plugin streams normalized events back as
// [protocol.MethodRunEvent] notifications. The [Client] correlates requests,
// forwards events to the caller's [EventSink], and — on [Client.Cancel] — sends
// [protocol.MethodRunCancel] and terminates the plugin's whole process group
// (spec: cancellation ends the whole process group).
//
// Boundaries (AC-28, §29.2): the plugin subprocess is spawned with an
// allowlisted environment; it never receives merge credentials or the daemon
// auth token.
package plugin
