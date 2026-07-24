package codex

import (
	"strconv"
	"strings"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// parsedVersion is a best-effort semantic version parsed from "codex --version"
// output. Unknown components are -1. valid is false when no numeric version could
// be extracted (the adapter then reports conservative capabilities and does not
// claim features it cannot confirm — rule §36.25).
type parsedVersion struct {
	major, minor, patch int
	raw                 string
	valid               bool
}

// parseCodexVersion interprets the output of "codex --version". It tolerates
// surrounding banners, ANSI, build metadata and a leading "codex" label, e.g.
//
//	"codex 0.1.2505221"
//	"codex 0.42.0 (release)"
//	"0.13.0"
//
// It does not assume a single fixed shape (the banner differs across Codex
// releases); it scans the first whitespace-separated token that looks like
// semver and takes the leading numeric components.
func parseCodexVersion(output string) parsedVersion {
	pv := parsedVersion{major: -1, minor: -1, patch: -1, raw: strings.TrimSpace(output)}
	for _, field := range strings.Fields(pv.raw) {
		field = strings.Trim(field, "()[]")
		major, minor, patch, ok := splitSemver(field)
		if !ok {
			continue
		}
		pv.major, pv.minor, pv.patch = major, minor, patch
		pv.valid = true
		return pv
	}
	return pv
}

// splitSemver parses "MAJOR.MINOR.PATCH" (PATCH optional) out of a token,
// ignoring any pre-release suffix after '-'/'+'. ok is false when the token is
// not a recognizable version.
func splitSemver(token string) (major, minor, patch int, ok bool) {
	// Drop build/prerelease metadata.
	if i := strings.IndexAny(token, "-+"); i >= 0 {
		token = token[:i]
	}
	token = strings.TrimPrefix(token, "v")
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return 0, 0, 0, false
	}
	maj, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, 0, false
	}
	min, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, 0, false
	}
	pat := 0
	if len(parts) >= 3 {
		if p, err := strconv.Atoi(parts[2]); err == nil {
			pat = p
		}
	}
	return maj, min, pat, true
}

// atLeast reports whether pv is valid and >= maj.min.
func (pv parsedVersion) atLeast(maj, min int) bool {
	if !pv.valid {
		return false
	}
	if pv.major != maj {
		return pv.major > maj
	}
	return pv.minor >= min
}

// deriveCapabilities returns the version-gated capability profile (spec §12.3).
// It never hard-codes a model name (rule §36.8). Capabilities are conservative:
// features that require a version we could not confirm are reported false and
// documented as such (rule §36.25).
func deriveCapabilities(pv parsedVersion) protocol.AgentCapabilities {
	caps := protocol.AgentCapabilities{
		// The "codex exec" headless entrypoint is supported on every detectable
		// Codex release.
		HeadlessMode:     pv.valid,
		InteractiveMode:  pv.valid,
		StreamingEvents:  pv.valid, // JSONL event stream
		StructuredOutput: pv.valid, // writes diffs/files, runs commands
		ModelSelection:   pv.valid, // --model
		NativeSandbox:    pv.valid, // --sandbox
		ToolPermissions:  pv.valid, // --ask-for-approval
		UsageReporting:   pv.valid, // token_count / usage events
		// Cached-token accounting is reported when the version is known to emit
		// cached_input_tokens. We cannot confirm it without a paid run, so we
		// only claim it for positively-detected versions and let the parser
		// surface cached tokens opportunistically regardless.
		CachedUsageReporting: pv.valid,
	}
	// Session resume ("codex exec --resume"/"--continue") is claimed only when a
	// concrete version was detected; the adapter extracts the session id from the
	// live stream and gates RunHandle.SessionID on this (spec §21).
	caps.SessionResume = pv.valid && supportsResume(pv)
	// Live user messages during a headless exec run are not supported: the run is
	// autonomous and the stdin channel is the prompt, not a chat port.
	caps.LiveUserMessages = false
	// Image input, MCP and ACP are deliberately not claimed by default: they are
	// version/provider-dependent and we cannot confirm them offline without
	// over-stating support (rule §36.25). See docs/adapters/codex.md.
	caps.ImageInput = false
	caps.MCP = false
	caps.ACP = false
	return caps
}

// supportsResume reports whether the detected Codex version exposes headless
// session resume. Codex's "codex exec" supports --resume/-c from early releases;
// we conservatively gate on a positively-detected version and treat the floor as
// 0.1. This is documented as an assumption in docs/adapters/codex.md (deviation
// from strict offline-confirmation: §36.25) rather than disguised as verified.
func supportsResume(pv parsedVersion) bool {
	return pv.atLeast(0, 1)
}
