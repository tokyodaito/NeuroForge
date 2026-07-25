package merge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"neuroforge/internal/adapter/vcs"
	"neuroforge/internal/audit"
	"neuroforge/internal/policy"
)

// Authority is the SINGLE holder of merge authority (spec §28, ADR-0009/0015).
//
// Every delivery operation (push, PR/MR create/update, auto-merge, merge,
// revert) on every [vcs.ChangeRequestProvider] MUST flow through the Authority.
// It re-checks, for each call:
//
//  1. the Merge Governor decision (from [Decide]) authorises the specific
//     action — e.g. Merge requires DecisionAllowMerge;
//  2. the resolved policy permits the action (policy.Allows, defense-in-depth);
//  3. a network provider is not invoked in a network-locked profile (AC-7).
//
// Only then does it call the provider and audit the result (§29.4). Agent
// processes never hold an Authority reference, so they structurally cannot
// perform delivery (§28 "Agent process does not have merge credentials",
// AC-28).
//
// The Authority is deterministic: same decision + policy + request ⇒ same
// outcome (rule §36.6).
type Authority struct {
	audit  *audit.Recorder
	logger *slog.Logger
	now    func() time.Time
}

// NewAuthority creates the delivery Authority. The recorder must not be nil; it
// is what makes push/PR/MR/merge auditable (§29.4).
func NewAuthority(rec *audit.Recorder, logger *slog.Logger) *Authority {
	if logger == nil {
		logger = slog.New(discardHandler())
	}
	return &Authority{audit: rec, logger: logger, now: func() time.Time { return time.Now().UTC() }}
}

// --- Push ---

// Push pushes a branch via the provider, but ONLY if the Governor decision
// permits push (DecisionAllowPush or higher) AND the resolved policy allows
// ActPush.
func (a *Authority) Push(ctx context.Context, dec Result, resolved policy.Resolved, p vcs.ChangeRequestProvider, req vcs.PushBranchRequest) (vcs.PushResult, error) {
	if err := a.authorise(dec, resolved, p, policy.ActPush, "push", requiresDecision(DecisionAllowPush)); err != nil {
		a.auditDeny(ctx, req.TaskID, "push", err)
		return vcs.PushResult{}, err
	}
	res, err := p.PushBranch(ctx, req)
	a.record(ctx, req.TaskID, "vcs.push", err, audit.Payload(
		"provider", string(p.ID()), "remote_branch", req.RemoteBranch, "sha", req.HeadSHA, "force", req.Force))
	return res, err
}

// --- CreateChangeRequest ---

// CreateChangeRequest opens a PR/MR, requiring DecisionAllowChangeRequest or
// higher AND policy.ActCreateChangeRequest.
func (a *Authority) CreateChangeRequest(ctx context.Context, dec Result, resolved policy.Resolved, p vcs.ChangeRequestProvider, req vcs.CreateChangeRequestRequest) (vcs.ChangeRequest, error) {
	if err := a.authorise(dec, resolved, p, policy.ActCreateChangeRequest, "change_request.create", requiresDecision(DecisionAllowChangeRequest)); err != nil {
		a.auditDeny(ctx, req.TaskID, "change_request.create", err)
		return vcs.ChangeRequest{}, err
	}
	cr, err := p.CreateChangeRequest(ctx, req)
	a.record(ctx, req.TaskID, "vcs.change_request.create", err, audit.Payload(
		"provider", string(p.ID()), "number", cr.Number, "head", req.HeadBranch, "base", req.BaseBranch, "url", cr.WebURL))
	return cr, err
}

// UpdateChangeRequest amends an existing PR/MR.
func (a *Authority) UpdateChangeRequest(ctx context.Context, dec Result, resolved policy.Resolved, p vcs.ChangeRequestProvider, req vcs.UpdateChangeRequestRequest) (vcs.ChangeRequest, error) {
	if err := a.authorise(dec, resolved, p, policy.ActUpdateChangeRequest, "change_request.update", requiresDecision(DecisionAllowChangeRequest)); err != nil {
		a.auditDeny(ctx, req.TaskID, "change_request.update", err)
		return vcs.ChangeRequest{}, err
	}
	cr, err := p.UpdateChangeRequest(ctx, req)
	a.record(ctx, req.TaskID, "vcs.change_request.update", err, audit.Payload(
		"provider", string(p.ID()), "number", req.Number, "state", req.State))
	return cr, err
}

