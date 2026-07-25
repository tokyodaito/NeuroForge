// Package imageprovider defines the image-provider adapter protocol and registry
// (spec §14, ADR-0006).
//
// STATUS: implemented for milestone M9.
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §14): the [Adapter] interface
// (ID/Health/ListModels/InspectQuota/Generate/Edit/AnalyzeFailure), image model
// tiers (IMAGE_DRAFT/IMAGE_STANDARD/IMAGE_HIGH_QUALITY, §14.3) not hard-bound
// to model names, the normalized image event stream, the separate image
// quota/budget tracking (§14.4) and the registry. Required providers: OpenAI GPT
// Image and Google Nano Banana (AC-19). A fake image provider (§33.2) is the
// first implementation and drives the design flow (§15) before any real
// provider is wired; real image calls are opt-in and excluded from CI (rule
// §33).
//
// Boundaries (rule §36.9): image generation is strictly separated from the
// coding agent. A coding agent may prepare a design brief or analyse a
// reference, but image generation itself is delegated to an [Adapter] here. The
// supervisor never lets a coding agent emit images directly.
//
// Versioning: the protocol stability boundary lives in sub-package
// [neuroforge/internal/adapter/imageprovider/protocol]. Shared primitives
// (Account, HealthResult, QuotaSnapshot, FailureClass) are re-exported from the
// coding-agent protocol package because the §32 taxonomy and §20 quota model
// are common to both adapter families.
package imageprovider
