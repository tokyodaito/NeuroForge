package opencode

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"neuroforge/internal/adapter/codingagent/fake"
)

// stubExitErr is a test-only error carrying an exit code via ExitCode().
type stubExitErr int

func (e stubExitErr) Error() string { return fmt.Sprintf("stub exit code %d", int(e)) }
func (e stubExitErr) ExitCode() int { return int(e) }

// stubReader serves recorded bytes, then either returns EOF or blocks on a kill
// signal (hang scenarios). It gives the supervision loop full control over
// stream/EOF/hang timing without spawning a real process (rule §36.5).
type stubReader struct {
	data []byte
	pos  int
	hang bool
	kill <-chan struct{}
}

func (r *stubReader) Read(p []byte) (int, error) {
	if r.pos < len(r.data) {
		n := copy(p, r.data[r.pos:])
		r.pos += n
		return n, nil
	}
	if r.hang {
		<-r.kill
		return 0, io.EOF
	}
	return 0, io.EOF
}

// stubRun is a test-only runProcess that replays a recorded JSONL byte stream,
// reports a fixed exit code + stderr, and supports hang-until-killed (for
// cancellation/timeout scenarios). Kill is idempotent.
type stubRun struct {
	r        *stubReader
	stderr   string
	exitCode int
	killOnce sync.Once
	killed   chan struct{}
}

func newStubRun(stream string, stderr string, exitCode int, hang bool) *stubRun {
	killed := make(chan struct{})
	return &stubRun{
		r:        &stubReader{data: []byte(stream), hang: hang, kill: killed},
		stderr:   stderr,
		exitCode: exitCode,
		killed:   killed,
	}
}

func (s *stubRun) Stdout() io.Reader { return s.r }
func (s *stubRun) Stderr() string    { return s.stderr }
func (s *stubRun) Wait() error {
	if s.r.hang {
		<-s.killed
	}
	if s.exitCode == 0 {
		return nil
	}
	return stubExitErr(s.exitCode)
}
func (s *stubRun) Kill() error {
	s.killOnce.Do(func() { close(s.killed) })
	return nil
}

// stubAdapter returns an Adapter wired so Detect always succeeds with a known
// version and spawn replays the given recorded run. It is the offline test
// factory (rule §36.5: no real paid calls; no real binary).
func stubAdapter(stream string, stderr string, exitCode int, hang bool, artifactsDir string) *Adapter {
	a := New(Options{ArtifactsDir: artifactsDir, Binary: "/fake/opencode"})
	a.lookPath = func(string) (string, error) { return "/fake/opencode", nil }
	a.runProbe = func(context.Context, string) (string, string, error) { return "opencode 0.1.48", "", nil }
	a.spawn = func([]string, string, []string) (runProcess, error) {
		return newStubRun(stream, stderr, exitCode, hang), nil
	}
	return a
}

// scenarioStream returns a recorded JSONL byte stream + exit/stderr/hang that
// mirrors a fake.Scenario's normalised-event shape, so the OpenCode adapter's
// run pipeline can be driven through the conformance suite offline. The adapter
// synthesises the opening run.started/run.resumed, so these fixtures begin with
// the first non-opening event.
func scenarioStream(s fake.Scenario) (stream, stderr string, exitCode int, hang bool) {
	switch s {
	case fake.ScenarioSuccess:
		stream = jline(`{"type":"message.delta","message":{"delta":"Hello from opencode"}}`) +
			jline(`{"type":"usage.updated","usage":{"input_tokens":120,"output_tokens":80,"cost":0.0001,"currency":"USD","confidence":"PROVIDER_REPORTED"}}`) +
			jline(`{"type":"file.changed","file_change":{"path":"src/hello.txt","action":"created","in_scope":true}}`) +
			jline(`{"type":"run.completed"}`)
		exitCode = 0
	case fake.ScenarioMalformedJSON:
		stream = "{not valid json\n" +
			jline(`{"type":"message.delta","message":{"delta":"still working"}}`) +
			jline(`{"type":"run.completed"}`)
		exitCode = 0
	case fake.ScenarioQuotaBeforeEdits:
		stream = jline(`{"type":"usage.updated","usage":{"confidence":"UNKNOWN"}}`) +
			jline(`{"type":"run.failed","failure":{"class":"PROVIDER_QUOTA","reason":"quota exhausted","exit_code":2}}`)
		stderr = "error: quota exhausted before any edits\n"
		exitCode = 2
	case fake.ScenarioResume:
		stream = jline(`{"type":"message.delta","message":{"delta":"resumed and finishing"}}`) +
			jline(`{"type":"file.changed","file_change":{"path":"src/resumed.txt","action":"modified","in_scope":true}}`) +
			jline(`{"type":"run.completed"}`)
		exitCode = 0
	case fake.ScenarioCancellation:
		stream = jline(`{"type":"message.delta","message":{"delta":"thinking"}}`)
		hang = true
		exitCode = 137
	case fake.ScenarioTimeout:
		stream = jline(`{"type":"message.delta","message":{"delta":"thinking"}}`)
		hang = true
		exitCode = 124
	case fake.ScenarioCrash:
		stream = jline(`{"type":"run.failed","failure":{"class":"ENGINE_CRASH","reason":"opencode panicked","exit_code":134}}`)
		stderr = "opencode panicked (simulated crash)\n"
		exitCode = 134
	default:
		// Unknown scenario: a clean completion keeps the suite honest.
		stream = jline(`{"type":"run.completed"}`)
	}
	return
}

// jline returns its argument as one JSONL line (with trailing newline).
func jline(s string) string { return strings.TrimRight(s, "\n") + "\n" }
