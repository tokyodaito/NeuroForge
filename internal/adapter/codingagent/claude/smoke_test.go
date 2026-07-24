//go:build claudesmoke

// Package claude_test contains the opt-in smoke test that exercises the adapter
// against a REAL Claude Code install. It is excluded from normal and CI runs by
// the `claudesmoke` build tag (rule §36.5: no real paid calls in CI). Run with:
//
//	go test -tags claudesmoke ./internal/adapter/codingagent/claude/ -run TestSmoke -v
//
// The test auto-skips if `claude` is not on PATH or CLAUDE_SMOKE is not set, so
// it is safe to run the tag accidentally on a host without credentials.
package claude

import (
	"context"
	"os"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/protocol"
)

func TestSmokeRealClaudeDetect(t *testing.T) {
	if os.Getenv("CLAUDE_SMOKE") == "" {
		t.Skip("set CLAUDE_SMOKE=1 to run the real Claude Code smoke test")
	}
	a, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	d := a.Detect(ctx)
	if !d.Installed {
		t.Skipf("claude not installed: %s", d.Detail)
	}
	t.Logf("detected: %s", d.Detail)

	v := a.Version(ctx)
	if v.ProtocolVersion != protocol.ProtocolVersion {
		t.Fatalf("protocol = %d, want 1", v.ProtocolVersion)
	}
	t.Logf("engine version: %s", v.EngineVersion)

	h := a.Health(ctx, protocol.Account{})
	t.Logf("health: %s (%s)", h.Status, h.Detail)
}

func TestSmokeRealClaudeRun(t *testing.T) {
	if os.Getenv("CLAUDE_SMOKE") == "" {
		t.Skip("set CLAUDE_SMOKE=1 to run the real Claude Code smoke test")
	}
	a, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.binary(); err != nil {
		t.Skipf("claude not installed: %v", err)
	}

	sink := &codingagent.SliceSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	handle, err := a.Start(ctx, protocol.AgentRunRequest{
		RunID: "smoke", Engine: EngineID, Model: "haiku",
		Workspace: os.TempDir(), Prompt: "Reply with the single word: pong",
		Timeout: 45 * time.Second,
	}, sink)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	evs := waitTerminal(sink, 50*time.Second)
	if len(evs) == 0 {
		t.Fatal("no events")
	}
	if evs[0].Type != protocol.EventRunStarted {
		t.Errorf("first = %s, want run.started", evs[0].Type)
	}
	if !evs[len(evs)-1].Type.IsTerminal() {
		t.Fatalf("no terminal event: %v", typesOf(evs))
	}
	t.Logf("smoke session: %s", a.SessionID(handle.RunID))
	t.Logf("events: %v", typesOf(evs))
}
