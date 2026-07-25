// Package fake is the §33.2 fake image provider. It is deterministic and
// performs no network or paid AI calls (rule §36.5, §33). The same [Scenario]
// scripts drive orchestration, conformance and CLI tests so that no real image
// API is ever called in CI (rule §33).
//
// Supported scenarios (superset of spec §33.2):
//
//	success, quota, invalid-image, timeout, failover, deterministic-fixture.
package fake

import (
	"context"
	"errors"
	"sync"
	"time"

	"neuroforge/internal/adapter/imageprovider"
	"neuroforge/internal/adapter/imageprovider/protocol"
	"neuroforge/internal/artifacts"
)

// Scenario names a deterministic fake image-provider behaviour (spec §33.2).
type Scenario string

const (
	ScenarioSuccess      Scenario = "success"
	ScenarioQuota        Scenario = "quota"
	ScenarioInvalidImage Scenario = "invalid-image"
	ScenarioTimeout      Scenario = "timeout"
	ScenarioFailover     Scenario = "failover"
	ScenarioFixture      Scenario = "deterministic-fixture"
	// ScenarioAuthFailure exercises the AUTH_REQUIRED path (used by conformance
	// and the failover orchestrator); not in the §33.2 list but kept for
	// parity with the coding-agent fake.
	ScenarioAuthFailure Scenario = "auth-failure"
)

// AllScenarios is the full, ordered scenario catalogue (spec §33.2 + auth).
var AllScenarios = []Scenario{
	ScenarioSuccess,
	ScenarioQuota,
	ScenarioInvalidImage,
	ScenarioTimeout,
	ScenarioFailover,
	ScenarioFixture,
	ScenarioAuthFailure,
}

// IsValidScenario reports whether s is known.
func IsValidScenario(s Scenario) bool {
	for _, x := range AllScenarios {
		if x == s {
			return true
		}
	}
	return false
}

// defaultModels are the fake provider's catalogue. Synthetic names so the core
// never depends on real model identifiers (rule §36.8). Each tier has a model.
var defaultModels = []protocol.ImageModel{
	{ID: "fake/draft", Engine: "fake-image", Tier: protocol.TierDraft, SupportsEdit: true, SupportsImageInput: true, MaxResolution: 1024 * 1024},
	{ID: "fake/standard", Engine: "fake-image", Tier: protocol.TierStandard, SupportsEdit: true, SupportsImageInput: true, MaxResolution: 1920 * 1080},
	{ID: "fake/hq", Engine: "fake-image", Tier: protocol.TierHighQuality, SupportsEdit: true, SupportsImageInput: true, MaxResolution: 2048 * 2048},
}

// Adapter is the in-process fake image provider (spec §33.2). It is
// deterministic and performs no network or AI calls (rule §36.5).
type Adapter struct {
	opts AdapterOptions

	mu  sync.Mutex
	gen int // generation counter for deterministic fixture content
}

// AdapterOptions configures a fake [Adapter].
type AdapterOptions struct {
	// Scenario selects the behaviour (default [ScenarioSuccess]).
	Scenario Scenario
	// Engine overrides the reported engine id (default "fake-image").
	Engine string
	// Models overrides the reported model catalogue.
	Models []protocol.ImageModel
	// Store is the artifact store where generated images are written. If nil,
	// generated artifacts have empty Path/Hash (in-memory only) — useful for
	// pure unit tests that do not care about bytes.
	Store *artifacts.Store
	// Installed, when false, makes Health report down.
	Installed bool
}

// New returns a fake adapter with the given options applied.
func New(opts AdapterOptions) *Adapter {
	if opts.Engine == "" {
		opts.Engine = "fake-image"
	}
	if opts.Scenario == "" {
		opts.Scenario = ScenarioSuccess
	}
	if len(opts.Models) == 0 {
		opts.Models = defaultModels
	}
	return &Adapter{opts: opts}
}

// ID implements imageprovider.Adapter.
func (a *Adapter) ID() string { return a.opts.Engine }

// Health implements imageprovider.Adapter.
func (a *Adapter) Health(context.Context, protocol.Account) protocol.HealthResult {
	if !a.opts.Installed {
		return protocol.HealthResult{Status: protocol.HealthDown, Detail: "fake image provider disabled"}
	}
	return protocol.HealthResult{Status: protocol.HealthOK, Detail: "fake image provider always healthy"}
}

// ListModels implements imageprovider.Adapter.
func (a *Adapter) ListModels(context.Context, protocol.Account) ([]protocol.ImageModel, error) {
	out := make([]protocol.ImageModel, len(a.opts.Models))
	copy(out, a.opts.Models)
	return out, nil
}

