// Package httpx contains shared HTTP machinery for real image-provider
// adapters (GPT Image, Nano Banana). It is intentionally tiny: each provider
// still owns its request/response schema (they differ), but the transport
// concerns (auth headers, status→failure-class mapping, artifact storage,
// JSON round-trip) are shared so error handling is consistent.
//
// The package depends only on the stdlib plus the artifact store; it never
// holds credentials at rest and never reads process env directly (AC-28).
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"neuroforge/internal/adapter/imageprovider"
	"neuroforge/internal/adapter/imageprovider/protocol"
	"neuroforge/internal/artifacts"
)

// AuthKind selects how credentials are conveyed.
type AuthKind int

const (
	// AuthBearer puts the key in an "Authorization: Bearer <key>" header
	// (OpenAI).
	AuthBearer AuthKind = iota
	// AuthHeader puts the key in a custom header (Google: x-goog-api-key).
	AuthHeader
)

// Client is a thin HTTP client wrapping image-provider concerns.
type Client struct {
	HTTP       *http.Client
	BaseURL    string
	AuthKind   AuthKind
	AuthHeader string // header name when AuthKind==AuthHeader (ignored for Bearer)
	UserAgent  string
}

// defaultHTTP returns a sane default client (image generation is slow).
func defaultHTTP() *http.Client { return &http.Client{Timeout: 5 * time.Minute} }

// Do sends a JSON request and returns the raw JSON response body. statusOk is
// the set of acceptable status codes (typically just 200). Non-JSON bodies are
// returned verbatim in body for diagnostics. The error is classified onto the
// §32 taxonomy via [imageprovider.DefaultClassify] so callers can pass it
// straight to AnalyzeFailure.
func (c *Client) Do(ctx context.Context, method, path string, reqBody any, apiKey string, statusOk map[int]bool) (status int, body []byte, err error) {
	if c.HTTP == nil {
		c.HTTP = defaultHTTP()
	}
	url := strings.TrimRight(c.BaseURL, "/") + path

	var reader io.Reader
	if reqBody != nil {
		buf, mErr := json.Marshal(reqBody)
		if mErr != nil {
			return 0, nil, fmt.Errorf("httpx: marshal request: %w", mErr)
		}
		reader = bytes.NewReader(buf)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return 0, nil, fmt.Errorf("httpx: build request: %w", err)
	}
	if reqBody != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	switch c.AuthKind {
	case AuthBearer:
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	case AuthHeader:
		hdr := c.AuthHeader
		if hdr == "" {
			hdr = "x-api-key"
		}
		httpReq.Header.Set(hdr, apiKey)
	}
	if c.UserAgent != "" {
		httpReq.Header.Set("User-Agent", c.UserAgent)
	}

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	status = resp.StatusCode
	if readErr != nil {
		return status, body, fmt.Errorf("httpx: read response: %w", readErr)
	}
	if statusOk != nil && !statusOk[status] {
		return status, body, classifyStatus(status, body)
	}
	return status, body, nil
}

// maxBodyBytes caps response reads (image responses can be large base64 blobs;
// 32 MiB is generous for a single image).
const maxBodyBytes = 32 << 20

// classifyStatus maps an HTTP status code onto the §32 taxonomy.
func classifyStatus(status int, body []byte) error {
	msg := string(body)
	msg = strings.TrimSpace(msg)
	switch {
	case status == 401 || status == 403:
		return fmt.Errorf("%w: http %d: %s", imageprovider.ErrAuthFailed, status, msg)
	case status == 429:
		return fmt.Errorf("%w: http %d: %s", imageprovider.ErrRateLimited, status, msg)
	case status == 404:
		// Model-not-found is the most common 404 for image APIs.
		return fmt.Errorf("%w: http 404: %s", imageprovider.ErrModelNotAvailable, msg)
	case status == 402 || isQuotaMessage(msg):
		return fmt.Errorf("%w: http %d: %s", imageprovider.ErrQuotaExhausted, status, msg)
	case status >= 500:
		return fmt.Errorf("httpx: server error http %d: %s", status, msg)
	default:
		return fmt.Errorf("httpx: unexpected http %d: %s", status, msg)
	}
}

// isQuotaMessage detects provider-side quota/grant-exhausted bodies.
func isQuotaMessage(msg string) bool {
	low := strings.ToLower(msg)
	return strings.Contains(low, "quota") || strings.Contains(low, "exhausted") ||
		strings.Contains(low, "insufficient_quota") || strings.Contains(low, "billing")
}

// StoreImage writes raw image bytes to the artifact store and returns a typed
// artifact. width/height/format are taken from the request when known (the
// provider response usually omits dimensions).
func StoreImage(store *artifacts.Store, content []byte, format protocol.ImageFormat, w, h int) (protocol.Artifact, error) {
	art := protocol.Artifact{
		Format: format,
		Width:  w,
		Height: h,
		Bytes:  len(content),
		Source: "generated",
	}
	if store != nil {
		hash, path, err := store.Write(content)
		if err != nil {
			return protocol.Artifact{}, fmt.Errorf("httpx: store image: %w", err)
		}
		art.Hash = hash
		art.Path = path
	} else {
		// Best-effort in-memory hash (no store configured).
		art.Hash = artifacts.Hash(content)
	}
	return art, nil
}

// DecodeJSON is a convenience that unmarshals body into out.
func DecodeJSON(body []byte, out any) error {
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("httpx: decode response: %w", err)
	}
	return nil
}

// ParseSize parses a "WxH" or "W*H" string into an ImageSize. Returns the zero
// value if s is empty.
func ParseSize(s string) (protocol.ImageSize, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return protocol.ImageSize{}, nil
	}
	sep := 'x'
	if i := strings.IndexByte(s, '*'); i >= 0 {
		sep = '*'
	}
	parts := strings.SplitN(s, string(sep), 2)
	if len(parts) != 2 {
		return protocol.ImageSize{}, fmt.Errorf("httpx: bad size %q", s)
	}
	w, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return protocol.ImageSize{}, fmt.Errorf("httpx: bad width %q", s)
	}
	h, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return protocol.ImageSize{}, fmt.Errorf("httpx: bad height %q", s)
	}
	return protocol.ImageSize{Width: w, Height: h}, nil
}
