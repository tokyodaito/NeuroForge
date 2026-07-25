// Package gptimage is the OpenAI GPT Image adapter (spec §14.1, AC-19).
//
// STATUS: implemented for milestone M9.
//
// The adapter speaks the OpenAI Images API (POST /v1/images/generations and
// /v1/images/edits) over net/http. Real image calls are OPT-IN (rule §33: no
// real providers in CI): the adapter is only "installed" when its credential
// resolver returns a key, and the daemon never registers it otherwise.
//
// Credentials are resolved via an injected [CredentialResolver] (never read
// directly from the process environment by the adapter — the daemon's secret
// store owns them, AC-28). The adapter itself holds no credentials at rest.
//
// Model names are NOT hard-coded in the adapter: the catalog (which model id
// maps to which tier) is supplied via [Catalog], so updating a model name is a
// config change, not a code change (rule §36.8, ADR-0006).
package gptimage

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"neuroforge/internal/adapter/imageprovider"
	"neuroforge/internal/adapter/imageprovider/protocol"
	"neuroforge/internal/artifacts"
)

// EngineID is the stable image-provider identifier.
const EngineID = "gpt-image"

// DefaultBaseURL is the OpenAI Images API root. Overridable for tests/proxies.
const DefaultBaseURL = "https://api.openai.com/v1"

// CredentialResolver returns the API key for an account, or false if no key is
// configured (the adapter reports not-installed). The daemon's secret store
// implements this; the adapter never reads process env directly (AC-28).
type CredentialResolver func(account protocol.Account) (apiKey string, ok bool)

// Catalog maps a requested tier to a concrete provider model id (rule §36.8:
// model names are config, not code). Updateable without an adapter release.
type Catalog struct {
	Draft       string // default "gpt-image-1-low"
	Standard    string // default "gpt-image-1"
	HighQuality string // default "gpt-image-1-hi"
}

// ModelFor resolves the model id for a tier, falling back to Standard then to
// the provider default.
func (c Catalog) ModelFor(tier protocol.ImageTier) string {
	switch tier {
	case protocol.TierDraft:
		if c.Draft != "" {
			return c.Draft
		}
	case protocol.TierHighQuality:
		if c.HighQuality != "" {
			return c.HighQuality
		}
	}
	if c.Standard != "" {
		return c.Standard
	}
	return "gpt-image-1"
}

// DefaultCatalog returns the canonical tier→model mapping. This is the ONLY
// place a model name appears in code; it is overridable via [Options.Catalog].
func DefaultCatalog() Catalog {
	return Catalog{
		Draft:       "gpt-image-1-low",
		Standard:    "gpt-image-1",
		HighQuality: "gpt-image-1-hi",
	}
}

// Options configures the adapter.
type Options struct {
	// Credentials resolves the API key for an account. Required.
	Credentials CredentialResolver
	// Catalog overrides the tier→model mapping.
	Catalog Catalog
	// BaseURL overrides the API root (tests/proxies). Defaults to
	// [DefaultBaseURL].
	BaseURL string
	// HTTP is the HTTP client. Defaults to a 5-minute client (image
	// generations are slow).
	HTTP *http.Client
	// Store is the artifact store where generated images are written.
	// Required for Generate to return a populated artifact.
	Store *artifacts.Store
	// Models overrides ListModels output (defaults derived from Catalog).
	Models []protocol.ImageModel
}

// Adapter is the GPT Image provider (spec §14.1, AC-19).
type Adapter struct {
	opts Options
}

// New returns a GPT Image adapter. Returns an error if Credentials is nil
// (misconfiguration).
func New(opts Options) (*Adapter, error) {
	if opts.Credentials == nil {
		return nil, errors.New("gptimage: Credentials resolver is required")
	}
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}
	if opts.Catalog.Standard == "" && opts.Catalog.Draft == "" && opts.Catalog.HighQuality == "" {
		opts.Catalog = DefaultCatalog()
	}
	if opts.HTTP == nil {
		opts.HTTP = &http.Client{Timeout: 5 * time.Minute}
	}
	if len(opts.Models) == 0 {
		opts.Models = defaultModels(opts.Catalog)
	}
	return &Adapter{opts: opts}, nil
}

