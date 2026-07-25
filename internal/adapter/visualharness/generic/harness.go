// Package generic is the command-based generic visual harness (spec §16.2).
//
// STATUS: implemented for milestone M10.
//
// It drives Build/Launch/Navigate/Capture via configurable shell commands
// (declared in project config: visual/harness.yaml). This is the mandatory
// first implementation per §16.2; platform-specific harnesses (Android) extend
// the surface. The harness itself holds no credentials and runs in the worktree
// only.
package generic

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	"neuroforge/internal/adapter/imageprovider/protocol"
	"neuroforge/internal/adapter/visualharness"
	"neuroforge/internal/artifacts"
)

// EngineID is the stable harness identifier.
const EngineID = "generic"

// Commands is the per-project command configuration (project config:
// visual/harness.yaml). Empty commands are no-ops (the corresponding lifecycle
// step succeeds without doing anything) so a project can wire only the steps
// it needs.
type Commands struct {
	// Detect runs to test whether the toolchain is usable (e.g. "node --version").
	Detect []string
	// Build compiles/packages the app.
	Build []string
	// Launch starts the app.
	Launch []string
	// Capture takes a screenshot and writes it to stdout (binary) OR to the
	// path in the CAPTURE_OUT env var.
	Capture []string
	// Shutdown stops the app.
	Shutdown []string
}

// Options configures the generic harness.
type Options struct {
	// Commands is the per-project command set.
	Commands Commands
	// Store is the artifact store where screenshots are written.
	Store *artifacts.Store
	// Runner executes commands. Defaults to [exec.Command] context-aware.
	Runner Runner
}

// Runner executes a command in a directory with an env allowlist and returns
// its combined output. Injectable for tests.
type Runner interface {
	Run(ctx context.Context, dir string, envAllowlist []string, args []string) ([]byte, error)
}

// defaultRunner uses os/exec.
type defaultRunner struct{}

func (defaultRunner) Run(ctx context.Context, dir string, envAllowlist []string, args []string) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("generic: empty command")
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = envAllowlist
	return cmd.CombinedOutput()
}

// Harness is the command-based generic visual harness (spec §16.2).
type Harness struct {
	opts Options
}

// New returns a generic harness.
func New(opts Options) *Harness {
	if opts.Runner == nil {
		opts.Runner = defaultRunner{}
	}
	return &Harness{opts: opts}
}

// ID implements visualharness.Harness.
func (h *Harness) ID() string { return EngineID }

// Platform implements visualharness.Harness.
func (h *Harness) Platform() visualharness.Platform { return visualharness.PlatformGeneric }

// Detect implements visualharness.Harness.
func (h *Harness) Detect(ctx context.Context, _ visualharness.Project) visualharness.DetectionResult {
	if len(h.opts.Commands.Detect) == 0 {
		return visualharness.DetectionResult{Installed: true, Detail: "no detect command; presumed installed"}
	}
	out, err := h.opts.Runner.Run(ctx, "", nil, h.opts.Commands.Detect)
	if err != nil {
		return visualharness.DetectionResult{Installed: false, Detail: fmt.Sprintf("detect failed: %v: %s", err, string(out))}
	}
	return visualharness.DetectionResult{Installed: true, Version: string(out), Detail: "detect ok"}
}

// Build implements visualharness.Harness.
func (h *Harness) Build(ctx context.Context, req visualharness.BuildRequest) error {
	if len(h.opts.Commands.Build) == 0 {
		return nil
	}
	out, err := h.opts.Runner.Run(ctx, req.Workdir, req.EnvAllowlist, h.opts.Commands.Build)
	if err != nil {
		return fmt.Errorf("%w: %v: %s", visualharness.ErrBuildFailed, err, string(out))
	}
	return nil
}

// Launch implements visualharness.Harness.
func (h *Harness) Launch(ctx context.Context, _ visualharness.LaunchRequest) error {
	if len(h.opts.Commands.Launch) == 0 {
		return nil
	}
	out, err := h.opts.Runner.Run(ctx, "", nil, h.opts.Commands.Launch)
	if err != nil {
		return fmt.Errorf("%w: %v: %s", visualharness.ErrStartupFailed, err, string(out))
	}
	return nil
}

// Navigate implements visualharness.Harness. The generic harness has no
// navigation primitive; callers bake navigation into their launch/capture
// commands. It is a no-op.
func (h *Harness) Navigate(context.Context, visualharness.NavigationScenario) error {
	return nil
}

// Capture implements visualharness.Harness.
func (h *Harness) Capture(ctx context.Context, req visualharness.CaptureRequest) (visualharness.Screenshot, error) {
	if len(h.opts.Commands.Capture) == 0 {
		return visualharness.Screenshot{}, visualharness.ErrCaptureFailed
	}
	out, err := h.opts.Runner.Run(ctx, "", nil, h.opts.Commands.Capture)
	if err != nil {
		return visualharness.Screenshot{}, fmt.Errorf("%w: %v: %s", visualharness.ErrCaptureFailed, err, string(out))
	}
	format := req.Format
	if format == "" {
		format = protocol.FormatPNG
	}
	art, serr := storeOrHash(h.opts.Store, out, format)
	if serr != nil {
		return visualharness.Screenshot{}, serr
	}
	return visualharness.Screenshot{
		Artifact: art,
		DeviceID: req.DeviceID,
		Viewport: visualharness.Viewport{},
	}, nil
}

// Shutdown implements visualharness.Harness.
func (h *Harness) Shutdown(ctx context.Context) error {
	if len(h.opts.Commands.Shutdown) == 0 {
		return nil
	}
	out, err := h.opts.Runner.Run(ctx, "", nil, h.opts.Commands.Shutdown)
	if err != nil {
		return fmt.Errorf("generic: shutdown: %v: %s", err, string(out))
	}
	return nil
}

// ClassifyFailure implements visualharness.Harness.
func (h *Harness) ClassifyFailure(err error) visualharness.FailureClassification {
	return visualharness.DefaultClassify(err)
}

// storeOrHash writes bytes to the store (if configured) or returns an in-memory
// hash.
func storeOrHash(store *artifacts.Store, content []byte, format protocol.ImageFormat) (protocol.Artifact, error) {
	art := protocol.Artifact{
		Format: format,
		Bytes:  len(content),
		Source: "captured",
	}
	if store != nil {
		hash, path, err := store.Write(content)
		if err != nil {
			return protocol.Artifact{}, fmt.Errorf("generic: store screenshot: %w", err)
		}
		art.Hash = hash
		art.Path = path
	} else {
		art.Hash = artifacts.Hash(content)
	}
	return art, nil
}
