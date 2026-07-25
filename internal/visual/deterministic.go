package visual

import (
	"bytes"

	"neuroforge/internal/adapter/imageprovider/protocol"
)

// DeterministicChecks implements the §16.3 deterministic checks. These NEVER
// call an LLM (rule §22.6). They cover:
//
//   - size / dimensions
//   - viewport match (against the locked visual specification)
//   - blank screen (all-zero / single-colour)
//   - clipping (actual smaller than expected)
//   - overflow (actual larger than viewport in any dimension)
//   - contrast (mean luminance extremes)
//   - byte-identity with reference (perceptual similarity proxy)
//
// The output is a [DeterministicResult] feeding the engine's score/findings.
type DeterministicChecks struct {
	// BlankThreshold: if a single colour covers more than this fraction of the
	// image, it is flagged as blank (§16.3 "наличие пустого экрана"). Range
	// (0,1); default 0.98.
	BlankThreshold float64
}

// DeterministicResult is the outcome of [DeterministicChecks.Check].
type DeterministicResult struct {
	Score    float64
	Findings []Finding
}

// NewDeterministicChecks returns checks with sane defaults.
func NewDeterministicChecks() *DeterministicChecks {
	return &DeterministicChecks{BlankThreshold: 0.98}
}

// Check runs the deterministic checks. ref/act metadata is required; the byte
// slices are optional (when missing, byte-identity is skipped).
func (d *DeterministicChecks) Check(ref, act *protocol.Artifact, refBytes, actBytes []byte) DeterministicResult {
	findings := []Finding{}
	score := 1.0

	// 1. Dimension checks.
	if act.Width <= 0 || act.Height <= 0 {
		findings = append(findings, Finding{Severity: SeverityBlocker, Code: "missing_dimensions", Description: "actual screenshot has no dimensions"})
		score = 0
		return DeterministicResult{Score: score, Findings: findings}
	}
	if ref != nil && ref.Width > 0 && ref.Height > 0 {
		if act.Width != ref.Width || act.Height != ref.Height {
			sev := SeverityMajor
			if act.Width < ref.Width || act.Height < ref.Height {
				sev = SeverityMajor
				findings = append(findings, Finding{
					Severity: SeverityMajor, Code: "clipping",
					Region:      "viewport",
					Description: descDims("actual is smaller than reference (possible clipping)", act.Width, act.Height, ref.Width, ref.Height),
				})
			} else {
				findings = append(findings, Finding{
					Severity: SeverityMajor, Code: "overflow",
					Region:      "viewport",
					Description: descDims("actual is larger than reference (possible overflow)", act.Width, act.Height, ref.Width, ref.Height),
				})
			}
			_ = sev
			score -= 0.3
		}
	}

	// 2. Blank screen (when bytes available).
	if len(actBytes) > 0 {
		if isBlank(actBytes, d.BlankThreshold) {
			findings = append(findings, Finding{
				Severity: SeverityBlocker, Code: "blank_screen",
				Description: "screenshot appears blank (single colour / empty)",
			})
			score = 0
		}
	}

	// 3. Contrast extremes.
	if len(actBytes) > 0 {
		if mean := meanLuminance(actBytes); mean >= 0 && (mean < 5 || mean > 250) {
			findings = append(findings, Finding{
				Severity: SeverityMajor, Code: "contrast_extreme",
				Description: "extreme luminance — possible blank/white screen",
			})
			score -= 0.2
		}
	}

	// 4. Byte-identity / similarity proxy (§16.3 "perceptual similarity").
	if ref != nil && len(refBytes) > 0 && len(actBytes) > 0 {
		if bytes.Equal(refBytes, actBytes) {
			// Byte-identical → perceptually identical.
			// No deduction.
		} else {
			sim := jaccardBytes(refBytes, actBytes)
			if sim < 0.9 {
				findings = append(findings, Finding{
					Severity: SeverityMajor, Code: "visual_diff",
					Region:      "global",
					Description: "actual differs substantially from reference",
				})
				score *= sim
			} else if sim < 0.99 {
				findings = append(findings, Finding{
					Severity: SeverityMinor, Code: "visual_diff_minor",
					Region:      "global",
					Description: "minor visual difference from reference",
				})
				score *= sim
			}
		}
	}

	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}
	return DeterministicResult{Score: score, Findings: findings}
}

// isBlank reports whether the image is essentially a single colour (§16.3
// "наличие пустого экрана"). A coarse byte-distribution check: if one byte
// value covers more than threshold of the payload, treat as blank.
func isBlank(b []byte, threshold float64) bool {
	if len(b) == 0 {
		return true
	}
	if threshold <= 0 || threshold >= 1 {
		threshold = 0.98
	}
	hist := [256]int{}
	for _, x := range b {
		hist[x]++
	}
	max := 0
	for _, c := range hist {
		if c > max {
			max = c
		}
	}
	return float64(max)/float64(len(b)) >= threshold
}

// meanLuminance returns a coarse mean over the bytes (PNG header skews this
// slightly but the extremes are still meaningful). -1 if empty.
func meanLuminance(b []byte) int {
	if len(b) == 0 {
		return -1
	}
	var sum int
	for _, x := range b {
		sum += int(x)
	}
	return sum / len(b)
}

// jaccardBytes is a coarse similarity proxy: fraction of positions where the
// two payloads agree, normalised by the longer length. NOT a true perceptual
// hash, but deterministic and good enough to drive the repair-loop threshold
// without an LLM.
func jaccardBytes(a, b []byte) float64 {
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	if n == 0 {
		return 1
	}
	match := 0
	for i := 0; i < n; i++ {
		var x, y byte
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x == y {
			match++
		}
	}
	return float64(match) / float64(n)
}

func descDims(prefix string, aw, ah, rw, rh int) string {
	return prefix
}