func defaultModels(c Catalog) []protocol.ImageModel {
	return []protocol.ImageModel{
		{ID: c.Draft, Engine: EngineID, Tier: protocol.TierDraft, SupportsEdit: true, SupportsImageInput: true, MaxResolution: 1024 * 1024},
		{ID: c.Standard, Engine: EngineID, Tier: protocol.TierStandard, SupportsEdit: true, SupportsImageInput: true, MaxResolution: 1536 * 1024},
		{ID: c.HighQuality, Engine: EngineID, Tier: protocol.TierHighQuality, SupportsEdit: true, SupportsImageInput: true, MaxResolution: 2048 * 2048},
	}
}

// ID implements imageprovider.Adapter.
func (a *Adapter) ID() string { return EngineID }

// configured reports whether the account has a resolvable key.
func (a *Adapter) configured(account protocol.Account) bool {
	_, ok := a.opts.Credentials(account)
	return ok
}

// Health implements imageprovider.Adapter.
func (a *Adapter) Health(_ context.Context, account protocol.Account) protocol.HealthResult {
	if !a.configured(account) {
		return protocol.HealthResult{Status: protocol.HealthUnknown, Detail: "no API key configured (real provider opt-in, §33)"}
	}
	return protocol.HealthResult{Status: protocol.HealthOK, Detail: "GPT Image adapter configured"}
}

// ListModels implements imageprovider.Adapter.
func (a *Adapter) ListModels(context.Context, protocol.Account) ([]protocol.ImageModel, error) {
	out := make([]protocol.ImageModel, len(a.opts.Models))
	copy(out, a.opts.Models)
	return out, nil
}

// InspectQuota implements imageprovider.Adapter. GPT Image does not expose a
// precise remaining-quota figure; we report UNKNOWN confidence (rule §36.10).
func (a *Adapter) InspectQuota(_ context.Context, account protocol.Account) protocol.QuotaSnapshot {
	if !a.configured(account) {
		return protocol.QuotaSnapshot{Confidence: protocol.QuotaConfUnknown, State: protocol.QuotaStateUnknown, Reason: "no API key configured"}
	}
	return protocol.QuotaSnapshot{
		Confidence: protocol.QuotaConfUnknown,
		State:      protocol.QuotaStateAvailable,
		Window:     "billing-period",
		Reason:     "GPT Image exposes no per-account quota API; tracked via usage events (§14.4)",
	}
}

// Generate implements imageprovider.Adapter.
func (a *Adapter) Generate(ctx context.Context, req protocol.ImageGenerationRequest, sink imageprovider.EventSink) (protocol.Result, error) {
	if sink == nil {
		sink = imageprovider.SinkFunc(func(context.Context, protocol.Event) error { return nil })
	}
	apiKey, ok := a.opts.Credentials(req.Account)
	if !ok {
		err := imageprovider.ErrAuthFailed
		fc := imageprovider.DefaultClassify(err)
		_ = sink.OnEvent(ctx, protocol.Event{Kind: protocol.EventFailed, RunID: req.RunID, Failure: &fc})
		return protocol.Result{}, fmt.Errorf("gptimage: %w (no api key for account %q)", err, req.Account.Name)
	}
	model := req.Model
	if model == "" {
		model = a.opts.Catalog.ModelFor(req.Tier)
	}
	_ = sink.OnEvent(ctx, protocol.Event{Kind: protocol.EventStarted, RunID: req.RunID, Engine: EngineID, Model: model})

	call := imageCall{
		engine:   EngineID,
		model:    model,
		tier:     req.Tier,
		apiKey:   apiKey,
		store:    a.opts.Store,
		client:   a.opts.HTTP,
		baseURL:  a.opts.BaseURL,
		endpoint: "/images/generations",
	}
	res, err := call.generate(ctx, req)
	if err != nil {
		fc := imageprovider.DefaultClassify(err)
		_ = sink.OnEvent(ctx, protocol.Event{Kind: protocol.EventFailed, RunID: req.RunID, Failure: &fc})
		return protocol.Result{}, err
	}
	res.RunID = req.RunID
	_ = sink.OnEvent(ctx, protocol.Event{Kind: protocol.EventArtifact, RunID: req.RunID, Engine: EngineID, Model: model, Artifact: &res.Artifacts[0]})
	_ = sink.OnEvent(ctx, protocol.Event{Kind: protocol.EventUsage, RunID: req.RunID, Usage: &res.Usage})
	_ = sink.OnEvent(ctx, protocol.Event{Kind: protocol.EventCompleted, RunID: req.RunID, Engine: EngineID, Model: model})
	return res, nil
}

