package protocol

import "time"

// Account is an opaque reference to a provider account. It is name-only on
// purpose: adapters resolve the account's credentials internally (from the
// daemon's secret store), and an agent process never receives credentials
// through the protocol (spec §29.2, AC-28).
type Account struct {
	// Name identifies the account within the engine (e.g. "work-api-account").
	// Resolved by the adapter; never passed verbatim to a child process.
	Name string
}

// ModelKind classifies what a model is used for. Coding agents expose coding
// models; image providers (a separate adapter family) expose image models.
// Keeping the kind here lets the router treat engines and models uniformly
// without hard-coding names (spec §12.1, §19.2, rule §36.8).
type ModelKind string

const (
	ModelKindCoding ModelKind = "coding"
	ModelKindImage  ModelKind = "image"
)

// ModelDescriptor describes a model an engine can target. Model names are
// provider-supplied; the core never hard-codes them (rule §36.8, §19.2).
type ModelDescriptor struct {
	// ID is the fully-qualified provider/model identifier (e.g. a router-assigned
	// or provider-native id). Opaque to the core.
	ID string
	// Engine is the agent engine that serves this model (spec §12.1: an engine
	// is not a model).
	Engine string
	// Kind distinguishes coding vs image models.
	Kind ModelKind
	// Tier is an optional, provider-declared complexity/economy hint
	// (e.g. IMAGE_DRAFT/IMAGE_STANDARD/IMAGE_HIGH_QUALITY, §14.3). The router
	// must not hard-bind a tier to a model name (§14.3).
	Tier string
	// ContextWindow is the max input context in tokens, if known (<=0 unknown).
	ContextWindow int
	// MaxOutput is the max output tokens, if known (<=0 unknown).
	MaxOutput int
	// SupportsImages reports image input capability.
	SupportsImages bool
	// CachedUsage reports whether the model can report cached-token usage
	// (spec §22.8 prompt-cache). Influences usage accounting.
	CachedUsage bool
}

// DetectionResult is the outcome of [CodingAgentAdapter.Detect] (spec §12.2):
// whether the engine binary/runtime is present and usable on this host.
type DetectionResult struct {
	// Installed reports whether the engine was found.
	Installed bool
	// Path is the resolved binary/runtime location, if any.
	Path string
	// Version is the detected engine version string, if reported.
	Version string
	// Detail is a human-readable diagnostic (used by `forge agent doctor`).
	Detail string
}

// VersionResult reports the adapter and engine versions (spec §12.2). The
// adapter version is independent from [ProtocolVersion].
type VersionResult struct {
	// AdapterVersion is the version of this adapter implementation.
	AdapterVersion string
	// EngineVersion is the version reported by the underlying engine runtime.
	EngineVersion string
	// ProtocolVersion is the coding-agent protocol version the adapter speaks
	// (always [ProtocolVersion] for a compliant adapter).
	ProtocolVersion int
	// Error carries a non-fatal detection error, if any.
	Error string
}

// HealthStatus is a coarse engine/account health signal (spec §12.2 Health).
type HealthStatus string

const (
	HealthOK       HealthStatus = "ok"
	HealthDegraded HealthStatus = "degraded"
	HealthDown     HealthStatus = "down"
	HealthUnknown  HealthStatus = "unknown"
)

// HealthResult is the outcome of [CodingAgentAdapter.Health] for one account.
type HealthResult struct {
	Status HealthStatus
	// Detail is a human-readable explanation.
	Detail string
	// Latency is the measured round-trip, if a probe was performed.
	Latency time.Duration
}

// QuotaConfidence models the spec §20.1 confidence levels. Adapters must not
// report a quota as more precise than the provider actually warrants (rule
// §36.10): default to the least precise applicable level.
type QuotaConfidence string

const (
	// QuotaConfExact means the provider returned an authoritative remaining figure.
	QuotaConfExact QuotaConfidence = "EXACT"
	// QuotaConfProviderReported means the provider exposes a usage/limit figure
	// that is computed but authoritative enough to display without an estimate
	// badge.
	QuotaConfProviderReported QuotaConfidence = "PROVIDER_REPORTED"
	// QuotaConfEstimated means NeuroForge inferred remaining quota from observed
	// usage (no provider figure available).
	QuotaConfEstimated QuotaConfidence = "ESTIMATED"
	// QuotaConfInferred means remaining quota is derived from heuristics (e.g.
	// error signals) rather than direct measurement.
	QuotaConfInferred QuotaConfidence = "INFERRED"
	// QuotaConfUnknown means no quota information is available.
	QuotaConfUnknown QuotaConfidence = "UNKNOWN"
)

// QuotaState models the spec §20.2 quota states as reported by an adapter. Full
// quota management (circuit breaker, cooldown accounting) is milestone M6; the
// adapter merely reports the snapshot.
type QuotaState string

const (
	QuotaStateAvailable   QuotaState = "AVAILABLE"
	QuotaStateDepleted    QuotaState = "DEPLETED"
	QuotaStateRateLimited QuotaState = "RATE_LIMITED"
	QuotaStateCooldown    QuotaState = "COOLDOWN"
	QuotaStateUnknown     QuotaState = "UNKNOWN"
)

// QuotaSnapshot is the outcome of [CodingAgentAdapter.InspectQuota] (spec §12.2,
// §20.1). All optional fields use pointer/value-zero semantics: Remaining is
// nil when unknown.
type QuotaSnapshot struct {
	// Confidence is the precision of this snapshot (spec §20.1, rule §36.10).
	Confidence QuotaConfidence
	// State is the current quota state (spec §20.2).
	State QuotaState
	// Remaining is the remaining quota in provider units, if known. nil unknown.
	Remaining *float64
	// Limit is the total quota, if known.
	Limit *float64
	// Window is the quota window (e.g. "daily"), provider-specific.
	Window string
	// ResetAt is when the quota resets, if known.
	ResetAt time.Time
	// RetryAfter is a hint to wait before retrying (e.g. after RATE_LIMITED).
	RetryAfter time.Duration
	// Reason is a human-readable explanation.
	Reason string
}
