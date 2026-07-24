package supervisor

import (
	"neuroforge/internal/adapter/codingagent/protocol"
)

// ResumeChoice is the outcome of the resume policy: either resume an existing
// provider session (same engine, same session id) or do a clean restart on a
// (possibly different) route from the continuation pack.
type ResumeChoice string

const (
	// ResumeSession continues an existing provider session on the same engine
	// (spec §21, §12.2 Resume). Used only when the failure was transient, the
	// engine supports [protocol.AgentCapabilities.SessionResume], a checkpoint
	// exists, and the same-route retry budget has not been exhausted.
	ResumeSession ResumeChoice = "resume"
	// CleanRestart starts a fresh run on a (possibly different) route from a
	// continuation pack (spec §21.2). The fallback agent receives ONLY the
	// pack — never the full conversation history.
	CleanRestart ResumeChoice = "clean_restart"
)

// ResumeDecision bundles the choice with the reasoning, for audit/explanation.
type ResumeDecision struct {
	Choice ResumeChoice
	Reason string
	// OnRoute is the route the decision applies to (the one to resume or the
	// fallback to restart on).
	OnRoute Route
}

// ResumePolicy decides whether an interrupted run should resume its provider
// session or do a clean restart (spec §21, §21.2). It is pure deterministic
// logic.
//
// Principles:
//   - Prefer resume when the failure is transient AND the engine can resume AND
//     the retry budget remains — this preserves the session cheaply.
//   - Force clean restart (failover) when the failure is provider-side (quota,
//     auth, model): the provider is the problem, so switching is safer, and the
//     continuation pack carries forward the useful state without the full
//     conversation (spec §21.2: "do not transfer the entire conversation").
//   - Force clean restart when retry budget is exhausted on this route.
type ResumePolicy struct{}

// NewResumePolicy returns the default policy.
func NewResumePolicy() *ResumePolicy { return &ResumePolicy{} }

// ResumeInput carries the signals the policy consumes.
type ResumeInput struct {
	// Decision is the recovery decision from the [RecoveryClassifier].
	Decision RecoveryDecision
	// CanResume reports whether the engine supports session resume and a
	// session id + checkpoint exist to resume from.
	CanResume bool
	// Fallback is the next fallback route to restart on (empty when none).
	Fallback Route
	// CurrentRoute is the route that just failed.
	CurrentRoute Route
}

// Decide returns the resume-vs-clean-restart decision.
func (p *ResumePolicy) Decide(in ResumeInput) ResumeDecision {
	d := in.Decision

	// Failover always means a clean restart on the fallback route: we are
	// changing provider, so the old session is irrelevant. The fallback gets
	// only the continuation pack (spec §21.2).
	if d.Action == ActionFailover {
		if in.Fallback.Engine == "" {
			return ResumeDecision{
				Choice:  CleanRestart,
				Reason:  "failover requested but no fallback route; will re-enter selection",
				OnRoute: in.CurrentRoute,
			}
		}
		return ResumeDecision{
			Choice:  CleanRestart,
			Reason:  "provider-side failure; clean restart on fallback route with continuation pack",
			OnRoute: in.Fallback,
		}
	}

	// Retry on the same route: prefer resuming the session if the engine
	// supports it and we still have budget — it preserves context cheaply.
	if d.Action == ActionRetry {
		if in.CanResume {
			return ResumeDecision{
				Choice:  ResumeSession,
				Reason:  "transient failure; resuming provider session after cooldown",
				OnRoute: in.CurrentRoute,
			}
		}
		// Cannot resume: clean restart on the same route from the checkpoint.
		return ResumeDecision{
			Choice:  CleanRestart,
			Reason:  "transient failure but engine cannot resume; clean restart from checkpoint on same route",
			OnRoute: in.CurrentRoute,
		}
	}

	// Terminal/wait/quarantine/pause do not choose a route; echo current.
	return ResumeDecision{
		Choice:  CleanRestart,
		Reason:  "no retry/failover; decision is terminal or requires human action",
		OnRoute: in.CurrentRoute,
	}
}

// CanResume reports whether a route's adapter supports session resume and the
// supporting state (session id + checkpoint path) is present. This keeps the
// policy free of adapter/registry coupling.
func CanResume(caps protocol.AgentCapabilities, sessionID, checkpointPath string) bool {
	return caps.SessionResume && sessionID != "" && checkpointPath != ""
}
