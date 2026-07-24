package codex

import (
	"os/exec"
	"time"
)

// Options configures a codex [Adapter]. Construct one with [New].
//
// Exported fields are stable configuration; the unexported fields are test seams
// (same-package tests inject a deterministic [Runner], a [lookup] override and a
// [now] clock). External callers leave them zero.
type Options struct {
	// BinaryPath overrides Codex binary resolution. When empty, [Adapter.Detect]
	// resolves "codex" via exec.LookPath (honouring PATHEXT on Windows, and
	// tolerating .exe/.cmd/.bat and npm shims).
	BinaryPath string

	// ArtifactsDir is where malformed/unknown Codex output lines are persisted
	// for forensics (spec: a malformed event is saved + classified, not fatal).
	// Defaults to os.TempDir() when empty.
	ArtifactsDir string

	// ExecArgs are extra arguments inserted between "codex exec" and the model
	// selector / prompt. They carry Codex sandbox and approval flags. When nil a
	// safe default set is applied (a workspace-write sandbox; the adapter never
	// enables a privilege-bypass / "danger" mode). Callers may override.
	ExecArgs []string

	// runner, when non-nil, replaces the default process spawner. It is the
	// deterministic offline seam used by tests (no real Codex, no paid call).
	runner Runner

	// lookup overrides binary resolution (default exec.LookPath) for detection
	// tests that exercise PATHEXT/Unicode handling without mutating PATH.
	lookup func(file string) (string, error)

	// now returns the current time (default time.Now).
	now func() time.Time
}

// resolve fills test seams with safe defaults.
func (o Options) resolve() Options {
	o2 := o
	if o2.now == nil {
		o2.now = time.Now
	}
	if o2.lookup == nil {
		o2.lookup = exec.LookPath
	}
	return o2
}

// DefaultExecArgs is the safe sandbox/approval flag set inserted into the
// "codex exec" command when [Options.ExecArgs] is nil. It selects a
// workspace-write sandbox and disables interactive approval (the run is
// autonomous and headless, so it cannot answer prompts). The adapter never adds
// a privilege-bypass / "danger-full-access" mode.
var DefaultExecArgs = []string{
	"--sandbox", "workspace-write",
	"--ask-for-approval", "never",
}

// bootstrapTimeout bounds how long [Adapter.Start] waits to observe a Codex
// session id from the live stream before returning (so Start never blocks
// forever on a quiet CLI). It is only consulted when the adapter advertises
// session resume.
const bootstrapTimeout = 3 * time.Second
