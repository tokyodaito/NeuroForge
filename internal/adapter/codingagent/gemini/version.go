package gemini

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// semver is a parsed semantic version major.minor.patch. Pre-release/build
// metadata are captured raw but not interpreted; capability gating uses only
// the numeric core.
type semver struct {
	major, minor, patch int
	raw                 string
}

func (v semver) String() string {
	if v.raw != "" {
		return v.raw
	}
	if v.isZero() {
		return ""
	}
	return formatSemver(v.major, v.minor, v.patch)
}

func (v semver) isZero() bool { return v.major == 0 && v.minor == 0 && v.patch == 0 }

// atLeast reports whether v >= min (numeric core only).
func (v semver) atLeast(major, minor, patch int) bool {
	if v.major != major {
		return v.major > major
	}
	if v.minor != minor {
		return v.minor > minor
	}
	return v.patch >= patch
}

func formatSemver(major, minor, patch int) string {
	return strconv.Itoa(major) + "." + strconv.Itoa(minor) + "." + strconv.Itoa(patch)
}

// errNoVersion is returned by parseGeminiVersion when no semver token is found.
var errNoVersion = errors.New("no version token in gemini --version output")

// versionLineRe captures the first numeric semver-like token in a version
// string. Gemini CLI prints a bare version (e.g. "0.23.0"); some shims prefix a
// package name (e.g. "@google/gemini-cli/0.23.0"). We extract the first
// major.minor.patch anywhere in the output.
var versionLineRe = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// parseGeminiVersion extracts the semver from `gemini --version` output. It is
// tolerant of surrounding package names, whitespace, trailing newlines and
// carriage returns. Returns the zero value when no version token is found.
func parseGeminiVersion(out string) (semver, error) {
	out = strings.TrimSpace(out)
	if out == "" {
		return semver{}, errNoVersion
	}
	m := versionLineRe.FindStringSubmatch(out)
	if m == nil {
		return semver{}, errNoVersion
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	patch, _ := strconv.Atoi(m[3])
	return semver{major: major, minor: minor, patch: patch, raw: m[0]}, nil
}

// Version implements [codingagent.Adapter]. It reports this adapter's version,
// the detected Gemini CLI engine version, and the protocol version (always
// [protocol.ProtocolVersion] == 1 for a compliant adapter). The engine version
// is probed lazily and cached for the call.
func (a *Adapter) Version(ctx context.Context) protocol.VersionResult {
	res := a.Detect(ctx)
	v := protocol.VersionResult{
		AdapterVersion:  adapterVersion,
		EngineVersion:   res.Version,
		ProtocolVersion: protocol.ProtocolVersion,
	}
	if !res.Installed || res.Version == "" {
		v.Error = "engine version unavailable: " + res.Detail
	}
	return v
}

// Health implements [codingagent.Adapter]. The Gemini CLI exposes no offline,
// reliable signal for account reachability or auth mode (OAuth vs API key vs
// Vertex); determining any of those reliably would require a paid call (rule
// §36.5) or brittle config-file sniffing. Therefore:
//
//   - when the CLI is not installed → HealthDown;
//   - when installed → HealthUnknown (the engine is present but the account's
//     reachability cannot be verified without a paid call, and we never guess
//     an auth mode — spec §36.10).
func (a *Adapter) Health(ctx context.Context, _ protocol.Account) protocol.HealthResult {
	d := a.Detect(ctx)
	if !d.Installed {
		return protocol.HealthResult{Status: protocol.HealthDown, Detail: d.Detail}
	}
	return protocol.HealthResult{
		Status: protocol.HealthUnknown,
		Detail: "gemini CLI installed; account reachability requires a paid call (not probed)",
	}
}

// capabilitiesFor derives the static capability profile from a detected engine
// version (rule §36.8: no hard-coded model names; rule §36.25: unimplemented
// features explicitly false, documented in docs/adapters/gemini.md).
//
// The adapter drives the headless one-shot mode (`gemini -p … --output-format
// json`), which emits a single final JSON response rather than incremental
// deltas. Accordingly StreamingEvents is false by default; translating the
// `stream-json` output format to incremental normalized events is explicitly
// NOT implemented (§36.25).
func capabilitiesFor(v semver) protocol.AgentCapabilities {
	caps := protocol.AgentCapabilities{
		HeadlessMode:     true, // `gemini -p` non-interactive one-shot.
		ModelSelection:   true, // `-m/--model`.
		UsageReporting:   true, // usage metadata parsed from the JSON response.
		StructuredOutput: true, // JSON response carries the model text + usage.
		// Explicitly NOT implemented (§36.25); see docs/adapters/gemini.md:
		StreamingEvents:  false, // stream-json translation not wired.
		ImageInput:       false, // image attachment not wired through -p.
		SessionResume:    false, // --resume is index-based; not mapped to continuation packs.
		LiveUserMessages: false, // headless -p has no live message channel.
		ToolPermissions:  false, // --allowed-tools not wired to the permission system.
		NativeSandbox:    false, // --sandbox exists but is not invoked by this adapter.
		MCP:              false, // gemini mcp subcommand not wired.
		ACP:              false, // --experimental-acp is experimental; not wired.
		InteractiveMode:  false,
	}

	// Cached-token accounting is reported when the engine is known to emit
	// cachedContentTokenCount. The JSON --output-format has done so since the
	// CLI's earliest stable releases; we conservatively require a known version
	// before claiming it, and downgrade to false for an undetectable version.
	if !v.isZero() {
		caps.CachedUsageReporting = true
	}
	return caps
}

// Capabilities implements [codingagent.Adapter]. It probes the engine version
// once and derives the version-gated profile. The profile is conservative: only
// capabilities the adapter actually exercises are reported.
func (a *Adapter) Capabilities(ctx context.Context) protocol.AgentCapabilities {
	v, _ := a.cachedVersion(ctx)
	return capabilitiesFor(v)
}

// cachedVersion resolves the engine version for capability derivation. It uses
// Detect (which caches nothing across calls but is cheap) and parses the
// version; a probe failure yields the zero semver, which capabilitiesFor maps to
// the least-capable profile.
func (a *Adapter) cachedVersion(ctx context.Context) (semver, error) {
	d := a.Detect(ctx)
	if d.Version == "" {
		return semver{}, nil
	}
	return parseGeminiVersion(d.Version)
}
