package codex

import (
	"io"
	"sync"
	"time"
)

// runScript describes the canned behaviour of a fake Codex process for one run.
type runScript struct {
	lines    []string // JSONL lines emitted on stdout (in order)
	hang     bool     // block forever after emitting lines (until killed)
	exitCode int      // exit code returned by Wait
	stderr   string   // stderr returned by Wait
}

// fakeRunner is a deterministic [Runner] for tests: no real Codex, no paid call
// (rule §36.5). It inspects argv: a "--version" probe returns the configured
// version output; an "exec" run returns the scripted JSONL stream + outcome.
type fakeRunner struct {
	mu sync.Mutex

	version     string // "--version" stdout
	versionErr  error  // launch error for the version probe (nil = launch ok)
	versionCode int    // exit code for the version probe

	script   *runScript // behaviour for exec runs (reused per Start)
	execErr  error      // launch error for exec runs
	startErr error      // if set, every Start returns this

	// started records every argv the runner was asked to spawn, for assertions.
	started [][]string
}

func (fr *fakeRunner) Start(argv []string, dir string, env []string) (Proc, error) {
	fr.mu.Lock()
	fr.started = append(fr.started, append([]string(nil), argv...))
	fr.mu.Unlock()

	if fr.startErr != nil {
		return nil, fr.startErr
	}
	// version probe?
	if isVersionProbe(argv) {
		if fr.versionErr != nil {
			return nil, fr.versionErr
		}
		return newFakeProc(&runScript{lines: []string{fr.version}, exitCode: fr.versionCode}), nil
	}
	// exec run
	if fr.execErr != nil {
		return nil, fr.execErr
	}
	script := fr.script
	if script == nil {
		script = &runScript{}
	}
	return newFakeProc(script), nil
}

// starts returns a copy of the argv lists the runner was asked to spawn.
func (fr *fakeRunner) starts() [][]string {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	out := make([][]string, len(fr.started))
	for i, s := range fr.started {
		out[i] = append([]string(nil), s...)
	}
	return out
}

func isVersionProbe(argv []string) bool {
	for _, a := range argv[1:] {
		if a == "--version" {
			return true
		}
		if a == "exec" {
			return false
		}
	}
	return false
}

// fakeProc is a [Proc] backed by an in-memory pipe. It emits the scripted lines
// then yields EOF, or — for hang scripts — blocks until Kill so the supervisor's
// cancellation/timeout path is exercised.
type fakeProc struct {
	r        *io.PipeReader
	w        *io.PipeWriter
	killed   chan struct{}
	done     chan struct{}
	exitCode int
	stderr   string
	once     sync.Once
}

func newFakeProc(script *runScript) *fakeProc {
	r, w := io.Pipe()
	p := &fakeProc{
		r: r, w: w,
		killed:   make(chan struct{}),
		done:     make(chan struct{}),
		exitCode: script.exitCode,
		stderr:   script.stderr,
	}
	go p.run(script)
	return p
}

func (p *fakeProc) run(script *runScript) {
	defer close(p.done)
	for _, line := range script.lines {
		select {
		case <-p.killed:
			_ = closePipe(p.w)
			return
		default:
		}
		if _, err := p.w.Write([]byte(line + "\n")); err != nil {
			return
		}
	}
	if script.hang {
		<-p.killed
	}
	_ = closePipe(p.w) // EOF on the reader side
}

func (p *fakeProc) Stdout() io.ReadCloser { return p.r }

func (p *fakeProc) Kill() error {
	p.once.Do(func() { close(p.killed) })
	_ = closePipe(p.w) // force any blocked reader/Write to unblock with EOF/err
	return nil
}

func (p *fakeProc) Wait() (int, string) {
	<-p.done
	return p.exitCode, p.stderr
}

func closePipe(w *io.PipeWriter) error {
	err := w.Close()
	return err
}

// newTestAdapter builds an Adapter wired to fr with deterministic test seams.
func newTestAdapter(fr *fakeRunner, extra ...func(*Options)) *Adapter {
	opts := Options{}
	opts.runner = fr
	opts.now = fixedNow
	for _, f := range extra {
		f(&opts)
	}
	return New(opts)
}

// fixedNow is a deterministic clock for tests (2023-01-01T00:00:00Z).
var fixedNow = func() time.Time { return time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC) }
