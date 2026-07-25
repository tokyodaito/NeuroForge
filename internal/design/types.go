// Package design implements the design-to-code pipeline (spec §15, ADR-0006).
//
// STATUS: implemented for milestone M9 (M9-6 + M9-7).
//
// Scope:
//   - [Brief]: a design brief distilled from the task description, project
//     design-system scan and any attached reference (§15.3).
//   - [Variant]: one generated image variant (§15.4); the orchestrator produces
//     N variants.
//   - [Specification] (the "visual specification", §15.6): the locked image +
//     viewport/theme/locale/density metadata handed to the coding agent. Once
//     locked, the coding agent MUST NOT arbitrarily change the design.
//   - [Selection]: HUMAN / AUTOMATIC / FIRST_VALID selection modes (§15.4).
//   - [Orchestrator]: ties the above together with image quota failover
//     (§15.5: open circuit breaker → fallback provider → use attached image →
//     continue without generation if optional → WAITING_QUOTA if mandatory).
//
// Boundaries (rule §36.9): the design engine orchestrates image PROVIDERS; it
// never lets a coding agent emit images directly. Image quota/budget is tracked
// separately from coding quota (§14.4).
package design

import (
	"context"
	"time"

	"neuroforge/internal/adapter/imageprovider/protocol"
)

// Mode is the design generation mode (spec §15.1).
type Mode string

const (
	// ModeOff: no design generation; visual verification only against an
	// attached reference (§15.1).
	ModeOff Mode = "OFF"
	// ModeReferenceOnly: use the attached image as the visual specification; do
	// not generate (§15.1).
	ModeReferenceOnly Mode = "REFERENCE_ONLY"
	// ModeGenerateIfMissing: generate a design only when no usable reference
	// is attached (§15.1).
	ModeGenerateIfMissing Mode = "GENERATE_IF_MISSING"
	// ModeAlwaysGenerate: always generate variants, ignoring any attachment
	// (§15.1).
	ModeAlwaysGenerate Mode = "ALWAYS_GENERATE"
)

// IsValid reports whether m is known.
func (m Mode) IsValid() bool {
	switch m {
	case ModeOff, ModeReferenceOnly, ModeGenerateIfMissing, ModeAlwaysGenerate:
		return true
	}
	return false
}

// SelectionMode is the variant-selection mode (spec §15.4).
type SelectionMode string

const (
	// SelectionHuman pauses the task in WAITING_DESIGN_SELECTION (§15.4); other
	// independent tasks continue. The human picks a variant.
	SelectionHuman SelectionMode = "HUMAN"
	// SelectionAutomatic picks the best variant by score (§15.4).
	SelectionAutomatic SelectionMode = "AUTOMATIC"
	// SelectionFirstValid takes the first non-failed variant (§15.4).
	SelectionFirstValid SelectionMode = "FIRST_VALID"
)

// IsValid reports whether m is known.
func (m SelectionMode) IsValid() bool {
	switch m {
	case SelectionHuman, SelectionAutomatic, SelectionFirstValid:
		return true
	}
	return false
}

// Brief is the design brief distilled from the task (§15.3). It is the input to
// image generation: a textual description plus viewport/theme/locale/density
// constraints. A coding agent prepares this brief; image generation is
// delegated to an image provider (rule §36.9).
type Brief struct {
	// TaskID is the task this brief belongs to.
	TaskID string
	// ProjectID is the owning project (for budget attribution).
	ProjectID string
	// Description is the textual design description (the "prompt").
	Description string
	// DesignSystemConstraints captures rules mined from the project design
	// system (colours, typography, component inventory).
	DesignSystemConstraints []string
	// Viewport is the target viewport (§15.6).
	Viewport Viewport
	// Theme is the target theme ("dark"/"light"/"").
	Theme string
	// Locale is the target locale tag (e.g. "ru").
	Locale string
	// Density is the target screen density (e.g. "xxhdpi").
	Density string
	// Mode is the design generation mode (§15.1).
	Mode Mode
	// Variants is the requested variant count (§15.4: design.generation.variants).
	Variants int
	// Tier is the requested image tier (§14.3: design.generation.model_tier).
	Tier protocol.ImageTier
	// Reference is the attached reference image (when Mode != ALWAYS_GENERATE).
	Reference *protocol.Artifact
	// CreatedAt is when the brief was prepared.
	CreatedAt time.Time
}

// Viewport is the target rendering viewport (spec §15.6).
type Viewport struct {
	Width  int
	Height int
}

// Variant is one generated image variant (§15.4).
type Variant struct {
	// Index is the 1-based variant ordinal.
	Index int
	// Engine is the provider that produced this variant.
	Engine string
	// Model is the provider model id.
	Model string
	// Artifact is the generated image.
	Artifact protocol.Artifact
	// Score is an optional quality score in [0,1] for automatic selection.
	Score float64
	// Error carries a per-variant failure (used in failover accounting).
	Error error
}

// Specification is the locked visual specification (§15.6). Once locked, the
// coding agent receives this and MUST NOT arbitrarily change the design.
type Specification struct {
	// TaskID this spec belongs to.
	TaskID string
	// Mode that produced this spec.
	Mode Mode
	// ArtifactHash is the content-addressed hash of the locked image (§9.5).
	ArtifactHash string
	// Artifact is the full artifact reference (path/format/dimensions).
	Artifact protocol.Artifact
	// Viewport is the locked viewport.
	Viewport Viewport
	// Theme is the locked theme.
	Theme string
	// Locale is the locked locale.
	Locale string
	// Density is the locked density.
	Density string
	// Source records how the spec was produced: "attached" (REFERENCE_ONLY),
	// "generated" (selected variant) or "fallback" (no provider available, used
	// attached image as last resort, §15.5).
	Source string
	// SelectedVariant is the 1-based variant ordinal, when Source=="generated".
	SelectedVariant int
	// LockedAt records when the spec was locked.
	LockedAt time.Time
}

// IsLocked reports whether a specification has been locked (has an artifact).
func (s Specification) IsLocked() bool {
	return s.ArtifactHash != ""
}

// ToGenerationRequest builds an [protocol.ImageGenerationRequest] for a variant
// of this brief. variantIndex is the 1-based ordinal.
func (b Brief) ToGenerationRequest(runID string, variantIndex int) protocol.ImageGenerationRequest {
	tier := b.Tier
	if tier == "" {
		tier = protocol.TierDraft
	}
	return protocol.ImageGenerationRequest{
		RunID:        runID,
		Engine:       "", // resolved by orchestrator
		Model:        "", // resolved by orchestrator via tier→catalog
		Tier:         tier,
		Prompt:       b.Description,
		Size:         protocol.ImageSize{Width: b.Viewport.Width, Height: b.Viewport.Height},
		Format:       protocol.FormatPNG,
		Theme:        b.Theme,
		Locale:       b.Locale,
		Density:      b.Density,
		Reference:    b.Reference,
		ProjectID:    b.ProjectID,
		TaskID:       b.TaskID,
		VariantIndex: variantIndex,
	}
}

// ctxKey is unexported so context values stay package-local.
type ctxKey int

// Context values used by the orchestrator to carry diagnostic metadata.
const (
	ctxKeyOrchestrator ctxKey = iota
)

// withOrchestrator tags a context with the orchestrator's run id (debug).
func withOrchestrator(ctx context.Context, runID string) context.Context {
	return context.WithValue(ctx, ctxKeyOrchestrator, runID)
}
