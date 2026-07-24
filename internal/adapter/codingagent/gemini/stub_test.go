package gemini

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
)

// stubHost is a controllable [host] for offline, deterministic tests. It never
// spawns a real process and never makes a paid call (rule §36.5). Each hook is
// optional; nil hooks fall back to sane canned behaviour.
type stubHost struct {
	lookPathFn func(string) (string, error)
	probeFn    func(ctx context.Context, argv, env []string) (string, string, error)
	launchFn   func(argv []string, dir string, env []string, stdin io.Reader) (launchedProcess, error)

	// launches records every launch invocation for assertion.
	launches []stubLaunch

	mu sync.Mutex
}

type stubLaunch struct {
	argv     []string
	dir      string
	env      []string
	hasStdin bool
}

func (s *stubHost) lookPath(name string) (string, error) {
	s.mu.Lock()
	fn := s.lookPathFn
	s.mu.Unlock()
	if fn != nil {
		return fn(name)
	}
	return "/usr/local/bin/" + name, nil
}

func (s *stubHost) probe(ctx context.Context, argv, env []string) (string, string, error) {
	s.mu.Lock()
	fn := s.probeFn
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx, argv, env)
	}
	return "0.23.0", "", nil
}

func (s *stubHost) launch(argv []string, dir string, env []string, stdin io.Reader) (launchedProcess, error) {
	s.mu.Lock()
	s.launches = append(s.launches, stubLaunch{
		argv:     append([]string(nil), argv...),
		dir:      dir,
		env:      append([]string(nil), env...),
		hasStdin: stdin != nil,
	})
	fn := s.launchFn
	s.mu.Unlock()
	if fn != nil {
		return fn(argv, dir, env, stdin)
	}
	return cannedProc([]byte("{}"), 0, ""), nil
}

// stubProc is a controllable [launchedProcess]. Its stdout is a fixed reader; on
// kill it optionally runs killFn (e.g. closing a held pipe to unblock a hang).
type stubProc struct {
	r      io.Reader
	exit   int
	stderr string
	killFn func()

	killed atomic.Bool
}

func (p *stubProc) stdout() io.Reader   { return p.r }
func (p *stubProc) wait() (int, string) { return p.exit, p.stderr }
func (p *stubProc) kill() error {
	if p.killed.Swap(true) {
		return nil
	}
	if p.killFn != nil {
		p.killFn()
	}
	return nil
}

// cannedProc builds a stubProc that immediately serves stdout then reports the
// given exit code and stderr. Used for the normal success/failure paths.
func cannedProc(stdout []byte, exit int, stderr string) *stubProc {
	return &stubProc{r: bytesReader(stdout), exit: exit, stderr: stderr}
}

// hangingProc builds a stubProc whose stdout blocks until kill is called (it
// holds the write end of a pipe and closes it on kill). Models a long-running /
// hung agent that only ends via cancellation or timeout.
func hangingProc(exit int, stderr string) *stubProc {
	pr, pw := io.Pipe()
	p := &stubProc{r: pr, exit: exit, stderr: stderr}
	// pw is kept alive by this closure and only closed on kill, which unblocks
	// the blocking stdout read (readAll receives EOF).
	p.killFn = func() { _ = pw.Close() }
	return p
}

// bytesReader returns a reader over a copy of b (nil-safe).
func bytesReader(b []byte) io.Reader {
	return &byteReader{data: append([]byte(nil), b...)}
}

// byteReader is a minimal io.Reader over an in-memory buffer, so tests avoid
// importing bytes here while keeping the stub self-contained.
type byteReader struct {
	data []byte
	pos  int
}

func (r *byteReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

// newTestAdapter builds an adapter wired to a stub host for offline tests.
func newTestAdapter(h *stubHost) *Adapter {
	return newWithHost(Options{Binary: "gemini", ArtifactsDir: ""}, h)
}
