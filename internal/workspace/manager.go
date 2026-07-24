package workspace

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"neuroforge/internal/audit"
	"neuroforge/internal/storage"
)

// State is the lifecycle state of a workspace.
type State string

const (
	// StatePending: the workspace record exists but the worktree has not been
	// created yet (or is being created).
	StatePending State = "pending"
	// StateActive: the worktree is created and the agent may run or be running.
	StateActive State = "active"
	// StateCompleted: the agent finished and the result branch is ready.
	StateCompleted State = "completed"
	// StateKept: the user chose to keep the result; the workspace is retained.
	StateKept State = "kept"
	// StateRejected: the user rejected the result; the workspace is deleted.
	StateRejected State = "rejected"
	// StateFailed: the agent run failed; the workspace is retained for
	// inspection.
	StateFailed State = "failed"
	// StateWaitingQuota: every route in the chain is quota-exhausted; the work
	// package is parked until an account resets (spec §15.5, §20.3, §32
	// PROVIDER_QUOTA). It is NOT terminal — it resumes automatically.
	StateWaitingQuota State = "waiting_quota"
	// StateQuarantined: an unrecoverable failure (protocol error, exhausted
	// retries, no fallback) requires human attention before the work can
	// continue (spec §28 QUARANTINE, §32). Terminal-ish; a human un-quarantines.
	StateQuarantined State = "quarantined"
	// StateDeleted: the worktree has been removed.
	StateDeleted State = "deleted"
)

// Workspace is the domain-level workspace entity.
type Workspace struct {
	ID            string
	ProjectID     string
	TaskID        string
	WorkPackageID string
	Attempt       int
	Kind          string // "attempt" | "result"
	Path          string // managed worktree filesystem path
	Branch        string // forge/<task>/<wp>/attempt-<n>
	ResultBranch  string // forge/result/<task> (when created)
	BaseSHA       string
	HeadSHA       string
	ResultSHA     string
	State         State
	Engine        string
	Model         string
	RunID         string
	SessionID     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// CheckpointMoment enumerates the checkpoint creation moments (spec §21.3).
type CheckpointMoment string

const (
	MomentPlan           CheckpointMoment = "plan"
	MomentFirstDiff      CheckpointMoment = "first-diff"
	MomentCompile        CheckpointMoment = "compile"
	MomentTests          CheckpointMoment = "tests"
	MomentScreenshot     CheckpointMoment = "screenshot"
	MomentPreQuotaSwitch CheckpointMoment = "pre-quota-switch"
	MomentPreRepair      CheckpointMoment = "pre-repair"
	MomentPreIntegration CheckpointMoment = "pre-integration"
	MomentManual         CheckpointMoment = "manual"
)

// Checkpoint is a durable record of a checkpoint commit.
type Checkpoint struct {
	ID        int64
	Workspace string
	CommitSHA string
	Moment    CheckpointMoment
	Message   string
	CreatedAt time.Time
}

// CreateRequest is the input for creating a new workspace.
type CreateRequest struct {
	ProjectID     string
	ProjectPath   string // the user's primary checkout (read-only)
	TaskID        string
	WorkPackageID string // defaults to "main"
	BaseBranch    string // defaults to the repo's current HEAD
}

// Manager owns the Git worktree lifecycle: creating worktrees, making
// checkpoint commits, building local result branches, and the review lifecycle.
//
// Security invariants (spec §17.1, §4.2, AC-7, AC-8, ADR-0007/0008):
//   - The user's primary checkout is NEVER modified. All writes happen inside
//     managed worktrees under the NeuroForge home.
//   - Zero Git network operations are performed: the git runner enforces an
//     allowlist that excludes push/fetch/pull/clone/ls-remote/etc.
//   - Task branches are never sent to a remote.
type Manager struct {
	db             *storage.DB
	audit          *audit.Recorder
	logger         *slog.Logger
	workspacesRoot string // ~/.neuroforge
	now            func() time.Time
}

// NewManager creates a Manager backed by db. workspacesRoot is the NeuroForge
// home directory (worktrees are created under <root>/workspaces/...).
func NewManager(db *storage.DB, rec *audit.Recorder, workspacesRoot string, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	}
	return &Manager{
		db:             db,
		audit:          rec,
		logger:         logger,
		workspacesRoot: workspacesRoot,
		now:            func() time.Time { return time.Now().UTC() },
	}
}

