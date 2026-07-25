package visual

import (
	"context"

	"neuroforge/internal/adapter/imageprovider/protocol"
)

// MultimodalEvaluator is the §16.3 multimodal evaluator interface. A vision
// evaluator checks: composition, visual hierarchy, reference match, spacing,
// sizes, colours, typography, element states, explicit visual defects.
//
// The default implementation ([DeterministicMultimodal]) is deterministic and
// uses only the artifact bytes/metadata (no LLM, rule §22.6). Production wires
// a real evaluator that delegates to a vision model via a CODING agent (rule
// §36.9: image ANALYSIS by a coding agent; image GENERATION is the only thing
// reserved for the image-provider adapter).
type MultimodalEvaluator interface {
	// Evaluate compares the actual screenshot against the reference and
	// returns a score in [0,1] plus findings. A score of -1 means "no signal"
	// (the engine ignores it).
	Evaluate(ctx context.Context, in MultimodalInput) MultimodalOutput
}

// MultimodalInput is the input to [MultimodalEvaluator.Evaluate].
type MultimodalInput struct {
	Reference      *protocol.Artifact
	Actual         *protocol.Artifact
	ReferenceBytes []byte
	ActualBytes    []byte
}

// MultimodalOutput is the outcome of [MultimodalEvaluator.Evaluate].
type MultimodalOutput struct {
	Score    float64
	Findings []Finding
}

// DeterministicMultimodal is the default, LLM-free evaluator. It augments the
// deterministic checks with structural signals derivable from the bytes. It is
// NOT a substitute for a vision model in production; it provides a baseline
// signal so the engine is fully testable without paid models (rule §33).
type DeterministicMultimodal struct{}

// Evaluate implements MultimodalEvaluator.
func (DeterministicMultimodal) Evaluate(_ context.Context, in MultimodalInput) MultimodalOutput {
	out := MultimodalOutput{Score: 1.0}
	if in.Actual == nil {
		out.Score = -1
		return out
	}
	// Colour palette diversity (a real UI has many colours; a blank/broken
	// screen has very few). Coarse byte-histogram count.
	if len(in.ActualBytes) > 0 {
		distinct := distinctByteCount(in.ActualBytes)
		if distinct < 4 {
			out.Findings = append(out.Findings, Finding{
				Severity: SeverityMajor, Code: "low_colour_diversity",
				Description: "very few distinct colours — possible blank or broken render",
			})
			out.Score -= 0.3
		}
	}
	// Reference match: if byte-identical, perfect; else leave the deterministic
	// engine's similarity to dominate.
	if in.Reference != nil && len(in.ReferenceBytes) > 0 && len(in.ActualBytes) > 0 {
		if !bytesEqual(in.ReferenceBytes, in.ActualBytes) {
			// Don't double-penalise; the deterministic checks already flagged
			// the diff. We only soften the score.
			out.Score -= 0.0
		}
	}
	if out.Score < 0 {
		out.Score = 0
	}
	return out
}

func distinctByteCount(b []byte) int {
	var seen [256]bool
	n := 0
	for _, x := range b {
		if !seen[x] {
			seen[x] = true
			n++
		}
	}
	return n
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
