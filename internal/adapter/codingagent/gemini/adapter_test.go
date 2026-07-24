package gemini

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/protocol"
)

// launchFn removed: test closures use the inline io.Reader signature.

func waitTerminal(s *codingagent.SliceSink, timeout time.Duration) []protocol.NormalizedEvent {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		evs := s.Events()
		if len(evs) > 0 && evs[len(evs)-1].Type.IsTerminal() {
			return evs
		}
		time.Sleep(3 * time.Millisecond)
	}
	return s.Events()
}

func lastType(evs []protocol.NormalizedEvent) protocol.EventType {
	if len(evs) == 0 {
		return ""
	}
	return evs[len(evs)-1].Type
}

func typesOf(evs []protocol.NormalizedEvent) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = string(e.Type)
	}
	return out
}

func TestRunSuccessEmitsOrderedEvents(t *testing.T) {
	doc := `{"response":{"text":"all done"},"usage":{"metadata":{"promptTokenCount":5,"candidatesTokenCount":7,"totalTokenCount":12,"cachedContentTokenCount":1}}}`
	h := &stubHost{
		launchFn: func([]string, string, []string, io.Reader) (launchedProcess, error) {
			return cannedProc([]byte(doc), 0, ""), nil
		},
	}
	a := newTestAdapter(h)
	sink := &codingagent.SliceSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	handle, err := a.Start(ctx, protocol.AgentRunRequest{
		RunID: "ok", Engine: "gemini", Model: "m1", Prompt: "hi",
	}, sink)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if handle.RunID != "ok" || handle.Engine != "gemini" || handle.Model != "m1" {
		t.Errorf("handle = %+v", handle)
	}
	evs := waitTerminal(sink, 3*time.Second)
	if len(evs) == 0 || evs[0].Type != protocol.EventRunStarted {
		t.Fatalf("first event = %v, want run.started", typesOf(evs))
	}
	if lastType(evs) != protocol.EventRunCompleted {
		t.Fatalf("last = %s, want run.completed: %v", lastType(evs), typesOf(evs))
	}
	hasMsg, hasUsage := false, false
	for _, e := range evs {
		if e.Type == protocol.EventMessageCompleted {
			hasMsg = true
		}
		if e.Type == protocol.EventUsageUpdated {
			hasUsage = true
		}
	}
	if !hasMsg {
		t.Errorf("message.completed missing: %v", typesOf(evs))
	}
	if !hasUsage {
		t.Errorf("usage.updated missing: %v", typesOf(evs))
	}
}

func TestRunFailureClassified(t *testing.T) {
	h := &stubHost{
		launchFn: func([]string, string, []string, io.Reader) (launchedProcess, error) {
			return cannedProc(nil, 2, "RESOURCE_EXHAUSTED: quota exceeded\n"), nil
		},
	}
	a := newTestAdapter(h)
	sink := &codingagent.SliceSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := a.Start(ctx, protocol.AgentRunRequest{RunID: "fail", Prompt: "hi"}, sink); err != nil {
		t.Fatal(err)
	}
	evs := waitTerminal(sink, 3*time.Second)
	if lastType(evs) != protocol.EventRunFailed {
		t.Fatalf("last = %s, want run.failed", lastType(evs))
	}
	last := evs[len(evs)-1]
	if last.Failure == nil || last.Failure.Class != protocol.FailureProviderQuota {
		t.Errorf("class = %+v, want PROVIDER_QUOTA", last.Failure)
	}
}

func TestRunMalformedDoesNotAbort(t *testing.T) {
	art := t.TempDir()
	h := &stubHost{
		launchFn: func([]string, string, []string, io.Reader) (launchedProcess, error) {
			return cannedProc([]byte("{not valid json"), 0, ""), nil
		},
	}
	a := newWithHost(Options{Binary: "gemini", ArtifactsDir: art}, h)
	sink := &codingagent.SliceSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := a.Start(ctx, protocol.AgentRunRequest{RunID: "mal", Prompt: "hi"}, sink); err != nil {
		t.Fatal(err)
	}
	evs := waitTerminal(sink, 3*time.Second)
	if !lastType(evs).IsTerminal() {
		t.Fatalf("malformed output aborted the run: %v", typesOf(evs))
	}
	hasWarn := false
	for _, e := range evs {
		if e.Type == protocol.EventWarning {
			hasWarn = true
		}
	}
	if !hasWarn {
		t.Errorf("no warning emitted for malformed output: %v", typesOf(evs))
	}
	entries, _ := os.ReadDir(art)
	if len(entries) == 0 {
		t.Errorf("no malformed artifact saved in %s", art)
	}
}

