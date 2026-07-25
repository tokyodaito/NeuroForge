package nanobanana_test

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
	"neuroforge/internal/adapter/imageprovider/nanobanana"
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

func cred(key string) nanobanana.CredentialResolver {
	return func(protocol.Account) (string, bool) { return key, key != "" }
}

func TestGenerate_Success(t *testing.T) {
	t.Parallel()
	png := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	b64 := base64.StdEncoding.EncodeToString(png)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/models/gemini-2.5-flash-image:generateContent") {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("x-goog-api-key"); got != "gem-key" {
			t.Errorf("api key header = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"responseModalities"`) {
			t.Errorf("body missing responseModalities: %s", body)
		}
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"` + b64 + `"}}]}}]}`))
	}))
	defer srv.Close()

	a, err := nanobanana.New(nanobanana.Options{Credentials: cred("gem-key"), BaseURL: srv.URL, Store: mustStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Generate(context.Background(), protocol.ImageGenerationRequest{
		RunID: "r1", Engine: nanobanana.EngineID, Model: "gemini-2.5-flash-image",
		Tier: protocol.TierStandard, Prompt: "a login screen",
		Size: protocol.ImageSize{Width: 16, Height: 16}, Format: protocol.FormatPNG,
	}, &imageprovider.SliceSink{})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(res.Artifacts) != 1 || res.Artifacts[0].Bytes == 0 {
		t.Fatalf("result = %+v", res)
	}
}

func TestGenerate_AuthFailure(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"API key not valid"}}`))
	}))
	defer srv.Close()
	a, _ := nanobanana.New(nanobanana.Options{Credentials: cred("bad"), BaseURL: srv.URL, Store: mustStore(t)})
	_, err := a.Generate(context.Background(), protocol.ImageGenerationRequest{
		RunID: "r", Engine: nanobanana.EngineID, Model: "gemini-2.5-flash-image",
	}, &imageprovider.SliceSink{})
	if !errors.Is(err, imageprovider.ErrAuthFailed) {
		t.Errorf("err = %v, want ErrAuthFailed", err)
	}
}

func TestGenerate_RateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limit"}}`))
	}))
	defer srv.Close()
	a, _ := nanobanana.New(nanobanana.Options{Credentials: cred("k"), BaseURL: srv.URL, Store: mustStore(t)})
	_, err := a.Generate(context.Background(), protocol.ImageGenerationRequest{
		RunID: "r", Engine: nanobanana.EngineID,
	}, &imageprovider.SliceSink{})
	if !errors.Is(err, imageprovider.ErrRateLimited) {
		t.Errorf("err = %v, want ErrRateLimited", err)
	}
}

func TestGenerate_QuotaExhausted(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Gemini returns 429 with quota status for quota; also handle 402.
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"error":{"message":"quota exceeded"}}`))
	}))
	defer srv.Close()
	a, _ := nanobanana.New(nanobanana.Options{Credentials: cred("k"), BaseURL: srv.URL, Store: mustStore(t)})
	_, err := a.Generate(context.Background(), protocol.ImageGenerationRequest{
		RunID: "r", Engine: nanobanana.EngineID,
	}, &imageprovider.SliceSink{})
	if !errors.Is(err, imageprovider.ErrQuotaExhausted) {
		t.Errorf("err = %v, want ErrQuotaExhausted", err)
	}
}

func TestCatalogTierResolution(t *testing.T) {
	t.Parallel()
	c := nanobanana.Catalog{Standard: "nano-x", Draft: "nano-draft", HighQuality: "nano-hq"}
	if c.ModelFor(protocol.TierStandard) != "nano-x" {
		t.Error("standard tier not mapped")
	}
	if c.ModelFor(protocol.TierHighQuality) != "nano-hq" {
		t.Error("hq tier not mapped")
	}
}
