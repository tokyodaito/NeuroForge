// Package visual implements the Visual Verification Engine (spec §16).
//
// STATUS: implemented for milestone M10.
//
// Scope:
//
//   - [Finding] and [Result] model §16.4 (visual_verification: status, score,
//     issues, artifacts: reference/actual/diff).
//   - [DeterministicChecks] implements the §16.3 deterministic checks (size,
//     viewport, blank screen, clipping/overflow, contrast, diff regions,
//     perceptual similarity). Deterministic checks NEVER call an LLM (rule
//     §22.6).
//   - [MultimodalEvaluator] is the §16.3 multimodal evaluator interface
//     (composition, hierarchy, reference match, spacing, sizes, colours,
//     typography, element states, visual defects). The default implementation
//     is deterministic; a real implementation delegates to a vision model via
//     a coding agent (rule §36.9: image analysis by a coding agent, image
//     GENERATION by an image provider).
//   - [Engine] orchestrates capture → deterministic checks → multimodal eval
//     → repair decision. AC-24: when visual verification is disabled (or did
//     not run), the engine MUST NOT claim the UI is verified.
//   - [ReferenceFreeReview] implements §16.6: checks visual integrity,
//     overflow, readability, design-system consistency and broken states
//     WITHOUT claiming pixel-perfect match (AC-24, §16.6: "не должен заявлять
//     о pixel-perfect соответствии при отсутствии reference").
//
// Boundaries: the engine never holds credentials and never calls an LLM
// directly for deterministic checks (rule §22.6). The multimodal evaluator is
// an injected interface.
package visual

import (
	"context"
	"time"

	"neuroforge/internal/adapter/imageprovider/protocol"
)

// Severity mirrors §16.4 issue severity.
type Severity string

const (
	// SeverityBlocker: the screen is blank, crashed, or unusable.
	SeverityBlocker Severity = "blocker"
	// SeverityMajor: significant visual divergence (wrong layout, missing
	// major element).
	SeverityMajor Severity = "major"
	// SeverityMinor: cosmetic (font weight, small spacing).
	SeverityMinor Severity = "minor"
	// SeverityInfo: observation, no repair needed.
	SeverityInfo Severity = "info"
)

// IsValid reports whether s is known.
func (s Severity) IsValid() bool {
	switch s {
	case SeverityBlocker, SeverityMajor, SeverityMinor, SeverityInfo:
		return true
	}
	return false
}

// Finding is one visual issue (spec §16.4 issues[].severity/region/description).
type Finding struct {
	Severity    Severity `json:"severity"`
	Region      string   `json:"region,omitempty"`
	Description string   `json:"description"`
	Code        string   `json:"code,omitempty"` // machine code, e.g. "blank_screen"
}

// IsActionable reports whether a finding warrants a repair (blocker/major).
// Minor/info are surfaced but do not trigger the repair loop on their own.
func (f Finding) IsActionable() bool {
	return f.Severity == SeverityBlocker || f.Severity == SeverityMajor
}

// Result is the §16.4 visual_verification result.
//
//   - Status: passed/failed/skipped/not_verified. CRITICAL (AC-24):
//     NotVerified is the ONLY status that may be claimed when no reference
//     comparison ran AND no reference-free review ran. The engine MUST NOT
//     claim "passed" without verification.
//   - Score: similarity in [0,1] (0 when no comparison was possible).
//   - Findings: the issues list (§16.4).
//   - Artifacts: reference/actual/diff hashes (§16.4).
//   - ReferenceBased reports whether a reference was used. When false, the
//     result is reference-free (§16.6) and PixelPerfect is ALWAYS false.
type Result struct {
	Status         Status            `json:"status"`
	Score          float64           `json:"score"`
	Findings       []Finding         `json:"findings,omitempty"`
	Artifacts      ResultArtifacts   `json:"artifacts"`
	ReferenceBased bool              `json:"reference_based"`
	PixelPerfect   bool              `json:"pixel_perfect"`
	Mode           ReferenceFreeMode `json:"mode,omitempty"`
	CheckedAt      time.Time         `json:"checked_at"`
	Reason         string            `json:"reason,omitempty"`
}

