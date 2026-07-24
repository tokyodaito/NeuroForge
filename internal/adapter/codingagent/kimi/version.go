package kimi

import (
	"regexp"
	"strconv"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// parsedVersion is a semantic version extracted from `kimi --version`.
type parsedVersion struct {
	Major int
	Minor int
	Patch int
	// ok reports whether a recognizable X.Y.Z (or X.Y) was found.
	ok bool
}

// atLeast reports whether v >= target (component-wise). Unknown versions (ok ==
// false) compare as zero, i.e. less than any non-zero target.
func (v parsedVersion) atLeast(major, minor, patch int) bool {
	if v.Major != major {
		return v.Major > major
	}
	if v.Minor != minor {
		return v.Minor > minor
	}
	return v.Patch >= patch
}

// versionRegexp matches the first X.Y[.Z] found anywhere in a version string.
// It tolerates prefixes ("Kimi Code v", "kimi ") and suffixes.
var versionRegexp = regexp.MustCompile(`(\d+)\.(\d+)(?:\.(\d+))?`)

// parseVersion extracts the first semver-like tuple from s. Returns ok=false
// when no version number is present. A two-component version (X.Y) is treated
// as X.Y.0.
func parseVersion(s string) parsedVersion {
	m := versionRegexp.FindStringSubmatch(s)
	if m == nil {
		return parsedVersion{}
	}
	maj, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	pat := 0
	if m[3] != "" {
		pat, _ = strconv.Atoi(m[3])
	}
	return parsedVersion{Major: maj, Minor: min, Patch: pat, ok: true}
}

// versionProfile derives the engine's capability profile and supported flag set
// from its version. The version thresholds below encode the earliest Kimi Code
// release that shipped each capability; an unknown/older version degrades
// gracefully by reporting fewer capabilities (rule §36.25: unimplemented
// features are explicitly absent, not faked).
//
// The thresholds are conservative defaults that can be overridden per-deploy
// via [Options.Capabilities] / [Options.ForceStreaming] when the daemon has
// more accurate knowledge of the installed engine.
type versionProfile struct {
	version parsedVersion

	// capabilities derived from the version.
	caps protocol.AgentCapabilities

	// supported flags (probed or version-gated).
	flagStreamJSON bool // --output stream-json
	flagModel      bool // --model
	flagContinue   bool // --continue (session resume)
	flagMaxTurns   bool // --max-turns
}

// newVersionProfile derives a profile from a parsed version. Capabilities are
// version-gated; when no version is recognized the profile reports only the
// baseline (headless) capability so the adapter can still attempt a run.
func newVersionProfile(v parsedVersion, forceStreaming bool) versionProfile {
	p := versionProfile{version: v}

	// Baseline: every Kimi Code release supports non-interactive `-p` runs and
	// model selection.
	p.caps = protocol.AgentCapabilities{
		HeadlessMode:     true,
		ModelSelection:   true,
		StructuredOutput: true,
	}
	p.flagModel = true
	p.flagStreamJSON = v.atLeast(1, 2, 0) || forceStreaming
	p.caps.StreamingEvents = p.flagStreamJSON
	p.caps.UsageReporting = v.atLeast(1, 1, 0)
	p.flagMaxTurns = v.atLeast(1, 1, 0)
	// Session resume via --continue shipped in 1.3.0.
	p.flagContinue = v.atLeast(1, 3, 0)
	p.caps.SessionResume = p.flagContinue
	// Cached-token usage reporting shipped in 1.2.0 (alongside stream-json).
	p.caps.CachedUsageReporting = v.atLeast(1, 2, 0)
	return p
}