// InspectQuota implements imageprovider.Adapter.
func (a *Adapter) InspectQuota(context.Context, protocol.Account) protocol.QuotaSnapshot {
	return protocol.QuotaSnapshot{
		Confidence: protocol.QuotaConfProviderReported,
		State:      protocol.QuotaStateAvailable,
		Window:     "unlimited",
		Reason:     "fake image provider has no real quota",
	}
}

// Generate implements imageprovider.Adapter.
func (a *Adapter) Generate(ctx context.Context, req protocol.ImageGenerationRequest, sink imageprovider.EventSink) (protocol.Result, error) {
	if sink == nil {
		sink = imageprovider.SinkFunc(func(context.Context, protocol.Event) error { return nil })
	}
	_ = sink.OnEvent(ctx, protocol.Event{Kind: protocol.EventStarted, RunID: req.RunID, Engine: req.Engine, Model: req.Model})

	switch a.opts.Scenario {
	case ScenarioQuota:
		err := imageprovider.ErrQuotaExhausted
		fc := imageprovider.DefaultClassify(err)
		_ = sink.OnEvent(ctx, protocol.Event{Kind: protocol.EventFailed, RunID: req.RunID, Failure: &fc})
		return protocol.Result{}, err
	case ScenarioAuthFailure:
		// Kept for parity with coding agent scenarios; not in §33.2 list but
		// exercised by conformance.
		err := imageprovider.ErrAuthFailed
		fc := imageprovider.DefaultClassify(err)
		_ = sink.OnEvent(ctx, protocol.Event{Kind: protocol.EventFailed, RunID: req.RunID, Failure: &fc})
		return protocol.Result{}, err
	case ScenarioInvalidImage:
		// "Generate" a zero-byte artifact: deterministic image validation then
		// rejects it.
		_ = sink.OnEvent(ctx, protocol.Event{Kind: protocol.EventProgress, RunID: req.RunID, Progress: 1.0})
		err := imageprovider.ErrInvalidImage
		fc := imageprovider.DefaultClassify(err)
		_ = sink.OnEvent(ctx, protocol.Event{Kind: protocol.EventFailed, RunID: req.RunID, Failure: &fc})
		return protocol.Result{}, err
	case ScenarioTimeout:
		// Block until the context expires, then return timeout.
		<-ctx.Done()
		err := imageprovider.ErrTimeout
		fc := imageprovider.DefaultClassify(err)
		_ = sink.OnEvent(ctx, protocol.Event{Kind: protocol.EventFailed, RunID: req.RunID, Failure: &fc})
		return protocol.Result{}, err
	case ScenarioFailover:
		// Failover scenario: emit a transient capacity failure that the
		// orchestrator should treat as "switch provider".
		err := errors.New("imageprovider: provider capacity — failover required")
		fc := imageprovider.DefaultClassify(err)
		_ = sink.OnEvent(ctx, protocol.Event{Kind: protocol.EventFailed, RunID: req.RunID, Failure: &fc})
		return protocol.Result{}, err
	case ScenarioSuccess, ScenarioFixture, "":
		// Fall through to generation.
	default:
		err := errors.New("imageprovider: unknown scenario: " + string(a.opts.Scenario))
		fc := imageprovider.DefaultClassify(err)
		_ = sink.OnEvent(ctx, protocol.Event{Kind: protocol.EventFailed, RunID: req.RunID, Failure: &fc})
		return protocol.Result{}, err
	}

	// Success path: synthesize a deterministic PNG-like fixture. The fixture
	// depends only on (prompt, theme, size, tier, variantIndex) — NOT on call
	// order — so identical requests produce identical bytes (§33.2
	// "deterministic fixture generation"). A monotonic counter is intentionally
	// avoided: the deterministic-fixture scenario is the CI/test path and must
	// be reproducible across runs.
	gen := variantSeed(req)

	art, err := a.fixture(req, gen)
	if err != nil {
		fc := imageprovider.DefaultClassify(err)
		_ = sink.OnEvent(ctx, protocol.Event{Kind: protocol.EventFailed, RunID: req.RunID, Failure: &fc})
		return protocol.Result{}, err
	}

	_ = sink.OnEvent(ctx, protocol.Event{Kind: protocol.EventProgress, RunID: req.RunID, Progress: 0.5})
	_ = sink.OnEvent(ctx, protocol.Event{Kind: protocol.EventArtifact, RunID: req.RunID, Engine: req.Engine, Model: req.Model, Artifact: &art})
	usage := protocol.Usage{
		ImageGens:  1,
		CostUSD:    0.0, // fake provider has no real cost (rule §36.5)
		Confidence: protocol.QuotaConfExact,
		Included:   true,
	}
	_ = sink.OnEvent(ctx, protocol.Event{Kind: protocol.EventUsage, RunID: req.RunID, Usage: &usage})
	_ = sink.OnEvent(ctx, protocol.Event{Kind: protocol.EventCompleted, RunID: req.RunID, Engine: req.Engine, Model: req.Model})

	return protocol.Result{
		RunID:        req.RunID,
		Engine:       req.Engine,
		Model:        req.Model,
		Tier:         req.Tier,
		Artifacts:    []protocol.Artifact{art},
		Usage:        usage,
		VariantIndex: req.VariantIndex,
	}, nil
}

