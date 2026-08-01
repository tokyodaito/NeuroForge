package opencode

import (
	"context"
	"errors"
	"fmt"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/protocol"
)

// Compile-time assertion that Adapter satisfies the full 13-method coding-agent
// surface (spec §12.2). If the interface changes, this fails the build here
// rather than at registration time.
var _ codingagent.Adapter = (*Adapter)(nil)

// ID is the stable engine identifier (spec §12.1): "opencode". It is independent
// from any model name (rule §36.8).
func (a *Adapter) ID() string { return "opencode" }

// Start begins a headless OpenCode run (spec §12.2). It builds the deterministic
// argv, spawns the process group with an allowlisted environment, and streams
// normalised events to sink. The request never carries credentials (AC-28); the
// account is name-only and resolved inside the engine. See [Adapter.runCommon]
// for the run contract (timeout/cancellation/malformed handling).
func (a *Adapter) Start(ctx context.Context, req protocol.AgentRunRequest, sink codingagent.EventSink) (protocol.RunHandle, error) {
	return a.runCommon(ctx, req, sink, false)
}

// Resume continues a previous run via an OpenCode session (--session). It is only
// honoured when [Adapter.Capabilities] reports SessionResume for the detected
// engine version (version-gated); otherwise it returns an explicit error rather
// than silently degrading (rule §36.25).
func (a *Adapter) Resume(ctx context.Context, req protocol.ResumeRequest, sink codingagent.EventSink) (protocol.RunHandle, error) {
	caps := a.Capabilities(ctx)
	if !caps.SessionResume {
		return protocol.RunHandle{}, errors.New("opencode: session resume unsupported by detected engine version")
	}
	areq := protocol.AgentRunRequest{
		RunID:        req.RunID,
		Engine:       req.Engine,
		Model:        req.Model,
		Account:      req.Account,
		Workspace:    req.Workspace,
		Scope:        req.Scope,
		AllowlistEnv: req.AllowlistEnv,
		TurnLimit:    req.TurnLimit,
		Timeout:      req.Timeout,
		SessionID:    req.SessionID,
	}
	return a.runCommon(ctx, areq, sink, true)
}

// SendMessage injects a user message into a running session (spec §12.2, §6.5).
// Headless one-shot OpenCode runs have no live session channel, so this is not
// supported (LiveUserMessages is false); it returns an explicit error (rule
// §36.25) rather than silently dropping the message.
func (a *Adapter) SendMessage(context.Context, protocol.RunHandle, protocol.AgentMessage) error {
	return errors.New("opencode: live user messages are not supported in headless run mode")
}

// Cancel terminates a run AND every descendant it spawned (spec: cancellation
// ends the whole process group). It cancels the supervision context and kills
// the group via [proctree.KillGroup]. Idempotent: cancelling an unknown or
// already-finished run is reported as an error but never panics.
func (a *Adapter) Cancel(_ context.Context, handle protocol.RunHandle) error {
	a.mu.Lock()
	st, ok := a.runs[handle.RunID]
	a.mu.Unlock()
	if !ok {
		return fmt.Errorf("opencode: unknown or already-finished run %q", handle.RunID)
	}
	// Record the cancel intent BEFORE the kill so a kill-induced EOF cannot
	// produce a non-cancelled terminal (KF-09 / invariant I.9).
	st.requestCancel()
	return nil
}

// Health probes the reachability of the OpenCode engine for one account (spec
// §12.2). It distinguishes ok / degraded / down / unknown: installed + version
// probe ok = ok; installed but version probe failed = degraded; not installed =
// down. Auth state for the backing provider is not directly observable from the
// headless CLI, so it does not flip Health on its own (reported as ok/degraded
// from presence alone).
func (a *Adapter) Health(ctx context.Context, account protocol.Account) protocol.HealthResult {
	d := a.rememberDetect(ctx)
	switch {
	case !d.installed:
		return protocol.HealthResult{Status: protocol.HealthDown, Detail: "opencode not installed: " + d.detail}
	case d.version == "":
		return protocol.HealthResult{Status: protocol.HealthDegraded, Detail: "opencode present but version unknown: " + d.detail}
	default:
		_ = account
		return protocol.HealthResult{Status: protocol.HealthOK, Detail: "opencode " + d.version + " ready (headless)"}
	}
}
