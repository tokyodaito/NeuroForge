// Package m12integration contains the M12 integration tests that exercise the
// post-merge sentinel + auto-revert and the token-optimisation context pipeline
// (spec §22, §37, milestone M12).
//
// These tests compose the M12 domain packages (repoinfo, postmerge, memory,
// quality) with deterministic fakes (rule §33: no real paid providers in CI).
// They verify the critical M12 invariants:
//
//   - §37:   a post-merge regression triggers auto-revert (AUTONOMOUS only) and
//     reopens the task.
//   - §22.1: the Context Pack never exceeds its token budget (no full repo dump).
//   - §22.5: the delta repair context does not replay the full conversation.
//   - §22.9: project memory feeds architectural rules into the Context Pack.
//   - §4.4:  auto-revert is structurally disabled outside AUTONOMOUS.
package m12integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"neuroforge/internal/memory"
	"neuroforge/internal/policy"
	"neuroforge/internal/postmerge"
	"neuroforge/internal/quality"
	"neuroforge/internal/repoinfo"
)

// --- shared fixtures ---

func writeRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module demo\n\ngo 1.23\n")
	write("user/user.go", "package user\n\ntype User struct {\n\tName string\n}\n\nfunc (u *User) Greet() string {\n\treturn u.Name\n}\n")
	write("user/user_test.go", "package user\n\nimport \"testing\"\n\nfunc TestGreet(t *testing.T) {}\n")
	write("server/server.go", "package server\n\nfunc Handler() {}\n")
	return root
}

func autonomous(autoRevert bool) policy.Resolved {
	p := policy.ProfileDefaults(policy.ProfileAutonomous)
	p.PostMerge = policy.PostMergeConfig{Enabled: true, AutoRevert: autoRevert}
	return policy.Resolved{Profile: policy.ProfileAutonomous, Pipeline: p}
}

func remoteReview() policy.Resolved {
	p := policy.ProfileDefaults(policy.ProfileRemoteReview)
	return policy.Resolved{Profile: policy.ProfileRemoteReview, Pipeline: p}
}

type fakeCheck struct {
	name string
	st   postmerge.SmokeStatus
}

func (f fakeCheck) Name() string { return f.name }
func (f fakeCheck) Run(context.Context) postmerge.CheckResult {
	return postmerge.CheckResult{Name: f.name, Status: f.st}
}

type fakeReverter struct {
	called bool
	sha    string
	err    error
}

func (f *fakeReverter) Revert(context.Context, string, string, string, int) (string, error) {
	f.called = true
	return f.sha, f.err
}

type fakeReopener struct {
	reopened []string
}

func (f *fakeReopener) Reopen(_ context.Context, taskID, _ string) error {
	f.reopened = append(f.reopened, taskID)
	return nil
}

// --- scenario: post-merge regression -> auto-revert + task reopen (§37, §4.4) ---

