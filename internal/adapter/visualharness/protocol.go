package visualharness

import (
	"context"

	"neuroforge/internal/adapter/imageprovider/protocol"
)

// Platform is the target platform a harness builds and launches (spec §16.2).
type Platform string

const (
	// PlatformGeneric is the command-based harness (§16.2: "Поддержать
	// command-based generic harness"): build/launch/capture via shell commands.
	PlatformGeneric Platform = "generic"
	// PlatformAndroid is the first-class Android harness (§16.2): emulator,
	// AVD, APK install, Activity launch, locale/theme/font-scale, screenshot.
	PlatformAndroid Platform = "android"
	// PlatformWeb is a future web/browser harness (§16.2: "Web harness может
	// быть реализован следующим milestone"). Reserved, not yet implemented.
	PlatformWeb Platform = "web"
)

// IsValid reports whether p is known.
func (p Platform) IsValid() bool {
	switch p {
	case PlatformGeneric, PlatformAndroid, PlatformWeb:
		return true
	}
	return false
}

// Project identifies a project for harness detection (§16.1).
type Project struct {
	ID      string
	Path    string // repository root (worktree)
	Profile string // autonomy profile
}

// DetectionResult reports whether the harness runtime is present (spec §16.1).
type DetectionResult struct {
	// Installed reports whether the required tooling is usable.
	Installed bool
	// Path is the resolved tool/binary location, if any.
	Path string
	// Version is the detected tool version, if reported.
	Version string
	// Detail is a human-readable diagnostic.
	Detail string
}

// BuildRequest drives the Build step (spec §16.1): compile/package the app in
// the isolated worktree. The harness MUST NOT touch the user's primary
// checkout (boundary: §17.1).
type BuildRequest struct {
	Project Project
	// Workdir is the isolated worktree to build in.
	Workdir string
	// Variant selects debug/release/etc. Provider-specific.
	Variant string
	// EnvAllowlist is the positive environment allowlist (§29.2). Merge tokens
	// and credentials must never reach the build (AC-28).
	EnvAllowlist []string
}

// LaunchRequest drives the Launch step (spec §16.1): start the app on a
// device/emulator with fixed settings so screenshots are reproducible.
type LaunchRequest struct {
	Project Project
	// DeviceId selects the emulator/device (Android: AVD id). Empty = default.
	DeviceID string
	// Activity is the entry activity (Android). Empty = launcher activity.
	Activity string
	// Locale is the target locale (e.g. "ru-RU").
	Locale string
	// Theme is the target theme ("dark"/"light"/"").
	Theme string
	// FontScale is the target font scale (1.0 = default).
	FontScale float64
	// Viewport is the fixed resolution (§16.2: "фиксированное разрешение").
	Viewport Viewport
}

// Viewport is the device resolution (§16.2: "фиксированное разрешение").
type Viewport struct {
	Width   int
	Height  int
	Density string // e.g. "xxhdpi" (Android)
}

// NavigationScenario drives Navigate (spec §16.1): navigate to a screen before
// capture. Simple harnesses may no-op.
type NavigationScenario struct {
	// Steps are provider-specific navigation directives (tap coords, deep
	// links, intents). Empty = capture the launched screen as-is.
	Steps []NavigationStep
}

// NavigationStep is one navigation action.
type NavigationStep struct {
	Kind   string // "deeplink", "tap", "input", "back"
	Target string // deeplink URI, label, or locator
}

// CaptureRequest drives the Capture step (spec §16.1): take a screenshot of the
// current screen.
type CaptureRequest struct {
	Project  Project
	DeviceID string
	// Format is the requested screenshot format (PNG recommended for lossless
	// deterministic comparison, §16.3).
	Format protocol.ImageFormat
}

// Screenshot is a captured device screenshot (spec §16.1). Content-addressed
// via the artifact store (§9.5).
type Screenshot struct {
	Artifact protocol.Artifact
	DeviceID string
	Locale   string
	Theme    string
	Viewport Viewport
}

// FailureClassification re-exports the §32 classification (shared with the
// coding-agent and image-provider protocols).
type FailureClassification = protocol.FailureClassification

// FailureClass re-exports the §32 class.
type FailureClass = protocol.FailureClass

// Harness is the visual verification harness protocol (spec §16.1).
//
// Lifecycle: Detect → Build → Launch → Navigate → Capture → Shutdown. The
// generic harness implements Build/Launch/Navigate/Capture as shell commands;
// the Android harness is first-class (§16.2). Web harness is reserved for a
// later milestone (§16.2).
//
// Harnesses run against an isolated build/worktree; they MUST NOT touch the
// user's primary checkout (boundary: §17.1) or perform network delivery
// actions (boundary: §29).
type Harness interface {
	// ID is the stable harness identifier (e.g. "generic", "android", "fake").
	ID() string

	// Platform reports which platform this harness targets (§16.2).
	Platform() Platform

	// Detect reports whether the harness tooling is present/usable (§16.1).
	Detect(ctx context.Context, project Project) DetectionResult

	// Build compiles/packages the app in the isolated worktree (§16.1).
	Build(ctx context.Context, req BuildRequest) error

	// Launch starts the app with fixed settings (§16.1, §16.2: locale, theme,
	// font scale, fixed resolution).
	Launch(ctx context.Context, req LaunchRequest) error

	// Navigate drives the app to the screen under test (§16.1).
	Navigate(ctx context.Context, scenario NavigationScenario) error

	// Capture takes a screenshot (§16.1). Returns the screenshot
	// content-addressed via the artifact store.
	Capture(ctx context.Context, req CaptureRequest) (Screenshot, error)

	// Shutdown stops the app/emulator and releases resources (§16.1).
	Shutdown(ctx context.Context) error

	// ClassifyFailure maps a native harness error onto the §32 taxonomy.
	ClassifyFailure(err error) FailureClassification
}
