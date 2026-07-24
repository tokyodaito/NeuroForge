//go:build codexsmoke

// This file is compiled ONLY when the "codexsmoke" build tag is set, so it is
// excluded from normal and CI test runs by construction. It exercises the
// adapter against a REAL, authenticated Codex CLI and therefore CAN make a paid
// API call — it is opt-in and guarded by an env var on top of the build tag.
//
// Run it explicitly:
//
//	CODEX_SMOKE=1 go test -tags codexsmoke -run TestCodexSmoke ./internal/adapter/codingagent/codex/...
//
// Rule §36.5 (no paid calls in CI) is preserved: the tag + env guard keep this
// out of every default invocation.
package codex

import (
	"context"
	"os"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/protocol"
)

func TestCodexSmoke(t *testing.T) {
	if os.Getenv("CODEX_SMOKE") != "1" {
		t.Skip("codex smoke test is opt-in; set CODEX_SMOKE=1 (and -tags codexsmoke) to run a real Codex CLI")
	}

	a := New(Options{}) // real proctree runner, real exec.LookPath detection
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	d := a.Detect(ctx)
	if !d.Installed {
		t.Skipf("codex not installed: %+v", d)
	}
	t.Logf("detected: %s (version %s)", d.Path, d.Version)
	t.Logf("capabilities: %+v", a.Capabilities(ctx))

	if h := a.Health(ctx, protocol.Account{Name: "default"}); h.Status == protocol.HealthDown {
		t.Skipf("codex health down: %s", h.Detail)
	}

	sink := &codingagent.SliceSink{}
	handle, err := a.Start(ctx, protocol.AgentRunRequest{
		RunID:     "smoke",
		Engine:    a.ID(),
		Workspace: os.TempDir(),
		Prompt:    "Reply with the single word: pong",
		Timeout:   60 * time.Second,
	}, sink)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Logf("handle: %+v", handle)

	evs := waitTerminalSmoke(sink, 90*time.Second)
	if len(evs) == 0 {
		t.Fatal("no events emitted")
	}
	t.Logf("event types: %v", typesOf(evs))
	if !evs[len(evs)-1].Type.IsTerminal() {
		t.Fatalf("run did not terminate; last = %s", evs[len(evs)-1].Type)
	}
}

func waitTerminalSmoke(sink *codingagent.SliceSink, timeout time.Duration) []protocol.NormalizedEvent {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		evs := sink.Events()
		if len(evs) > 0 && evs[len(evs)-1].Type.IsTerminal() {
			return evs
		}
		time.Sleep(50 * time.Millisecond)
	}
	return sink.Events()
}
