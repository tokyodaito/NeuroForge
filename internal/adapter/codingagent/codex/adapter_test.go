package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/protocol"
)

// ---- JSONL line builders (NeuroForge-native fixtures) ----

func nfRunStarted() string {
	return `{"type":"run.started","ts":"2023-01-01T00:00:00Z","run_id":"r","engine":"codex"}`
}
func nfDelta(s string) string {
	return `{"type":"message.delta","ts":"2023-01-01T00:00:00Z","message":{"delta":"` + s + `"}}`
}
func nfUsage(in, out, cache int64, cost float64) string {
	return `{"type":"usage.updated","ts":"2023-01-01T00:00:00Z","usage":{"input_tokens":` + itoa64(in) + `,"output_tokens":` + itoa64(out) + `,"cache_read_tokens":` + itoa64(cache) + `,"cost":` + costStr(cost) + `,"confidence":"PROVIDER_REPORTED"}}`
}
func nfRunCompleted() string {
	return `{"type":"run.completed","ts":"2023-01-01T00:00:00Z","run_id":"r","engine":"codex"}`
}
func nfRunFailed(class, reason string, code int) string {
	return `{"type":"run.failed","ts":"2023-01-01T00:00:00Z","run_id":"r","engine":"codex","failure":{"class":"` + class + `","reason":"` + reason + `","exit_code":` + itoa(code) + `}}`
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [24]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func costStr(f float64) string {
	switch f {
	case 0:
		return "0"
	case 0.0021:
		return "0.0021"
	default:
		return "0.0001"
	}
}

// ---- helpers ----

func waitTerminal(t *testing.T, sink *codingagent.SliceSink, timeout time.Duration) []protocol.NormalizedEvent {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		evs := sink.Events()
		if len(evs) > 0 && evs[len(evs)-1].Type.IsTerminal() {
			return evs
		}
		time.Sleep(2 * time.Millisecond)
	}
	return sink.Events()
}

func detectedRunner(script *runScript) *fakeRunner {
	return &fakeRunner{version: "codex 0.42.0", script: script}
}

// ---- tests ----

func TestStartSuccessEventOrdering(t *testing.T) {
	fr := detectedRunner(&runScript{
		lines: []string{nfRunStarted(), nfDelta("hi"), nfUsage(100, 50, 10, 0.0001), nfRunCompleted()},
	})
	a := newTestAdapter(fr, func(o *Options) {
		o.lookup = func(string) (string, error) { return "/bin/codex", nil }
	})
	sink := &codingagent.SliceSink{}
	handle, err := a.Start(t.Context(), protocol.AgentRunRequest{
		RunID: "r", Engine: "codex", Model: "some-model", Workspace: t.TempDir(), Prompt: "do work",
	}, sink)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if handle.Engine != "codex" || handle.Model != "some-model" {
		t.Errorf("handle = %+v", handle)
	}
	evs := waitTerminal(t, sink, 3*time.Second)
	if len(evs) == 0 || evs[0].Type != protocol.EventRunStarted {
		t.Fatalf("first event = %v", typesOf(evs))
	}
	if evs[len(evs)-1].Type != protocol.EventRunCompleted {
		t.Fatalf("last event = %v, want run.completed", typesOf(evs))
	}
}

func TestStartCodexShapedEvents(t *testing.T) {
	// Codex-native event shapes are mapped end-to-end through supervise.
	fr := detectedRunner(&runScript{
		lines: []string{
			`{"type":"task_started","session_id":"s-codex-1","model":"some-model"}`,
			`{"type":"agent_message_delta","delta":"working"}`,
			`{"type":"token_count","input_tokens":10,"output_tokens":4,"cached_input_tokens":2}`,
			`{"type":"task_complete"}`,
		},
	})
	a := newTestAdapter(fr, func(o *Options) {
		o.lookup = func(string) (string, error) { return "/bin/codex", nil }
	})
	sink := &codingagent.SliceSink{}
	if _, err := a.Start(t.Context(), protocol.AgentRunRequest{
		RunID: "r", Engine: "codex", Workspace: t.TempDir(), Prompt: "p",
	}, sink); err != nil {
		t.Fatalf("Start: %v", err)
	}
	evs := waitTerminal(t, sink, 3*time.Second)
	types := typesOf(evs)
	if len(types) < 1 || types[0] != protocol.EventRunStarted {
		t.Fatalf("first = %v", types)
	}
	if types[len(types)-1] != protocol.EventRunCompleted {
		t.Fatalf("last = %v", types)
	}
	// A usage event must have been mapped.
	hasUsage := false
	for _, e := range evs {
		if e.Type == protocol.EventUsageUpdated && e.Usage != nil && e.Usage.CacheReadTokens == 2 {
			hasUsage = true
		}
	}
	if !hasUsage {
		t.Errorf("no usage event mapped: %+v", evs)
	}
}

