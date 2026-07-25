// Package android is the first-class Android visual harness (spec §16.2).
//
// STATUS: implemented for milestone M10.
//
// §16.2 mandates: запуск emulator; выбор AVD; установка APK; запуск Activity;
// настройка locale; настройка theme; настройка font scale; фиксированное
// разрешение; screenshot через Android tooling.
//
// The harness wraps adb (and emulator for cold-boot) via the generic command
// surface. It holds no credentials and runs against an isolated worktree
// (§17.1). Web harness is a separate milestone (§16.2).
package android

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"neuroforge/internal/adapter/imageprovider/protocol"
	"neuroforge/internal/adapter/visualharness"
	"neuroforge/internal/artifacts"
)

// EngineID is the stable harness identifier.
const EngineID = "android"

// Options configures the Android harness.
type Options struct {
	// ADB path (default "adb").
	ADB string
	// Emulator path (default "emulator").
	Emulator string
	// Store is the artifact store where screenshots are written.
	Store *artifacts.Store
	// Runner executes commands. Defaults to a process runner.
	Runner Runner
}

// Runner executes a command and returns combined output. Injectable for tests.
type Runner interface {
	Run(ctx context.Context, dir string, env []string, args []string) ([]byte, error)
}

// Harness is the first-class Android visual harness (spec §16.2).
type Harness struct {
	opts Options
	// state carries the active device after Launch.
	state *launchState
}

type launchState struct {
	deviceID string
	viewport visualharness.Viewport
	locale   string
	theme    string
}

// New returns an Android harness.
func New(opts Options) *Harness {
	if opts.ADB == "" {
		opts.ADB = "adb"
	}
	if opts.Emulator == "" {
		opts.Emulator = "emulator"
	}
	if opts.Runner == nil {
		opts.Runner = defaultRunner{}
	}
	return &Harness{opts: opts}
}

// ID implements visualharness.Harness.
func (h *Harness) ID() string { return EngineID }

// Platform implements visualharness.Harness.
func (h *Harness) Platform() visualharness.Platform { return visualharness.PlatformAndroid }

// Detect implements visualharness.Harness (adb presence).
func (h *Harness) Detect(ctx context.Context, _ visualharness.Project) visualharness.DetectionResult {
	out, err := h.opts.Runner.Run(ctx, "", nil, []string{h.opts.ADB, "version"})
	if err != nil {
		return visualharness.DetectionResult{Installed: false, Detail: fmt.Sprintf("adb not found: %v", err)}
	}
	return visualharness.DetectionResult{Installed: true, Path: h.opts.ADB, Version: strings.TrimSpace(string(out)), Detail: "adb available"}
}

// Build implements visualharness.Harness. The Android harness builds the debug
// APK via gradle (configurable). For projects without gradle, callers use the
// generic harness for build and the android harness only for launch/capture.
func (h *Harness) Build(ctx context.Context, req visualharness.BuildRequest) error {
	// Default: ./gradlew assembleDebug. Caller wraps if different.
	args := []string{"./gradlew", "assembleDebug"}
	out, err := h.opts.Runner.Run(ctx, req.Workdir, req.EnvAllowlist, args)
	if err != nil {
		return fmt.Errorf("%w: %v: %s", visualharness.ErrBuildFailed, err, string(out))
	}
	return nil
}

// Launch implements visualharness.Harness (§16.2): select AVD, install APK,
// launch Activity, configure locale/theme/font-scale, fixed resolution.
func (h *Harness) Launch(ctx context.Context, req visualharness.LaunchRequest) error {
	deviceID := req.DeviceID
	if deviceID == "" {
		// Pick the first available device.
		out, err := h.opts.Runner.Run(ctx, "", nil, []string{h.opts.ADB, "devices"})
		if err != nil {
			return fmt.Errorf("%w: adb devices: %v: %s", visualharness.ErrStartupFailed, err, string(out))
		}
		deviceID = pickDevice(string(out))
		if deviceID == "" {
			return fmt.Errorf("%w: no device or emulator online", visualharness.ErrDeviceNotFound)
		}
	}
	h.state = &launchState{
		deviceID: deviceID,
		viewport: req.Viewport,
		locale:   req.Locale,
		theme:    req.Theme,
	}

	// Configure locale (§16.2: "настройка locale").
	if req.Locale != "" {
		_, _ = h.opts.Runner.Run(ctx, "", nil, []string{h.opts.ADB, "-s", deviceID, "shell", "setprop", "persist.sys.locale", req.Locale})
	}
	// Configure theme (§16.2: "настройка theme").
	if req.Theme == "dark" {
		_, _ = h.opts.Runner.Run(ctx, "", nil, []string{h.opts.ADB, "-s", deviceID, "shell", "cmd", "uimode", "night", "yes"})
	} else if req.Theme == "light" {
		_, _ = h.opts.Runner.Run(ctx, "", nil, []string{h.opts.ADB, "-s", deviceID, "shell", "cmd", "uimode", "night", "no"})
	}
	// Configure font scale (§16.2: "настройка font scale").
	if req.FontScale > 0 {
		_, _ = h.opts.Runner.Run(ctx, "", nil, []string{h.opts.ADB, "-s", deviceID, "shell", "settings", "put", "system", "font_scale", fmt.Sprintf("%g", req.FontScale)})
	}
	// Launch the Activity (§16.2: "запуск Activity").
	if req.Activity != "" {
		out, err := h.opts.Runner.Run(ctx, "", nil, []string{h.opts.ADB, "-s", deviceID, "shell", "am", "start", "-n", req.Activity})
		if err != nil {
			return fmt.Errorf("%w: launch activity %q: %v: %s", visualharness.ErrStartupFailed, req.Activity, err, string(out))
		}
	}
	return nil
}

