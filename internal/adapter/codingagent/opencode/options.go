package opencode

import (
	"context"
	"sync"
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// adapterVersion is the version of this adapter implementation (independent of
// protocol.ProtocolVersion and of the wrapped OpenCode engine version).
const adapterVersion = "opencode-adapter-v1"

// Options configures an OpenCode [Adapter]. All fields are optional.
type Options struct {
	// Binary is the OpenCode executable to invoke. If empty, [Adapter.Detect]
	// resolves "opencode" on PATH (honouring PATHEXT on Windows and tolerating
	// the .cmd/.bat shims produced by npm-style installers).
	Binary string

	// Agent is the OpenCode agent profile passed via --agent. If empty, no
	// --agent flag is emitted and OpenCode uses its built-in default agent.
	Agent string

	// ArtifactsDir, when set, is where malformed agent output lines are persisted
	// for forensics (spec: malformed events are saved + classified). When empty,
	// os.TempDir() is used.
	ArtifactsDir string

	// ExtraArgs are appended verbatim after the adapter's own flags and before
	// the prompt argument. Intended for documented OpenCode flags only; callers
	// are responsible for not weakening NeuroForge policy (e.g. never --share).
	ExtraArgs []string
}

// Adapter is the in-process OpenCode coding-agent adapter. It implements
// [neuroforge/internal/adapter/codingagent.Adapter] (the 13-method Protocol-v1
// surface) but does NOT self-register: construct it with [New] and register the
// returned value with the daemon registry.
//
// The exported methods are safe for concurrent use. Run lifecycle methods
// ([Adapter.Start], [Adapter.Resume], [Adapter.Cancel]) coordinate per-run
// state under an internal mutex.
type Adapter struct {
	opts Options

	mu   sync.Mutex
	runs map[string]*runState

	// detected caches the result of the last Detect so Capabilities/Version do
	// not re-spawn the version probe.
	detected  detection
	hasDetect bool

	// hooks default to production implementations in [New]; tests in this
	// package override them to exercise the run pipeline offline with recorded
	// byte streams and to avoid a real OpenCode binary (rule §36.5).
	lookPath func(file string) (string, error)
	runProbe func(ctx context.Context, binary string) (stdout, stderr string, err error)
	spawn    spawnFunc
}

// New returns an OpenCode adapter configured by opts. It never performs I/O and
// never registers itself with any registry (callers register explicitly).
func New(opts Options) *Adapter {
	a := &Adapter{
		opts: opts,
		runs: map[string]*runState{},
	}
	a.lookPath = defaultLookPath
	a.runProbe = defaultRunProbe
	a.spawn = defaultSpawn
	return a
}

// runState tracks one live run for cancellation and timeout.
type runState struct {
	proc     runProcess
	cancel   context.CancelFunc // cancels the run's supervision context
	runID    string
	engine   string
	model    string
	timeout  time.Duration
	isResume bool
}

// detection is the internal, cached form of [protocol.DetectionResult].
type detection struct {
	installed bool
	path      string
	version   string // raw version string from `opencode --version`
	detail    string
}

// toResult maps the internal detection cache to the protocol type.
func (d detection) toResult() protocol.DetectionResult {
	return protocol.DetectionResult{
		Installed: d.installed,
		Path:      d.path,
		Version:   d.version,
		Detail:    d.detail,
	}
}
