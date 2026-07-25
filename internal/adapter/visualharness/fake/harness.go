// Package fake is the §33.3 fake visual harness. It is deterministic and
// performs no real device/emulator calls (rule §36.5, §33). Drives the same
// lifecycle as a real harness so visual-verification tests never need a real
// device.
//
// Supported scenarios (spec §33.3): matching screenshot; mismatch; blank
// screen; clipped UI; startup failure.
package fake

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"neuroforge/internal/adapter/imageprovider/fake"
	"neuroforge/internal/adapter/imageprovider/protocol"
	"neuroforge/internal/adapter/visualharness"
	"neuroforge/internal/artifacts"
)

// Scenario names a deterministic fake harness behaviour (spec §33.3).
type Scenario string

const (
	// ScenarioMatch produces a screenshot matching the reference (for the
	// repair loop happy path).
	ScenarioMatch Scenario = "matching"
	// ScenarioMismatch produces a screenshot that differs from the reference.
	ScenarioMismatch Scenario = "mismatch"
	// ScenarioBlank produces a blank screen (§33.3: "blank screen").
	ScenarioBlank Scenario = "blank-screen"
	// ScenarioClipped produces a clipped UI (§33.3: "clipped UI").
	ScenarioClipped Scenario = "clipped-ui"
	// ScenarioStartupFail fails Launch (§33.3: "startup failure").
	ScenarioStartupFail Scenario = "startup-failure"
)

// AllScenarios is the full, ordered scenario catalogue.
var AllScenarios = []Scenario{
	ScenarioMatch, ScenarioMismatch, ScenarioBlank, ScenarioClipped, ScenarioStartupFail,
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

// Options configures the fake harness.
type Options struct {
	// Scenario selects the behaviour (default [ScenarioMatch]).
	Scenario Scenario
	// Store is the artifact store where screenshots are written.
	Store *artifacts.Store
	// Reference, when set, makes ScenarioMatch produce a screenshot byte-equal
	// to the reference (for the repair loop happy path).
	Reference *protocol.Artifact
	// MismatchBytes, when set, makes ScenarioMismatch produce these bytes.
	MismatchBytes []byte
}

// Harness is the fake visual harness (§33.3).
type Harness struct {
	opts Options

	mu       sync.Mutex
	built    bool
	launched bool
}

// New returns a fake harness.
func New(opts Options) *Harness {
	if opts.Scenario == "" {
		opts.Scenario = ScenarioMatch
	}
	return &Harness{opts: opts}
}

// ID implements visualharness.Harness.
func (h *Harness) ID() string { return "fake" }

// Platform implements visualharness.Harness.
func (h *Harness) Platform() visualharness.Platform { return visualharness.PlatformGeneric }

// Detect implements visualharness.Harness (always installed in CI).
func (h *Harness) Detect(context.Context, visualharness.Project) visualharness.DetectionResult {
	return visualharness.DetectionResult{Installed: true, Path: "fake", Version: "1.0.0-fake", Detail: "fake visual harness (§33.3)"}
}

// Build implements visualharness.Harness.
func (h *Harness) Build(context.Context, visualharness.BuildRequest) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.built = true
	return nil
}

// Launch implements visualharness.Harness.
func (h *Harness) Launch(_ context.Context, _ visualharness.LaunchRequest) error {
	if h.opts.Scenario == ScenarioStartupFail {
		return fmt.Errorf("%w: fake startup failure (§33.3)", visualharness.ErrStartupFailed)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.launched = true
	return nil
}

// Navigate implements visualharness.Harness (no-op).
func (h *Harness) Navigate(context.Context, visualharness.NavigationScenario) error { return nil }

// Capture implements visualharness.Harness.
func (h *Harness) Capture(ctx context.Context, req visualharness.CaptureRequest) (visualharness.Screenshot, error) {
	h.mu.Lock()
	launched := h.launched
	h.mu.Unlock()
	if !launched && h.opts.Scenario != ScenarioStartupFail {
		// Be lenient: if Capture is called without Launch (common in tests),
		// still produce a screenshot.
	}

	var content []byte
	switch h.opts.Scenario {
	case ScenarioMatch:
		if h.opts.Reference != nil && h.opts.Reference.Hash != "" && h.opts.Store != nil {
			ref, err := h.opts.Store.Read(h.opts.Reference.Hash)
			if err == nil {
				content = ref
			}
		}
		if content == nil {
			content = fake.MinimalPNG(32, 32, 0xCC)
		}
	case ScenarioMismatch:
		if len(h.opts.MismatchBytes) > 0 {
			content = h.opts.MismatchBytes
		} else {
			content = fake.MinimalPNG(32, 32, 0x33)
		}
	case ScenarioBlank:
		content = fake.MinimalPNG(32, 32, 0x00) // all black
	case ScenarioClipped:
		// Clipped = a smaller-than-requested image.
		content = fake.MinimalPNG(16, 8, 0x77)
	default:
		return visualharness.Screenshot{}, errors.New("fake harness: unknown scenario")
	}

	format := req.Format
	if format == "" {
		format = protocol.FormatPNG
	}
	art := protocol.Artifact{
		Format: format,
		Bytes:  len(content),
		Source: "captured",
	}
	if h.opts.Store != nil {
		hash, path, err := h.opts.Store.Write(content)
		if err != nil {
			return visualharness.Screenshot{}, fmt.Errorf("fake harness: store: %w", err)
		}
		art.Hash = hash
		art.Path = path
	} else {
		art.Hash = artifacts.Hash(content)
	}
	return visualharness.Screenshot{Artifact: art, DeviceID: req.DeviceID}, nil
}

// Shutdown implements visualharness.Harness.
func (h *Harness) Shutdown(context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.built = false
	h.launched = false
	return nil
}

// ClassifyFailure implements visualharness.Harness.
func (h *Harness) ClassifyFailure(err error) visualharness.FailureClassification {
	return visualharness.DefaultClassify(err)
}