func TestRunCancellationKillsGroup(t *testing.T) {
	var proc *stubProc
	h := &stubHost{
		launchFn: func([]string, string, []string, io.Reader) (launchedProcess, error) {
			proc = hangingProc(137, "")
			return proc, nil
		},
	}
	a := newTestAdapter(h)
	sink := &codingagent.SliceSink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handle, err := a.Start(ctx, protocol.AgentRunRequest{RunID: "cancel", Prompt: "hi"}, sink)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond) // let the run reach its hang
	if err := a.Cancel(context.Background(), handle); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	evs := waitTerminal(sink, 3*time.Second)
	if lastType(evs) != protocol.EventRunCancelled {
		t.Fatalf("last = %s, want run.cancelled: %v", lastType(evs), typesOf(evs))
	}
	if !proc.killed.Load() {
		t.Error("process group was not killed on cancel")
	}
}

func TestRunTimeoutEmitsFailedTimeout(t *testing.T) {
	h := &stubHost{
		launchFn: func([]string, string, []string, io.Reader) (launchedProcess, error) {
			return hangingProc(0, ""), nil
		},
	}
	a := newTestAdapter(h)
	sink := &codingagent.SliceSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := a.Start(ctx, protocol.AgentRunRequest{
		RunID: "to", Prompt: "hi", Timeout: 60 * time.Millisecond,
	}, sink); err != nil {
		t.Fatal(err)
	}
	evs := waitTerminal(sink, 3*time.Second)
	if lastType(evs) != protocol.EventRunFailed {
		t.Fatalf("last = %s, want run.failed (timeout)", lastType(evs))
	}
	last := evs[len(evs)-1]
	if last.Failure == nil || last.Failure.Class != protocol.FailureTimeout {
		t.Errorf("class = %+v, want TIMEOUT", last.Failure)
	}
}

func TestRunSecretInStderrRedacted(t *testing.T) {
	secret := "AIzaSyAaBBccDDeeFFggHHiiJJkkLLmmNNoopp"
	h := &stubHost{
		launchFn: func([]string, string, []string, io.Reader) (launchedProcess, error) {
			return cannedProc(nil, 2, "error: "+secret+" rejected\n"), nil
		},
	}
	a := newTestAdapter(h)
	sink := &codingagent.SliceSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := a.Start(ctx, protocol.AgentRunRequest{RunID: "secret", Prompt: "hi"}, sink); err != nil {
		t.Fatal(err)
	}
	evs := waitTerminal(sink, 3*time.Second)
	for _, e := range evs {
		blob, _ := json.Marshal(e)
		if strings.Contains(string(blob), secret) {
			t.Errorf("secret leaked into event %s: %s", e.Type, blob)
		}
	}
}

func TestRunPromptFilePipesStdin(t *testing.T) {
	pf := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(pf, []byte("hello from file"), 0o644); err != nil {
		t.Fatal(err)
	}
	var seenStdin atomic.Bool
	h := &stubHost{
		launchFn: func(argv []string, _ string, _ []string, stdin io.Reader) (launchedProcess, error) {
			if stdin != nil {
				seenStdin.Store(true)
			}
			joined := strings.Join(argv, " ")
			if strings.Contains(joined, " -p ") {
				t.Errorf("-p should be omitted when piping via stdin: %s", joined)
			}
			return cannedProc([]byte(`{"response":{"text":"ok"}}`), 0, ""), nil
		},
	}
	a := newTestAdapter(h)
	sink := &codingagent.SliceSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := a.Start(ctx, protocol.AgentRunRequest{RunID: "pf", PromptFile: pf}, sink); err != nil {
		t.Fatal(err)
	}
	waitTerminal(sink, 3*time.Second)
	if !seenStdin.Load() {
		t.Error("stdin was not piped for PromptFile run")
	}
}

