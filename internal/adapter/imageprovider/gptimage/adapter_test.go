package gptimage_test

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"neuroforge/internal/adapter/imageprovider"
	"neuroforge/internal/adapter/imageprovider/gptimage"
	"neuroforge/internal/adapter/imageprovider/protocol"
	"neuroforge/internal/artifacts"
)

func mustStore(t *testing.T) *artifacts.Store {
	t.Helper()
	s, err := artifacts.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// cred is a resolver that always returns the given key.
func cred(key string) gptimage.CredentialResolver {
	return func(protocol.Account) (string, bool) { return key, key != "" }
}

// TestGenerate_Success verifies a happy-path OpenAI-shaped response.
func TestGenerate_Success(t *testing.T) {
	t.Parallel()
	// 2x2 red PNG.
	png := []byte{
		0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A,
	}
	b64 := base64.StdEncoding.EncodeToString(png)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/images/generations" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("auth = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model"`) {
			t.Errorf("body missing model: %s", body)
		}
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + b64 + `"}]}`))
	}))
	defer srv.Close()

	store := mustStore(t)
	a, err := gptimage.New(gptimage.Options{
		Credentials: cred("sk-test"),
		BaseURL:     srv.URL,
		Store:       store,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Generate(context.Background(), protocol.ImageGenerationRequest{
		RunID: "r1", Engine: gptimage.EngineID, Model: "gpt-image-1",
		Tier: protocol.TierStandard, Prompt: "login screen",
		Size: protocol.ImageSize{Width: 16, Height: 16}, Format: protocol.FormatPNG,
	}, &imageprovider.SliceSink{})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(res.Artifacts) != 1 || res.Artifacts[0].Bytes == 0 {
		t.Fatalf("result = %+v", res)
	}
	if res.Artifacts[0].Hash == "" {
		t.Error("artifact hash empty")
	}
	if res.Usage.ImageGens != 1 {
		t.Errorf("usage.ImageGens = %d", res.Usage.ImageGens)
	}
}

// TestGenerate_AuthFailure verifies a 401 maps to ErrAuthFailed.
func TestGenerate_AuthFailure(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer srv.Close()
	a, _ := gptimage.New(gptimage.Options{
		Credentials: cred("bad"), BaseURL: srv.URL, Store: mustStore(t),
	})
	_, err := a.Generate(context.Background(), protocol.ImageGenerationRequest{
		RunID: "r", Engine: gptimage.EngineID, Model: "gpt-image-1",
	}, &imageprovider.SliceSink{})
	if !errors.Is(err, imageprovider.ErrAuthFailed) {
		t.Errorf("err = %v, want ErrAuthFailed", err)
	}
}

// TestGenerate_RateLimit verifies 429 maps to ErrRateLimited.
func TestGenerate_RateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
	}))
	defer srv.Close()
	a, _ := gptimage.New(gptimage.Options{Credentials: cred("k"), BaseURL: srv.URL, Store: mustStore(t)})
	_, err := a.Generate(context.Background(), protocol.ImageGenerationRequest{
		RunID: "r", Engine: gptimage.EngineID,
	}, &imageprovider.SliceSink{})
	if !errors.Is(err, imageprovider.ErrRateLimited) {
		t.Errorf("err = %v, want ErrRateLimited", err)
	}
}

// TestGenerate_QuotaExhausted verifies a quota message maps to ErrQuotaExhausted
// (critical for image quota failover §15.5).
func TestGenerate_QuotaExhausted(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"error":{"message":"insufficient_quota"}}`))
	}))
	defer srv.Close()
	a, _ := gptimage.New(gptimage.Options{Credentials: cred("k"), BaseURL: srv.URL, Store: mustStore(t)})
	_, err := a.Generate(context.Background(), protocol.ImageGenerationRequest{
		RunID: "r", Engine: gptimage.EngineID,
	}, &imageprovider.SliceSink{})
	if !errors.Is(err, imageprovider.ErrQuotaExhausted) {
		t.Errorf("err = %v, want ErrQuotaExhausted", err)
	}
}

// TestUnconfiguredReportsAuth verifies an account without a key fails fast.
func TestUnconfiguredReportsAuth(t *testing.T) {
	t.Parallel()
	a, _ := gptimage.New(gptimage.Options{Credentials: cred(""), Store: mustStore(t)})
	_, err := a.Generate(context.Background(), protocol.ImageGenerationRequest{
		RunID: "r", Engine: gptimage.EngineID,
	}, &imageprovider.SliceSink{})
	if !errors.Is(err, imageprovider.ErrAuthFailed) {
		t.Errorf("err = %v, want ErrAuthFailed (no key)", err)
	}
}

// TestCatalogTierResolution verifies the tier→model mapping is swappable (rule
// §36.8).
func TestCatalogTierResolution(t *testing.T) {
	t.Parallel()
	c := gptimage.Catalog{Standard: "custom-std", Draft: "custom-draft", HighQuality: "custom-hq"}
	if c.ModelFor(protocol.TierStandard) != "custom-std" {
		t.Error("standard tier not mapped")
	}
	if c.ModelFor(protocol.TierDraft) != "custom-draft" {
		t.Error("draft tier not mapped")
	}
	if c.ModelFor(protocol.TierHighQuality) != "custom-hq" {
		t.Error("hq tier not mapped")
	}
}
