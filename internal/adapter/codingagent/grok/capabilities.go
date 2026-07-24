package grok

import (
	"context"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// deriveCapabilities computes the version-gated capability profile (spec §12.3).
// Unknown future capability fields are ignored by definition (the struct is
// additive). Several bits are ASSUMED-pending-confirmation (rule §36.25); see
// package doc.
func (a *Adapter) deriveCapabilities() protocol.AgentCapabilities {
	v := a.versionSnapshot()

	caps := protocol.AgentCapabilities{
		// Confirmed by Grok's documented headless mode.
		HeadlessMode:     true,
		StreamingEvents:  true,
		StructuredOutput: true,
		ModelSelection:   true,
		UsageReporting:   true,
		// Coding agents and image providers are strictly separate (rule §36.9);
		// image input is not advertised for the coding surface.
		ImageInput: false,
		// No live message channel in headless -p mode (see SendMessage).
		LiveUserMessages: false,
		// Native sandbox / tool-permissioning are not assumed.
		NativeSandbox:   false,
		ToolPermissions: false,
		// MCP support is not assumed.
		MCP: false,
		// Optional ACP toggle (does not alter protocol v1).
		ACP: a.opts.EnableACP,
	}

	// Version-gated features.
	caps.SessionResume = v.known && v.atLeast(minVersionSessionResume)
	if a.opts.ResumeEnabled != nil {
		caps.SessionResume = *a.opts.ResumeEnabled
	}
	caps.CachedUsageReporting = v.known && v.atLeast(minVersionCachedUsage)

	return caps
}

// Capabilities implements codingagent.Adapter.
func (a *Adapter) Capabilities(context.Context) protocol.AgentCapabilities {
	return a.deriveCapabilities()
}

// versionSnapshot returns the cached detected version under the adapter lock. If
// Detect has not run yet, it returns the zero (unknown) versionInfo; callers
// that need a fresh probe should call Detect first.
func (a *Adapter) versionSnapshot() versionInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cachedVersion
}

// Version implements codingagent.Adapter. ProtocolVersion is always
// [protocol.ProtocolVersion] (currently 1); the adapter never changes the
// protocol to accommodate engine features (ADR-0012).
func (a *Adapter) Version(context.Context) protocol.VersionResult {
	v := a.versionSnapshot()
	engine := v.raw
	if engine == "" {
		engine = "unknown"
	}
	return protocol.VersionResult{
		AdapterVersion:  AdapterVersion,
		EngineVersion:   engine,
		ProtocolVersion: protocol.ProtocolVersion,
	}
}
