package imageprovider

import (
	"context"

	"neuroforge/internal/adapter/imageprovider/protocol"
)

// Adapter is the unified surface every image-generation engine implements (spec
// §14.2, ADR-0006). Methods are grouped:
//
//   - metadata: [Adapter.ID], [Adapter.Health], [Adapter.ListModels],
//     [Adapter.InspectQuota];
//   - generation: [Adapter.Generate], [Adapter.Edit];
//   - diagnostics: [Adapter.AnalyzeFailure].
//
// This is a SEPARATE adapter family from coding agents (rule §36.9): a coding
// agent may prepare a design brief or analyse a reference, but image generation
// itself is delegated to an Adapter here. The protocol package is the versioned
// stability boundary.
type Adapter interface {
	// ID is the stable image-provider identifier (e.g. "gpt-image",
	// "nano-banana", "fake"). Independent from any model name.
	ID() string

	// Health probes one account's reachability (spec §14.2).
	Health(ctx context.Context, account protocol.Account) protocol.HealthResult

	// ListModels returns the image models this engine can target for an
	// account (spec §14.2). Model names are provider-supplied; the core never
	// hard-codes them (rule §36.8). Models are exposed by tier (§14.3) and the
	// router never hard-binds a tier to a model name.
	ListModels(ctx context.Context, account protocol.Account) ([]protocol.ImageModel, error)

	// InspectQuota returns the current quota snapshot for an account (spec
	// §14.2, §14.4, §20.1). Image quota is tracked SEPARATELY from coding
	// quota.
	InspectQuota(ctx context.Context, account protocol.Account) protocol.QuotaSnapshot

	// Generate produces one or more image artifacts from a textual brief (spec
	// §14.2, §15.3). Progress and intermediate artifacts stream to sink. The
	// request never carries credentials (AC-28).
	Generate(ctx context.Context, req protocol.ImageGenerationRequest, sink EventSink) (protocol.Result, error)

	// Edit modifies an existing image guided by a prompt (spec §14.2). Used by
	// the repair loop and design-variant refinement. Engines that do not
	// support editing return a classified failure.
	Edit(ctx context.Context, req protocol.ImageEditRequest, sink EventSink) (protocol.Result, error)

	// AnalyzeFailure maps a native error onto the §32 taxonomy (spec §14.2,
	// §32). Adapters without a specific signal defer to [DefaultClassify].
	AnalyzeFailure(err error) protocol.FailureClassification
}

// ImageProviderAdapter is an alias for [Adapter], kept for spec readability
// (§14.2 names the interface ImageProviderAdapter).
type ImageProviderAdapter = Adapter

// EventSink consumes the normalized event stream produced by an image-provider
// generation/edit (spec §14.2). Implementations must be safe for concurrent use
// when the adapter emits from multiple goroutines. Returning a non-nil error
// signals the adapter to abort the run (e.g. on caller cancellation); otherwise
// the event is delivered in order and the run continues.
type EventSink interface {
	OnEvent(ctx context.Context, ev protocol.Event) error
}

// SinkFunc lets ordinary functions satisfy [EventSink].
type SinkFunc func(ctx context.Context, ev protocol.Event) error

// OnEvent implements [EventSink].
func (f SinkFunc) OnEvent(ctx context.Context, ev protocol.Event) error { return f(ctx, ev) }