// Create creates a new Git worktree for a task attempt and persists its
// metadata. The worktree is branched off baseBranch (or the repo's current
// HEAD) from the user's primary checkout. The primary checkout is never
// modified (§17.1).
func (m *Manager) Create(ctx context.Context, req CreateRequest) (Workspace, error) {
	if req.ProjectPath == "" {
		return Workspace{}, errors.New("workspace: project path is required")
	}
	if req.TaskID == "" {
		return Workspace{}, errors.New("workspace: task id is required")
	}
	wpID := req.WorkPackageID
	if wpID == "" {
		wpID = "main"
	}

	// Determine the next attempt number for this task + work package.
	attempt, err := m.nextAttempt(ctx, req.TaskID, wpID)
	if err != nil {
		return Workspace{}, err
	}

	branch := AttemptBranch(req.TaskID, wpID, attempt)
	wtPath := WorktreePath(m.workspacesRoot, req.ProjectID, req.TaskID, wpID, attempt)

	// Resolve the base commit from the user's primary checkout (read-only).
	baseRunner := gitRunner{dir: req.ProjectPath}
	baseRef := req.BaseBranch
	if baseRef == "" {
		// Use the current HEAD commit SHA as the base.
		out, err := baseRunner.run(ctx, "rev-parse", "HEAD")
		if err != nil {
			return Workspace{}, fmt.Errorf("workspace: resolve base HEAD: %w", err)
		}
		baseRef = strings.TrimSpace(out)
	}
	baseSHA, err := baseRunner.run(ctx, "rev-parse", baseRef)
	if err != nil {
		return Workspace{}, fmt.Errorf("workspace: resolve base ref %q: %w", baseRef, err)
	}
	baseSHA = strings.TrimSpace(baseSHA)

	// Persist the workspace record BEFORE creating the worktree (state before
	// external action, §11.4). This ensures a crash between persist and worktree
	// creation leaves a 'pending' record the reconciler can clean up.
	now := m.now()
	ws := Workspace{
		ID:            WorkspaceID(req.TaskID, wpID, attempt),
		ProjectID:     req.ProjectID,
		TaskID:        req.TaskID,
		WorkPackageID: wpID,
		Attempt:       attempt,
		Kind:          "attempt",
		Path:          wtPath,
		Branch:        branch,
		BaseSHA:       baseSHA,
		State:         StatePending,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	tx, err := m.db.BeginTx(ctx)
	if err != nil {
		return Workspace{}, fmt.Errorf("workspace: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := tx.CreateWorkspace(ctx, storageWorkspace(ws, now)); err != nil {
		return Workspace{}, err
	}
	if err := m.auditTx(ctx, tx, ws.ID, "workspace.create_started", audit.Payload(
		"task", ws.TaskID, "branch", ws.Branch, "base_sha", ws.BaseSHA)); err != nil {
		return Workspace{}, err
	}
	if err := tx.Commit(); err != nil {
		return Workspace{}, fmt.Errorf("workspace: commit create: %w", err)
	}

	// Create the worktree from the user's primary checkout. This creates a new
	// branch in the shared object database but does NOT modify the user's
	// working tree.
	if err := os.MkdirAll(filepath.Dir(wtPath), 0o700); err != nil {
		return Workspace{}, fmt.Errorf("workspace: mkdir worktree parent: %w", err)
	}
	if _, err := baseRunner.run(ctx, "worktree", "add", "-b", branch, wtPath, baseRef); err != nil {
		// Mark as failed if the worktree could not be created.
		_ = m.updateState(ctx, ws.ID, StateFailed, "", "", "")
		return Workspace{}, fmt.Errorf("workspace: create worktree: %w", err)
	}

	// Read back the resolved HEAD of the new worktree.
	wtRunner := gitRunner{dir: wtPath}
	headSHA, err := wtRunner.run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return Workspace{}, fmt.Errorf("workspace: read worktree HEAD: %w", err)
	}
	ws.HeadSHA = strings.TrimSpace(headSHA)
	ws.State = StateActive
	ws.UpdatedAt = m.now()

	if err := m.updateState(ctx, ws.ID, StateActive, ws.HeadSHA, "", ""); err != nil {
		return Workspace{}, err
	}
	if err := m.auditEvent(ctx, ws.ID, "workspace.created", audit.Payload(
		"path", ws.Path, "branch", ws.Branch, "head_sha", ws.HeadSHA)); err != nil {
		m.logger.Warn("audit workspace.created failed", "err", err)
	}

	m.logger.Info("workspace created", "id", ws.ID, "path", ws.Path, "branch", ws.Branch)
	return ws, nil
}

// nextAttempt returns the next attempt number for a task + work package.
func (m *Manager) nextAttempt(ctx context.Context, taskID, wpID string) (int, error) {
	existing, err := m.db.ListWorkspacesByTask(ctx, taskID)
	if err != nil {
		return 0, fmt.Errorf("workspace: list existing: %w", err)
	}
	max := 0
	for _, w := range existing {
		if w.WorkPackageID == wpID && w.Attempt > max {
			max = w.Attempt
		}
	}
	return max + 1, nil
}

// Get returns a workspace by id.
func (m *Manager) Get(ctx context.Context, id string) (Workspace, error) {
	sw, err := m.db.GetWorkspace(ctx, id)
	if err != nil {
		return Workspace{}, err
	}
	return fromStorageWorkspace(sw), nil
}

// ListByTask returns all workspaces for a task.
func (m *Manager) ListByTask(ctx context.Context, taskID string) ([]Workspace, error) {
	rows, err := m.db.ListWorkspacesByTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	out := make([]Workspace, len(rows))
	for i, sw := range rows {
		out[i] = fromStorageWorkspace(sw)
	}
	return out, nil
}

// ListByProject returns all workspaces for a project.
func (m *Manager) ListByProject(ctx context.Context, projectID string) ([]Workspace, error) {
	rows, err := m.db.ListWorkspacesByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]Workspace, len(rows))
	for i, sw := range rows {
		out[i] = fromStorageWorkspace(sw)
	}
	return out, nil
}

