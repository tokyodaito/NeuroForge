package protocol

// AgentCapabilities describes what an engine can do (spec §12.3). Every field is
// a value type so capabilities can be copied and compared freely. Unknown /
// future capability fields must be ignored by older consumers (additive change;
// no major-version bump — see [ProtocolVersion]).
type AgentCapabilities struct {
	// InteractiveMode reports support for a live interactive session.
	InteractiveMode bool
	// HeadlessMode reports support for non-interactive (headless) runs — the
	// mode the supervisor uses for autonomous execution.
	HeadlessMode bool
	// StreamingEvents reports whether the adapter emits incremental normalized
	// events (message deltas, tool progress) rather than only a final result.
	StreamingEvents bool
	// StructuredOutput reports whether the engine produces parseable structured
	// output (files, diffs, commands) as opposed to free-form prose only.
	StructuredOutput bool
	// ImageInput reports support for image attachments (spec §9.4, §16).
	ImageInput bool
	// SessionResume reports support for resuming a previous session
	// (spec §21 failover, continuation packs).
	SessionResume bool
	// LiveUserMessages reports support for sending messages to an in-flight run
	// (spec §12.2 SendMessage, §6.5 composer).
	LiveUserMessages bool
	// ModelSelection reports whether the engine accepts a model argument
	// (spec §12.1: engine != model).
	ModelSelection bool
	// UsageReporting reports whether the adapter reports token usage events
	// (spec §22, §14.4). When false, usage is UNKNOWN.
	UsageReporting bool
	// CachedUsageReporting reports whether usage events include cached-token
	// accounting (spec §22.8 prompt cache).
	CachedUsageReporting bool
	// ToolPermissions reports support for fine-grained tool permissioning.
	ToolPermissions bool
	// NativeSandbox reports whether the engine has its own execution sandbox.
	NativeSandbox bool
	// MCP reports support for the Model Context Protocol.
	MCP bool
	// ACP reports support for the Agent Client/Server Protocol.
	ACP bool
}

// Merge returns capabilities with every field set if either a or b has it set.
// Used when combining declared capabilities with probed ones.
func (a AgentCapabilities) Merge(b AgentCapabilities) AgentCapabilities {
	return AgentCapabilities{
		InteractiveMode:      a.InteractiveMode || b.InteractiveMode,
		HeadlessMode:         a.HeadlessMode || b.HeadlessMode,
		StreamingEvents:      a.StreamingEvents || b.StreamingEvents,
		StructuredOutput:     a.StructuredOutput || b.StructuredOutput,
		ImageInput:           a.ImageInput || b.ImageInput,
		SessionResume:        a.SessionResume || b.SessionResume,
		LiveUserMessages:     a.LiveUserMessages || b.LiveUserMessages,
		ModelSelection:       a.ModelSelection || b.ModelSelection,
		UsageReporting:       a.UsageReporting || b.UsageReporting,
		CachedUsageReporting: a.CachedUsageReporting || b.CachedUsageReporting,
		ToolPermissions:      a.ToolPermissions || b.ToolPermissions,
		NativeSandbox:        a.NativeSandbox || b.NativeSandbox,
		MCP:                  a.MCP || b.MCP,
		ACP:                  a.ACP || b.ACP,
	}
}