// Edit implements imageprovider.Adapter. The fake reuses the generate path with
// the input artifact as a reference.
func (a *Adapter) Edit(ctx context.Context, req protocol.ImageEditRequest, sink imageprovider.EventSink) (protocol.Result, error) {
	genReq := protocol.ImageGenerationRequest{
		RunID:     req.RunID,
		Account:   req.Account,
		Engine:    req.Engine,
		Model:     req.Model,
		Tier:      req.Tier,
		Prompt:    req.Prompt,
		Format:    req.Input.Format,
		Reference: &req.Input,
		ProjectID: req.ProjectID,
		TaskID:    req.TaskID,
	}
	if genReq.Format == "" {
		genReq.Format = protocol.FormatPNG
	}
	return a.Generate(ctx, genReq, sink)
}

// AnalyzeFailure implements imageprovider.Adapter.
func (a *Adapter) AnalyzeFailure(err error) protocol.FailureClassification {
	return imageprovider.DefaultClassify(err)
}

// fixture synthesizes a deterministic PNG fixture for the request. The bytes
// are a valid minimal PNG whose pixel content encodes (prompt hash, generation
// counter, dimensions) so the same request produces the same image (§33.2
// "deterministic fixture generation") and different prompts differ.
func (a *Adapter) fixture(req protocol.ImageGenerationRequest, gen int) (protocol.Artifact, error) {
	w, h := req.Size.Width, req.Size.Height
	if w <= 0 {
		w = 64
	}
	if h <= 0 {
		h = 64
	}
	format := req.Format
	if format == "" {
		format = protocol.FormatPNG
	}
	content := FixturePNG(req.Prompt, req.Theme, w, h, gen)

	art := protocol.Artifact{
		Format: format,
		Width:  w,
		Height: h,
		Bytes:  len(content),
		Source: "generated",
	}
	if a.opts.Store != nil {
		hash, path, err := a.opts.Store.Write(content)
		if err != nil {
			return protocol.Artifact{}, err
		}
		art.Hash = hash
		art.Path = path
	} else {
		art.Hash = artifacts.Hash(content)
	}
	return art, nil
}

// FixturePNG returns a deterministic minimal valid PNG whose identity depends
// on (prompt, theme, w, h, seed). The same inputs always produce the same bytes
// (§33.2 deterministic fixture generation). The PNG is a tiny valid image; it
// is NOT meant to be visually meaningful, only byte-stable and decodable.
func FixturePNG(prompt, theme string, w, h, seed int) []byte {
	return MinimalPNG(w, h, promptSeed(prompt, theme, seed))
}

// variantSeed derives a deterministic seed for a generation request so the
// same request always yields the same fixture bytes (§33.2). Distinct prompts,
// themes, tiers or variant indices differ.
func variantSeed(req protocol.ImageGenerationRequest) int {
	seed := req.VariantIndex
	for i := 0; i < len(req.Prompt); i++ {
		seed += int(req.Prompt[i])
	}
	switch req.Tier {
	case protocol.TierDraft:
		seed += 1
	case protocol.TierHighQuality:
		seed += 3
	default:
		seed += 2
	}
	if seed == 0 {
		seed = 1
	}
	return seed
}

// promptSeed derives a deterministic byte from the prompt/theme/seed so the
// fixture differs across distinct prompts.
func promptSeed(prompt, theme string, seed int) byte {
	var acc byte = 0
	for i := 0; i < len(prompt); i++ {
		acc += prompt[i]
	}
	for i := 0; i < len(theme); i++ {
		acc ^= theme[i]
	}
	acc += byte(seed)
	// Theme "dark" biases low; "light" biases high — so deterministic checks
	// can distinguish fixtures by mean luminance.
	if theme == "dark" {
		acc = acc / 3
	} else if theme == "light" {
		acc = 128 + acc/3
	}
	if acc == 0 {
		acc = 1 // PNG all-zero filter bytes are valid but make checks trivial
	}
	return acc
}

// hangHint keeps the timeout scenario from spinning in a hot loop before ctx
// expires (yield occasionally).
var hangHint = 10 * time.Millisecond
