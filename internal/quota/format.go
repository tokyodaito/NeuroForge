package quota

import (
	"fmt"
	"strings"
)

// FormatRemaining renders a remaining figure with a confidence-distinct prefix
// (spec §6.1, AC-18): EXACT/PROVIDER_REPORTED print plainly ("125k"), ESTIMATED
// and INFERRED print with a leading "~" ("~125k"), UNKNOWN prints "unknown".
//
// Estimated usage must NEVER be displayed as exact (rule §36.10). The renderer
// is the single chokepoint that enforces this for the dashboard/CLI.
func FormatRemaining(s Snapshot) string {
	if s.Confidence == ConfUnknown || s.Remaining == nil {
		return "unknown"
	}
	val := formatCount(*s.Remaining)
	switch s.Confidence {
	case ConfEstimated, ConfInferred:
		return "~" + val
	default:
		return val
	}
}

// FormatLimit renders the limit analogously to FormatRemaining.
func FormatLimit(s Snapshot) string {
	if s.Confidence == ConfUnknown || s.Limit == nil {
		return "unknown"
	}
	val := formatCount(*s.Limit)
	switch s.Confidence {
	case ConfEstimated, ConfInferred:
		return "~" + val
	default:
		return val
	}
}

// ConfidenceTag is a short tag suitable for inline display, e.g. "(estimated)".
func ConfidenceTag(c Confidence) string {
	switch c {
	case ConfExact:
		return "(exact)"
	case ConfProviderReported:
		return "(provider-reported)"
	case ConfEstimated:
		return "(estimated)"
	case ConfInferred:
		return "(inferred)"
	default:
		return "(unknown)"
	}
}

// formatCount renders a provider-unit count with a k/M suffix for readability.
func formatCount(v float64) string {
	switch {
	case v >= 1_000_000:
		return trimZero(fmt.Sprintf("%.2f", v/1_000_000)) + "M"
	case v >= 1_000:
		return trimZero(fmt.Sprintf("%.2f", v/1_000)) + "k"
	default:
		return trimZero(fmt.Sprintf("%.0f", v))
	}
}

// trimZero strips a trailing ".0" / ".00" from a formatted number.
func trimZero(s string) string {
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
	}
	return s
}