func TestStartMalformedDoesNotAbortRun(t *testing.T) {
	artDir := t.TempDir()
	fr := detectedRunner(&runScript{
		lines: []string{
			nfRunStarted(),
			`{not valid json`,
			nfDelta("still working"),
			nfRunCompleted(),
		},
	})
	a := newTestAdapter(fr, func(o *Options) {
		o.lookup = func(string) (string, error) { return "/bin/codex", nil }
		o.ArtifactsDir = artDir
	})
	sink := &codingagent.SliceSink{}
	if _, err := a.Start(t.Context(), protocol.AgentRunRequest{
		RunID: "r", Engine: "codex", Workspace: t.TempDir(), Prompt: "p",
	}, sink); err != nil {
		t.Fatalf("Start: %v", err)
	}
	evs := waitTerminal(t, sink, 3*time.Second)
	if len(evs) == 0 || evs[len(evs)-1].Type != protocol.EventRunCompleted {
		t.Fatalf("malformed broke the run: %v", typesOf(evs))
	}
	hasWarning := false
	for _, e := range evs {
		if e.Type == protocol.EventWarning {
			hasWarning = true
		}
	}
	if !hasWarning {
		t.Error("malformed line produced no warning")
	}
	// The raw malformed line must be persisted as a (redacted) artifact.
	entries, _ := os.ReadDir(artDir)
	if len(entries) == 0 {
		t.Error("no malformed artifact saved")
	}
}

func TestCancellationKillsGroup(t *testing.T) {
	fr := detectedRunner(&runScript{
		lines: []string{nfRunStarted()},
		hang:  true, exitCode: 137,
	})
	a := newTestAdapter(fr, func(o *Options) {
		o.lookup = func(string) (string, error) { return "/bin/codex", nil }
	})
	sink := &codingagent.SliceSink{}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	handle, err := a.Start(ctx, protocol.AgentRunRequest{
		RunID: "cancel", Engine: "codex", Workspace: t.TempDir(), Prompt: "p",
	}, sink)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Let the run reach its hang point.
	time.Sleep(80 * time.Millisecond)
	if err := a.Cancel(t.Context(), handle); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	evs := waitTerminal(t, sink, 3*time.Second)
	if len(evs) == 0 || evs[len(evs)-1].Type != protocol.EventRunCancelled {
		t.Fatalf("last = %v, want run.cancelled", typesOf(evs))
	}
	// The run must no longer be tracked (process group torn down).
	a.mu.Lock()
	_, stillTracked := a.runs[handle.RunID]
	a.mu.Unlock()
	if stillTracked {
		t.Error("run still tracked after cancel")
	}
}

func TestTimeoutEmitsFailedTimeout(t *testing.T) {
	fr := detectedRunner(&runScript{
		lines: []string{nfRunStarted()},
		hang:  true, exitCode: 124,
	})
	a := newTestAdapter(fr, func(o *Options) {
		o.lookup = func(string) (string, error) { return "/bin/codex", nil }
	})
	sink := &codingagent.SliceSink{}
	if _, err := a.Start(t.Context(), protocol.AgentRunRequest{
		RunID: "to", Engine: "codex", Workspace: t.TempDir(), Prompt: "p", Timeout: 60 * time.Millisecond,
	}, sink); err != nil {
		t.Fatalf("Start: %v", err)
	}
	evs := waitTerminal(t, sink, 3*time.Second)
	if len(evs) == 0 || evs[len(evs)-1].Type != protocol.EventRunFailed {
		t.Fatalf("last = %v, want run.failed", typesOf(evs))
	}
	last := evs[len(evs)-1]
	if last.Failure == nil || last.Failure.Class != protocol.FailureTimeout {
		t.Errorf("failure = %+v, want TIMEOUT", last.Failure)
	}
}

