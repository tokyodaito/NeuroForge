// Package protocol defines the versioned stability boundary for image-provider
// adapters (spec §14.2, ADR-0006).
//
// It is the image-provider analogue of package
// `neuroforge/internal/adapter/codingagent/protocol`: the types an
// [imageprovider.Adapter] implementation and its consumers agree on. Shared
// primitives ([Account], [HealthResult], [QuotaSnapshot],
// [FailureClassification], [FailureClass]) are re-exported from the coding-agent
// protocol package because the §32 taxonomy and §20 quota model are common to
// both adapter families — duplicating them would silently drift. Image-specific
// surfaces (request/result/event/artifact/tier) live here.
//
// Coding agents and image providers are strictly separate abstractions
// (spec §14, rule §36.9): a coding agent may prepare a design brief or analyse a
// reference, but image generation itself is delegated to an
// [imageprovider.Adapter]. This package enforces that boundary by type.
package protocol

import (
	"neuroforge/internal/adapter/codingagent/protocol"
)

// ProtocolVersion is the image-provider protocol major version implemented by
// this package (spec §14.2, ADR-0006). Independent from the coding-agent
// protocol version: this family stabilises on its own cadence.
//
// Versioning rules mirror the coding-agent protocol: additive, backwards-
// compatible changes (new optional fields, new event kinds) do NOT bump the
// major version; consumers must ignore unknown event kinds rather than fail.
//
// Current: 1 (stabilised in milestone M9).
const ProtocolVersion = 1

// Account is an opaque reference to a provider account. Re-exported from the
// coding-agent protocol so credentials never cross the wire (spec §29.2,
// AC-28): adapters resolve the account internally.
type Account = protocol.Account

// HealthResult is the outcome of Health() (spec §14.2, §12.2).
type HealthResult = protocol.HealthResult

// HealthStatus is the coarse health signal (spec §12.2).
type HealthStatus = protocol.HealthStatus

// QuotaSnapshot is the outcome of InspectQuota() (spec §14.2, §14.4, §20.1).
// Image quota is tracked SEPARATELY from coding quota (§14.4): an image account
// is keyed by its image engine, not by any coding engine.
type QuotaSnapshot = protocol.QuotaSnapshot

// QuotaConfidence is the precision of a quota figure (spec §20.1, rule §36.10).
type QuotaConfidence = protocol.QuotaConfidence

// QuotaState is the reported quota state (spec §20.2).
type QuotaState = protocol.QuotaState

// FailureClass enumerates the §32 error taxonomy. Re-used: image providers map
// native errors onto the same vocabulary so the supervisor/quota/budget
// subsystems consume a single signal.
type FailureClass = protocol.FailureClass

// FailureClassification is the outcome of AnalyzeFailure() (spec §14.2, §32).
type FailureClassification = protocol.FailureClassification

// FailurePolicy is the recommended disposition for a failure class (spec §32).
type FailurePolicy = protocol.FailurePolicy

// ImageTier is the provider-declared quality/economy tier (spec §14.3). Tiers
// are NOT hard-bound to model names (rule §14.3, ADR-0006): the router selects
// by tier and resolves the concrete model from a swappable catalog.
type ImageTier string

const (
	// TierDraft is the cheapest tier (exploration, variant exploration).
	TierDraft ImageTier = "IMAGE_DRAFT"
	// TierStandard is the default quality tier.
	TierStandard ImageTier = "IMAGE_STANDARD"
	// TierHighQuality is the premium tier (final visual specification).
	TierHighQuality ImageTier = "IMAGE_HIGH_QUALITY"
)

// IsValid reports whether t is a known tier.
func (t ImageTier) IsValid() bool {
	switch t {
	case TierDraft, TierStandard, TierHighQuality:
		return true
	}
	return false
}

// Tiers enumerates every tier in quality order.
func Tiers() []ImageTier { return []ImageTier{TierDraft, TierStandard, TierHighQuality} }

// ImageFormat is the encoded format of a generated image artifact.
type ImageFormat string

const (
	FormatPNG  ImageFormat = "image/png"
	FormatJPEG ImageFormat = "image/jpeg"
	FormatWebP ImageFormat = "image/webp"
)

// IsValid reports whether f is a known format.
func (f ImageFormat) IsValid() bool {
	switch f {
	case FormatPNG, FormatJPEG, FormatWebP:
		return true
	}
	return false
}

// ImageSize is the requested rendering dimensions in pixels.
type ImageSize struct {
	Width  int
	Height int
}

// Pixels returns width*height (0 when either is unset).
func (s ImageSize) Pixels() int {
	if s.Width <= 0 || s.Height <= 0 {
		return 0
	}
	return s.Width * s.Height
}

// Artifact is a content-addressed reference to a generated or input image
// (spec §9.5, §14.2, §16.4). Large binaries live on the filesystem (§31); the
// store keys them by SHA-256.
type Artifact struct {
	// Hash is the SHA-256 of the artifact bytes (content-addressed key).
	Hash string
	// Path is the filesystem location inside the artifact store.
	Path string
	// Format is the encoded image format.
	Format ImageFormat
	// Width/Height are the pixel dimensions, if known.
	Width  int
	Height int
	// Bytes is the encoded payload size.
	Bytes int
	// Source labels the origin: "generated", "uploaded", "captured", "fixture".
	Source string
}

