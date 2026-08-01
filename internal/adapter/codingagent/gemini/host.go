package gemini

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"sync"

	"neuroforge/internal/adapter/codingagent/proctree"
)

// host is the seam between the adapter and the operating system. It abstracts
// executable resolution, synchronous probes (`gemini --version`) and process
// spawning so the adapter can be unit-tested deterministically without a real
// (paid) Gemini CLI. The production implementation is [realHost]; tests inject
// a stub.
type host interface {
	// lookPath resolves the Gemini CLI executable, tolerating spaces/Unicode in
	// PATH entries. Returns an absolute or PATH-relative path, or an error if
	// not found.
	lookPath(name string) (string, error)

	// probe runs a short-lived command to completion (e.g. `gemini --version`)
	// and returns its combined stdout/stderr. A non-zero exit is reported via
	// err; callers inspect stderr for classification.
	probe(ctx context.Context, argv []string, env []string) (stdout, stderr string, err error)

	// launch starts the headless agent process in a new process group. dir is
	// the workspace; env is the allowlisted environment; stdin (when non-nil)
	// is piped to the child. The returned [launchedProcess] lets the caller
	// stream stdout, await the exit code, and terminate the whole group.
	launch(argv []string, dir string, env []string, stdin io.Reader) (launchedProcess, error)
}

// launchedProcess is a running agent process group. Stdout is streamed;
// Stderr is captured; the whole group can be terminated.
type launchedProcess interface {
	// stdout returns a reader over the agent's stdout stream. Reads block until
	// data is available or the stream ends (EOF at process exit / group kill).
	stdout() io.Reader

	// wait blocks until the process exits and returns its exit code plus the
	// captured stderr (redaction is the caller's responsibility).
	wait() (exitCode int, stderr string)

	// kill terminates the entire process group led by this process. It is
	// best-effort and idempotent (killing an already-dead group is not an
	// error), so cancellation never fails spuriously (spec: cancellation ends
	// the whole process group).
	kill() error
}

// realHost is the production host backed by [os/exec] and [proctree].
type realHost struct{}

func newRealHost() realHost { return realHost{} }

// lookPath implements [host].
func (realHost) lookPath(name string) (string, error) { return lookPath(name) }

// probe implements [host].
func (realHost) probe(ctx context.Context, argv []string, env []string) (string, string, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = env
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return out.String(), errBuf.String(), err
}

// launch implements [host]. It creates a new process group (proctree) so the
// whole tree can be killed on cancellation, sets the workspace dir and the
// allowlisted environment, and pipes stdin when provided.
func (realHost) launch(argv []string, dir string, env []string, stdin io.Reader) (launchedProcess, error) {
	cmd := proctree.NewGroupCommand(argv[0], argv[1:]...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = env
	if stdin != nil {
		cmd.Stdin = stdin
	}
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execProc{cmd: cmd, stdoutPipe: stdout, stderrBuf: stderr}, nil
}

// execProc is the [launchedProcess] backed by a real *exec.Cmd.
type execProc struct {
	cmd        *exec.Cmd
	stdoutPipe io.ReadCloser
	stderrBuf  *bytes.Buffer

	killOnce sync.Once
}

func (p *execProc) stdout() io.Reader { return p.stdoutPipe }

func (p *execProc) wait() (int, string) {
	err := p.cmd.Wait()
	return exitCodeFromErr(err), p.stderrBuf.String()
}

func (p *execProc) kill() error {
	p.killOnce.Do(func() {
		_ = proctree.KillGroup(p.cmd, proctree.SigKill)
	})
	return nil
}

// exitCodeFromErr extracts the process exit code from an exec error, defaulting
// to 1 for non-exec errors (e.g. the binary could not be waited on).
func exitCodeFromErr(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if ok := errors.As(err, &ee); ok {
		return ee.ExitCode()
	}
	return 1
}