// --- GetChecks (read-only; allowed whenever push is allowed) ---

// GetChecks reads CI status. Read-only, so it requires only that push is
// policy-permitted (no separate Governor decision level needed beyond the
// push-level allows). Network providers still respect the network lock.
func (a *Authority) GetChecks(ctx context.Context, resolved policy.Resolved, p vcs.ChangeRequestProvider, req vcs.GetChecksRequest) (vcs.CheckStatus, error) {
	if !p.Capabilities().GetChecks {
		return vcs.CheckStatus{}, vcs.Unsupported(p.ID(), "GetChecks")
	}
	if p.Capabilities().IsNetwork && resolved.Profile.IsNetworkLocked() {
		return vcs.CheckStatus{}, fmt.Errorf("%w: GetChecks on %s", vcs.ErrNetworkLocked, p.ID())
	}
	if d := resolved.Allows(policy.ActPush); !d.Allow {
		return vcs.CheckStatus{}, fmt.Errorf("%w: %s", vcs.ErrPolicyDenied, d)
	}
	cs, err := p.GetChecks(ctx, req)
	a.record(ctx, req.TaskID, "vcs.checks.read", err, audit.Payload(
		"provider", string(p.ID()), "number", req.Number, "all_passed", cs.AllPassed))
	return cs, err
}

// --- EnableAutoMerge ---

// EnableAutoMerge requests the platform auto-merge, requiring DecisionAllowMerge
// (auto-merge is a merge commitment).
func (a *Authority) EnableAutoMerge(ctx context.Context, dec Result, resolved policy.Resolved, p vcs.ChangeRequestProvider, req vcs.EnableAutoMergeRequest) error {
	if err := a.authorise(dec, resolved, p, policy.ActMerge, "merge", requiresDecision(DecisionAllowMerge)); err != nil {
		a.auditDeny(ctx, req.TaskID, "enable_auto_merge", err)
		return err
	}
	err := p.EnableAutoMerge(ctx, req)
	a.record(ctx, req.TaskID, "vcs.auto_merge.enable", err, audit.Payload(
		"provider", string(p.ID()), "number", req.Number, "method", string(req.Method)))
	return err
}

// --- Merge (the single merge-authority path) ---

// Merge integrates a change. This is the ONLY way to merge; it requires
// DecisionAllowMerge from the Governor AND policy.ActMerge. No second path
// exists (§28).
func (a *Authority) Merge(ctx context.Context, dec Result, resolved policy.Resolved, p vcs.ChangeRequestProvider, req vcs.MergeRequest) (vcs.MergeResult, error) {
	if err := a.authorise(dec, resolved, p, policy.ActMerge, "merge", requiresDecision(DecisionAllowMerge)); err != nil {
		a.auditDeny(ctx, req.TaskID, "merge", err)
		return vcs.MergeResult{}, err
	}
	res, err := p.Merge(ctx, req)
	a.record(ctx, req.TaskID, "vcs.merge", err, audit.Payload(
		"provider", string(p.ID()), "number", req.Number, "method", string(req.Method),
		"merged", res.Merged, "commit_sha", res.CommitSHA, "base", res.BaseBranch))
	return res, err
}

// --- Revert ---

// Revert undoes a merged change. Requires ActMerge authority (revert is a
// delivery mutation on the target branch).
func (a *Authority) Revert(ctx context.Context, dec Result, resolved policy.Resolved, p vcs.ChangeRequestProvider, req vcs.RevertRequest) (vcs.RevertResult, error) {
	if err := a.authorise(dec, resolved, p, policy.ActMerge, "merge", requiresDecision(DecisionAllowMerge)); err != nil {
		a.auditDeny(ctx, req.TaskID, "revert", err)
		return vcs.RevertResult{}, err
	}
	res, err := p.Revert(ctx, req)
	a.record(ctx, req.TaskID, "vcs.revert", err, audit.Payload(
		"provider", string(p.ID()), "number", req.Number, "commit", req.CommitSHA, "reverted", res.Reverted))
	return res, err
}

// --- authorisation core ---