func TestPostMergeRegressionTriggersAutoRevertAndReopen(t *testing.T) {
	rev := &fakeReverter{sha: "revertSHA"}
	reo := &fakeReopener{}
	sentinel := postmerge.NewSentinel([]postmerge.SmokeCheck{
		fakeCheck{"build", postmerge.SmokePassed},
		fakeCheck{"smoke-tests", postmerge.SmokeFailed},
	}, rev, reo)

	out := postmerge.MergeOutcome{TaskID: "WORK-9", CommitSHA: "merged123", BaseBranch: "main", Number: 7, MergedAt: time.Now()}
	res, err := sentinel.Run(context.Background(), autonomous(true), out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.Decision != postmerge.DecisionRevert {
		t.Fatalf("decision = %s, want REVERT", res.Decision)
	}
	if !res.Reverted || res.RevertSHA != "revertSHA" {
		t.Errorf("auto-revert not recorded: %+v", res)
	}
	if !rev.called {
		t.Errorf("reverter not invoked through the authority path")
	}
	if len(reo.reopened) != 1 || reo.reopened[0] != "WORK-9" {
		t.Errorf("task not reopened: %v", reo.reopened)
	}
	if postmerge.ReopenState(res) != postmerge.TaskReopenReopened {
		t.Errorf("task lifecycle should be REOPENED after auto-revert (§37)")
	}
}

// --- scenario: healthy post-merge keeps the task closed ---

func TestHealthyPostMergeKeepsTaskClosed(t *testing.T) {
	reo := &fakeReopener{}
	sentinel := postmerge.NewSentinel([]postmerge.SmokeCheck{
		fakeCheck{"build", postmerge.SmokePassed},
	}, nil, reo)
	res, _ := sentinel.Run(context.Background(), autonomous(true), postmerge.MergeOutcome{TaskID: "T1"})
	if res.Decision != postmerge.DecisionHealthy {
		t.Errorf("decision = %s want HEALTHY", res.Decision)
	}
	if len(reo.reopened) != 0 {
		t.Errorf("task should stay closed when healthy")
	}
}

// --- scenario: auto-revert structurally disabled outside AUTONOMOUS (§4.4) ---

func TestAutoRevertDisabledOutsideAutonomous(t *testing.T) {
	rev := &fakeReverter{sha: "x"}
	reo := &fakeReopener{}
	sentinel := postmerge.NewSentinel([]postmerge.SmokeCheck{
		fakeCheck{"smoke", postmerge.SmokeFailed},
	}, rev, reo)
	// REMOTE_REVIEW never enables merge/post-merge (§4.3).
	res, err := sentinel.Run(context.Background(), remoteReview(), postmerge.MergeOutcome{TaskID: "T2"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != postmerge.DecisionSkipped {
		t.Errorf("decision = %s want SKIPPED outside AUTONOMOUS", res.Decision)
	}
	if rev.called {
		t.Errorf("reverter must never run outside AUTONOMOUS")
	}
}

// --- scenario: failed revert downgrades to alert + reopen (no silent failure) ---

func TestFailedRevertDowngradesAndReopens(t *testing.T) {
	rev := &fakeReverter{err: errors.New("provider unavailable")}
	reo := &fakeReopener{}
	sentinel := postmerge.NewSentinel([]postmerge.SmokeCheck{
		fakeCheck{"smoke", postmerge.SmokeFailed},
	}, rev, reo)
	res, err := sentinel.Run(context.Background(), autonomous(true), postmerge.MergeOutcome{TaskID: "T3"})
	if err == nil {
		t.Fatal("expected error when revert fails")
	}
	if res.Decision != postmerge.DecisionAlertOnly {
		t.Errorf("decision = %s want ALERT_ONLY after revert failure", res.Decision)
	}
	if len(reo.reopened) != 1 {
		t.Errorf("task must be reopened for human review when revert fails")
	}
}

// --- scenario: context budget is respected (§22.1: no full repo dump) ---

func TestContextPackRespectsBudgetAndFeedsMemory(t *testing.T) {
	root := writeRepo(t)
	idx, err := repoinfo.Build(root)
	if err != nil {
		t.Fatal(err)
	}

	// Project memory feeds architectural rules into the pack (§22.9 -> §22.3).
	mem := memory.NewStore("proj-1")
	mem.Learn(memory.Record{
		Key:        "no-core-in-adapters",
		Category:   memory.CatArchitectureFact,
		Value:      "Adapters must not import core packages.",
		Confidence: memory.ConfidenceHigh,
	})
	mem.Learn(memory.Record{
		Key:      "known-flake",
		Category: memory.CatKnownFailure,
		Value:    "server tests flake under load; retry once",
	})

	budget := 700
	pack, err := idx.AssemblePack(repoinfo.PackOptions{
		Specification:      "Implement a user greeting.",
		AllowedScope:       []string{"user/user.go"},
		QueryTerms:         []string{"user", "greet"},
		ArchitecturalRules: mem.HighConfidenceRules(),
		RecentFailures:     mem.KnownFailures(),
		Commands:           idx.BuildCmds,
		Budget:             budget,
		MaxFiles:           8,
		ExcerptLines:       50,
	})
	if err != nil {
		t.Fatal(err)
	}

	if pack.EstimatedTokens > budget+50 {
		t.Errorf("pack exceeded token budget (§22.1): %d > %d", pack.EstimatedTokens, budget)
	}
	// Architectural rule from memory must be present.
	foundRule := false
	for _, r := range pack.ArchitecturalRules {
		if strings.Contains(r, "Adapters must not import") {
			foundRule = true
		}
	}
	if !foundRule {
		t.Errorf("architectural rule from project memory missing from pack")
	}
	// The allowed-scope file must be included.
	found := false
	for _, f := range pack.RelevantFiles {
		if f.Path == "user/user.go" {
			found = true
		}
	}
	if !found {
		t.Errorf("allowed-scope file not in pack")
	}
}

// --- scenario: log slicing + delta repair stays small (§22.4/§22.5) ---

func TestLogSliceAndDeltaRepairAreSmall(t *testing.T) {
	raw := makeLargeLog()
	slice := repoinfo.SliceLog(raw, 1, "go test ./...", "/logs/full.log")
	if slice.EstimatedTokens > repoinfo.MaxLogTokens {
		t.Errorf("log slice too big: %d (§22.4)", slice.EstimatedTokens)
	}
	if slice.FirstError == "" {
		t.Errorf("first error not captured")
	}

	root := writeRepo(t)
	idx, err := repoinfo.Build(root)
	if err != nil {
		t.Fatal(err)
	}
	delta, err := idx.AssembleDelta(repoinfo.DeltaOptions{
		Finding:         "Greet returns empty",
		Severity:        "major",
		Diff:            "- return u.Name\n+ return \"\"",
		FailingTest:     "TestGreet",
		FailingTestLog:  slice.Render(),
		ImplicatedPaths: []string{"user/user.go"},
		NextObjective:   "Return a non-empty default greeting.",
	})
	if err != nil {
		t.Fatal(err)
	}
	rendered := delta.Render()
	if strings.Contains(rendered, "full research history") {
		t.Errorf("delta must not replay full history (§22.5)")
	}
	if delta.EstimatedTokens > repoinfo.DefaultDeltaBudget+50 {
		t.Errorf("delta exceeds budget (§22.5): %d", delta.EstimatedTokens)
	}
}

func makeLargeLog() string {
	var b strings.Builder
	b.WriteString("building...\ncompiling\n")
	for i := 0; i < 500; i++ {
		b.WriteString("verbose progress line that is noise\n")
	}
	b.WriteString("user/user_test.go:14: TestGreet failed\n")
	b.WriteString("\tuser/user_test.go:15: expected non-empty greeting\n")
	b.WriteString("\tuser/user_test.go:16: got empty string\n")
	for i := 0; i < 200; i++ {
		b.WriteString("more noise\n")
	}
	return b.String()
}

// --- scenario: prompt-cache fingerprint stable (§22.8) ---

func TestPromptCacheFingerprintStableAcrossRuns(t *testing.T) {
	idx, err := repoinfo.Build(writeRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	rules := []string{"rule-a", "rule-b"}
	cmds := idx.BuildCmds

	fp1 := repoinfo.FingerprintPrompt(append(append([]string{}, rules...), cmds...))
	// Re-render with different map iteration order → same fingerprint.
	fp2 := repoinfo.FingerprintPrompt(append(append([]string{}, cmds...), rules...))
	if !repoinfo.IsCacheHit(fp1, fp2) {
		t.Errorf("stable prefix not cache-hit (§22.8)")
	}
	// StablePrefix is byte-deterministic for the same logical inputs regardless
	// of the order within each slice (§22.8).
	a := repoinfo.StablePrefix([]string{"rule-b", "rule-a"}, []string{"cmd2", "cmd1"}, "MAP")
	b := repoinfo.StablePrefix([]string{"rule-a", "rule-b"}, []string{"cmd1", "cmd2"}, "MAP")
	if a != b {
		t.Errorf("StablePrefix not deterministic (§22.8)")
	}
}

// --- scenario: token accounting + quality stats feed routing signals (§6.1/§19.1) ---

func TestTokenAccountingAndQualityStats(t *testing.T) {
	acc := quality.NewAccounting()
	acc.Record(quality.UsageEvent{
		TaskID: "T1", ProjectID: "P1", Provider: "codex", Model: "alpha",
		Kind: quality.UsageCoding, InputTokens: 2000, CachedInputTokens: 800,
		OutputTokens: 300, CostUSD: 0.4,
	})
	stats := quality.NewStatistics()
	stats.Record(quality.TaskOutcome{
		TaskID: "T1", Engine: "codex", Model: "alpha", RouteTier: "STANDARD",
		Outcome: quality.OutcomeSuccess, TokensUsed: 3100, CostUSD: 0.4,
	})
	stats.Record(quality.TaskOutcome{
		TaskID: "T2", Engine: "codex", Model: "alpha", RouteTier: "STANDARD",
		Outcome: quality.OutcomeFailure, RepairIterations: 2, Findings: 4,
	})

	snap := acc.Snapshot()
	if snap.CodingInput != 2000 || snap.CachedInput != 800 {
		t.Errorf("accounting wrong: %+v", snap)
	}
	if snap.CacheHitRatio() < 0.28 || snap.CacheHitRatio() > 0.29 {
		t.Errorf("cache ratio = %v want ~0.286", snap.CacheHitRatio())
	}
	byModel := stats.SuccessRateByModel()
	if len(byModel) != 1 || byModel[0].SuccessRate < 0.49 || byModel[0].SuccessRate > 0.51 {
		t.Errorf("per-model success rate wrong: %+v", byModel)
	}
}
