package grok

import (
	"strconv"
	"strings"
)

// versionInfo is a parsed SemVer-ish version. `known` is false when no numeric
// version could be extracted.
type versionInfo struct {
	major, minor, patch int
	raw                 string
	known               bool
}

// parseVersion extracts the first "X.Y.Z" numeric version from s, tolerating
// prefixes like "grok version 1.2.3" or "v1.2.3-beta". It never returns an
// error: an unparseable string yields versionInfo{known: false}.
func parseVersion(s string) versionInfo {
	v := versionInfo{raw: strings.TrimSpace(s)}
	nums, ok := firstVersionRun(s)
	if !ok || len(nums) == 0 {
		return v
	}
	v.known = true
	v.major = nums[0]
	if len(nums) > 1 {
		v.minor = nums[1]
	}
	if len(nums) > 2 {
		v.patch = nums[2]
	}
	return v
}

// firstVersionRun scans s for the first run of digits, then continues while it
// sees groups of "." or "-" separated digits. Returns the numeric components.
func firstVersionRun(s string) ([]int, bool) {
	i := 0
	for i < len(s) && !isDigit(s[i]) {
		i++
	}
	if i >= len(s) {
		return nil, false
	}
	var nums []int
	for i < len(s) {
		if !isDigit(s[i]) {
			break
		}
		j := i
		for j < len(s) && isDigit(s[j]) {
			j++
		}
		n, err := strconv.Atoi(s[i:j])
		if err != nil {
			return nums, len(nums) > 0
		}
		nums = append(nums, n)
		i = j
		// Accept a single separator before the next numeric group.
		if i < len(s) && (s[i] == '.' || s[i] == '-') {
			i++
			// Stop if the run ends here (e.g. trailing "." or pre-release tag).
			if i >= len(s) || !isDigit(s[i]) {
				break
			}
			continue
		}
		break
	}
	return nums, len(nums) > 0
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// atLeast reports whether v >= o, comparing major, then minor, then patch.
func (v versionInfo) atLeast(o versionInfo) bool {
	switch {
	case v.major != o.major:
		return v.major >= o.major
	case v.minor != o.minor:
		return v.minor >= o.minor
	default:
		return v.patch >= o.patch
	}
}

// String renders the canonical "M.m.p" form (or the raw text when unknown).
func (v versionInfo) String() string {
	if !v.known {
		return v.raw
	}
	return strconv.Itoa(v.major) + "." + strconv.Itoa(v.minor) + "." + strconv.Itoa(v.patch)
}

// Version-gate thresholds. These are ASSUMED (rule §36.25): Grok's feature
// history is not fully documented upstream. The values are conservative (any
// parseable version qualifies) and may be tightened once the installed CLI's
// feature flags are confirmed. Override per-adapter via [Options].
var (
	minVersionSessionResume = versionInfo{known: true, major: 0, minor: 1, patch: 0}
	minVersionCachedUsage   = versionInfo{known: true, major: 0, minor: 1, patch: 0}
)
