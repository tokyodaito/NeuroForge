//go:build geminismoke

// This file is compiled ONLY with -tags geminismoke. In normal and CI test runs
// it is excluded entirely, so a real (paid) Gemini API call is never made (rule
// §36.5). Enable explicitly:
//
//	go test -tags geminismoke -run TestGeminiSmoke ./internal/adapter/codingagent/gemini/...
//
// The test additionally requires GEMINI_SMOKE=1 to be set and the Gemini CLI to
// be on PATH with working auth, so an accidental run still cannot spend quota.

package gemini

import (
	"context"
	"os"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/protocol"
)

func TestGeminiSmokeRealCLI(t *testing.T) {
	if os.Getenv("GEMINI_SMOKE") != "1" {
		t.Skip("set GEMINI_SMOKE=1 to run the real (paid) Gemini CLI smoke test")
	}
	if testing.Short() {
		t.Skip("smoke test is not a short test")
	}

	a := New(Options{Binary: "gemini", ArtifactsDir: t.TempDir()})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	d := a.Detect(ctx)
	if !d.Installed {
		t.Skipf("gemini CLI not installed/usable: %+v", d)
	}

	sink := &codingagent.SliceSink{}
	handle, err := a.Start(ctx, protocol.AgentRunRequest{
		RunID:  "smoke",
		Engine: a.ID(),
		Prompt: "Reply with exactly the word: pong",
	}, sink)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	_ = handle

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		evs := sink.Events()
		if len(evs) > 0 && evs[len(evs)-1].Type.IsTerminal() {
			last := evs[len(evs)-1]
			if last.Type != protocol.EventRunCompleted {
				t.Fatalf("run did not complete cleanly; last=%+v", last)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("smoke run did not reach a terminal event within 90s")
}