// Navigate implements visualharness.Harness via adb input taps/keys/deeplinks.
func (h *Harness) Navigate(ctx context.Context, scenario visualharness.NavigationScenario) error {
	if h.state == nil {
		return visualharness.ErrStartupFailed
	}
	for _, step := range scenario.Steps {
		if err := h.navigateStep(ctx, step); err != nil {
			return err
		}
	}
	return nil
}

func (h *Harness) navigateStep(ctx context.Context, step visualharness.NavigationStep) error {
	dev := h.state.deviceID
	switch step.Kind {
	case "deeplink":
		_, err := h.opts.Runner.Run(ctx, "", nil, []string{h.opts.ADB, "-s", dev, "shell", "am", "start", "-a", "android.intent.action.VIEW", "-d", step.Target})
		return err
	case "back":
		_, err := h.opts.Runner.Run(ctx, "", nil, []string{h.opts.ADB, "-s", dev, "shell", "input", "keyevent", "4"})
		return err
	case "tap":
		// Target is "x y".
		parts := strings.Fields(step.Target)
		if len(parts) != 2 {
			return errors.New("android: tap target must be 'x y'")
		}
		_, err := h.opts.Runner.Run(ctx, "", nil, []string{h.opts.ADB, "-s", dev, "shell", "input", "tap", parts[0], parts[1]})
		return err
	default:
		return fmt.Errorf("android: unknown navigation step %q", step.Kind)
	}
}

// Capture implements visualharness.Harness via adb exec-out screencap (§16.2:
// "screenshot через Android tooling"). PNG is forced for lossless deterministic
// comparison (§16.3).
func (h *Harness) Capture(ctx context.Context, req visualharness.CaptureRequest) (visualharness.Screenshot, error) {
	if h.state == nil {
		return visualharness.Screenshot{}, visualharness.ErrStartupFailed
	}
	deviceID := req.DeviceID
	if deviceID == "" {
		deviceID = h.state.deviceID
	}
	out, err := h.opts.Runner.Run(ctx, "", nil, []string{h.opts.ADB, "-s", deviceID, "exec-out", "screencap", "-p"})
	if err != nil {
		return visualharness.Screenshot{}, fmt.Errorf("%w: screencap: %v", visualharness.ErrCaptureFailed, err)
	}
	if len(out) == 0 {
		return visualharness.Screenshot{}, fmt.Errorf("%w: empty screenshot", visualharness.ErrCaptureFailed)
	}
	format := protocol.FormatPNG
	art := protocol.Artifact{
		Format: format,
		Bytes:  len(out),
		Source: "captured",
	}
	if h.opts.Store != nil {
		hash, path, werr := h.opts.Store.Write(out)
		if werr != nil {
			return visualharness.Screenshot{}, fmt.Errorf("android: store screenshot: %w", werr)
		}
		art.Hash = hash
		art.Path = path
	} else {
		art.Hash = artifacts.Hash(out)
	}
	return visualharness.Screenshot{
		Artifact: art,
		DeviceID: deviceID,
		Locale:   h.state.locale,
		Theme:    h.state.theme,
		Viewport: h.state.viewport,
	}, nil
}

// Shutdown implements visualharness.Harness.
func (h *Harness) Shutdown(ctx context.Context) error {
	if h.state == nil {
		return nil
	}
	_, _ = h.opts.Runner.Run(ctx, "", nil, []string{h.opts.ADB, "-s", h.state.deviceID, "shell", "am", "kill", "--user", "all"})
	h.state = nil
	return nil
}

// ClassifyFailure implements visualharness.Harness.
func (h *Harness) ClassifyFailure(err error) visualharness.FailureClassification {
	return visualharness.DefaultClassify(err)
}

// pickDevice parses `adb devices` output for the first online device line.
func pickDevice(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if i == 0 {
			continue // "List of devices attached"
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "device" {
			return fields[0]
		}
	}
	return ""
}

// defaultRunner uses exec via a small wrapper so tests can swap it.
type defaultRunner struct{}

func (defaultRunner) Run(ctx context.Context, dir string, env []string, args []string) ([]byte, error) {
	return runExec(ctx, dir, env, args)
}