func TestRunWorkspaceIsCwd(t *testing.T) {
	ws := t.TempDir()
	var seen atomic.Bool
	h := &stubHost{
		launchFn: func(_ []string, dir string, _ []string, _ io.Reader) (launchedProcess, error) {
			if dir == ws {
				seen.Store(true)
			}
			return cannedProc([]byte(`{}`), 0, ""), nil
		},
	}
	a := newTestAdapter(h)
	sink := &codingagent.SliceSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := a.Start(ctx, protocol.AgentRunRequest{RunID: "ws", Prompt: "hi", Workspace: ws}, sink); err != nil {
		t.Fatal(err)
	}
	waitTerminal(sink, 3*time.Second)
	if !seen.Load() {
		t.Error("workspace was not set as the process CWD")
	}
}

func TestRunEnvIsAllowlisted(t *testing.T) {
	t.Setenv("FORGE_DAEMON_TOKEN", "should-not-leak")
	var seen atomic.Bool
	h := &stubHost{
		launchFn: func(_ []string, _ string, env []string, _ io.Reader) (launchedProcess, error) {
			joined := strings.Join(env, "\n")
			if strings.Contains(joined, "should-not-leak") {
				t.Errorf("daemon token leaked into launched env: %s", joined)
			}
			seen.Store(true)
			return cannedProc([]byte(`{}`), 0, ""), nil
		},
	}
	a := newTestAdapter(h)
	sink := &codingagent.SliceSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := a.Start(ctx, protocol.AgentRunRequest{RunID: "env", Prompt: "hi"}, sink); err != nil {
		t.Fatal(err)
	}
	waitTerminal(sink, 3*time.Second)
	if !seen.Load() {
		t.Error("launch not invoked")
	}
}

func TestResumeNotSupported(t *testing.T) {
	a := newTestAdapter(&stubHost{})
	_, err := a.Resume(context.Background(), protocol.ResumeRequest{RunID: "x"}, &codingagent.SliceSink{})
	if err != ErrSessionResumeNotSupported {
		t.Errorf("Resume err = %v, want ErrSessionResumeNotSupported", err)
	}
}

func TestSendMessageNotSupported(t *testing.T) {
	a := newTestAdapter(&stubHost{})
	err := a.SendMessage(context.Background(), protocol.RunHandle{RunID: "x"}, protocol.AgentMessage{Text: "hi"})
	if err != ErrLiveMessagesNotSupported {
		t.Errorf("SendMessage err = %v, want ErrLiveMessagesNotSupported", err)
	}
}

func TestCancelUnknownRun(t *testing.T) {
	a := newTestAdapter(&stubHost{})
	if err := a.Cancel(context.Background(), protocol.RunHandle{RunID: "nope"}); err == nil {
		t.Error("Cancel of unknown run should error")
	}
}

func TestStartCLINotFound(t *testing.T) {
	h := &stubHost{
		lookPathFn: func(string) (string, error) { return "", &notFoundError{name: "gemini"} },
	}
	a := newTestAdapter(h)
	_, err := a.Start(context.Background(), protocol.AgentRunRequest{Prompt: "hi"}, &codingagent.SliceSink{})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("Start with missing CLI should error, got %v", err)
	}
}

func TestConcurrentRuns(t *testing.T) {
	doc := []byte(`{"response":{"text":"ok"}}`)
	h := &stubHost{
		launchFn: func([]string, string, []string, io.Reader) (launchedProcess, error) {
			return cannedProc(doc, 0, ""), nil
		},
	}
	a := newTestAdapter(h)
	done := make(chan struct{}, 5)
	for i := 0; i < 5; i++ {
		go func(i int) {
			sink := &codingagent.SliceSink{}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := a.Start(ctx, protocol.AgentRunRequest{RunID: "c", Prompt: "hi"}, sink); err != nil {
				t.Errorf("Start %d: %v", i, err)
			}
			waitTerminal(sink, 3*time.Second)
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 5; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent run timed out")
		}
	}
}
