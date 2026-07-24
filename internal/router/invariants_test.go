package router

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"neuroforge/internal/risk"
)

// TestNoHardcodedModelNames scans the router package sources (and its fakes
// subpackage) for real-world model identifiers. The router core must never
// hard-code model names (rule §36.8, §19.2) — models are provider-supplied via
// the catalog. Fakes use clearly-non-real identifiers (alpha-*, beta-*).
func TestNoHardcodedModelNames(t *testing.T) {
	forbidden := []string{
		"gpt-4", "gpt-3", "gpt4", "gpt3", "gpt-5", "gpt5",
		"claude-3", "claude3", "claude-2", "claude-opus", "claude-sonnet", "claude-haiku",
		"gemini-1", "gemini-pro", "gemini-1.5", "gemini-2",
		"grok-1", "grok-2", "grok1", "grok2",
		"moonshot", "kimi-k", "kimik",
	}
	sources := pkgSources(t, "")
	fakeSources := pkgSources(t, "fakes")
	sources = append(sources, fakeSources...)
	for _, path := range sources {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		low := strings.ToLower(string(data))
		for _, bad := range forbidden {
			if strings.Contains(low, bad) {
				t.Errorf("%s: source contains hard-coded model name %q (rule §36.8)", filepath.Base(path), bad)
			}
		}
	}
}

func pkgSources(t *testing.T, rel string) []string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(thisFile), rel)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		out = append(out, filepath.Join(dir, name))
	}
	return out
}

// TestFallbackChainProperty asserts the structural invariant of orderedFallbackTiers:
// the target tier is first, escalation tiers follow in increasing order, then
// de-escalation tiers in decreasing order. This is the §21.1 contract.
func TestFallbackChainProperty(t *testing.T) {
	for target := TierTiny; target <= TierFrontier; target++ {
		chain := orderedFallbackTiers(target)
		if chain[0] != target {
			t.Errorf("target %s: chain[0] = %s, want target first", target, chain[0])
		}
		seen := map[Tier]bool{target: true}
		// Escalation group must be contiguous, ascending.
		for i := 1; i < len(chain); i++ {
			t2 := chain[i]
			if seen[t2] {
				t.Errorf("target %s: tier %s repeated in fallback chain", target, t2)
			}
			seen[t2] = true
		}
		if len(seen) != int(TierFrontier)+1 {
			t.Errorf("target %s: fallback chain covered %d tiers, want all 5", target, len(seen))
		}
	}
}

// TestRenderExplanation_NonEmpty ensures a decision always renders something
// explainable (§19.6).
func TestRenderExplanation_NonEmpty(t *testing.T) {
	r := newTestRouter()
	ex, err := r.Route(context.Background(), Request{Complexity: C2, Risk: risk.R1})
	if err != nil {
		t.Fatal(err)
	}
	out := RenderExplanation(ex)
	if !strings.Contains(out, "ROUTE DECISION") || !strings.Contains(out, "selected:") {
		t.Errorf("rendered explanation missing required sections:\n%s", out)
	}
}
