//go:build kimismoke

// This file is compiled ONLY when the `kimismoke` build tag is set, so it never
// runs in normal/CI test runs (rule §36.5). It exercises the adapter against a
// REAL Kimi Code installation on PATH. Run explicitly via:
//
//	go test -tags kimismoke -run TestKimiSmoke ./internal/adapter/codingagent/kimi/...
//
// It is skipped under -short and when Kimi is not detected, so an accidental
// invocation without a real engine fails fast rather than hanging.
package kimi

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/protocol"
)

func TestKimiSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("kimi smoke test requires a real engine; skipping under -short")
	}
	if _, err := exec.LookPath("kimi"); err != nil {
		t.Skip("kimi not on PATH; skipping real-engine smoke test")
	}

	a := New(Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	d := a.Detect(ctx)
	if !d.Installed {
		t.Skipf("kimi detected but not usable: %+v", d)
	}

	sink := &codingagent.SliceSink{}
	handle, err := a.Start(ctx, protocol.AgentRunRequest{
		RunID: "smoke", Engine: "kimi", Workspace: t.TempDir(),
		Prompt: "Reply with exactly: ok", Timeout: 45 * time.Second,
	}, sink)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = a.Cancel(context.Background(), handle) }()

	// Wait for a terminal event.
	deadline := time.Now().Add(50 * time.Second)
	for time.Now().Before(deadline) {
		evs := sink.Events()
		if len(evs) > 0 && evs[len(evs)-1].Type.IsTerminal() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("kimi smoke run did not reach a terminal event")
}
