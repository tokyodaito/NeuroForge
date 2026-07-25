package design

import (
	"context"
	"errors"
	"fmt"
	"time"

	"neuroforge/internal/adapter/imageprovider"
	"neuroforge/internal/adapter/imageprovider/protocol"
)

// WaitState is the task-level wait state the orchestrator can request (spec
// §15.4 WAITING_DESIGN_SELECTION, §15.5 WAITING_QUOTA).
type WaitState string

const (
	// WaitNone: no wait; the spec is ready.
	WaitNone WaitState = ""
	// WaitDesignSelection: HUMAN selection mode; the task pauses until the user
	// picks a variant (§15.4). Other independent tasks continue.
	WaitDesignSelection WaitState = "WAITING_DESIGN_SELECTION"
	// WaitQuota: all image providers are exhausted/unavailable AND design
	// generation is mandatory (§15.5).
	WaitQuota WaitState = "WAITING_QUOTA"
)

// Outcome is the result of [Orchestrator.Run]: the locked specification, or the
// wait state the task should enter, or the fallback decision.
type Outcome struct {
	// Spec is the locked visual specification (when WaitState==WaitNone).
	Spec Specification
	// WaitState is non-empty when the task must pause (§15.4/§15.5).
	WaitState WaitState
	// Variants is the variants produced (for HUMAN selection, even on wait).
	Variants []Variant
	// Failovers is the number of providers that failed and were skipped.
	Failovers int
	// UsedFallback reports whether the attached reference was used because no
	// provider could serve the request (§15.5).
	UsedFallback bool
	// Reason is a human-readable explanation (route explanation §19.6).
	Reason string
}

// ProviderRoute is one image-provider candidate in the route chain (spec §21.1).
type ProviderRoute struct {
	Engine  string
	Account protocol.Account
	Model   string // optional override; resolved from tier if empty
}

// OrchestratorOptions configures the orchestrator.
type OrchestratorOptions struct {
	// Registry is the image-provider registry (always present).
	Registry *imageprovider.Registry
	// Selection is the variant selection mode (§15.4).
	Selection SelectionMode
	// MaxVariants caps the variant count (§23 image.maximum_variants_per_task;
	// 0 disables the cap).
	MaxVariants int
	// GenerationRequired reports whether design generation is mandatory for
	// this task. When false and all providers fail, the orchestrator returns
	// OutCome.UsedFallback / WaitNone (continue without generation, §15.5).
	GenerationRequired bool
	// Now is the clock; defaults to time.Now().UTC().
	Now func() time.Time
}

// Orchestrator drives the design pipeline: generate variants → select → lock
// the visual specification, with image quota failover (§15.5).
//
// The orchestrator is deterministic orchestration. It never calls an LLM
// directly (rule §22.6); it delegates image generation to image-provider
// adapters (rule §36.9). Image quota/budget is tracked separately from coding
// quota (§14.4).
type Orchestrator struct {
	opts OrchestratorOptions
}

