package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
	"neuroforge/internal/audit"
	"neuroforge/internal/storage"
)

// VerificationStatus is one check result captured in a continuation pack
// (spec §21.2 "verification:"). Empty means the check did not run.
type VerificationStatus string

const (
	VerificationPassed  VerificationStatus = "passed"
	VerificationFailed  VerificationStatus = "failed"
	VerificationSkipped VerificationStatus = "skipped"
)

// ContinuationPack is the durable artifact written when switching providers or
// recovering from a crash (spec §21.2). It carries the essential state needed
// to resume a run WITHOUT transferring the entire conversation.
//
// The fallback agent receives ONLY this pack (rendered via
// [RenderFallbackPrompt]); it never sees the prior run's full message history
// (spec §21.2: "do not transfer the entire conversation").
type ContinuationPack struct {
	WorkPackageID     string                        `json:"work_package_id"`
	SpecificationHash string                        `json:"specification_hash,omitempty"`
	BaseSHA           string                        `json:"base_sha"`
	CurrentSHA        string                        `json:"current_sha"`
	Completed         []string                      `json:"completed"`
	Remaining         []string                      `json:"remaining"`
	ChangesPatch      string                        `json:"changes_patch,omitempty"`
	Failures          []string                      `json:"failures,omitempty"`
	NextObjective     string                        `json:"next_objective,omitempty"`
	Verification      map[string]VerificationStatus `json:"verification,omitempty"`
	// OriginEngine records which engine produced the state the pack captures.
	// The fallback agent runs on a different engine (spec §21).
	OriginEngine string `json:"origin_engine,omitempty"`
}

// WriteContinuationPack writes a continuation pack to disk under artifactsDir and
// records its path durably. It is used for provider switching (§21) and crash
// recovery (AC-15, AC-27).
func WriteContinuationPack(ctx context.Context, db *storage.DB, rec *audit.Recorder, workspaceID, artifactsDir string, pack ContinuationPack) (string, error) {
	if err := os.MkdirAll(artifactsDir, 0o700); err != nil {
		return "", fmt.Errorf("supervisor: mkdir artifacts: %w", err)
	}

	data, err := json.MarshalIndent(pack, "", "  ")
	if err != nil {
		return "", fmt.Errorf("supervisor: marshal pack: %w", err)
	}

	ts := time.Now().UTC().Format("20060102-150405")
	filename := fmt.Sprintf("pack-%s-%s.json", workspaceID, ts)
	path := filepath.Join(artifactsDir, filename)

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("supervisor: write pack: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.CreateContinuationPack(ctx, storage.ContinuationPack{
		WorkspaceID:       workspaceID,
		FilePath:          path,
		SpecificationHash: pack.SpecificationHash,
		BaseSHA:           pack.BaseSHA,
		CurrentSHA:        pack.CurrentSHA,
		CreatedAt:         now,
	}); err != nil {
		return "", fmt.Errorf("supervisor: persist pack record: %w", err)
	}

	if rec != nil {
		_, _ = rec.Record(ctx, audit.Event{
			Type:    "agent.continuation_pack_written",
			Scope:   audit.ScopeTask,
			ScopeID: workspaceID,
			Actor:   audit.ActorDaemon,
			Payload: audit.Payload(
				"path", path,
				"base_sha", pack.BaseSHA,
				"current_sha", pack.CurrentSHA),
		})
	}
	return path, nil
}

// BuildPackFromRun constructs a ContinuationPack from the outcome of a run. It
// captures the current state so a fallback provider can pick up where the run
// left off (AC-15). Completed steps are deduplicated and sorted so a fallback
// agent never repeats finished work.
func BuildPackFromRun(workspaceID, workPackageID, baseSHA, headSHA, specHash string, result RunResult) ContinuationPack {
	pack := ContinuationPack{
		WorkPackageID:     workPackageID,
		SpecificationHash: specHash,
		BaseSHA:           baseSHA,
		CurrentSHA:        headSHA,
		NextObjective:     "Continue from the current checkpoint; do not redo completed steps.",
	}
	if result.Handle.Engine != "" {
		pack.OriginEngine = result.Handle.Engine
	}

	var failedClass string
	for _, ev := range result.Events {
		switch ev.Type {
		case protocol.EventCheckpointCreated:
			if ev.Checkpoint != nil {
				pack.Completed = append(pack.Completed, "checkpoint:"+ev.Checkpoint.Reason)
			}
		case protocol.EventFileChanged:
			if ev.FileChange != nil && ev.FileChange.InScope {
				pack.Completed = append(pack.Completed, "edit:"+ev.FileChange.Path)
			}
		case protocol.EventRunFailed:
			if ev.Failure != nil {
				failedClass = string(ev.Failure.Class)
			}
		}
	}

	if failedClass != "" {
		pack.Failures = append(pack.Failures, failedClass)
	}
	if result.Failed {
		pack.Remaining = append(pack.Remaining, "complete-the-objective")
	}
	pack.Completed = dedupeSorted(pack.Completed)
	pack.Failures = dedupeSorted(pack.Failures)
	return pack
}