// ImageModel describes a model an image provider can target (spec §14.2, §14.3).
// Model names are provider-supplied; the core never hard-codes them (rule
// §36.8).
type ImageModel struct {
	// ID is the fully-qualified provider/model identifier. Opaque to the core.
	ID string
	// Engine is the image engine that serves this model.
	Engine string
	// Tier is the provider-declared quality tier. The router must not hard-bind
	// a tier to a model name (§14.3).
	Tier ImageTier
	// MaxResolution is the max pixel count, 0 if unknown.
	MaxResolution int
	// SupportsEdit reports whether Edit() is supported (not just Generate()).
	SupportsEdit bool
	// SupportsImageInput reports whether the model accepts a reference image.
	SupportsImageInput bool
}

// ImageGenerationRequest is the input to Generate() (spec §14.2, §15.3, §15.4).
// The request never carries credentials (AC-28).
type ImageGenerationRequest struct {
	// RunID correlates events and usage records.
	RunID string
	// Account is the provider account to bill/attribute.
	Account Account
	// Engine identifies the image provider (e.g. "gpt-image", "nano-banana").
	Engine string
	// Model is the provider-specific model id (resolved by the router from the
	// requested Tier via the catalog; never hard-coded by the core).
	Model string
	// Tier is the requested quality/economy tier (§14.3).
	Tier ImageTier
	// Prompt is the design brief / textual description (§15.3).
	Prompt string
	// NegativePrompt lists elements to avoid, if any.
	NegativePrompt string
	// Size is the requested pixel dimensions.
	Size ImageSize
	// Format is the requested encoding (default PNG).
	Format ImageFormat
	// Theme is the desired theme: "dark"/"light"/"" (unspecified).
	Theme string
	// Locale is the desired locale tag (e.g. "ru").
	Locale string
	// Density is the desired screen density (e.g. "xxhdpi").
	Density string
	// Reference is an optional reference image (REFERENCE_ONLY / edit modes).
	Reference *Artifact
	// ProjectID / TaskID attribute the generation for budget/quota.
	ProjectID string
	TaskID    string
	// VariantIndex is the 1-based variant ordinal within a multi-variant
	// request (§15.4), 0 when not part of a variant batch.
	VariantIndex int
}

// ImageEditRequest is the input to Edit() (spec §14.2): modify an existing
// image guided by a prompt. The request never carries credentials (AC-28).
type ImageEditRequest struct {
	RunID     string
	Account   Account
	Engine    string
	Model     string
	Tier      ImageTier
	Input     Artifact
	Prompt    string
	Mask      *Artifact // optional edit mask
	ProjectID string
	TaskID    string
}

// Usage reports the cost/usage of one generation or edit (spec §14.4, §23).
// Image usage is recorded on the image budget, separate from coding usage.
type Usage struct {
	InputTokens  int
	OutputTokens int
	ImageGens    int
	CostUSD      float64
	Confidence   QuotaConfidence
	// Included means the generation was covered by subscription quota and has
	// no marginal paid cost (§23). Included usage never counts against a paid
	// hard limit.
	Included bool
}

// Result is the outcome of Generate()/Edit() (spec §14.2). A generation may
// produce more than one artifact (variants, §15.4).
type Result struct {
	RunID  string
	Engine string
	Model  string
	Tier   ImageTier
	// Artifacts is the produced image(s); len >= 1 on success.
	Artifacts []Artifact
	// Usage reports the cost of this generation.
	Usage Usage
	// VariantIndex echoes the request variant ordinal, if any.
	VariantIndex int
}

// Primary returns the first artifact, or a zero value if none. Convenience for
// single-image callers.
func (r Result) Primary() Artifact {
	if len(r.Artifacts) == 0 {
		return Artifact{}
	}
	return r.Artifacts[0]
}

// EventKind enumerates the normalized image event kinds. The set parallels the
// coding-agent normalized events (§12.4) but is image-specific; unknown kinds
// MUST be ignored, not fatal (additive versioning).
type EventKind string

const (
	EventStarted   EventKind = "image.started"
	EventProgress  EventKind = "image.progress"
	EventArtifact  EventKind = "image.artifact"
	EventUsage     EventKind = "image.usage"
	EventCompleted EventKind = "image.completed"
	EventFailed    EventKind = "image.failed"
	EventCancelled EventKind = "image.cancelled"
)

// IsTerminal reports whether k ends the event stream for a run.
func (k EventKind) IsTerminal() bool {
	switch k {
	case EventCompleted, EventFailed, EventCancelled:
		return true
	}
	return false
}

// Event is a normalized image-provider event pushed to an EventSink.
type Event struct {
	Kind     EventKind
	RunID    string
	Engine   string
	Model    string
	Progress float64 // 0..1, for EventProgress
	Artifact *Artifact
	Usage    *Usage
	Failure  *FailureClassification
	Detail   string
}
