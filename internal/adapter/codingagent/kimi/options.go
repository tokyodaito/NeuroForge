package kimi

import (
	"neuroforge/internal/adapter/codingagent/protocol"
)

// Options configures a [*Adapter]. All fields are optional; a zero-value
// [Options] yields an adapter that detects `kimi` on PATH with sensible
// defaults.
type Options struct {
	// BinaryName is the executable name looked up on PATH (default "kimi").
	// Ignored when BinaryOverride is set.
	BinaryName string

	// BinaryOverride, when non-empty, is used verbatim as the executable path
	// for detection and runs instead of looking BinaryName up on PATH. Intended
	// for wiring/tests that must pin a specific binary. It is never interpreted
	// by a shell (no /bin/sh); it is passed straight to exec.
	BinaryOverride string

	// ArtifactsDir is where malformed agent output lines are persisted for
	// forensics (spec: malformed event is saved to artifacts). Defaults to
	// os.TempDir() when empty.
	ArtifactsDir string

	// ExtraEnv are additional KEY=VAL environment variables the adapter
	// unconditionally sets for the agent process — e.g. an isolated home
	// computed by the daemon, or test-harness controls. The adapter still
	// allowlists the rest of the environment per §29.2; this field does not
	// bypass that. Never use it to forward secrets.
	ExtraEnv []string

	// ExtraArgs are appended verbatim to the headless argv after the standard
	// flags. Intended for test harnesses that need to exercise defensive
	// handling of unknown flags; production wiring leaves it empty.
	ExtraArgs []string

	// HomeEnvName is the env var used to relocate Kimi's config/home to an
	// isolated per-run directory (default "KIMI_HOME"). Set to the variable the
	// installed Kimi version actually honours if it differs.
	HomeEnvName string

	// AdapterVersion overrides the reported adapter version (default
	// adapterVersion).
	AdapterVersion string

	// Models overrides the default model catalogue reported by ListModels.
	// When nil, a minimal opaque catalogue is reported; the core never
	// hard-codes model names (rule §36.8).
	Models []protocol.ModelDescriptor

	// Capabilities overrides the version-derived capability profile. When nil,
	// capabilities are derived from the detected engine version (see
	// versionProfile).
	Capabilities *protocol.AgentCapabilities

	// ForceStreaming, when true, reports StreamingEvents=true and emits
	// --output stream-json even if the detected version predates streaming
	// support. Useful when the daemon knows the engine streams regardless of
	// its version string. Defaults to false (version-gated).
	ForceStreaming bool

	// DisableIsolation, when true, skips relocating Kimi's home/config to a
	// per-run directory. NOT recommended: by default the adapter isolates Kimi
	// state so a run never mutates the user's global profile. Provided only for
	// diagnostic harnesses that must reuse an existing profile.
	DisableIsolation bool
}

func (o *Options) binaryName() string {
	if o.BinaryName != "" {
		return o.BinaryName
	}
	return defaultBinaryName
}

func (o *Options) homeEnvName() string {
	if o.HomeEnvName != "" {
		return o.HomeEnvName
	}
	return defaultHomeEnvName
}

func (o *Options) adapterVersion() string {
	if o.AdapterVersion != "" {
		return o.AdapterVersion
	}
	return adapterVersion
}
