package supervisor

import (
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// RecoveryAction is the disposition decided by the [RecoveryClassifier] for a
// failed run (spec §21, §32). It is the single answer to "what do we do next?"
// after a run fails.
type RecoveryAction string

const (
	// ActionRetry retries the SAME route after an optional cooldown. Reserved
	// for transient classes (rate limit, capacity, crash, timeout, malformed
	// output). Bounded by the per-class retry budget so no class triggers an
	// infinite retry (spec §32).
	ActionRetry RecoveryAction = "retry"
	// ActionFailover switches to a FALLBACK route (different engine/model/
	// account) using a continuation pack so progress is kept without
	// transferring the full conversation (spec §21.2, AC-15). Used for
	// provider-side failures: quota, auth, capacity, model-not-available.
	ActionFailover RecoveryAction = "failover"
	// ActionWaitQuota parks the work package in WAITING_QUOTA because every
	// route is unavailable (quota exhausted across the chain). The work
	// resumes automatically once an account resets (§15.5, §20.3).
	ActionWaitQuota RecoveryAction = "wait_quota"
	// ActionQuarantine marks the work package QUARANTINED: an unrecoverable
	// failure (protocol error, exhausted retries) requires human attention
	// before the work can continue (§28 QUARANTINE decision).
	ActionQuarantine RecoveryAction = "quarantine"
	// ActionTerminal surfaces the failure to the user without retrying. Used
	// for deterministic failures that are not provider faults: build, test,
	// visual, scope and policy violations (spec §32).
	ActionTerminal RecoveryAction = "terminal"
	// ActionPause pauses spending/execution for a reason that needs human
	// action: engine not installed, budget exhausted (§23, §32).
	ActionPause RecoveryAction = "pause"
)

// RecoveryDecision is the output of the [RecoveryClassifier]. It bundles the
// chosen action with the cooldown to apply and a machine+human-readable reason.
type RecoveryDecision struct {
	Action   RecoveryAction
	Cooldown time.Duration
	// AttemptsUsed is how many same-route retries have been consumed so far
	// (mirrored for auditability).
	AttemptsUsed int
	// AttemptsMax is the bound for this class (0 when not retrying).
	AttemptsMax int
	// FailoverReason is the §32 class that drove the decision.
	FailureClass protocol.FailureClass
	Reason       string
}

// Route describes one selectable route for the recovery classifier. It is a
// minimal projection of the router's Route — the supervisor does not depend on
// the router package (ADR-0005 keeps the adapter boundary clean). Callers map
// their route chain into this type.
type Route struct {
	Engine  string
	Model   string
	Account string
}

// RecoveryClassifier maps a §32 failure classification onto a bounded recovery
// action (spec §21, §32). It is pure deterministic logic — never an LLM
// (rule §22.6: no LLM for policy/recovery decisions).
//
// The classifier honours:
//   - the per-class retry/cooldown/failover policy from [protocol.DefaultPolicy];
//   - the current same-route retry budget (no infinite retry, spec §32);
//   - whether a fallback route exists (failover only when one does);
//   - the distinction between "all providers exhausted" (WAITING_QUOTA) and
//     "unrecoverable" (QUARANTINE).
type RecoveryClassifier struct {
	// Jitter is the fraction of random jitter added to a cooldown (§20.3).
	// Zero disables jitter (useful for deterministic tests).
	Jitter float64
	// Now is the clock; defaults to time.Now().UTC().
	Now func() time.Time
	// Rand is the jitter source; defaults to a package-level deterministic
	// source so tests are reproducible. In production a real source is wired
	// by the caller.
	Rand func() float64
}

// NewRecoveryClassifier returns a classifier with jitter enabled.
func NewRecoveryClassifier() *RecoveryClassifier {
	return &RecoveryClassifier{
		Jitter: 0.25,
		Now:    func() time.Time { return time.Now().UTC() },
		Rand:   defaultRand,
	}
}

// Input carries the context the classifier needs.
type RecoveryInput struct {
	// Failure is the adapter's classification of the run that just failed.
	Failure protocol.FailureClassification
	// AttemptsUsed is how many same-route retries have already happened on
	// the current route for this class.
	AttemptsUsed int
	// FallbacksAvailable reports whether at least one unused fallback route
	// remains in the chain (spec §21.1).
	FallbacksAvailable bool
	// AnyRouteAvailable reports whether ANY route in the chain is currently
	// routable (quota not exhausted). When false and the failure is quota-
	// related, the work goes to WAITING_QUOTA rather than quarantine.
	AnyRouteAvailable bool
}

// Classify returns the bounded recovery decision. It never returns an action
// that would lead to an unbounded retry.
func (c *RecoveryClassifier) Classify(in RecoveryInput) RecoveryDecision {
	pol := in.Failure
	if pol.Class == "" || !pol.Class.IsValid() {
		// Unknown failure: treat as terminal for safety (never loop).
		return RecoveryDecision{
			Action:       ActionTerminal,
			FailureClass: protocol.FailureInternalError,
			Reason:       "unclassified failure; treated as terminal for safety",
		}
	}

	maxAttempts := pol.MaxRetries
	if maxAttempts < 0 {
		maxAttempts = 0
	}

	switch pol.Policy {
	case protocol.PolicyFailover:
		// PROVIDER_QUOTA / PROVIDER_AUTH / MODEL_NOT_AVAILABLE / PROVIDER_CAPACITY.
		if in.FallbacksAvailable {
			return RecoveryDecision{
				Action:       ActionFailover,
				FailureClass: pol.Class,
				Reason:       pol.Reason,
			}
		}
		// No fallback left. Distinguish "wait for quota reset" from
		// "give up". A quota/rate-limit class with no routable route waits;
		// auth or model issues quarantine (they need human action).
		if isQuotaWaitable(pol.Class) && !in.AnyRouteAvailable {
			return RecoveryDecision{
				Action:       ActionWaitQuota,
				FailureClass: pol.Class,
				Reason:       "all routes quota-exhausted; parked in WAITING_QUOTA until reset",
			}
		}
		return RecoveryDecision{
			Action:       ActionQuarantine,
			FailureClass: pol.Class,
			Reason:       "no fallback route available and failure is not quota-waitable; quarantined",
		}

	case protocol.PolicyRetry, protocol.PolicyCooldown:
		// Transient: rate limit, capacity, crash, timeout, malformed, image,
		// internal error. Bounded retry on the SAME route, then failover.
		if in.AttemptsUsed < maxAttempts {
			return RecoveryDecision{
				Action:       ActionRetry,
				Cooldown:     c.cooldown(pol),
				AttemptsUsed: in.AttemptsUsed + 1,
				AttemptsMax:  maxAttempts,
				FailureClass: pol.Class,
				Reason:       "transient failure; bounded retry on same route after cooldown",
			}
		}
		// Same-route budget exhausted: failover if possible, else wait/quarantine.
		if in.FallbacksAvailable {
			return RecoveryDecision{
				Action:       ActionFailover,
				FailureClass: pol.Class,
				Reason:       "same-route retry budget exhausted; failover to fallback route",
			}
		}
		if isQuotaWaitable(pol.Class) && !in.AnyRouteAvailable {
			return RecoveryDecision{
				Action:       ActionWaitQuota,
				FailureClass: pol.Class,
				Reason:       "retries exhausted and all routes quota-exhausted; WAITING_QUOTA",
			}
		}
		return RecoveryDecision{
			Action:       ActionQuarantine,
			FailureClass: pol.Class,
			Reason:       "retries exhausted with no fallback; quarantined for human review",
		}

	case protocol.PolicyQuarantine:
		// ENGINE_PROTOCOL_ERROR: one bounded attempt then quarantine.
		if in.AttemptsUsed < maxAttempts {
			return RecoveryDecision{
				Action:       ActionRetry,
				Cooldown:     c.cooldown(pol),
				AttemptsUsed: in.AttemptsUsed + 1,
				AttemptsMax:  maxAttempts,
				FailureClass: pol.Class,
				Reason:       "protocol error; one bounded retry then quarantine",
			}
		}
		return RecoveryDecision{
			Action:       ActionQuarantine,
			FailureClass: pol.Class,
			Reason:       "protocol error persists; adapter quarantined",
		}

	case protocol.PolicyTerminal:
		// BUILD/TEST/VISUAL/SCOPE/POLICY/MERGE_CONFLICT/CANCELLED: surface, no retry.
		return RecoveryDecision{
			Action:       ActionTerminal,
			FailureClass: pol.Class,
			Reason:       pol.Reason,
		}

	case protocol.PolicyPause:
		// ENGINE_NOT_INSTALLED / BUDGET_EXCEEDED: pause for human/onboarding.
		return RecoveryDecision{
			Action:       ActionPause,
			FailureClass: pol.Class,
			Reason:       pol.Reason,
		}

	case protocol.PolicyEscalation:
		// Escalation is expressed through failover to a stronger model.
		if in.FallbacksAvailable {
			return RecoveryDecision{
				Action:       ActionFailover,
				FailureClass: pol.Class,
				Reason:       "escalation: failover to a stronger fallback route",
			}
		}
		return RecoveryDecision{
			Action:       ActionQuarantine,
			FailureClass: pol.Class,
			Reason:       "escalation requested but no stronger route available; quarantined",
		}

	default:
		return RecoveryDecision{
			Action:       ActionTerminal,
			FailureClass: pol.Class,
			Reason:       "unmapped failure policy; treated as terminal for safety",
		}
	}
}

// cooldown converts a classification's CooldownSeconds into a Duration, adding
// jitter (§20.3). A zero/unsupported cooldown still yields a small backoff so
// a tight retry loop cannot hot-spin.
func (c *RecoveryClassifier) cooldown(pol protocol.FailureClassification) time.Duration {
	base := time.Duration(pol.CooldownSeconds) * time.Second
	if base <= 0 {
		base = 1 * time.Second
	}
	if c.Jitter <= 0 || c.Rand == nil {
		return base
	}
	jitter := time.Duration(float64(base) * c.Jitter * c.Rand())
	return base + jitter
}

// isQuotaWaitable reports whether a quota/rate-limit failure should park the
// work in WAITING_QUOTA (rather than quarantine) when no route is available.
func isQuotaWaitable(class protocol.FailureClass) bool {
	switch class {
	case protocol.FailureProviderQuota, protocol.FailureProviderRateLimit,
		protocol.FailureProviderCapacity:
		return true
	}
	return false
}
