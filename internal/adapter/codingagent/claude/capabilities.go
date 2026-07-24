package claude

import (
	"context"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// Capabilities implements codingagent.Adapter. The profile is version-gated by
// the detected CLI semver: capabilities that depend on flags introduced in a
// specific release (e.g. `--effort`) are only advertised when the installed CLI
// is new enough. When the version is unknown the adapter advertises the safe
// base set and leaves version-sensitive capabilities off (we never claim a
// capability we cannot honour).
func (a *Adapter) Capabilities(ctx context.Context) protocol.AgentCapabilities {
	pv, _ := a.detectedVersion(ctx)
	return capabilitiesFor(pv, a.opts.Effort != "")
}

// detectedVersion runs the version probe once and caches the result on the
// adapter so repeated Capabilities/Start calls do not re-probe.
func (a *Adapter) detectedVersion(ctx context.Context) (parsedVersion, bool) {
	a.vonce.Do(func() {
		bin, err := a.binary()
		if err != nil {
			return
		}
		out, _, exitCode, perr := a.runProbe(ctx, bin, []string{"--version"})
		if perr != nil || exitCode != 0 {
			return
		}
		if pv, ok := parseVersion(string(out)); ok {
			a.cachedVer = pv
		}
	})
	return a.cachedVer, a.cachedVer.Full != ""
}

// capabilitiesFor derives the static capability profile for a CLI version.
//
// Base set (always advertised when the CLI is detected): headless (-p),
// streaming (stream-json), structured output, model selection, usage reporting,
// cached-token usage reporting, fine-grained tool permissions, session resume,
// and MCP. Capabilities the adapter does not wire up are reported false
// (LiveUserMessages, NativeSandbox, ACP, ImageInput) so we never overstate.
//
// Version-sensitive:
//   - EffortLevels is only meaningful when the caller sets Options.Effort AND
//     the CLI is >= 2.1.x. We do not add a dedicated capability bit for it
//     (there is none in protocol.AgentCapabilities); instead the version gate
//     happens at command-build time.
func capabilitiesFor(pv parsedVersion, _ bool) protocol.AgentCapabilities {
	caps := protocol.AgentCapabilities{
		HeadlessMode:         true,
		StreamingEvents:      true,
		StructuredOutput:     true,
		ModelSelection:       true,
		UsageReporting:       true,
		CachedUsageReporting: true,
		ToolPermissions:      true,
		SessionResume:        true,
		MCP:                  true,
	}
	// ImageInput: the adapter does not surface image attachments in headless
	// mode today, so we do not advertise it (do not claim what we do not wire).
	// LiveUserMessages: -p text mode reads stdin once; no live injection.
	// NativeSandbox: Claude Code has a permission system, not an execution
	// sandbox; report false to avoid overstating isolation.
	// ACP: not supported by Claude Code.

	// Session resume via `--resume`/`--continue` has existed since early
	// releases; keep it on regardless of version. If a future floor is needed,
	// gate it here on pv.atLeast(...).
	_ = pv
	return caps
}