// Edit implements imageprovider.Adapter via /v1/images/edits.
func (a *Adapter) Edit(ctx context.Context, req protocol.ImageEditRequest, sink imageprovider.EventSink) (protocol.Result, error) {
	if sink == nil {
		sink = imageprovider.SinkFunc(func(context.Context, protocol.Event) error { return nil })
	}
	apiKey, ok := a.opts.Credentials(req.Account)
	if !ok {
		err := imageprovider.ErrAuthFailed
		return protocol.Result{}, fmt.Errorf("gptimage: %w (no api key)", err)
	}
	model := req.Model
	if model == "" {
		model = a.opts.Catalog.ModelFor(req.Tier)
	}
	_ = sink.OnEvent(ctx, protocol.Event{Kind: protocol.EventStarted, RunID: req.RunID, Engine: EngineID, Model: model})
	genReq := protocol.ImageGenerationRequest{
		RunID:     req.RunID,
		Account:   req.Account,
		Engine:    EngineID,
		Model:     model,
		Tier:      req.Tier,
		Prompt:    req.Prompt,
		Format:    req.Input.Format,
		Reference: &req.Input,
		ProjectID: req.ProjectID,
		TaskID:    req.TaskID,
	}
	call := imageCall{
		engine:   EngineID,
		model:    model,
		tier:     req.Tier,
		apiKey:   apiKey,
		store:    a.opts.Store,
		client:   a.opts.HTTP,
		baseURL:  a.opts.BaseURL,
		endpoint: "/images/edits",
	}
	res, err := call.generate(ctx, genReq)
	if err != nil {
		fc := imageprovider.DefaultClassify(err)
		_ = sink.OnEvent(ctx, protocol.Event{Kind: protocol.EventFailed, RunID: req.RunID, Failure: &fc})
		return protocol.Result{}, err
	}
	res.RunID = req.RunID
	_ = sink.OnEvent(ctx, protocol.Event{Kind: protocol.EventCompleted, RunID: req.RunID, Engine: EngineID, Model: model})
	return res, nil
}

// AnalyzeFailure implements imageprovider.Adapter.
func (a *Adapter) AnalyzeFailure(err error) protocol.FailureClassification {
	return imageprovider.DefaultClassify(err)
}

// Installed reports whether the adapter has at least one configured account,
// for `forge image-provider doctor`. The daemon consults this to decide whether
// to register the adapter at startup (real image calls are opt-in, §33).
func (a *Adapter) Installed(accounts ...protocol.Account) bool {
	for _, acc := range accounts {
		if a.configured(acc) {
			return true
		}
	}
	return false
}

// Models returns the catalog as a slice (debug/CLI).
func (a *Adapter) Models() []protocol.ImageModel {
	out := make([]protocol.ImageModel, len(a.opts.Models))
	copy(out, a.opts.Models)
	return out
}
