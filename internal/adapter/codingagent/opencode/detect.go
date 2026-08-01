package opencode

import (
	"context"
	"errors"
	"os/exec"
	"strings"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// binaryName is the executable resolved by Detect when Options.Binary is empty.
const binaryName = "opencode"

// Detect reports whether the OpenCode engine runtime is present and usable
// (spec §12.2). Resolution order:
//
//  1. Options.Binary, if set (used verbatim, allowing absolute/Unicode paths).
//  2. exec.LookPath("opencode").
//
// When a binary is found, Detect runs `opencode --version` to capture the engine
// version string (used to gate capabilities). A failed version probe downgrades
// the result but still reports Installed when the binary exists.
func (a *Adapter) Detect(ctx context.Context) protocol.DetectionResult {
	a.mu.Lock()
	d := a.detectLocked(ctx)
	a.detected = d
	a.hasDetect = true
	a.mu.Unlock()
	return d.toResult()
}

// detectLocked is the non-locking core of Detect.
func (a *Adapter) detectLocked(ctx context.Context) detection {
	bin := a.opts.Binary
	resolved := bin
	detail := "OpenCode engine"
	if bin == "" {
		path, err := a.lookPath(binaryName)
		if err != nil {
			return detection{installed: false, detail: "opencode not found on PATH: " + err.Error()}
		}
		resolved = path
		bin = path
	}
	// Probe the version. A missing/failed probe is non-fatal: the binary exists,
	// so we still report Installed, just without a version.
	stdout, _, err := a.runProbe(ctx, bin)
	if err != nil {
		return detection{installed: true, path: resolved, detail: detail + " present; version probe failed: " + err.Error()}
	}
	ver := parseVersionString(stdout)
	return detection{installed: true, path: resolved, version: ver, detail: detail + " " + ver}
}

// defaultLookPath resolves name on PATH exactly like exec.LookPath and
// tolerates spaces and Unicode in directory names. It is the production hook.
func defaultLookPath(file string) (string, error) {
	return exec.LookPath(file)
}

// defaultRunProbe runs `<binary> --version` and returns its stdout (and stderr).
// It is the production version-probe hook.
func defaultRunProbe(ctx context.Context, binary string) (string, string, error) {
	cmd := exec.CommandContext(ctx, binary, "--version")
	out, err := cmd.Output()
	stderr := ""
	if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
		stderr = string(ee.Stderr)
	}
	if err != nil {
		return string(out), stderr, err
	}
	return strings.TrimSpace(string(out)), stderr, nil
}

// rememberDetect returns the cached detection, running Detect once when absent.
// It is used so Capabilities/Version do not re-spawn the version probe.
func (a *Adapter) rememberDetect(ctx context.Context) detection {
	a.mu.Lock()
	if a.hasDetect {
		d := a.detected
		a.mu.Unlock()
		return d
	}
	a.mu.Unlock()
	d := detection{}
	r := a.Detect(ctx)
	d.installed = r.Installed
	d.path = r.Path
	d.version = r.Version
	d.detail = r.Detail
	return d
}
