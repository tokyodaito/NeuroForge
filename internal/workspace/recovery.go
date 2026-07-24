package workspace

import (
	"context"
	"fmt"
	"time"

	"neuroforge/internal/audit"
)

// SetState transitions a workspace to a recovery-related state (spec §15.5,
// §20.3, §28, §32) and audits it. It is the path the failover controller uses
// to park a workspace in WAITING_QUOTA or mark it QUARANTINED.
//
// Only the recovery states are reachable here; the normal lifecycle
// (active/completed/kept/rejected) uses the dedicated methods. An invalid
// transition is rejected.
func (m *Manager) SetState(ctx context.Context, workspaceID string, target State) (Workspace, error) {
	ws, err := m.Get(ctx, workspaceID)
	if err != nil {
		return Workspace{}, err
	}
	if !allowedRecoveryTransition(ws.State, target) {
		return Workspace{}, fmt.Errorf("workspace: illegal recovery transition %s -> %s for %q", ws.State, target, workspaceID)
	}
	if err := m.updateState(ctx, workspaceID, target, ws.HeadSHA, ws.RunID, ws.SessionID); err != nil {
		return Workspace{}, err
	}
	event := "workspace.state_changed"
	payload := audit.Payload("from", string(ws.State), "to", string(target))
	switch target {
	case StateWaitingQuota:
		event = "workspace.waiting_quota"
		payload = audit.Payload("from", string(ws.State), "reason", "all routes quota-exhausted")
	case StateQuarantined:
		event = "workspace.quarantined"
		payload = audit.Payload("from", string(ws.State), "reason", "unrecoverable failure; human review required")
	case StateActive:
		event = "workspace.resumed"
		payload = audit.Payload("from", string(ws.State), "reason", "route became available again")
	}
	if err := m.auditEvent(ctx, workspaceID, event, payload); err != nil {
		m.logger.Warn("audit workspace state change failed", "err", err)
	}
	ws.State = target
	ws.UpdatedAt = m.now()
	return ws, nil
}

// allowedRecoveryTransition reports whether moving from one state to a recovery
// state is legal. The recovery states are designed to be re-entrant: a parked
// or quarantined workspace can return to active when a route is available or a
// human un-quarantines it.
func allowedRecoveryTransition(from, to State) bool {
	if from == StateDeleted || from == StateRejected {
		return false
	}
	switch to {
	case StateWaitingQuota, StateQuarantined, StateActive, StateFailed:
		return true
	}
	return false
}

// RetentionConfig bounds how many checkpoints a workspace keeps (spec §21.3
// checkpoint retention). Older checkpoints beyond KeepMostRecent are pruned
// from durable storage once the run settles. Zero means "keep all" (the safe
// default until a retention policy is configured).
type RetentionConfig struct {
	KeepMostRecent int
}

// DefaultRetention returns the default retention policy: keep the most recent
// checkpoints per workspace, pruning only after a run settles.
func DefaultRetention() RetentionConfig {
	return RetentionConfig{KeepMostRecent: 20}
}

// RetainCheckpoints enforces the checkpoint retention policy for a workspace.
// It deletes the oldest checkpoint records beyond KeepMostRecent (spec §21.3 —
// checkpoints are durable recovery points; only the excess are pruned, and only
// the durable record, never a commit that is still referenced by the branch).
//
// The underlying checkpoint COMMITS stay in the attempt branch (they are the
// recovery substrate); only the bookkeeping rows beyond the retention window
// are removed, so listing stays bounded and readable.
func (m *Manager) RetainCheckpoints(ctx context.Context, workspaceID string, cfg RetentionConfig) (int, error) {
	if cfg.KeepMostRecent <= 0 {
		return 0, nil
	}
	cps, err := m.db.ListCheckpoints(ctx, workspaceID)
	if err != nil {
		return 0, fmt.Errorf("workspace: list checkpoints for retention: %w", err)
	}
	if len(cps) <= cfg.KeepMostRecent {
		return 0, nil
	}
	// ListCheckpoints returns oldest-first; prune the head of the list.
	pruneCount := len(cps) - cfg.KeepMostRecent
	pruned := 0
	now := m.now().Format(time.RFC3339Nano)
	for i := 0; i < pruneCount; i++ {
		if _, err := m.db.Exec(ctx, `DELETE FROM checkpoints WHERE id = ?`, cps[i].ID); err != nil {
			return pruned, fmt.Errorf("workspace: prune checkpoint %d: %w", cps[i].ID, err)
		}
		pruned++
	}
	if m.audit != nil {
		_, _ = m.audit.Record(ctx, audit.Event{
			Type:    "workspace.checkpoints_pruned",
			Scope:   audit.ScopeTask,
			ScopeID: workspaceID,
			Actor:   audit.ActorDaemon,
			Payload: audit.Payload("pruned", pruned, "kept", cfg.KeepMostRecent, "at", now),
		})
	}
	return pruned, nil
}