// authorise is the single chokepoint check. It enforces, in order:
//  1. The Governor decision is at the required ALLOW level (or higher) for the
//     action — the Governor is the authority, not the caller.
//  2. The resolved policy permits the action (defense-in-depth; the Governor
//     already consulted policy, but we re-check so a stale decision cannot
//     bypass a network lock).
//  3. A network provider is not used in a network-locked profile (AC-7).
//
// Step 3 is structural: even if the Governor decision and policy somehow
// disagreed, a network-locked profile can never perform a Git network op.
func (a *Authority) authorise(dec Result, resolved policy.Resolved, p vcs.ChangeRequestProvider, act policy.Action, label string, levelOK func(Decision) bool) error {
	if !levelOK(dec.Decision) {
		return fmt.Errorf("%w: governor decision %q does not permit %s (gates: %s)",
			vcs.ErrPolicyDenied, dec.Decision, label, gateNames(dec.Gates))
	}
	d := resolved.Allows(act)
	if !d.Allow {
		return fmt.Errorf("%w: %s", vcs.ErrPolicyDenied, d)
	}
	if p.Capabilities().IsNetwork && resolved.Profile.IsNetworkLocked() {
		return fmt.Errorf("%w: %s on %s (LOCAL_REVIEW/PLAN_ONLY)", vcs.ErrNetworkLocked, label, p.ID())
	}
	return nil
}

// requiresDecision returns a checker that accepts the given level OR a strictly
// stronger ALLOW decision. Allow ordering: LocalResult < Push < ChangeRequest <
// Merge (a Merge authorisation also permits Push/CR, but callers always pass the
// specific required level; the Governor has already chosen the precise level).
func requiresDecision(min Decision) func(Decision) bool {
	return func(d Decision) bool {
		return decisionRank(d) >= decisionRank(min)
	}
}

// decisionRank orders the ALLOW decisions from weakest to strongest. Non-allow
// decisions (REQUIRE_REBASE/REQUIRE_REPAIR/POLICY_BLOCKED/QUARANTINE) have rank
// -1 and never satisfy any requireDecision.
func decisionRank(d Decision) int {
	switch d {
	case DecisionAllowLocalResult:
		return 0
	case DecisionAllowPush:
		return 1
	case DecisionAllowChangeRequest:
		return 2
	case DecisionAllowMerge:
		return 3
	default:
		return -1
	}
}

func gateNames(gates []Gate) string {
	if len(gates) == 0 {
		return "none"
	}
	out := ""
	for i, g := range gates {
		if i > 0 {
			out += ","
		}
		st := "pass"
		if !g.Passed {
			st = "FAIL"
		}
		out += g.Name + "=" + st
	}
	return out
}

// record appends the audit row for a delivery action (§29.4). Errors are
// logged but never block the delivery result.
func (a *Authority) record(ctx context.Context, taskID, eventType string, callErr error, payload map[string]any) {
	if a.audit == nil {
		return
	}
	if payload == nil {
		payload = map[string]any{}
	}
	if callErr != nil {
		payload["error"] = callErr.Error()
	}
	payload["outcome"] = "ok"
	if callErr != nil {
		payload["outcome"] = "error"
	}
	scope := audit.ScopeTask
	scopeID := taskID
	if scopeID == "" {
		scope = audit.ScopeGlobal
	}
	if _, err := a.audit.Record(ctx, audit.Event{
		Type:      eventType,
		Scope:     scope,
		ScopeID:   scopeID,
		Actor:     audit.ActorDaemon,
		Payload:   payload,
		Timestamp: a.now(),
	}); err != nil {
		a.logger.Warn("audit delivery event failed", "type", eventType, "err", err)
	}
}

// auditDeny records a refused delivery attempt (important for AC-7/AC-29
// observability: a denied push in LOCAL_REVIEW leaves an audit trail).
func (a *Authority) auditDeny(ctx context.Context, taskID, action string, denyErr error) {
	if a.audit == nil {
		return
	}
	if errors.Is(denyErr, vcs.ErrUnsupported) {
		return // unsupported ops are not delivery attempts
	}
	scope := audit.ScopeTask
	scopeID := taskID
	if scopeID == "" {
		scope = audit.ScopeGlobal
	}
	_, _ = a.audit.Record(ctx, audit.Event{
		Type:    "vcs.delivery.denied",
		Scope:   scope,
		ScopeID: scopeID,
		Actor:   audit.ActorDaemon,
		Payload: audit.Payload("action", action, "reason", denyErr.Error(), "outcome", "denied"),
	})
}