// Status is the verification status.
type Status string

const (
	// StatusPassed: verification ran and the score met the threshold.
	StatusPassed Status = "passed"
	// StatusFailed: verification ran and the score was below the threshold, OR
	// a blocker finding was raised.
	StatusFailed Status = "failed"
	// StatusSkipped: visual verification is disabled (§5: visual_verification:
	// false). AC-24: StatusSkipped MUST NOT be presented as "verified".
	StatusSkipped Status = "skipped"
	// StatusNotVerified: verification did not run (no harness, harness error,
	// etc.). AC-24: MUST NOT be presented as "verified".
	StatusNotVerified Status = "not_verified"
)

// IsVerified reports whether the UI may be presented as visually verified
// (AC-24: only Passed counts; Skipped/NotVerified/Failed do NOT).
func (s Status) IsVerified() bool { return s == StatusPassed }

// ResultArtifacts holds the artifact references (§16.4).
type ResultArtifacts struct {
	Reference *protocol.Artifact `json:"reference,omitempty"`
	Actual    *protocol.Artifact `json:"actual,omitempty"`
	Diff      *protocol.Artifact `json:"diff,omitempty"`
}

// ReferenceFreeMode records how a reference-free review ran (§16.6).
type ReferenceFreeMode string

const (
	// ReferenceFreeNone: no reference-free review (a reference was used).
	ReferenceFreeNone ReferenceFreeMode = ""
	// ReferenceFreeRan: a reference-free review ran (§16.6). PixelPerfect is
	// ALWAYS false in this mode.
	ReferenceFreeRan ReferenceFreeMode = "reference_free"
)

// VerifyInput is the input to [Engine.Verify].
type VerifyInput struct {
	// Reference is the locked visual specification screenshot. When nil, the
	// engine runs a reference-free review (§16.6).
	Reference *protocol.Artifact
	// Actual is the captured device screenshot (from the harness).
	Actual *protocol.Artifact
	// Enabled mirrors policy: pipeline.design.visual_verification. When false,
	// the engine returns StatusSkipped (AC-24).
	Enabled bool
	// MinimumScore is the §16.5 minimum_score threshold (default 0.9).
	MinimumScore float64
	// Store is used to read artifact bytes for comparison.
	Store byteSource
}

// byteSource reads artifact bytes by hash (the artifact store implements this).
type byteSource interface {
	Read(hash string) ([]byte, error)
}

// Engine is the Visual Verification Engine (spec §16).
type Engine struct {
	deterministic *DeterministicChecks
	multimodal    MultimodalEvaluator
	now           func() time.Time
}

// Options configures the engine.
type Options struct {
	Deterministic *DeterministicChecks
	Multimodal    MultimodalEvaluator
	Now           func() time.Time
}