// MergePacks folds a prior pack into a new one so a multi-hop failover
// accumulates progress across engines without losing earlier completed steps.
// The newer pack wins for current_sha / next_objective; completed lists are
// unioned.
func MergePacks(prior, latest ContinuationPack) ContinuationPack {
	merged := latest
	if prior.SpecificationHash != "" && merged.SpecificationHash == "" {
		merged.SpecificationHash = prior.SpecificationHash
	}
	if prior.BaseSHA != "" && merged.BaseSHA == "" {
		merged.BaseSHA = prior.BaseSHA
	}
	merged.Completed = dedupeSorted(append(append([]string{}, prior.Completed...), latest.Completed...))
	merged.Failures = dedupeSorted(append(append([]string{}, prior.Failures...), latest.Failures...))
	merged.Remaining = dedupeSorted(latest.Remaining)
	if merged.NextObjective == "" {
		merged.NextObjective = prior.NextObjective
	}
	if merged.OriginEngine == "" {
		merged.OriginEngine = prior.OriginEngine
	}
	if merged.Verification == nil && prior.Verification != nil {
		merged.Verification = prior.Verification
	}
	return merged
}

// RenderFallbackPrompt renders the prompt handed to a FALLBACK agent from a
// continuation pack (spec §21.2). It is the ONLY context a fallback agent
// receives about prior work — the full conversation history of the failed run
// is deliberately NOT transferred (spec §21.2: "do not transfer the entire
// conversation"). This keeps the context budget bounded and prevents a stale
// or poisoned transcript from leaking across engines.
//
// The prompt lists completed steps (so they are not repeated), the remaining
// work, the failure that triggered the switch, and the current checkpoint
// state. It is deterministic and contains no credentials (AC-28).
func RenderFallbackPrompt(pack ContinuationPack) string {
	var b strings.Builder
	b.WriteString("You are continuing a task that was interrupted on a different coding engine.\n")
	b.WriteString("Do NOT redo work that is already marked completed. Pick up from the current state.\n\n")

	if pack.NextObjective != "" {
		fmt.Fprintf(&b, "Objective:\n%s\n\n", pack.NextObjective)
	}
	fmt.Fprintf(&b, "Work package: %s\n", pack.WorkPackageID)
	if pack.BaseSHA != "" || pack.CurrentSHA != "" {
		fmt.Fprintf(&b, "Base SHA: %s\nCurrent SHA: %s\n", pack.BaseSHA, pack.CurrentSHA)
	}
	if pack.OriginEngine != "" {
		fmt.Fprintf(&b, "Prior engine: %s (failed; do not rely on its transcript)\n", pack.OriginEngine)
	}
	if len(pack.Completed) > 0 {
		b.WriteString("\nAlready completed (do not repeat):\n")
		for _, c := range pack.Completed {
			fmt.Fprintf(&b, "  - %s\n", c)
		}
	}
	if len(pack.Remaining) > 0 {
		b.WriteString("\nRemaining:\n")
		for _, r := range pack.Remaining {
			fmt.Fprintf(&b, "  - %s\n", r)
		}
	}
	if len(pack.Failures) > 0 {
		b.WriteString("\nFailures that triggered the switch:\n")
		for _, f := range pack.Failures {
			fmt.Fprintf(&b, "  - %s\n", f)
		}
	}
	if len(pack.Verification) > 0 {
		b.WriteString("\nVerification so far:\n")
		for _, k := range verificationKeysSorted(pack.Verification) {
			fmt.Fprintf(&b, "  - %s: %s\n", k, pack.Verification[k])
		}
	}
	if pack.ChangesPatch != "" {
		b.WriteString("\nCurrent changes are captured in the workspace checkpoint; review the diff there.\n")
	}
	return b.String()
}

// ReadContinuationPack reads a pack from disk (used on crash recovery, AC-27).
func ReadContinuationPack(path string) (ContinuationPack, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ContinuationPack{}, fmt.Errorf("supervisor: read continuation pack: %w", err)
	}
	var pack ContinuationPack
	if err := json.Unmarshal(data, &pack); err != nil {
		return ContinuationPack{}, fmt.Errorf("supervisor: parse continuation pack: %w", err)
	}
	return pack, nil
}

// dedupeSorted returns a sorted, de-duplicated copy of in.
func dedupeSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func verificationKeysSorted(m map[string]VerificationStatus) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
