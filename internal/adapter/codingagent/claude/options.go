package claude

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// EngineID is the stable NeuroForge engine identifier for Claude Code (spec
// §12.1: an engine is not a model). It is independent of any model alias.
const EngineID = "claude"

// DefaultAdapterVersion is the version string reported by [Adapter.Version] for
// this adapter implementation. It is distinct from the engine (CLI) version and
// from protocol.ProtocolVersion.
const DefaultAdapterVersion = "claude-v1"

// defaultBinaryName is the executable resolved by [Detect] when Options.BinaryPath
// is empty.
const defaultBinaryName = "claude"

// PromptStrategy controls how the compiled task prompt is delivered to the
// headless `claude -p` process.
type PromptStrategy string

const (
	// PromptStdin (default) pipes the prompt text through the child's stdin.
	// This keeps argv short and stable regardless of prompt size, which matters
	// on Windows where CreateProcess caps the command line near 32 000 chars.
	// It mirrors the documented `cat file | claude -p` pattern.
	PromptStdin PromptStrategy = "stdin"
	// PromptPositional passes the prompt text as the positional argument to
	// `-p`. Suitable only for short prompts.
	PromptPositional PromptStrategy = "positional"
)

// Options configures a [Adapter]. The zero value is not usable; construct an
// adapter with [New]. All probe/spawn seams default to real OS operations and
// exist so the adapter can be exercised deterministically without a real
// (paid) Claude Code install (rule §36.5).
type Options struct {
	// BinaryPath overrides executable resolution. When empty, [Detect] resolves
	// "claude" via PATH/PATHEXT (see [lookPathClaude]).
	BinaryPath string

	// ArtifactsDir is where malformed agent output lines are persisted for
	// forensics (spec: malformed event is saved + classified). Defaults to
	// os.TempDir() when empty.
	ArtifactsDir string

	// Bare toggles Claude Code's `--bare` headless mode (skip auto-discovery of
	// hooks/skills/plugins/MCP/CLAUDE.md). Default false → project-aware run.
	// The task requires both bare and project-aware modes be supported.
	Bare bool

	// PermissionMode maps onto Claude Code's `--permission-mode`. Must be one of
	// default|acceptEdits|plan|auto|dontAsk|manual. The value
	// "bypassPermissions" is rejected (security: never enable a dangerous
	// permission bypass). Empty defaults to "default".
	PermissionMode string

	// Effort maps onto `--effort` (low|medium|high|xhigh|max). Optional; only
	// emitted when non-empty. Support is version-gated (see capabilities.go).
	Effort string

	// AdditionalDirs maps onto one or more `--add-dir` flags.
	AdditionalDirs []string

	// ExtraArgs are appended verbatim to the run argv after the built-in flags.
	// Validated by [validateExtraArgs]: values that would weaken security
	// (bypass flags, model overrides owned by the router, output-format/max-turns
	// owned by the adapter) are rejected so callers cannot accidentally subvert
	// the adapter contract.
	ExtraArgs []string

	// Models is the provider-supplied model catalogue returned by ListModels.
	// Defaults to the documented Claude Code `--model` aliases when empty (see
	// defaultModels). Never hard-code versioned model names in the core (rule
	// §36.8); this field is the provider-supplied source.
	Models []protocol.ModelDescriptor

	// AdapterVersion overrides the adapter version reported by [Adapter.Version]
	// (defaults to [DefaultAdapterVersion]).
	AdapterVersion string

	// PromptStrategy selects how the prompt reaches the agent (default stdin).
	PromptStrategy PromptStrategy

	// ProbeTimeout bounds the detect/version/health probes. Zero defaults to
	// 5s. These probes must never hang the daemon.
	ProbeTimeout time.Duration

	// ---- test/determinism seams (defaults perform real OS operations) ----

	// LookPath resolves the executable for detection. Defaults to a PATHEXT-aware
	// resolver built on [os/exec.LookPath] (see detect.go).
	LookPath lookPathFunc

	// Probe runs a short-lived probe command (version/auth) and returns its
	// captured stdout, stderr and exit code. Defaults to a real
	// [os/exec.Cmd] implementation.
	Probe probeFunc

	// Spawn builds the long-lived agent process group for a run. Defaults to
	// the proctree-backed spawner (production). Tests inject a recorded
	// byte-stream spawner (rule §36.5: no real paid calls).
	Spawn spawner

	// Now returns the current time. Defaults to [time.Now]. Overriding it makes
	// timestamp-dependent logic deterministic in tests.
	Now func() time.Time
}

