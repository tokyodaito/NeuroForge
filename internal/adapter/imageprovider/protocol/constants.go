package protocol

import (
	"neuroforge/internal/adapter/codingagent/protocol"
)

// Constants below re-export the shared §20/§32 constants from the coding-agent
// protocol package so image-provider code can refer to them by the short
// `protocol.HealthOK` / `protocol.QuotaConfExact` form without importing both
// packages. The values are identical (the §32 taxonomy and §20 quota model are
// common to both adapter families — ADR-0006).

// Health status values (spec §12.2).
const (
	HealthOK       = protocol.HealthOK
	HealthDegraded = protocol.HealthDegraded
	HealthDown     = protocol.HealthDown
	HealthUnknown  = protocol.HealthUnknown
)

// Quota confidence values (spec §20.1).
const (
	QuotaConfExact            = protocol.QuotaConfExact
	QuotaConfProviderReported = protocol.QuotaConfProviderReported
	QuotaConfEstimated        = protocol.QuotaConfEstimated
	QuotaConfInferred         = protocol.QuotaConfInferred
	QuotaConfUnknown          = protocol.QuotaConfUnknown
)

// Quota state values (spec §20.2).
const (
	QuotaStateAvailable   = protocol.QuotaStateAvailable
	QuotaStateDepleted    = protocol.QuotaStateDepleted
	QuotaStateRateLimited = protocol.QuotaStateRateLimited
	QuotaStateCooldown    = protocol.QuotaStateCooldown
	QuotaStateUnknown     = protocol.QuotaStateUnknown
)

// §32 failure classes (shared taxonomy). Aliases for the image-provider
// surface; see codingagent/protocol/failure.go for the canonical list.
const (
	FailureProviderQuota      = protocol.FailureProviderQuota
	FailureProviderRateLimit  = protocol.FailureProviderRateLimit
	FailureProviderCapacity   = protocol.FailureProviderCapacity
	FailureProviderAuth       = protocol.FailureProviderAuth
	FailureEngineNotInstalled = protocol.FailureEngineNotInstalled
	FailureEngineCrash        = protocol.FailureEngineCrash
	FailureEngineProtocol     = protocol.FailureEngineProtocol
	FailureModelNotAvailable  = protocol.FailureModelNotAvailable
	FailureImageProvider      = protocol.FailureImageProvider
	FailureTimeout            = protocol.FailureTimeout
	FailureCancelled          = protocol.FailureCancelled
	FailureBuildFailure       = protocol.FailureBuildFailure
	FailureTestFailure        = protocol.FailureTestFailure
	FailureVisualFailure      = protocol.FailureVisualFailure
	FailureScopeViolation     = protocol.FailureScopeViolation
	FailurePolicyViolation    = protocol.FailurePolicyViolation
	FailureMalformedOutput    = protocol.FailureMalformedOutput
	FailureMergeConflict      = protocol.FailureMergeConflict
	FailureBudgetExceeded     = protocol.FailureBudgetExceeded
	FailureInternalError      = protocol.FailureInternalError
)

// §32 failure policies.
const (
	PolicyRetry      = protocol.PolicyRetry
	PolicyCooldown   = protocol.PolicyCooldown
	PolicyFailover   = protocol.PolicyFailover
	PolicyEscalation = protocol.PolicyEscalation
	PolicyPause      = protocol.PolicyPause
	PolicyQuarantine = protocol.PolicyQuarantine
	PolicyTerminal   = protocol.PolicyTerminal
)

// DefaultPolicy re-exports the canonical §32 classification for a class so
// image-provider adapters share the same retry/failover/cooldown mapping.
func DefaultPolicy(c FailureClass) FailureClassification {
	return protocol.DefaultPolicy(c)
}