// ListAll returns all workspaces.
func (m *Manager) ListAll(ctx context.Context) ([]Workspace, error) {
	rows, err := m.db.ListAllWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Workspace, len(rows))
	for i, sw := range rows {
		out[i] = fromStorageWorkspace(sw)
	}
	return out, nil
}

// SetRunInfo records the engine/model/run-id/session-id for a workspace after
// an agent run starts.
func (m *Manager) SetRunInfo(ctx context.Context, id, engine, model, runID, sessionID string) error {
	ws, err := m.Get(ctx, id)
	if err != nil {
		return err
	}
	now := m.now().Format(time.RFC3339Nano)
	tx, err := m.db.BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("workspace: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.UpdateWorkspaceState(ctx, id, string(ws.State), ws.HeadSHA, runID, sessionID, now); err != nil {
		return err
	}
	// Also persist engine/model via direct exec (not in the update method).
	if _, err := tx.Exec(ctx, `UPDATE workspaces SET engine = ?, model = ? WHERE id = ?`,
		engine, model, id); err != nil {
		return err
	}
	if err := m.auditTx(ctx, tx, id, "workspace.run_started", audit.Payload(
		"engine", engine, "model", model, "run_id", runID, "session_id", sessionID)); err != nil {
		return err
	}
	return tx.Commit()
}

// refreshHead reads the current HEAD of the worktree and updates head_sha.
func (m *Manager) refreshHead(ctx context.Context, ws Workspace) (string, error) {
	r := gitRunner{dir: ws.Path}
	out, err := r.run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(out)
	if sha != ws.HeadSHA {
		if err := m.updateState(ctx, ws.ID, ws.State, sha, ws.RunID, ws.SessionID); err != nil {
			return "", err
		}
	}
	return sha, nil
}

func (m *Manager) updateState(ctx context.Context, id string, state State, headSHA, runID, sessionID string) error {
	now := m.now().Format(time.RFC3339Nano)
	return m.db.UpdateWorkspaceState(ctx, id, string(state), headSHA, runID, sessionID, now)
}

func (m *Manager) auditEvent(ctx context.Context, scopeID, eventType string, payload map[string]any) error {
	if m.audit == nil {
		return nil
	}
	_, err := m.audit.Record(ctx, audit.Event{
		Type:    eventType,
		Scope:   audit.ScopeTask,
		ScopeID: scopeID,
		Actor:   audit.ActorDaemon,
		Payload: payload,
	})
	return err
}

func (m *Manager) auditTx(ctx context.Context, tx *storage.Tx, scopeID, eventType string, payload map[string]any) error {
	if m.audit == nil {
		return nil
	}
	_, err := m.audit.RecordTx(ctx, tx, audit.Event{
		Type:    eventType,
		Scope:   audit.ScopeTask,
		ScopeID: scopeID,
		Actor:   audit.ActorDaemon,
		Payload: payload,
	})
	return err
}

// ---- DTO converters ----

func storageWorkspace(w Workspace, now time.Time) storage.Workspace {
	return storage.Workspace{
		ID:            w.ID,
		ProjectID:     w.ProjectID,
		TaskID:        w.TaskID,
		WorkPackageID: w.WorkPackageID,
		Attempt:       w.Attempt,
		Kind:          w.Kind,
		Path:          w.Path,
		Branch:        w.Branch,
		ResultBranch:  w.ResultBranch,
		BaseSHA:       w.BaseSHA,
		HeadSHA:       w.HeadSHA,
		ResultSHA:     w.ResultSHA,
		State:         string(w.State),
		Engine:        w.Engine,
		Model:         w.Model,
		RunID:         w.RunID,
		SessionID:     w.SessionID,
		CreatedAt:     now.Format(time.RFC3339Nano),
		UpdatedAt:     now.Format(time.RFC3339Nano),
	}
}

func fromStorageWorkspace(sw storage.Workspace) Workspace {
	created, _ := time.Parse(time.RFC3339Nano, sw.CreatedAt)
	updated, _ := time.Parse(time.RFC3339Nano, sw.UpdatedAt)
	return Workspace{
		ID:            sw.ID,
		ProjectID:     sw.ProjectID,
		TaskID:        sw.TaskID,
		WorkPackageID: sw.WorkPackageID,
		Attempt:       sw.Attempt,
		Kind:          sw.Kind,
		Path:          sw.Path,
		Branch:        sw.Branch,
		ResultBranch:  sw.ResultBranch,
		BaseSHA:       sw.BaseSHA,
		HeadSHA:       sw.HeadSHA,
		ResultSHA:     sw.ResultSHA,
		State:         State(sw.State),
		Engine:        sw.Engine,
		Model:         sw.Model,
		RunID:         sw.RunID,
		SessionID:     sw.SessionID,
		CreatedAt:     created,
		UpdatedAt:     updated,
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