// New returns an engine. Defaults: deterministic checks + a deterministic
// multimodal evaluator. Callers inject a real multimodal evaluator (delegating
// to a vision model via a coding agent, rule §36.9) for production.
func New(opts Options) *Engine {
	if opts.Deterministic == nil {
		opts.Deterministic = NewDeterministicChecks()
	}
	if opts.Multimodal == nil {
		opts.Multimodal = DeterministicMultimodal{}
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Engine{deterministic: opts.Deterministic, multimodal: opts.Multimodal, now: opts.Now}
}

// Verify runs the verification pipeline (§16.3, §16.4).
//
// AC-24 (critical): when input.Enabled is false, the result is StatusSkipped
// and IsVerified() returns false. The system MUST NOT present a skipped
// verification as "verified".
//
// §16.6: when no reference is provided, a reference-free review runs and the
// result has ReferenceBased=false, PixelPerfect=false (never claims
// pixel-perfect without a reference).
func (e *Engine) Verify(ctx context.Context, in VerifyInput) Result {
	if !in.Enabled {
		return Result{Status: StatusSkipped, CheckedAt: e.now(), Reason: "visual verification disabled by policy (§5)"}
	}
	if in.Actual == nil || in.Actual.Hash == "" {
		return Result{Status: StatusNotVerified, CheckedAt: e.now(), Reason: "no captured screenshot available"}
	}
	minScore := in.MinimumScore
	if minScore <= 0 {
		minScore = 0.9
	}

	if in.Reference == nil || in.Reference.Hash == "" {
		// §16.6 reference-free review.
		findings := e.runReferenceFree(ctx, in)
		score := referenceFreeScore(findings)
		status := StatusPassed
		if hasBlocker(findings) || score < minScore {
			status = StatusFailed
		}
		return Result{
			Status:         status,
			Score:          score,
			Findings:       findings,
			Artifacts:      ResultArtifacts{Actual: in.Actual},
			ReferenceBased: false,
			PixelPerfect:   false, // NEVER true without a reference (AC-24/§16.6)
			Mode:           ReferenceFreeRan,
			CheckedAt:      e.now(),
			Reason:         "reference-free review (§16.6); pixel-perfect NOT claimed",
		}
	}

	// Reference-based verification: deterministic checks first, then multimodal.
	actualBytes, _ := readBytes(in.Store, in.Actual.Hash)
	refBytes, _ := readBytes(in.Store, in.Reference.Hash)
	dc := e.deterministic.Check(in.Reference, in.Actual, refBytes, actualBytes)
	findings := dc.Findings
	score := dc.Score

	// Multimodal evaluation (§16.3): delegated to the injected evaluator. The
	// default is deterministic; production delegates to a vision model.
	mm := e.multimodal.Evaluate(ctx, MultimodalInput{
		Reference: in.Reference, Actual: in.Actual,
		ReferenceBytes: refBytes, ActualBytes: actualBytes,
	})
	findings = append(findings, mm.Findings...)
	// Blend scores: deterministic similarity weighted with multimodal match.
	if mm.Score >= 0 {
		score = (score + mm.Score) / 2
	}

	status := StatusPassed
	if hasBlocker(findings) || score < minScore {
		status = StatusFailed
	}
	pixelPerfect := score >= 0.999 && len(filterActionable(findings)) == 0
	return Result{
		Status:         status,
		Score:          score,
		Findings:       findings,
		Artifacts:      ResultArtifacts{Reference: in.Reference, Actual: in.Actual},
		ReferenceBased: true,
		PixelPerfect:   pixelPerfect,
		Mode:           ReferenceFreeNone,
		CheckedAt:      e.now(),
	}
}

func (e *Engine) runReferenceFree(_ context.Context, in VerifyInput) []Finding {
	actualBytes, _ := readBytes(in.Store, in.Actual.Hash)
	return ReferenceFreeReview(in.Actual, actualBytes)
}

func hasBlocker(findings []Finding) bool {
	for _, f := range findings {
		if f.Severity == SeverityBlocker {
			return true
		}
	}
	return false
}

func filterActionable(findings []Finding) []Finding {
	out := make([]Finding, 0, len(findings))
	for _, f := range findings {
		if f.IsActionable() {
			out = append(out, f)
		}
	}
	return out
}

func referenceFreeScore(findings []Finding) float64 {
	// Start at perfect; each finding deducts by severity.
	score := 1.0
	for _, f := range findings {
		switch f.Severity {
		case SeverityBlocker:
			score -= 0.5
		case SeverityMajor:
			score -= 0.2
		case SeverityMinor:
			score -= 0.05
		}
	}
	if score < 0 {
		score = 0
	}
	return score
}

func readBytes(s byteSource, hash string) ([]byte, error) {
	if s == nil || hash == "" {
		return nil, nil
	}
	return s.Read(hash)
}
