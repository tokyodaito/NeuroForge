// Package nanobanana is the Google Nano Banana (Gemini Image) adapter (spec
// §14.1, AC-19).
//
// STATUS: implemented for milestone M9.
//
// The adapter speaks the Gemini generateContent API with
// responseModalities=["IMAGE"] over net/http. Real image calls are OPT-IN (rule
// §33: no real providers in CI): the adapter is only "installed" when its
// credential resolver returns a key.
//
// Credentials are resolved via an injected [CredentialResolver] (never read
// directly from process env — AC-28). Model names are NOT hard-coded in the
// adapter: the tier→model mapping comes from [Catalog] (rule §36.8).
package nanobanana

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"neuroforge/internal/adapter/imageprovider"
	"neuroforge/internal/adapter/imageprovider/httpx"
	"neuroforge/internal/adapter/imageprovider/protocol"
	"neuroforge/internal/artifacts"
)

// EngineID is the stable image-provider identifier.
const EngineID = "nano-banana"

// DefaultBaseURL is the Gemini API root.
const DefaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"

// CredentialResolver returns the API key for an account, or false if none.
type CredentialResolver func(account protocol.Account) (apiKey string, ok bool)

// Catalog maps tiers to Gemini model ids (rule §36.8).
type Catalog struct {
	Draft       string // default "gemini-2.5-flash-image-lite"
	Standard    string // default "gemini-2.5-flash-image"
	HighQuality string // default "gemini-2.5-pro-image"
}

// ModelFor resolves the model id for a tier.
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
	return "gemini-2.5-flash-image"
}

// DefaultCatalog returns the canonical tier→model mapping.
func DefaultCatalog() Catalog {
	return Catalog{
		Draft:       "gemini-2.5-flash-image-lite",
		Standard:    "gemini-2.5-flash-image",
		HighQuality: "gemini-2.5-pro-image",
	}
}

// Options configures the adapter.
type Options struct {
	Credentials CredentialResolver
	Catalog     Catalog
	BaseURL     string
	HTTP        *http.Client
	Store       *artifacts.Store
	Models      []protocol.ImageModel
}

// Adapter is the Nano Banana provider (spec §14.1, AC-19).
type Adapter struct {
	opts Options
}

// New returns a Nano Banana adapter.
func New(opts Options) (*Adapter, error) {
	if opts.Credentials == nil {
		return nil, errors.New("nanobanana: Credentials resolver is required")
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
		{ID: c.Standard, Engine: EngineID, Tier: protocol.TierStandard, SupportsEdit: true, SupportsImageInput: true, MaxResolution: 1920 * 1080},
		{ID: c.HighQuality, Engine: EngineID, Tier: protocol.TierHighQuality, SupportsEdit: true, SupportsImageInput: true, MaxResolution: 2048 * 2048},
	}
}

// ID implements imageprovider.Adapter.
func (a *Adapter) ID() string { return EngineID }

func (a *Adapter) configured(account protocol.Account) bool {
	_, ok := a.opts.Credentials(account)
	return ok
}

// Health implements imageprovider.Adapter.
func (a *Adapter) Health(_ context.Context, account protocol.Account) protocol.HealthResult {
	if !a.configured(account) {
		return protocol.HealthResult{Status: protocol.HealthUnknown, Detail: "no API key configured (real provider opt-in, §33)"}
	}
	return protocol.HealthResult{Status: protocol.HealthOK, Detail: "Nano Banana adapter configured"}
}

// ListModels implements imageprovider.Adapter.
func (a *Adapter) ListModels(context.Context, protocol.Account) ([]protocol.ImageModel, error) {
	out := make([]protocol.ImageModel, len(a.opts.Models))
	copy(out, a.opts.Models)
	return out, nil
}

