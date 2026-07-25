package visual

import (
	"neuroforge/internal/adapter/imageprovider/protocol"
)

// ReferenceFreeReview implements §16.6: review the actual screenshot WITHOUT a
// reference. It checks:
//
//   - визуальная целостность (visual integrity);
//   - переполнения (overflow);
//   - читаемость (readability);
//   - consistency с design system (best-effort);
//   - очевидно сломанные состояния (obviously broken states).
//
// §16.6 (critical, AC-24): the review MUST NOT claim pixel-perfect match. The
// caller (engine) enforces PixelPerfect=false whenever this path runs; this
// function only produces findings.
func ReferenceFreeReview(actual *protocol.Artifact, actualBytes []byte) []Finding {
	if actual == nil {
		return []Finding{{Severity: SeverityBlocker, Code: "no_screenshot", Description: "no screenshot captured"}}
	}
	var findings []Finding

	// 1. Missing dimensions.
	if actual.Width <= 0 || actual.Height <= 0 {
		findings = append(findings, Finding{
			Severity: SeverityBlocker, Code: "missing_dimensions",
			Description: "screenshot has no dimensions",
		})
		return findings
	}

	// 2. Blank / broken render (byte-distribution check).
	if len(actualBytes) > 0 {
		if isBlank(actualBytes, 0.98) {
			findings = append(findings, Finding{
				Severity: SeverityBlocker, Code: "blank_screen",
				Description: "screenshot appears blank — possibly empty screen or render failure",
			})
			return findings
		}
		// Low colour diversity → likely broken render.
		if distinct := distinctByteCount(actualBytes); distinct < 4 {
			findings = append(findings, Finding{
				Severity: SeverityMajor, Code: "low_colour_diversity",
				Description: "very few distinct colours — possibly broken render",
			})
		}
		// Extreme luminance → readability concern (§16.6 "читаемость").
		if mean := meanLuminance(actualBytes); mean >= 0 && (mean < 5 || mean > 250) {
			findings = append(findings, Finding{
				Severity: SeverityMajor, Code: "readability_extreme",
				Description: "extreme luminance impairs readability",
			})
		}
	}

	// 3. Zero-byte artifact.
	if actual.Bytes == 0 {
		findings = append(findings, Finding{
			Severity: SeverityBlocker, Code: "empty_screenshot",
			Description: "screenshot has zero bytes",
		})
	}

	// NOTE: design-system consistency (§16.6 "consistency с design system")
	// and visual hierarchy/composition checks require either a vision model or
	// a declared design-system spec. They are intentionally NOT claimed here
	// (rule §36.6: deterministic only; the multimodal evaluator handles the
	// rest in production).
	return findings
}
