package daemon

import (
	"context"

	"neuroforge/internal/adapter/codingagent/protocol"
	"neuroforge/internal/quota"
	"neuroforge/internal/supervisor"
	"neuroforge/internal/workspace"
)

// workspaceHook adapts the workspace.Manager to the supervisor.WorkspaceHook
// interface. It lives in the daemon package so the supervisor never imports the
// workspace package directly (keeping the supervisor focused on agent
// supervision; the daemon is the composition root).
type workspaceHook struct {
	wm *workspace.Manager
}

// Compile-time assertion.
var _ supervisor.WorkspaceHook = (*workspaceHook)(nil)

func (h *workspaceHook) Checkpoint(ctx context.Context, workspaceID string, moment supervisor.CheckpointMoment, message string) (string, error) {
	cp, err := h.wm.Checkpoint(ctx, workspaceID, workspace.CheckpointMoment(moment), message)
	if err != nil {
		return "", err
	}
	return cp.CommitSHA, nil
}

func (h *workspaceHook) SetState(ctx context.Context, workspaceID, state string) error {
	_, err := h.wm.SetState(ctx, workspaceID, workspace.State(state))
	return err
}

func (h *workspaceHook) HeadSHA(ctx context.Context, workspaceID string) (string, error) {
	ws, err := h.wm.Get(ctx, workspaceID)
	if err != nil {
		return "", err
	}
	return ws.HeadSHA, nil
}

// quotaHook adapts the quota.Manager to the supervisor.QuotaHook interface.
type quotaHook struct {
	qm *quota.Manager
}

// Compile-time assertion.
var _ supervisor.QuotaHook = (*quotaHook)(nil)

func (h *quotaHook) IsAvailable(engine, account string) bool {
	return h.qm.IsAvailable(quota.AccountID{Engine: engine, Account: account})
}

func (h *quotaHook) RecordFailure(engine, account string, class protocol.FailureClass) {
	h.qm.RecordFailure(quota.AccountID{Engine: engine, Account: account}, class)
}

func (h *quotaHook) RecordSuccess(engine, account string) {
	h.qm.RecordSuccess(quota.AccountID{Engine: engine, Account: account})
}