// New returns an orchestrator. Panics if Registry is nil (misconfiguration).
func New(opts OrchestratorOptions) *Orchestrator {
	if opts.Registry == nil {
		panic("design: Registry is required")
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	if opts.Selection == "" {
		opts.Selection = SelectionFirstValid
	}
	return &Orchestrator{opts: opts}
}

// Run executes the design pipeline for a brief.
//
// Behaviour by Mode (spec §15.1):
//
//   - ModeOff / ModeReferenceOnly: if a reference is attached, lock it as the
//     spec (source="attached"); no generation. If no reference, fall through to
//     generation only when Mode != ModeOff.
//   - ModeGenerateIfMissing: generate only when no usable reference is
//     attached.
//   - ModeAlwaysGenerate: always generate variants (even with a reference).
//
// On provider failure (quota/auth/capacity), the orchestrator:
//  1. Skips the provider and tries the next route (§15.5 fallback provider).
//  2. If all routes fail and a reference exists, uses it (source="fallback").
//  3. If all routes fail, no reference, and generation is mandatory: returns
//     WaitQuota.
//  4. If all routes fail, no reference, and generation is optional: returns
//     OutCome with no spec and WaitNone (continue without design, §15.5).
func (o *Orchestrator) Run(ctx context.Context, brief Brief, routes []ProviderRoute) (Outcome, error) {
	ctx = withOrchestrator(ctx, brief.TaskID)
	out := Outcome{}

	// Step 1: short-circuit when generation is not requested (§15.1).
	if brief.Mode == ModeOff || brief.Mode == ModeReferenceOnly {
		if brief.Reference != nil && brief.Reference.Hash != "" {
			out.Spec = o.lockFromAttached(brief, "attached")
			out.Reason = "reference-only: locked attached image as visual specification"
			return out, nil
		}
		if brief.Mode == ModeReferenceOnly {
			return Outcome{Reason: "reference-only mode requested but no reference attached"}, nil
		}
		// ModeOff without a reference: nothing to do.
		return Outcome{Reason: "design generation off and no reference attached"}, nil
	}

	// Step 2: GENERATE_IF_MISSING short-circuits when a reference exists.
	if brief.Mode == ModeGenerateIfMissing && brief.Reference != nil && brief.Reference.Hash != "" {
		out.Spec = o.lockFromAttached(brief, "attached")
		out.Reason = "generate-if-missing: reference present, skipped generation"
		return out, nil
	}

	// Step 3: generate variants across the route chain.
	variants, failovers := o.generateAcrossRoutes(ctx, brief, routes)
	out.Variants = variants
	out.Failovers = failovers

	usable := filterUsable(variants)
	if len(usable) == 0 {
		// All providers failed.
		if brief.Reference != nil && brief.Reference.Hash != "" {
			// §15.5: use the attached image if present.
			out.Spec = o.lockFromAttached(brief, "fallback")
			out.UsedFallback = true
			out.Reason = "all image providers failed; used attached image as fallback (§15.5)"
			return out, nil
		}
		if o.opts.GenerationRequired {
			out.WaitState = WaitQuota
			out.Reason = "all image providers exhausted and design generation mandatory (§15.5 WAITING_QUOTA)"
			return out, nil
		}
		out.Reason = "all image providers failed; design generation optional, continuing without spec (§15.5)"
		return out, nil
	}

	// Step 4: HUMAN selection → wait state (§15.4). Other independent tasks
	// continue (the task scheduler handles the pause).
	if o.opts.Selection == SelectionHuman {
		out.WaitState = WaitDesignSelection
		out.Reason = "human selection requested; task enters WAITING_DESIGN_SELECTION (§15.4)"
		return out, nil
	}

	// Step 5: automatic / first-valid selection → lock the spec.
	chosen := selectVariant(usable, o.opts.Selection)
	out.Spec = Specification{
		TaskID:          brief.TaskID,
		Mode:            brief.Mode,
		ArtifactHash:    chosen.Artifact.Hash,
		Artifact:        chosen.Artifact,
		Viewport:        brief.Viewport,
		Theme:           brief.Theme,
		Locale:          brief.Locale,
		Density:         brief.Density,
		Source:          "generated",
		SelectedVariant: chosen.Index,
		LockedAt:        o.opts.Now(),
	}
	out.Reason = fmt.Sprintf("selected variant %d from %s (score=%.2f)", chosen.Index, chosen.Engine, chosen.Score)
	return out, nil
}

// ResolveHumanSelection locks a spec from a user-selected variant (§15.4
// HUMAN). Called by the task layer after the user picks a variant.
func (o *Orchestrator) ResolveHumanSelection(brief Brief, variant Variant) Specification {
	return Specification{
		TaskID:          brief.TaskID,
		Mode:            brief.Mode,
		ArtifactHash:    variant.Artifact.Hash,
		Artifact:        variant.Artifact,
		Viewport:        brief.Viewport,
		Theme:           brief.Theme,
		Locale:          brief.Locale,
		Density:         brief.Density,
		Source:          "generated",
		SelectedVariant: variant.Index,
		LockedAt:        o.opts.Now(),
	}
}

// generateAcrossRoutes runs the variant generation across the route chain,
// failing over to the next route on quota/auth/capacity errors (§15.5). Returns
// the produced variants and the count of routes that failed.
func (o *Orchestrator) generateAcrossRoutes(ctx context.Context, brief Brief, routes []ProviderRoute) ([]Variant, int) {
	want := brief.Variants
	if want <= 0 {
		want = 1
	}
	if o.opts.MaxVariants > 0 && want > o.opts.MaxVariants {
		want = o.opts.MaxVariants
	}
	variants := make([]Variant, 0, want)
	failovers := 0

	// Distribute variants across routes: try each route in order; on a route
	// failure (quota/auth/capacity), fail over to the next route for the
	// remaining variants (§15.5).
	remaining := want
	for _, route := range routes {
		if remaining <= 0 {
			break
		}
		adapter, ok := o.opts.Registry.Lookup(route.Engine)
		if !ok {
			failovers++
			continue
		}
		produced, err := o.generateOnRoute(ctx, adapter, brief, route, len(variants)+1, remaining)
		if err != nil {
			failovers++
			continue
		}
		variants = append(variants, produced...)
		remaining = want - len(variants)
	}
	return variants, failovers
}

// generateOnRoute produces up to `count` variants on one route. On the first
// quota/auth/capacity error it aborts the route (returning whatever variants
// succeeded) so the orchestrator can fail over (§15.5).
func (o *Orchestrator) generateOnRoute(ctx context.Context, adapter imageprovider.Adapter, brief Brief, route ProviderRoute, startIdx, count int) ([]Variant, error) {
	out := make([]Variant, 0, count)
	for i := 0; i < count; i++ {
		idx := startIdx + i
		req := brief.ToGenerationRequest(runIDFor(brief, idx), idx)
		req.Account = route.Account
		req.Engine = route.Engine
		req.Model = route.Model
		sink := &imageprovider.SliceSink{}
		res, err := adapter.Generate(ctx, req, sink)
		if err != nil {
			// Record the per-variant failure for the first attempt on this
			// route; then abort the route.
			if len(out) == 0 {
				return nil, err
			}
			return out, err
		}
		v := Variant{
			Index:    idx,
			Engine:   route.Engine,
			Model:    res.Model,
			Artifact: res.Primary(),
			Score:    variantScore(brief, res.Primary()),
		}
		out = append(out, v)
	}
	return out, nil
}

// variantScore is a deterministic quality proxy for automatic selection. The
// orchestrator never calls an LLM (rule §22.6); a real implementation would
// defer to the multimodal visual evaluator. Here we score by artifact size
// (larger ≈ more detailed) normalised against the requested resolution.
func variantScore(brief Brief, art protocol.Artifact) float64 {
	if art.Bytes <= 0 {
		return 0
	}
	want := brief.Viewport.Width * brief.Viewport.Height
	if want <= 0 {
		return 0.5
	}
	got := art.Width * art.Height
	if got <= 0 {
		return 0.5
	}
	ratio := float64(got) / float64(want)
	if ratio > 1 {
		ratio = 1
	}
	if ratio < 0 {
		ratio = 0
	}
	// 0.5 base + up to 0.5 for full resolution match.
	return 0.5 + 0.5*ratio
}

func selectVariant(vs []Variant, mode SelectionMode) Variant {
	if mode == SelectionFirstValid {
		return vs[0]
	}
	// SelectionAutomatic: highest score wins; ties go to the lowest index.
	best := vs[0]
	for _, v := range vs[1:] {
		if v.Score > best.Score {
			best = v
		}
	}
	return best
}

func filterUsable(vs []Variant) []Variant {
	out := make([]Variant, 0, len(vs))
	for _, v := range vs {
		if v.Error == nil && v.Artifact.Hash != "" {
			out = append(out, v)
		}
	}
	return out
}

func (o *Orchestrator) lockFromAttached(brief Brief, source string) Specification {
	return Specification{
		TaskID:       brief.TaskID,
		Mode:         brief.Mode,
		ArtifactHash: brief.Reference.Hash,
		Artifact:     *brief.Reference,
		Viewport:     brief.Viewport,
		Theme:        brief.Theme,
		Locale:       brief.Locale,
		Density:      brief.Density,
		Source:       source,
		LockedAt:     o.opts.Now(),
	}
}

func runIDFor(brief Brief, idx int) string {
	return fmt.Sprintf("design-%s-v%d-%d", brief.TaskID, idx, brief.CreatedAt.UnixNano())
}

// ErrNoRoutes is returned when [Orchestrator.Run] is called with no routes and
// generation is required.
var ErrNoRoutes = errors.New("design: no image-provider routes configured")