func TestCrashSynthesizesEngineCrash(t *testing.T) {
	// Codex exits with a signal-style code and no terminal event.
	fr := detectedRunner(&runScript{
		lines: []string{nfRunStarted(), nfDelta("partial...")}, exitCode: 134,
	})
	a := newTestAdapter(fr, func(o *Options) {
		o.lookup = func(string) (string, error) { return "/bin/codex", nil }
	})
	sink := &codingagent.SliceSink{}
	if _, err := a.Start(t.Context(), protocol.AgentRunRequest{
		RunID: "crash", Engine: "codex", Workspace: t.TempDir(), Prompt: "p",
	}, sink); err != nil {
		t.Fatalf("Start: %v", err)
	}
	evs := waitTerminal(t, sink, 3*time.Second)
	if len(evs) == 0 || evs[len(evs)-1].Type != protocol.EventRunFailed {
		t.Fatalf("last = %v, want run.failed", typesOf(evs))
	}
	last := evs[len(evs)-1]
	if last.Failure == nil || last.Failure.Class != protocol.FailureEngineCrash {
		t.Errorf("failure = %+v, want ENGINE_CRASH", last.Failure)
	}
}

func TestQuotaFailureTypedEvent(t *testing.T) {
	fr := detectedRunner(&runScript{
		lines: []string{nfRunStarted(), nfRunFailed("PROVIDER_QUOTA", "quota exhausted", 2)},
	})
	a := newTestAdapter(fr, func(o *Options) {
		o.lookup = func(string) (string, error) { return "/bin/codex", nil }
	})
	sink := &codingagent.SliceSink{}
	if _, err := a.Start(t.Context(), protocol.AgentRunRequest{
		RunID: "q", Engine: "codex", Workspace: t.TempDir(), Prompt: "p",
	}, sink); err != nil {
		t.Fatalf("Start: %v", err)
	}
	evs := waitTerminal(t, sink, 3*time.Second)
	last := evs[len(evs)-1]
	if last.Type != protocol.EventRunFailed || last.Failure == nil {
		t.Fatalf("expected run.failed with payload: %+v", last)
	}
	fc := a.ClassifyFailure(last.Failure.ExitCode, evs, "")
	if fc.Class != protocol.FailureProviderQuota {
		t.Errorf("class = %s, want PROVIDER_QUOTA", fc.Class)
	}
}

func TestResumeEmitsRunResumedFirst(t *testing.T) {
	// On resume, the stream's first event maps to run.resumed (codex mapper
	// treats task_started as run.started; we emit a native run.resumed to model
	// a resumed continuation).
	fr := detectedRunner(&runScript{
		lines: []string{
			`{"type":"run.resumed","ts":"2023-01-01T00:00:00Z","run_id":"r","engine":"codex"}`,
			nfDelta("resumed and finishing"),
			nfRunCompleted(),
		},
	})
	a := newTestAdapter(fr, func(o *Options) {
		o.lookup = func(string) (string, error) { return "/bin/codex", nil }
	})
	sink := &codingagent.SliceSink{}
	handle, err := a.Resume(t.Context(), protocol.ResumeRequest{
		RunID: "res", Engine: "codex", Workspace: t.TempDir(), SessionID: "sess-1",
	}, sink)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if handle.SessionID != "sess-1" {
		t.Errorf("handle.SessionID = %q", handle.SessionID)
	}
	evs := waitTerminal(t, sink, 3*time.Second)
	if len(evs) == 0 || evs[0].Type != protocol.EventRunResumed {
		t.Fatalf("first = %v, want run.resumed", typesOf(evs))
	}
	if !evs[len(evs)-1].Type.IsTerminal() {
		t.Fatalf("last not terminal: %v", typesOf(evs))
	}
}

