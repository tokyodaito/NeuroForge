package gptimage

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"

	"neuroforge/internal/adapter/imageprovider/httpx"
	"neuroforge/internal/adapter/imageprovider/protocol"
	"neuroforge/internal/artifacts"
)

// imageCall holds the per-request shared state for Generate/Edit.
type imageCall struct {
	engine   string
	model    string
	tier     protocol.ImageTier
	apiKey   string
	store    *artifacts.Store
	client   *http.Client
	baseURL  string
	endpoint string // /images/generations or /images/edits
}

// generateRequest is the OpenAI Images API request body.
type generateRequest struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	N              int    `json:"n,omitempty"`
	Size           string `json:"size,omitempty"`
	Quality        string `json:"quality,omitempty"`
	ResponseFormat string `json:"response_format,omitempty"`
}

// generateResponse is the OpenAI Images API response body.
type generateResponse struct {
	Data []struct {
		B64JSON string `json:"b64_json,omitempty"`
		URL     string `json:"url,omitempty"`
	} `json:"data"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
}

// tierToQuality maps §14.3 tiers onto OpenAI quality values.
func tierToQuality(t protocol.ImageTier) string {
	switch t {
	case protocol.TierDraft:
		return "low"
	case protocol.TierHighQuality:
		return "high"
	}
	return "auto"
}

// generate executes the HTTP call and parses the response into a Result.
func (c imageCall) generate(ctx context.Context, req protocol.ImageGenerationRequest) (protocol.Result, error) {
	body := generateRequest{
		Model:          c.model,
		Prompt:         req.Prompt,
		N:              1,
		Size:           sizeString(req.Size),
		Quality:        tierToQuality(c.tier),
		ResponseFormat: "b64_json",
	}
	hc := &httpx.Client{
		HTTP:     c.client,
		BaseURL:  c.baseURL,
		AuthKind: httpx.AuthBearer,
	}
	status, respBody, err := hc.Do(ctx, http.MethodPost, c.endpoint, body, c.apiKey, map[int]bool{200: true})
	if err != nil {
		return protocol.Result{}, err
	}

	var parsed generateResponse
	if err := httpx.DecodeJSON(respBody, &parsed); err != nil {
		return protocol.Result{}, fmt.Errorf("gptimage: http %d: %w", status, err)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return protocol.Result{}, fmt.Errorf("gptimage: provider error: %s", parsed.Error.Message)
	}
	if len(parsed.Data) == 0 {
		return protocol.Result{}, fmt.Errorf("gptimage: empty response (no data)")
	}
	item := parsed.Data[0]
	if item.B64JSON == "" && item.URL == "" {
		return protocol.Result{}, fmt.Errorf("gptimage: response item has no image")
	}

	format := req.Format
	if format == "" {
		format = protocol.FormatPNG
	}

	var imgBytes []byte
	if item.B64JSON != "" {
		dec, derr := base64.StdEncoding.DecodeString(item.B64JSON)
		if derr != nil {
			return protocol.Result{}, fmt.Errorf("gptimage: decode b64_json: %w", derr)
		}
		imgBytes = dec
	} else {
		// URL-only responses require a second fetch; out of scope for the
		// first cut (response_format=b64_json avoids this).
		return protocol.Result{}, fmt.Errorf("gptimage: url-only responses unsupported (set response_format=b64_json)")
	}

	art, err := httpx.StoreImage(c.store, imgBytes, format, req.Size.Width, req.Size.Height)
	if err != nil {
		return protocol.Result{}, err
	}
	return protocol.Result{
		Engine:       c.engine,
		Model:        c.model,
		Tier:         c.tier,
		Artifacts:    []protocol.Artifact{art},
		VariantIndex: req.VariantIndex,
		Usage: protocol.Usage{
			ImageGens:  1,
			CostUSD:    tierCost(c.tier),
			Confidence: protocol.QuotaConfEstimated,
		},
	}, nil
}

// sizeString formats an ImageSize into the OpenAI "WxH" convention.
func sizeString(s protocol.ImageSize) string {
	if s.Width <= 0 || s.Height <= 0 {
		return ""
	}
	return fmt.Sprintf("%dx%d", s.Width, s.Height)
}

// tierCost is a coarse cost estimate per tier (USD per generation). Real cost
// comes from the provider invoice; this is an estimate for budget soft-signals
// (rule §36.10: confidence is ESTIMATED, not EXACT).
func tierCost(t protocol.ImageTier) float64 {
	switch t {
	case protocol.TierDraft:
		return 0.011
	case protocol.TierHighQuality:
		return 0.080
	}
	return 0.040
}
