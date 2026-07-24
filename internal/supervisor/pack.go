package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
	"neuroforge/internal/audit"
	"neuroforge/internal/storage"
)

// ContinuationPack is the durable artifact written when switching providers or
// recovering from a crash (spec §21.2). It carries the essential state needed
// to resume a run WITHOUT transferring the entire conversation.
type ContinuationPack struct {
	WorkPackageID     string   `json:"work_package_id"`
	SpecificationHash string   `json:"specification_hash,omitempty"`
	BaseSHA           string   `json:"base_sha"`
	CurrentSHA        string   `json:"current_sha"`
	Completed         []string `json:"completed"`
	Remaining         []string `json:"remaining"`
	ChangesPatch      string   `json:"changes_patch,omitempty"`
	Failures          []string `json:"failures,omitempty"`
	NextObjective     string   `json:"next_objective,omitempty"`
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
// left off (AC-15).
func BuildPackFromRun(workspaceID, workPackageID, baseSHA, headSHA, specHash string, result RunResult) ContinuationPack {
	pack := ContinuationPack{
		WorkPackageID:     workPackageID,
		SpecificationHash: specHash,
		BaseSHA:           baseSHA,
		CurrentSHA:        headSHA,
		NextObjective:     "Continue from checkpoint.",
	}

	for _, ev := range result.Events {
		switch ev.Type {
		case protocol.EventCheckpointCreated:
			if ev.Checkpoint != nil {
				pack.Completed = append(pack.Completed, "checkpoint:"+ev.Checkpoint.Reason)
			}
		case protocol.EventFileChanged:
			if ev.FileChange != nil {
				pack.Completed = append(pack.Completed, "edit:"+ev.FileChange.Path)
			}
		case protocol.EventRunFailed:
			if ev.Failure != nil {
				pack.Failures = append(pack.Failures, string(ev.Failure.Class))
			}
		}
	}

	if result.Failed {
		pack.Remaining = append(pack.Remaining, "retry-after-failure")
	}
	return pack
}
