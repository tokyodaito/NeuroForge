package kimi

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

// probe is the cached result of detecting the engine: the resolved binary path,
// its version string/profile, and the flags its `--help` advertises. It is
// computed once per adapter (the installed engine does not change mid-process)
// and reused by Detect/Version/Capabilities/Health/ListModels.
type probe struct {
	path        string
	installed   bool
	versionStr  string
	detail      string
	profile     versionProfile
	flags       probedFlags
	flagsProbed bool
}

// probedFlags records which CLI flags the installed `kimi --help` advertises.
// When the help text could not be parsed the values stay false and the adapter
// falls back to version-gated defaults.
type probedFlags struct {
	streamJSON bool
	model      bool
	continued  bool
	maxTurns   bool
}

// detectBinary resolves the engine executable. BinaryOverride wins; otherwise
// exec.LookPath searches PATH (honouring PATHEXT on Windows, so .exe/.cmd/.bat
// and npm shims are found) and tolerates spaces and Unicode in the path.
func detectBinary(opts *Options) (path string, found bool) {
	if opts.BinaryOverride != "" {
		// An explicit override is trusted verbatim (it may be a test harness
		// path). We still sanity-check it via LookPath so a typo is reported.
		if p, err := exec.LookPath(opts.BinaryOverride); err == nil {
			return p, true
		}
		return opts.BinaryOverride, true
	}
	p, err := exec.LookPath(opts.binaryName())
	if err != nil {
		return "", false
	}
	return p, true
}

// runProbe detects the binary and runs `kimi --version` (and best-effort
// `kimi --help`) to populate the profile. It never panics; a missing or
// misbehaving binary yields installed=false with a diagnostic detail.
func runProbe(ctx context.Context, opts *Options) probe {
	pr := probe{}
	path, found := detectBinary(opts)
	if !found {
		pr.detail = "kimi: executable not found on PATH (looked for " + opts.binaryName() + ")"
		return pr
	}
	pr.path = path
	pr.installed = true

	vstr, verr := captureVersion(ctx, path, opts.ExtraEnv)
	if verr != nil {
		// The binary exists but `--version` failed. Treat as installed but
		// degraded (we cannot confirm version/capabilities).
		pr.detail = "kimi: found at " + path + " but --version failed: " + verr.Error()
		pr.profile = newVersionProfile(parsedVersion{}, opts.ForceStreaming)
		return pr
	}
	pr.versionStr = vstr
	pv := parseVersion(vstr)
	pr.profile = newVersionProfile(pv, opts.ForceStreaming)
	pr.detail = "kimi " + vstr + " (" + path + ")"

	// Best-effort flag probe. Failures here are non-fatal: the adapter falls
	// back to version-gated flags.
	pf, pok := probeFlags(ctx, path, opts.ExtraEnv)
	if pok {
		pr.flags = pf
		pr.flagsProbed = true
	}
	return pr
}

// captureVersion runs `<binary> --version` and returns the trimmed first line.
// extra is included so the probe runs the engine in the same environment the
// adapter uses for a run (the engine may need its config/home to start); it is
// adapter-controlled and never carries secrets.
func captureVersion(ctx context.Context, binary string, extra []string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(cctx, binary, "--version")
	cmd.Stdout = &out
	cmd.Env = append(baseEnvKeys(), extra...)
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}

// probeFlags runs `<binary> --help` and records which flags the help text
// mentions. Returns ok=false if help could not be obtained.
func probeFlags(ctx context.Context, binary string, extra []string) (probedFlags, bool) {
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(cctx, binary, "--help")
	cmd.Stdout = &out
	cmd.Env = append(baseEnvKeys(), extra...)
	if err := cmd.Run(); err != nil {
		// Some engines exit non-zero from --help while still printing usage.
		// Accept the output as long as we got something to parse.
		if out.Len() == 0 {
			return probedFlags{}, false
		}
	}
	help := out.String()
	return probedFlags{
		streamJSON: containsFlag(help, "output", "stream-json"),
		model:      containsFlag(help, "model"),
		continued:  containsFlag(help, "continue", "resume"),
		maxTurns:   containsFlag(help, "max-turns", "max_turns", "turns"),
	}, true
}

// containsFlag reports whether help mentions any of the given tokens in a
// flag-like position (after - or --, or as a bare word).
func containsFlag(help string, tokens ...string) bool {
	for _, t := range tokens {
		if strings.Contains(help, "--"+t) || strings.Contains(help, "-"+t) || strings.Contains(help, t) {
			return true
		}
	}
	return false
}

// errNotDetected is returned by Start/Resume when the engine is not installed.
var errNotDetected = errors.New("kimi: engine not detected")
