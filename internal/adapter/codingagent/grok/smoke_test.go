//go:build groksmoke

// This file is only compiled when the `groksmoke` build tag is set, so it never
// runs in normal/CI test invocations (rule §36.5: no real paid calls in CI).
//
// Run explicitly against an installed Grok CLI:
//
//	go test -tags groksmoke -run TestGrokSmoke -v ./internal/adapter/codingagent/grok/...
//
// It performs no paid work itself; it only probes metadata and (optionally, when
// GROK_SMOKE_RUN=1) starts a single short headless run in a throwaway temp
// workspace. Set GROK_SMOKE_RUN=1 to opt into the run; otherwise only detection
// / version / health / capabilities are exercised.

package grok

import (
	"context"
	"os"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/protocol"
)

func TestGrokSmokeMetadata(t *testing.T) {
	a := New(Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	d := a.Detect(ctx)
	if !d.Installed {
		t.Skipf("grok CLI not installed; skipping smoke test (detail: %s)", d.Detail)
	}
	t.Logf("detected: %+v", d)

	v := a.Version(ctx)
	if v.ProtocolVersion != protocol.ProtocolVersion {
		t.Fatalf("ProtocolVersion = %d, want %d", v.ProtocolVersion, protocol.ProtocolVersion)
	}
	t.Logf("version: %+v", v)

	h := a.Health(ctx, protocol.Account{})
	t.Logf("health: %+v", h)

	caps := a.Capabilities(ctx)
	t.Logf("capabilities: %+v", caps)
	if !caps.HeadlessMode {
		t.Error("installed grok should report headless mode")
	}
}

func TestGrokSmokeRun(t *testing.T) {
	if os.Getenv("GROK_SMOKE_RUN") != "1" {
		t.Skip("set GROK_SMOKE_RUN=1 to run a real (potentially paid) headless grok run")
	}
	a := New(Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	sink := &codingagent.SliceSink{}
	ws := t.TempDir()
	_, err := a.Start(ctx, protocol.AgentRunRequest{
		RunID: "smoke", Engine: a.ID(), Workspace: ws,
		Prompt: "Print the text 'smoke-ok' and exit.", Timeout: 45 * time.Second,
	}, sink)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	evs := waitForTerminal(sink, 50*time.Second)
	t.Logf("smoke produced %d events; last=%s", len(evs), lastType(evs))
	if len(evs) == 0 || !evs[len(evs)-1].Type.IsTerminal() {
		t.Fatalf("smoke run did not reach a terminal event")
	}
}