// lookPathFunc resolves an executable name to an absolute path.
type lookPathFunc func(file string) (string, error)

// probeFunc runs a probe command and returns captured stdout, stderr and exit
// code. err is non-nil only for launch/infrastructure failures (not non-zero
// exit), mirroring [defaultProbe].
type probeFunc func(ctx context.Context, name string, args []string, env []string) (stdout, stderr []byte, exitCode int, err error)

// New constructs a Claude Code adapter from opts, applying defaults and
// validating security-sensitive fields. It does not register the adapter.
func New(opts Options) (*Adapter, error) {
	if opts.PermissionMode == "" {
		opts.PermissionMode = "default"
	}
	if err := validatePermissionMode(opts.PermissionMode); err != nil {
		return nil, err
	}
	if err := validateExtraArgs(opts.ExtraArgs); err != nil {
		return nil, err
	}
	if err := validateEffort(opts.Effort); err != nil {
		return nil, err
	}
	if opts.AdapterVersion == "" {
		opts.AdapterVersion = DefaultAdapterVersion
	}
	if opts.PromptStrategy == "" {
		opts.PromptStrategy = PromptStdin
	}
	if opts.ProbeTimeout <= 0 {
		opts.ProbeTimeout = 5 * time.Second
	}
	if opts.Models == nil {
		opts.Models = defaultModels()
	}
	if opts.LookPath == nil {
		opts.LookPath = defaultLookPath
	}
	if opts.Probe == nil {
		opts.Probe = defaultProbe
	}
	if opts.Spawn == nil {
		opts.Spawn = proctreeSpawner
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	// runs is the shared active-run registry. It is allocated once here (never
	// lazy-initialised in startRun) so every subsequent read/write under a.mu is
	// race-free even when many Start calls run concurrently. The map itself is
	// shared bookkeeping; per-run state lives in dedicated runState values and
	// independent runs still execute fully in parallel.
	a := &Adapter{opts: opts, runs: map[string]*runState{}}
	return a, nil
}

// forbiddenArgTokens are substrings that must never appear in ExtraArgs: they
// either bypass Claude Code's permission system or override adapter-owned flags.
var forbiddenArgTokens = []string{
	"--dangerously-skip-permissions",
	"--allow-dangerously-skip-permissions",
	"bypassPermissions",
	"--permission-mode",          // owned/validated by Options.PermissionMode
	"--output-format",            // owned by the adapter (stream-json)
	"--input-format",             // owned by the adapter
	"--verbose",                  // owned by the adapter
	"--include-partial-messages", // owned by the adapter
	"--max-turns",                // mapped from AgentRunRequest.TurnLimit
	"--model",                    // mapped from AgentRunRequest.Model (no hard-coded names)
	"--resume",                   // owned by Resume
	"--continue",                 // owned by Resume
	"-c",                         // shorthand for --continue (ambiguous as a standalone token)
	"-r",                         // shorthand for --resume
	"--session-id",               // owned by the adapter
	"--bare",                     // owned by Options.Bare
}

func validateExtraArgs(args []string) error {
	for _, a := range args {
		low := strings.ToLower(a)
		for _, bad := range forbiddenArgTokens {
			badLow := strings.ToLower(bad)
			if low == badLow || strings.HasPrefix(low, badLow+"=") {
				return fmt.Errorf("claude: ExtraArgs may not include %q (adapter-owned or security-sensitive)", a)
			}
		}
	}
	return nil
}

func validatePermissionMode(mode string) error {
	switch mode {
	case "default", "acceptEdits", "plan", "auto", "dontAsk", "manual":
		return nil
	case "bypassPermissions", "":
		if mode == "" {
			return nil
		}
		return errors.New("claude: PermissionMode bypassPermissions is forbidden (security: never enable a dangerous permission bypass)")
	}
	return fmt.Errorf("claude: unsupported PermissionMode %q", mode)
}

func validateEffort(e string) error {
	if e == "" {
		return nil
	}
	switch e {
	case "low", "medium", "high", "xhigh", "max":
		return nil
	}
	return fmt.Errorf("claude: unsupported Effort %q", e)
}

// artifactsDir resolves the malformed-output artifacts directory (spec: malformed
// event is saved to artifacts), defaulting to the system temp tree. It never
// returns empty.
func (o Options) artifactsDir() string {
	if d := strings.TrimSpace(o.ArtifactsDir); d != "" {
		return d
	}
	return os.TempDir()
}
