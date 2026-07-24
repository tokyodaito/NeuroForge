package opencode

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// Version implements codingagent.Adapter. It reports the adapter version, the
// detected engine version (from `opencode --version`) and the Protocol-v1
// version this adapter speaks (spec §12.2). A failed detection surfaces as a
// non-fatal Error rather than an incorrect EngineVersion.
func (a *Adapter) Version(ctx context.Context) protocol.VersionResult {
	d := a.rememberDetect(ctx)
	vr := protocol.VersionResult{
		AdapterVersion:  adapterVersion,
		EngineVersion:   d.version,
		ProtocolVersion: protocol.ProtocolVersion,
	}
	if !d.installed {
		vr.Error = "opencode not detected: " + d.detail
	}
	return vr
}

// version-gating thresholds (conservative floors derived from documented CLI
// surface). Resume via --continue/--session and cached-token usage reporting are
// gated on these; older engines report the lower-fidelity capability instead.
const (
	// minResumeVersion is the minimum engine version that supports resuming a
	// session via --session/--continue. Below this, SessionResume is false.
	minResumeVersion = "0.1.0"
	// minCachedUsageVersion gates CachedUsageReporting (cached-token accounting).
	minCachedUsageVersion = "0.1.0"
)

// versionRe captures a major.minor.patch triple, optionally preceded by "v".
// It tolerates prefixes like "opencode" and suffixes like "-dev", "+1".
var versionRe = regexp.MustCompile(`v?(\d+)\.(\d+)\.(\d+)`)

// parseVersionString extracts the first semver triple from a `--version` string.
// Returns "" when no version is found. Examples accepted:
//
//	"0.1.48"            -> "0.1.48"
//	"v0.1.48"           -> "0.1.48"
//	"opencode 0.1.48"   -> "0.1.48"
//	"version 1.2.3-dev" -> "1.2.3"
func parseVersionString(s string) string {
	m := versionRe.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return m[1] + "." + m[2] + "." + m[3]
}

// semver is a parsed major.minor.patch.
type semver struct {
	major, minor, patch int
	ok                  bool
}

func parseSemver(s string) semver {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "v") {
		s = s[1:]
	}
	parts := strings.SplitN(s, ".", 3)
	if len(parts) != 3 {
		return semver{}
	}
	// Strip any pre-release suffix on patch (e.g. "3-rc1").
	parts[2] = strings.SplitN(parts[2], "-", 2)[0]
	parts[2] = strings.SplitN(parts[2], "+", 2)[0]
	v := semver{ok: true}
	var err error
	if v.major, err = strconv.Atoi(parts[0]); err != nil {
		return semver{}
	}
	if v.minor, err = strconv.Atoi(parts[1]); err != nil {
		return semver{}
	}
	if v.patch, err = strconv.Atoi(parts[2]); err != nil {
		return semver{}
	}
	return v
}

func (s semver) cmp(o semver) int {
	if !s.ok && !o.ok {
		return 0
	}
	if !s.ok {
		return -1
	}
	if !o.ok {
		return 1
	}
	if s.major != o.major {
		return cmpInt(s.major, o.major)
	}
	if s.minor != o.minor {
		return cmpInt(s.minor, o.minor)
	}
	return cmpInt(s.patch, o.patch)
}

// atLeast reports whether s >= floor (an unparseable floor is treated as "always
// satisfied" so a version-gated feature degrades gracefully on unknown versions).
func (s semver) atLeast(floor string) bool {
	f := parseSemver(floor)
	if !f.ok {
		return true
	}
	return s.cmp(f) >= 0
}

func cmpInt(a, b int) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

// Capabilities implements codingagent.Adapter. It derives the capability
// profile from the detected engine version so version-gated features degrade on
// older engines rather than being claimed unconditionally (spec §12.3, rule
// §36.25). Unknown/future capability signals are ignored.
func (a *Adapter) Capabilities(ctx context.Context) protocol.AgentCapabilities {
	d := a.rememberDetect(ctx)
	return capabilitiesForVersion(d.version)
}

// capabilitiesForVersion derives the Protocol-v1 capability profile from an
// engine version string. An empty/unparseable version yields a conservative
// baseline (headless + streaming + model selection only) so detection never
// overstates support (rule §36.25).
func capabilitiesForVersion(version string) protocol.AgentCapabilities {
	v := parseSemver(version)
	resume := v.atLeast(minResumeVersion)
	cachedUsage := v.atLeast(minCachedUsageVersion)
	return protocol.AgentCapabilities{
		HeadlessMode:         true, // `opencode run` non-interactive mode
		StreamingEvents:      true, // `--format json` line-delimited events
		StructuredOutput:     true, // emits structured tool/file/command events
		ModelSelection:       true, // `--model provider/model`
		ImageInput:           true, // `--file` may attach images
		UsageReporting:       true, // usage.updated events when reported
		CachedUsageReporting: cachedUsage && v.ok,
		SessionResume:        resume && v.ok,
		MCP:                  true, // OpenCode supports MCP servers
		ACP:                  true, // `opencode acp` server
		ToolPermissions:      true, // OpenCode permission system
		// LiveUserMessages: headless one-shot runs cannot receive mid-run
		// messages (no live session channel); remains false (§6.5).
		LiveUserMessages: false,
		// NativeSandbox: OpenCode has no execution sandbox of its own; NeuroForge
		// supplies isolation via the worktree (§17). Remains false.
		NativeSandbox:   false,
		InteractiveMode: false, // this adapter drives headless runs only
	}
}
