package imageprovider

import (
	"errors"
	"strings"

	"neuroforge/internal/adapter/codingagent/protocol" // §32 taxonomy
	iprotocol "neuroforge/internal/adapter/imageprovider/protocol"
)

// Failure sentinel values image providers commonly return. They are mapped onto
// the §32 taxonomy by [DefaultClassify]. Real adapters wrap provider-specific
// errors into these so the supervisor/quota/budget subsystems consume one set.
var (
	// ErrQuotaExhausted signals the image provider has no remaining quota for
	// this account/window. Maps to PROVIDER_QUOTA → failover.
	ErrQuotaExhausted = errors.New("imageprovider: quota exhausted")
	// ErrRateLimited signals a transient rate limit (retry-after applies).
	// Maps to PROVIDER_RATE_LIMIT → cooldown.
	ErrRateLimited = errors.New("imageprovider: rate limited")
	// ErrAuthFailed signals an auth/permission problem. Maps to PROVIDER_AUTH
	// → stop automatic retry.
	ErrAuthFailed = errors.New("imageprovider: auth failed")
	// ErrInvalidImage signals the generated image failed validation (corrupt,
	// zero-byte, wrong format). Maps to IMAGE_PROVIDER_FAILURE.
	ErrInvalidImage = errors.New("imageprovider: invalid image")
	// ErrModelNotAvailable signals the requested model is unknown to the
	// provider. Maps to MODEL_NOT_AVAILABLE → failover.
	ErrModelNotAvailable = errors.New("imageprovider: model not available")
	// ErrTimeout signals the generation exceeded its deadline.
	ErrTimeout = errors.New("imageprovider: timeout")
)

// DefaultClassify maps a native image-provider error onto the §32 taxonomy (spec
// §14.2 AnalyzeFailure, §32). Deterministic (no LLM — rule §22.6). Adapters
// without a more specific signal call this.
//
// The classifier never produces an infinite-retry classification (rule §32):
// retryable classes carry a bounded MaxRetries via [protocol.DefaultPolicy].
func DefaultClassify(err error) iprotocol.FailureClassification {
	if err == nil {
		fc := protocol.DefaultPolicy(protocol.FailureInternalError)
		fc.Reason = "AnalyzeFailure called with nil error"
		fc.Retryable = false
		fc.Policy = protocol.PolicyTerminal
		return fc
	}
	switch {
	case errors.Is(err, ErrQuotaExhausted):
		return protocol.DefaultPolicy(protocol.FailureProviderQuota)
	case errors.Is(err, ErrRateLimited):
		return protocol.DefaultPolicy(protocol.FailureProviderRateLimit)
	case errors.Is(err, ErrAuthFailed):
		return protocol.DefaultPolicy(protocol.FailureProviderAuth)
	case errors.Is(err, ErrModelNotAvailable):
		return protocol.DefaultPolicy(protocol.FailureModelNotAvailable)
	case errors.Is(err, ErrInvalidImage):
		return protocol.DefaultPolicy(protocol.FailureImageProvider)
	case errors.Is(err, ErrTimeout):
		return protocol.DefaultPolicy(protocol.FailureTimeout)
	}
	// Fall back on substring heuristics for unwrapped native errors.
	low := strings.ToLower(err.Error())
	switch {
	case strings.Contains(low, "quota") || strings.Contains(low, "exhausted"):
		return protocol.DefaultPolicy(protocol.FailureProviderQuota)
	case strings.Contains(low, "rate limit") || strings.Contains(low, "429"):
		return protocol.DefaultPolicy(protocol.FailureProviderRateLimit)
	case strings.Contains(low, "unauthorized") || strings.Contains(low, "401") || strings.Contains(low, "auth"):
		return protocol.DefaultPolicy(protocol.FailureProviderAuth)
	case strings.Contains(low, "model") && (strings.Contains(low, "not available") || strings.Contains(low, "not found")):
		return protocol.DefaultPolicy(protocol.FailureModelNotAvailable)
	case strings.Contains(low, "invalid image") || strings.Contains(low, "corrupt"):
		return protocol.DefaultPolicy(protocol.FailureImageProvider)
	case strings.Contains(low, "timeout") || strings.Contains(low, "deadline"):
		return protocol.DefaultPolicy(protocol.FailureTimeout)
	}
	fc := protocol.DefaultPolicy(protocol.FailureImageProvider)
	fc.Reason = "unclassified image-provider error: " + err.Error()
	return fc
}
