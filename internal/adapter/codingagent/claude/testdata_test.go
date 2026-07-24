package claude

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/protocol"
)

// TestRecordedFixtureFromDisk drives the adapter from a real on-disk recorded
// Claude Code stream-json file (rule §36.5: deterministic, offline). It proves
// the "recorded byte-stream fixture" workflow end-to-end against the recorded
// shape under testdata/, exercising tool.started/tool.completed + cached-token
// usage in addition to the inline fixtures used elsewhere.
func TestRecordedFixtureFromDisk(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "recorded_success.jsonl"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	spec := replaySpec{lines: splitLines(string(data)), exitCode: 0}
	a, err := New(Options{
		BinaryPath:   "claude",
		Spawn:        replaySpawner(spec),
		ArtifactsDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sink := &codingagent.SliceSink{}
	handle, err := a.Start(context.Background(), protocol.AgentRunRequest{
		RunID: "rec", Engine: a.ID(), Model: "sonnet", Workspace: os.TempDir(),
	}, sink)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	evs := waitTerminal(sink, 3*time.Second)

	types := typesOf(evs)
	if len(types) == 0 || types[0] != string(protocol.EventRunStarted) {
		t.Fatalf("first = %v, want run.started", types)
	}
	if types[len(types)-1] != string(protocol.EventRunCompleted) {
		t.Fatalf("last = %v, want run.completed", types)
	}
	joined := strings.Join(types, " ")
	if !strings.Contains(joined, "tool.started") || !strings.Contains(joined, "tool.completed") {
		t.Errorf("expected tool events in: %s", joined)
	}
	// Cached-token usage from the recorded result must be present.
	var usage *protocol.UsagePayload
	for _, e := range evs {
		if e.Type == protocol.EventUsageUpdated {
			usage = e.Usage
		}
	}
	if usage == nil {
		t.Fatal("usage.updated missing")
	}
	if usage.CacheReadTokens != 3201 || usage.CacheWriteTokens != 5110 {
		t.Errorf("cached tokens wrong: %+v", usage)
	}
	if usage.InputTokens != 10234 || usage.OutputTokens != 1289 {
		t.Errorf("tokens wrong: %+v", usage)
	}
	if usage.Confidence != protocol.QuotaConfProviderReported {
		t.Errorf("confidence = %q", usage.Confidence)
	}
	if got := a.SessionID(handle.RunID); got != "" {
		// Session is captured but the run already untracked after completion.
		t.Logf("session after completion: %q (run untracked)", got)
	}
}

func splitLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
