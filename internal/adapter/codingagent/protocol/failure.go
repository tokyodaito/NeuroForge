package protocol

// FailureClass enumerates the error taxonomy (spec §32). The taxonomy is the
// shared vocabulary every adapter maps native errors onto; the supervisor and
// quota/budget subsystems consume it to drive retry, cooldown, failover,
// escalation, pause and quarantine decisions. No class maps to an infinite
// retry (rule §32 "Бесконечные retry запрещены").
type FailureClass string

const (
	FailureProviderQuota      FailureClass = "PROVIDER_QUOTA"
	FailureProviderRateLimit  FailureClass = "PROVIDER_RATE_LIMIT"
	FailureProviderCapacity   FailureClass = "PROVIDER_CAPACITY"
	FailureProviderAuth       FailureClass = "PROVIDER_AUTH"
	FailureEngineNotInstalled FailureClass = "ENGINE_NOT_INSTALLED"
	FailureEngineCrash        FailureClass = "ENGINE_CRASH"
	FailureEngineProtocol     FailureClass = "ENGINE_PROTOCOL_ERROR"
	FailureModelNotAvailable  FailureClass = "MODEL_NOT_AVAILABLE"
	FailureImageProvider      FailureClass = "IMAGE_PROVIDER_FAILURE"
	FailureTimeout            FailureClass = "TIMEOUT"
	FailureCancelled          FailureClass = "CANCELLED"
	FailureBuildFailure       FailureClass = "BUILD_FAILURE"
	FailureTestFailure        FailureClass = "TEST_FAILURE"
	FailureVisualFailure      FailureClass = "VISUAL_FAILURE"
	FailureScopeViolation     FailureClass = "SCOPE_VIOLATION"
	FailurePolicyViolation    FailureClass = "POLICY_VIOLATION"
	FailureMalformedOutput    FailureClass = "MALFORMED_OUTPUT"
	FailureMergeConflict      FailureClass = "MERGE_CONFLICT"
	FailureBudgetExceeded     FailureClass = "BUDGET_EXCEEDED"
	FailureInternalError      FailureClass = "INTERNAL_ERROR"
)

// IsValid reports whether c is a known §32 class.
func (c FailureClass) IsValid() bool {
	switch c {
	case FailureProviderQuota, FailureProviderRateLimit, FailureProviderCapacity,
		FailureProviderAuth, FailureEngineNotInstalled, FailureEngineCrash,
		FailureEngineProtocol, FailureModelNotAvailable, FailureImageProvider,
		FailureTimeout, FailureCancelled, FailureBuildFailure, FailureTestFailure,
		FailureVisualFailure, FailureScopeViolation, FailurePolicyViolation,
		FailureMalformedOutput, FailureMergeConflict, FailureBudgetExceeded,
		FailureInternalError:
		return true
	}
	return false
}

// FailurePolicy is the recommended disposition for a failure class (spec §32):
// retry, cooldown, failover, escalation, pause or quarantine. The supervisor
// applies the policy bounded by its own retry limits; the adapter only suggests.
type FailurePolicy string

const (
	PolicyRetry      FailurePolicy = "retry"
	PolicyCooldown   FailurePolicy = "cooldown"
	PolicyFailover   FailurePolicy = "failover"
	PolicyEscalation FailurePolicy = "escalation"
	PolicyPause      FailurePolicy = "pause"
	PolicyQuarantine FailurePolicy = "quarantine"
	PolicyTerminal   FailurePolicy = "terminal" // do not retry; the run is finished
)

// FailureClassification is the outcome of [CodingAgentAdapter.ClassifyFailure]
// (spec §12.2, §32). It maps a native failure signal (exit code + events +
// stderr) onto the taxonomy and a recommended policy.
type FailureClassification struct {
	// Class is the §32 taxonomy bucket.
	Class FailureClass
	// Policy is the recommended disposition.
	Policy FailurePolicy
	// Retryable hints that the supervisor MAY retry (bounded). True only for
	// transient classes (rate limit, capacity, timeout, crash). Never true with
	// an unbounded budget.
	Retryable bool
	// Failover hints that the run should be retried on a different
	// engine/model/account (spec §21). True for quota/auth/capacity/model issues.
	Failover bool
	// MaxRetries is the adapter's suggested bound (0 = use supervisor default).
	// Honoured so no class triggers an infinite retry (rule §32).
	MaxRetries int
	// Cooldown, when > 0, suggests how long to back off before retrying this
	// engine/account (e.g. after RATE_LIMITED).
	CooldownSeconds int
	// Reason is a human-readable explanation.
	Reason string
	// ExitCode echoes the process exit code that informed the classification.
	ExitCode int
}

