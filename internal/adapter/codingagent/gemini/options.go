package gemini

import (
	"context"
	"os"
	"sync"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// engineID is the stable NeuroForge engine identifier (spec §12.1). It is
// independent from any model name: the engine "gemini" can target any model the
// installed CLI accepts (rule §36.8 — the core never hard-codes model names).
const engineID = "gemini"

// adapterVersion is the version of this adapter implementation, reported via
// Version(). It is independent from [protocol.ProtocolVersion].
const adapterVersion = "gemini-adapter-v1"

// Options configures a Gemini [Adapter]. All fields are optional; sensible
// defaults are applied by [New].
type Options struct {
	// Binary is the Gemini CLI executable name to locate on PATH (default
	// "gemini"). May be an absolute or relative path.
	Binary string

	// ArtifactsDir is where malformed agent output lines are persisted for
	// forensics (spec: malformed event is saved + classified, never fatal).
	// Defaults to [os.TempDir] when empty.
	ArtifactsDir string

	// ExtraArgs are appended verbatim to every headless invocation, after the
	// adapter's own safe-default flags. They must NEVER include unsafe modes
	// (--yolo / --approval-mode yolo / unrestricted file access); the adapter
	// does not validate them — the caller is trusted wiring code. This exists
	// for forward-compatible, request-independent options (e.g. telemetry off).
	ExtraArgs []string
}

// Adapter is the in-process Gemini CLI coding agent (spec §12.2). It implements
// the full [codingagent.Adapter] surface. A run spawns the headless Gemini CLI
// in a new process group, streams its output into normalized events, and maps
// failures onto the §32 taxonomy.
//
// Adapter is safe for concurrent use: each Start/Resume run is tracked by run
// id and may be cancelled independently.
type Adapter struct {
	opts Options

	// host abstracts process spawning/look-up so the adapter is fully testable
	// without a real (paid) Gemini CLI. The production adapter uses a realHost
	// (proctree + os/exec); tests inject a stub.
	host host

	artDir string

	mu   sync.Mutex
	runs map[string]*runState
}

// runState tracks one live Gemini run for cancellation and timeout.
type runState struct {
	proc   launchedProcess
	cancel context.CancelFunc
}

// New returns a Gemini adapter configured by opts. It does not self-register;
// the caller registers the returned adapter into a [codingagent.Registry] at the
// daemon wiring site (AC-6).
func New(opts Options) *Adapter {
	applyDefaults(&opts)
	a := &Adapter{
		opts:   opts,
		host:   newRealHost(),
		artDir: opts.ArtifactsDir,
		runs:   map[string]*runState{},
	}
	return a
}

// newWithHost is the test seam that injects a host (real or stub). It is
// unexported because the host abstraction is an internal implementation detail.
func newWithHost(opts Options, h host) *Adapter {
	applyDefaults(&opts)
	return &Adapter{
		opts:   opts,
		host:   h,
		artDir: opts.ArtifactsDir,
		runs:   map[string]*runState{},
	}
}

func applyDefaults(opts *Options) {
	if opts.Binary == "" {
		opts.Binary = engineID
	}
	if opts.ArtifactsDir == "" {
		opts.ArtifactsDir = os.TempDir()
	}
}

// ID implements [codingagent.Adapter]. It returns the stable engine id
// "gemini", independent from any model name (spec §12.1).
func (a *Adapter) ID() string { return engineID }

// ListModels implements [codingagent.Adapter]. The Gemini CLI does not expose a
// reliable, offline model catalogue (enumerating models requires a paid API
// call, which the adapter never makes — rule §36.5). Model names are therefore
// provider-supplied via the request/catalogue (rule §36.8); the adapter reports
// no statically-known models. The model to target always arrives on
// [protocol.AgentRunRequest.Model].
func (a *Adapter) ListModels(context.Context, protocol.Account) ([]protocol.ModelDescriptor, error) {
	return nil, nil
}

// InspectQuota implements [codingagent.Adapter]. The Gemini CLI exposes no
// quota figure without a paid call, so the adapter reports UNKNOWN (spec §20.1,
// rule §36.10 — never overstate precision).
func (a *Adapter) InspectQuota(context.Context, protocol.Account) protocol.QuotaSnapshot {
	return protocol.QuotaSnapshot{
		Confidence: protocol.QuotaConfUnknown,
		State:      protocol.QuotaStateUnknown,
		Reason:     "gemini CLI exposes no quota figure without a paid call",
	}
}
