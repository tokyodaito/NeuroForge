package codex

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"

	"neuroforge/internal/adapter/codingagent/proctree"
)

// Runner spawns a Codex process. The default implementation
// ([proctreeRunner]) uses [proctree.NewGroupCommand] so [Proc.Kill] terminates
// the entire process tree (spec: cancellation ends the whole process group).
//
// Tests inject a deterministic Runner that serves recorded byte-stream fixtures
// (rule §36.5: no real paid API calls in tests).
type Runner interface {
	// Start spawns the process. dir is the workspace; env is the allowlisted
	// environment. argv is the fully-resolved command (no shell). It returns a
	// [Proc] the supervisor reads from, or an error if the process could not be
	// launched.
	Start(argv []string, dir string, env []string) (Proc, error)
}

// Proc is a handle on a spawned Codex process. The supervisor reads the JSONL
// event stream from Stdout, waits for exit via Wait, and terminates the whole
// group via Kill.
type Proc interface {
	// Stdout returns the JSONL event stream. Reads block until data is available
	// and yield io.EOF when the process exits or is killed.
	Stdout() io.ReadCloser

	// Wait blocks until the process exits and returns its exit code and captured
	// stderr. It is safe to call after Stdout has reached EOF. The captured
	// stderr is used by [Adapter.ClassifyFailure] (and never persists secrets:
	// the redactor strips tokens before it is stored).
	Wait() (exitCode int, stderr string)

	// Kill terminates the entire process group (spec). Best-effort and
	// idempotent: a second Kill, or killing an already-exited group, is not an
	// error.
	Kill() error
}

// proctreeRunner is the production Runner: it spawns Codex in a new process
// group via [proctree] so the whole tree can be torn down on cancellation.
type proctreeRunner struct{}

func newProctreeRunner() *proctreeRunner { return &proctreeRunner{} }

// Start implements [Runner].
func (proctreeRunner) Start(argv []string, dir string, env []string) (Proc, error) {
	if len(argv) == 0 {
		return nil, errors.New("codex: empty argv")
	}
	cmd := proctree.NewGroupCommand(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("codex: stdout pipe: %w", err)
	}
	// Capture stderr into a buffer (mirrors the declarative adapter). A buffer,
	// unlike a pipe, cannot deadlock when the agent writes a lot of stderr
	// before stdout is fully consumed.
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("codex: start %q: %w", argv[0], err)
	}
	return &realProc{cmd: cmd, stdout: stdout, stderr: &stderr}, nil
}

// realProc wraps an exec.Cmd started in its own process group.
type realProc struct {
	cmd    *exec.Cmd
	stdout io.ReadCloser
	stderr *bytes.Buffer
}

func (p *realProc) Stdout() io.ReadCloser { return p.stdout }

func (p *realProc) Kill() error {
	if p.cmd == nil {
		return nil
	}
	return proctree.KillGroup(p.cmd, proctree.SigKill)
}

func (p *realProc) Wait() (int, string) {
	err := p.cmd.Wait()
	stderr := p.stderr.String()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			code = 1
		}
	}
	return code, redact(stderr)
}