// DefaultPolicy returns the canonical policy/retryability/failover mapping for a
// §32 class. This is the single source of truth adapters should defer to when
// they lack a more specific signal. It guarantees no infinite retry: Retryable
// classes carry a bounded MaxRetries.
func DefaultPolicy(c FailureClass) FailureClassification {
	switch c {
	case FailureProviderQuota:
		return FailureClassification{Class: c, Policy: PolicyFailover, Failover: true, MaxRetries: 3, Reason: "provider quota exhausted; failover to another route"}
	case FailureProviderRateLimit:
		return FailureClassification{Class: c, Policy: PolicyCooldown, Retryable: true, MaxRetries: 5, CooldownSeconds: 30, Reason: "provider rate limited; cooldown then retry"}
	case FailureProviderCapacity:
		return FailureClassification{Class: c, Policy: PolicyCooldown, Retryable: true, Failover: true, MaxRetries: 3, CooldownSeconds: 15, Reason: "provider capacity; retry or failover"}
	case FailureProviderAuth:
		return FailureClassification{Class: c, Policy: PolicyFailover, Failover: true, MaxRetries: 1, Reason: "auth failed; stop auto-retry, failover possible"}
	case FailureEngineNotInstalled:
		return FailureClassification{Class: c, Policy: PolicyPause, Reason: "engine not installed; pause for onboarding"}
	case FailureEngineCrash:
		return FailureClassification{Class: c, Policy: PolicyRetry, Retryable: true, MaxRetries: 2, Reason: "engine process crashed; bounded retry"}
	case FailureEngineProtocol:
		return FailureClassification{Class: c, Policy: PolicyQuarantine, MaxRetries: 1, Reason: "protocol error; quarantine adapter"}
	case FailureModelNotAvailable:
		return FailureClassification{Class: c, Policy: PolicyFailover, Failover: true, MaxRetries: 2, Reason: "model unavailable; failover route"}
	case FailureImageProvider:
		return FailureClassification{Class: c, Policy: PolicyCooldown, Retryable: true, MaxRetries: 3, CooldownSeconds: 20, Reason: "image provider failure; cooldown"}
	case FailureTimeout:
		return FailureClassification{Class: c, Policy: PolicyRetry, Retryable: true, MaxRetries: 2, Reason: "timeout; bounded retry"}
	case FailureCancelled:
		return FailureClassification{Class: c, Policy: PolicyTerminal, Reason: "run cancelled by caller"}
	case FailureBuildFailure, FailureTestFailure, FailureVisualFailure:
		return FailureClassification{Class: c, Policy: PolicyTerminal, Reason: "verification failure; surface for repair loop"}
	case FailureScopeViolation:
		return FailureClassification{Class: c, Policy: PolicyTerminal, Reason: "agent modified files outside its allowed scope"}
	case FailurePolicyViolation:
		return FailureClassification{Class: c, Policy: PolicyTerminal, Reason: "run violated project security policy"}
	case FailureMalformedOutput:
		return FailureClassification{Class: c, Policy: PolicyRetry, Retryable: true, MaxRetries: 2, Reason: "malformed agent output; bounded retry"}
	case FailureMergeConflict:
		return FailureClassification{Class: c, Policy: PolicyTerminal, Reason: "merge conflict; surface for resolution"}
	case FailureBudgetExceeded:
		return FailureClassification{Class: c, Policy: PolicyPause, Reason: "budget exhausted; pause spending"}
	case FailureInternalError:
		return FailureClassification{Class: c, Policy: PolicyRetry, Retryable: true, MaxRetries: 1, Reason: "internal error; bounded retry"}
	default:
		return FailureClassification{Class: FailureInternalError, Policy: PolicyTerminal, Reason: "unclassified failure; treated as terminal for safety"}
	}
}