// InspectQuota implements imageprovider.Adapter. Gemini exposes per-project
// quota via a separate API; we report UNKNOWN confidence (rule §36.10).
func (a *Adapter) InspectQuota(_ context.Context, account protocol.Account) protocol.QuotaSnapshot {
	if !a.configured(account) {
		return protocol.QuotaSnapshot{Confidence: protocol.QuotaConfUnknown, State: protocol.QuotaStateUnknown, Reason: "no API key configured"}
	}
	return protocol.QuotaSnapshot{
		Confidence: protocol.QuotaConfUnknown,
		State:      protocol.QuotaStateAvailable,
		Window:     "per-minute",
		Reason:     "Gemini per-project quota tracked via usage events (§14.4)",
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
		return protocol.Result{}, fmt.Errorf("nanobanana: %w (no api key for account %q)", err, req.Account.Name)
	}
	model := req.Model
	if model == "" {
		model = a.opts.Catalog.ModelFor(req.Tier)
	}
	_ = sink.OnEvent(ctx, protocol.Event{Kind: protocol.EventStarted, RunID: req.RunID, Engine: EngineID, Model: model})

	res, err := a.callGenerate(ctx, req, model, apiKey)
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

// Edit implements imageprovider.Adapter. Gemini accepts an inline image part in
// the request, so Edit reuses Generate with the input image attached.
func (a *Adapter) Edit(ctx context.Context, req protocol.ImageEditRequest, sink imageprovider.EventSink) (protocol.Result, error) {
	if sink == nil {
		sink = imageprovider.SinkFunc(func(context.Context, protocol.Event) error { return nil })
	}
	apiKey, ok := a.opts.Credentials(req.Account)
	if !ok {
		return protocol.Result{}, fmt.Errorf("nanobanana: %w (no api key)", imageprovider.ErrAuthFailed)
	}
	model := req.Model
	if model == "" {
		model = a.opts.Catalog.ModelFor(req.Tier)
	}
	_ = sink.OnEvent(ctx, protocol.Event{Kind: protocol.EventStarted, RunID: req.RunID, Engine: EngineID, Model: model})
	genReq := protocol.ImageGenerationRequest{
		RunID: req.RunID, Account: req.Account, Engine: EngineID, Model: model, Tier: req.Tier,
		Prompt: req.Prompt, Format: req.Input.Format, Reference: &req.Input,
		ProjectID: req.ProjectID, TaskID: req.TaskID,
	}
	res, err := a.callGenerate(ctx, genReq, model, apiKey)
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

// Installed reports whether at least one account is configured.
func (a *Adapter) Installed(accounts ...protocol.Account) bool {
	for _, acc := range accounts {
		if a.configured(acc) {
			return true
		}
	}
	return false
}

// Models returns the catalog slice (debug/CLI).
func (a *Adapter) Models() []protocol.ImageModel {
	out := make([]protocol.ImageModel, len(a.opts.Models))
	copy(out, a.opts.Models)
	return out
}

// ---- Gemini request/response ----

type geminiPart struct {
	Text       string        `json:"text,omitempty"`
	InlineData *geminiInline `json:"inlineData,omitempty"`
}

type geminiInline struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"` // base64
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiRequest struct {
	Contents         []geminiContent `json:"contents"`
	GenerationConfig geminiGenConfig `json:"generationConfig,omitempty"`
}

type geminiGenConfig struct {
	ResponseModalities []string `json:"responseModalities"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text       string `json:"text,omitempty"`
				InlineData *struct {
					MimeType string `json:"mimeType"`
					Data     string `json:"data"`
				} `json:"inlineData,omitempty"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error,omitempty"`
}

// callGenerate performs the Gemini generateContent call.
func (a *Adapter) callGenerate(ctx context.Context, req protocol.ImageGenerationRequest, model, apiKey string) (protocol.Result, error) {
	parts := []geminiPart{{Text: req.Prompt}}
	if req.Reference != nil && req.Reference.Hash != "" {
		mime := "image/png"
		if req.Reference.Format != "" {
			mime = string(req.Reference.Format)
		}
		var data string
		if req.Reference.Path != "" && a.opts.Store != nil {
			content, err := a.opts.Store.Read(req.Reference.Hash)
			if err == nil {
				data = base64.StdEncoding.EncodeToString(content)
			}
		}
		if data != "" {
			parts = append(parts, geminiPart{InlineData: &geminiInline{MimeType: mime, Data: data}})
		}
	}
	body := geminiRequest{
		Contents: []geminiContent{{Role: "user", Parts: parts}},
		GenerationConfig: geminiGenConfig{
			ResponseModalities: []string{"IMAGE"},
		},
	}
	path := fmt.Sprintf("/models/%s:generateContent", model)
	hc := &httpx.Client{
		HTTP:       a.opts.HTTP,
		BaseURL:    a.opts.BaseURL,
		AuthKind:   httpx.AuthHeader,
		AuthHeader: "x-goog-api-key",
	}
	_, respBody, err := hc.Do(ctx, http.MethodPost, path, body, apiKey, map[int]bool{200: true})
	if err != nil {
		return protocol.Result{}, err
	}

	var parsed geminiResponse
	if err := httpx.DecodeJSON(respBody, &parsed); err != nil {
		return protocol.Result{}, fmt.Errorf("nanobanana: decode: %w", err)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return protocol.Result{}, fmt.Errorf("nanobanana: provider error: %s", parsed.Error.Message)
	}
	for _, cand := range parsed.Candidates {
		for _, p := range cand.Content.Parts {
			if p.InlineData != nil && p.InlineData.Data != "" {
				imgBytes, derr := base64.StdEncoding.DecodeString(p.InlineData.Data)
				if derr != nil {
					return protocol.Result{}, fmt.Errorf("nanobanana: decode image: %w", derr)
				}
				format := protocol.ImageFormat(strings.TrimPrefix(p.InlineData.MimeType, "image/"))
				if format == "" {
					format = "png"
				}
				format = protocol.ImageFormat("image/" + format)
				if !format.IsValid() {
					format = protocol.FormatPNG
				}
				art, serr := httpx.StoreImage(a.opts.Store, imgBytes, format, req.Size.Width, req.Size.Height)
				if serr != nil {
					return protocol.Result{}, serr
				}
				return protocol.Result{
					Engine:       EngineID,
					Model:        model,
					Tier:         req.Tier,
					Artifacts:    []protocol.Artifact{art},
					VariantIndex: req.VariantIndex,
					Usage: protocol.Usage{
						ImageGens:  1,
						CostUSD:    tierCost(req.Tier),
						Confidence: protocol.QuotaConfEstimated,
					},
				}, nil
			}
		}
	}
	return protocol.Result{}, fmt.Errorf("nanobanana: response had no image data")
}

func tierCost(t protocol.ImageTier) float64 {
	switch t {
	case protocol.TierDraft:
		return 0.020
	case protocol.TierHighQuality:
		return 0.090
	}
	return 0.040
}