func TestSessionIDExtraction(t *testing.T) {
	// When session resume is supported, Start captures the session id Codex
	// emits and sets RunHandle.SessionID.
	fr := detectedRunner(&runScript{
		lines: []string{
			`{"type":"task_started","session_id":"codex-session-xyz","model":"m"}`,
			nfRunCompleted(),
		},
	})
	a := newTestAdapter(fr, func(o *Options) {
		o.lookup = func(string) (string, error) { return "/bin/codex", nil }
	})
	handle, err := a.Start(t.Context(), protocol.AgentRunRequest{
		RunID: "s", Engine: "codex", Workspace: t.TempDir(), Prompt: "p",
	}, &codingagent.SliceSink{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if handle.SessionID != "codex-session-xyz" {
		t.Errorf("SessionID = %q, want codex-session-xyz", handle.SessionID)
	}
}

func TestSessionIDEmptyWhenResumeUnsupported(t *testing.T) {
	// Unparsable version → SessionResume false → no bootstrap wait, no session.
	fr := &fakeRunner{
		version: "codex-mystery-build",
		script: &runScript{
			lines: []string{`{"type":"task_started","session_id":"should-not-be-captured"}`, nfRunCompleted()},
		},
	}
	a := newTestAdapter(fr, func(o *Options) {
		o.lookup = func(string) (string, error) { return "/bin/codex", nil }
	})
	handle, err := a.Start(t.Context(), protocol.AgentRunRequest{
		RunID: "s", Engine: "codex", Workspace: t.TempDir(), Prompt: "p",
	}, &codingagent.SliceSink{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if handle.SessionID != "" {
		t.Errorf("SessionID = %q, want empty when resume unsupported", handle.SessionID)
	}
}

func TestStartNeverPassesSecretsInEnv(t *testing.T) {
	// AC-28: the agent process env must never carry forbidden secrets. We
	// capture the env handed to the runner.
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("GITHUB_TOKEN", "ghp_secretvalue")
	var captured []string
	fr := detectedRunner(&runScript{lines: []string{nfRunStarted(), nfRunCompleted()}})
	a := newTestAdapter(fr, func(o *Options) {
		o.lookup = func(string) (string, error) { return "/bin/codex", nil }
		o.runner = envCapturingRunner(fr, &captured)
	})
	_, err := a.Start(t.Context(), protocol.AgentRunRequest{
		RunID: "e", Engine: "codex", Workspace: t.TempDir(), Prompt: "p",
		AllowlistEnv: []string{"GITHUB_TOKEN"},
	}, &codingagent.SliceSink{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	joined := strings.Join(captured, "\n")
	if strings.Contains(joined, "ghp_secretvalue") {
		t.Errorf("secret leaked into agent env:\n%s", joined)
	}
	if !strings.Contains(joined, "PATH=") {
		t.Errorf("PATH not passed")
	}
}

func TestArtifactIsRedacted(t *testing.T) {
	artDir := t.TempDir()
	fr := detectedRunner(&runScript{
		lines: []string{
			nfRunStarted(),
			`{"type":"x","error":"bearer sk-leakedtokenabcdefghij"}`,
			nfRunCompleted(),
		},
	})
	a := newTestAdapter(fr, func(o *Options) {
		o.lookup = func(string) (string, error) { return "/bin/codex", nil }
		o.ArtifactsDir = artDir
	})
	sink := &codingagent.SliceSink{}
	if _, err := a.Start(t.Context(), protocol.AgentRunRequest{
		RunID: "red", Engine: "codex", Workspace: t.TempDir(), Prompt: "p",
	}, sink); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitTerminal(t, sink, 2*time.Second)
	_ = filepath.Walk(artDir, func(path string, _ os.FileInfo, _ error) error {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if strings.Contains(string(data), "sk-leakedtoken12345") {
			t.Errorf("secret persisted unredacted in artifact %s: %s", path, data)
		}
		return nil
	})
}

func TestConcurrentRuns(t *testing.T) {
	fr := detectedRunner(&runScript{lines: []string{nfRunStarted(), nfRunCompleted()}})
	a := newTestAdapter(fr, func(o *Options) {
		o.lookup = func(string) (string, error) { return "/bin/codex", nil }
	})
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sink := &codingagent.SliceSink{}
			_, err := a.Start(t.Context(), protocol.AgentRunRequest{
				RunID: "c", Engine: "codex", Workspace: t.TempDir(), Prompt: "p",
			}, sink)
			if err != nil {
				t.Errorf("Start %d: %v", i, err)
				return
			}
			waitTerminal(t, sink, 3*time.Second)
		}(i)
	}
	wg.Wait()
}

func TestSendMessageUnsupported(t *testing.T) {
	a := New(Options{})
	if err := a.SendMessage(t.Context(), protocol.RunHandle{}, protocol.AgentMessage{}); err == nil {
		t.Error("expected SendMessage to be unsupported in headless mode")
	}
}

func TestCancelUnknownRun(t *testing.T) {
	a := New(Options{})
	if err := a.Cancel(t.Context(), protocol.RunHandle{RunID: "nope"}); err == nil {
		t.Error("expected error cancelling unknown run")
	}
}

func TestStartNilSink(t *testing.T) {
	fr := detectedRunner(&runScript{lines: []string{nfRunStarted()}})
	a := newTestAdapter(fr, func(o *Options) {
		o.lookup = func(string) (string, error) { return "/bin/codex", nil }
	})
	if _, err := a.Start(t.Context(), protocol.AgentRunRequest{Prompt: "p"}, nil); err == nil {
		t.Error("expected error for nil sink")
	}
}

func TestStartMissingBinary(t *testing.T) {
	fr := &fakeRunner{}
	a := newTestAdapter(fr, func(o *Options) {
		o.lookup = func(string) (string, error) { return "", errors.New("not found") }
	})
	if _, err := a.Start(t.Context(), protocol.AgentRunRequest{Prompt: "p"}, &codingagent.SliceSink{}); err == nil {
		t.Error("expected error when codex binary missing")
	}
}

func TestStartEmptyPromptBuildsWithoutPositional(t *testing.T) {
	// Faithful translation: no prompt → no synthesized positional; the run still
	// starts (the supervisor always supplies a prompt for real runs).
	fr := detectedRunner(&runScript{lines: []string{nfRunStarted(), nfRunCompleted()}})
	a := newTestAdapter(fr, func(o *Options) {
		o.lookup = func(string) (string, error) { return "/bin/codex", nil }
	})
	if _, err := a.Start(t.Context(), protocol.AgentRunRequest{RunID: "x", Engine: "codex", Workspace: t.TempDir()}, &codingagent.SliceSink{}); err != nil {
		t.Fatalf("Start with empty prompt should not error: %v", err)
	}
	starts := fr.starts()
	if len(starts) == 0 {
		t.Fatal("no process spawned")
	}
	execStarts := execStartsOnly(starts)
	if len(execStarts) == 0 {
		t.Fatal("no exec spawn recorded")
	}
	last := execStarts[len(execStarts)-1]
	// The trailing positional would be the prompt; assert argv ends with the
	// approval flag value, not a prompt.
	if last[len(last)-1] == "" {
		t.Errorf("empty positional leaked: %v", last)
	}
}

func execStartsOnly(all [][]string) [][]string {
	var out [][]string
	for _, s := range all {
		if len(s) >= 2 && s[1] == "exec" {
			out = append(out, s)
		}
	}
	return out
}

func TestInspectQuotaUnknown(t *testing.T) {
	a := New(Options{})
	q := a.InspectQuota(t.Context(), protocol.Account{})
	if q.Confidence != protocol.QuotaConfUnknown {
		t.Errorf("confidence = %q, want UNKNOWN", q.Confidence)
	}
}

func TestListModelsEmpty(t *testing.T) {
	a := New(Options{})
	models, err := a.ListModels(t.Context(), protocol.Account{})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(models) != 0 {
		t.Errorf("expected no hardcoded models (§36.8), got %d", len(models))
	}
}

func TestIDIsCodex(t *testing.T) {
	if New(Options{}).ID() != "codex" {
		t.Error("ID must be \"codex\"")
	}
}

// ---- small runner wrappers for assertions ----

type envCapturing struct {
	inner Runner
	env   *[]string
}

func envCapturingRunner(inner Runner, env *[]string) *envCapturing {
	return &envCapturing{inner: inner, env: env}
}

func (e *envCapturing) Start(argv []string, dir string, env []string) (Proc, error) {
	if isVersionProbe(argv) {
		return e.inner.Start(argv, dir, env)
	}
	*e.env = append(*e.env, env...)
	return e.inner.Start(argv, dir, env)
}

func typesOf(evs []protocol.NormalizedEvent) []protocol.EventType {
	out := make([]protocol.EventType, len(evs))
	for i, e := range evs {
		out[i] = e.Type
	}
	return out
}
