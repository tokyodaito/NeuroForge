// Package imageprovider defines the image-provider adapter protocol and registry.
//
// STATUS: scaffold — not implemented (planned for milestone M9).
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §14): the ImageProviderAdapter interface
// (Health/ListModels/InspectQuota/Generate/Edit/AnalyzeFailure), image model tiers
// (IMAGE_DRAFT/IMAGE_STANDARD/IMAGE_HIGH_QUALITY) not hard-bound to model names,
// and the separate image quota/budget tracking. Required providers: OpenAI GPT
// Image and Google Nano Banana. See ADR-0006.
//
// Boundaries: image generation is strictly separated from the coding agent (rule
// §36.9); a coding agent may prepare a design brief or analyze a reference but may
// not itself generate images.
package imageprovider
