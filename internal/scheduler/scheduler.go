// Package scheduler dispatches work packages to agent runs through the real
// daemon execution path (spec §10, milestone M2-8 follow-up).
//
// STATUS: implemented — this is the production composition root that wires the
// M12/M13 domain packages (quality, memory, repoinfo, postmerge) into the live
// daemon runtime. It is the single owner of the task execution path: a task
// flows scheduler → dispatcher (workspace) → supervisor (agent run), with usage
// events, context packs, project memory and quality statistics recorded on the
// way, and the post-merge sentinel driven after a merge.
//
// The scheduler holds NO provider-specific logic (rule: no provider-specific
// logic in scheduler). It consumes the stabilised adapter protocol via the
// injected Runtime. It never calls an LLM (rule §22.6) and never performs Git
// network operations (AC-7).
package scheduler

import (
	"context"
	"time"

	"neuroforge/internal/policy"
	"neuroforge/internal/quality"
)

// Runtime is the daemon-supplied execution surface. The scheduler calls it to
// create attempts (workspaces), run agents, and resolve project/task context.
// Defining it here keeps the scheduler free of a daemon import (the daemon is
// the composition root and implements this interface).
type Runtime interface {
	// CreateAttempt creates a workspace (attempt) for a task and returns the
	// workspace id + filesystem path of the isolated worktree.
	CreateAttempt(ctx context.Context, taskID, workPackageID, baseBranch string) (workspaceID, workspacePath string, err error)
	// RunAgent runs a coding agent inside the workspace at workspacePath with
	// the given engine/model/prompt. It returns the terminal outcome label
	// ("completed"/"failed"/"cancelled") and the raw event stream so the
	// scheduler can extract usage events.
	RunAgent(ctx context.Context, workspaceID, workspacePath, engine, model, prompt string, timeout time.Duration) (outcome string, events []AgentEvent, err error)
}

// AgentEvent is the minimal event shape the scheduler consumes from a run. It is
// a trimmed view of protocol.NormalizedEvent so the scheduler does not import
// the adapter protocol package (the Runtime adapter translates).
type AgentEvent struct {
	Type         string  // run.completed | run.failed | run.cancelled | usage.updated | file.changed
	InputTokens  int64   // valid for usage.updated
	OutputTokens int64   // valid for usage.updated
	CacheRead    int64   // valid for usage.updated (cached input, §22.8)
	CostUSD      float64 // valid for usage.updated
}

// ProjectContext is the resolved project+task+policy a dispatch operates on.
type ProjectContext struct {
	ProjectID   string
	ProjectPath string
	TaskID      string
	TaskDesc    string
	Profile     policy.Profile
	Resolved    policy.Resolved
}

// ProjectResolver resolves the project + task + policy context for a task. The
// daemon implements this against the live registries.
type ProjectResolver interface {
	Resolve(ctx context.Context, taskID string) (ProjectContext, error)
}

// TaskReopener reopens a task idempotently (used by the post-merge sentinel,
// §37). It is the same contract as postmerge.TaskReopener but defined here so
// the scheduler can pass itself as the reopener to the sentinel.
type TaskReopener interface {
	Reopen(ctx context.Context, taskID, reason string) error
}

// UsageSink durably persists a usage event (the daemon backs this with
// storage.RecordUsageEvent). The scheduler also feeds the in-process
// quality.Accounting so routing feedback signals stay live.
type UsageSink interface {
	RecordUsage(ctx context.Context, e quality.UsageEvent) error
}

// MemorySink reads and learns structured project memory (§22.9). The daemon
// backs this with storage + the in-process memory.Store.
type MemorySink interface {
	Learn(ctx context.Context, projectID string, category, key, value, confidence string) error
	Rules(ctx context.Context, projectID string) []string
}

// PostMergeSink durably persists a post-merge check result.
type PostMergeSink interface {
	RecordPostMerge(ctx context.Context, r PostMergeRecord) error
}

// PostMergeRecord is the durable record of a post-merge sentinel run (mirrors
// the post_merge_checks row).
type PostMergeRecord struct {
	TaskID     string
	CommitSHA  string
	BaseBranch string
	Decision   string
	AllPassed  bool
	Reverted   bool
	RevertSHA  string
	OccurredAt time.Time
}

// PackBuilder builds a token-budgeted Context Pack (§22.3) from a project repo
// path + task description + architectural rules. The daemon implements this
// against the repoinfo package; it never dumps the whole repo (rule §36.11).
type PackBuilder interface {
	BuildPack(ctx context.Context, projectPath, taskDesc string, rules []string) (string, error)
}
