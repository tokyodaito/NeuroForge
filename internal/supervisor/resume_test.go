package supervisor_test

import (
	"strings"
	"testing"

	"neuroforge/internal/adapter/codingagent/protocol"
	"neuroforge/internal/supervisor"
)

func TestResumePolicy_FailoverMeansCleanRestart(t *testing.T) {
	p := supervisor.NewResumePolicy()
	d := p.Decide(supervisor.ResumeInput{
		Decision:     supervisor.RecoveryDecision{Action: supervisor.ActionFailover},
		CurrentRoute: supervisor.Route{Engine: "codex", Model: "m1"},
		Fallback:     supervisor.Route{Engine: "kimi", Model: "m2"},
	})
	if d.Choice != supervisor.CleanRestart {
		t.Fatalf("failover: want clean_restart, got %s", d.Choice)
	}
	if d.OnRoute.Engine != "kimi" {
		t.Fatalf("failover route: want kimi, got %s", d.OnRoute.Engine)
	}
}

func TestResumePolicy_RetryWithResumeCapability(t *testing.T) {
	p := supervisor.NewResumePolicy()
	d := p.Decide(supervisor.ResumeInput{
		Decision:     supervisor.RecoveryDecision{Action: supervisor.ActionRetry},
		CanResume:    true,
		CurrentRoute: supervisor.Route{Engine: "codex"},
	})
	if d.Choice != supervisor.ResumeSession {
		t.Fatalf("retry + can-resume: want resume, got %s", d.Choice)
	}
}

func TestResumePolicy_RetryWithoutResumeCapability(t *testing.T) {
	p := supervisor.NewResumePolicy()
	d := p.Decide(supervisor.ResumeInput{
		Decision:     supervisor.RecoveryDecision{Action: supervisor.ActionRetry},
		CanResume:    false,
		CurrentRoute: supervisor.Route{Engine: "codex"},
	})
	if d.Choice != supervisor.CleanRestart {
		t.Fatalf("retry + cannot-resume: want clean_restart, got %s", d.Choice)
	}
}

func TestCanResume(t *testing.T) {
	caps := protocol.AgentCapabilities{SessionResume: true}
	if !supervisor.CanResume(caps, "sess-1", "/path/pack.json") {
		t.Error("expected CanResume true")
	}
	if supervisor.CanResume(caps, "", "/path/pack.json") {
		t.Error("expected CanResume false without session")
	}
	if supervisor.CanResume(protocol.AgentCapabilities{SessionResume: false}, "s", "p") {
		t.Error("expected CanResume false without capability")
	}
}

func TestRenderFallbackPrompt_NoFullConversation(t *testing.T) {
	pack := supervisor.ContinuationPack{
		WorkPackageID: "TASK-1-ui",
		BaseSHA:       "abc",
		CurrentSHA:    "def",
		OriginEngine:  "codex",
		NextObjective: "Fix the visual difference on the subscription card.",
		Completed:     []string{"checkpoint:plan", "edit:src/ui/Card.tsx", "checkpoint:first-diff"},
		Remaining:     []string{"fix-visual-difference", "run-verification"},
		Failures:      []string{"PROVIDER_QUOTA"},
		Verification: map[string]supervisor.VerificationStatus{
			"build":      supervisor.VerificationPassed,
			"screenshot": supervisor.VerificationFailed,
		},
	}
	prompt := supervisor.RenderFallbackPrompt(pack)

	// The prompt MUST explicitly tell the agent not to redo completed work.
	if !strings.Contains(prompt, "do not repeat") && !strings.Contains(prompt, "Do NOT redo") {
		t.Errorf("prompt must instruct not to redo work; got:\n%s", prompt)
	}
	// The completed steps must appear so the agent knows what is done.
	for _, c := range pack.Completed {
		if !strings.Contains(prompt, c) {
			t.Errorf("prompt missing completed step %q", c)
		}
	}
	// The failure that triggered the switch must appear.
	if !strings.Contains(prompt, "PROVIDER_QUOTA") {
		t.Error("prompt missing failure class")
	}
	// CRITICAL: the prompt must NOT carry a conversation transcript. The pack
	// has no transcript field, so verify the prompt does not contain the
	// sentinel that would indicate raw history was attached.
	if strings.Contains(prompt, "<transcript>") || strings.Contains(prompt, "message.delta") {
		t.Errorf("prompt must not contain conversation transcript")
	}
	// The origin engine is named so the fallback knows NOT to trust its transcript.
	if !strings.Contains(prompt, "codex") {
		t.Error("prompt should name the prior engine")
	}
}

func TestMergePacks_UnionCompleted(t *testing.T) {
	prior := supervisor.ContinuationPack{
		Completed:     []string{"edit:a", "checkpoint:plan"},
		BaseSHA:       "abc",
		OriginEngine:  "codex",
		NextObjective: "first",
	}
	latest := supervisor.ContinuationPack{
		Completed:     []string{"edit:b", "checkpoint:plan"},
		CurrentSHA:    "def",
		NextObjective: "second",
	}
	merged := supervisor.MergePacks(prior, latest)
	want := []string{"checkpoint:plan", "edit:a", "edit:b"}
	if len(merged.Completed) != len(want) {
		t.Fatalf("merged completed len = %d, want %d (%v)", len(merged.Completed), len(want), merged.Completed)
	}
	for i, c := range want {
		if merged.Completed[i] != c {
			t.Errorf("merged[%d] = %q, want %q", i, merged.Completed[i], c)
		}
	}
	if merged.CurrentSHA != "def" {
		t.Errorf("latest current_sha should win, got %s", merged.CurrentSHA)
	}
	if merged.NextObjective != "second" {
		t.Errorf("latest objective should win, got %s", merged.NextObjective)
	}
}

func TestBuildPackFromRun_DedupesCompleted(t *testing.T) {
	result := supervisor.RunResult{
		Handle: protocol.RunHandle{Engine: "codex", SessionID: "s1"},
		Events: []protocol.NormalizedEvent{
			{Type: protocol.EventFileChanged, FileChange: &protocol.FileChangePayload{Path: "src/a.go", Action: "modified", InScope: true}},
			{Type: protocol.EventFileChanged, FileChange: &protocol.FileChangePayload{Path: "src/a.go", Action: "modified", InScope: true}},
			{Type: protocol.EventFileChanged, FileChange: &protocol.FileChangePayload{Path: "OUTSIDE/secret", Action: "created", InScope: false}},
			{Type: protocol.EventRunFailed, Failure: &protocol.FailurePayload{Class: protocol.FailureProviderQuota, Reason: "quota"}},
		},
		Failed: true,
	}
	pack := supervisor.BuildPackFromRun("ws-1", "wp-1", "abc", "def", "spec", result)
	// The duplicate edit:a.go should collapse; the out-of-scope edit excluded.
	wantCompleted := []string{"edit:src/a.go"}
	if len(pack.Completed) != len(wantCompleted) {
		t.Fatalf("completed = %v, want %v", pack.Completed, wantCompleted)
	}
	if pack.Completed[0] != wantCompleted[0] {
		t.Errorf("completed[0] = %q, want %q", pack.Completed[0], wantCompleted[0])
	}
	if pack.OriginEngine != "codex" {
		t.Errorf("origin engine = %q, want codex", pack.OriginEngine)
	}
	if len(pack.Failures) != 1 || pack.Failures[0] != "PROVIDER_QUOTA" {
		t.Errorf("failures = %v, want [PROVIDER_QUOTA]", pack.Failures)
	}
}
