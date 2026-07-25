package visualharness

import (
	"errors"
	"strings"

	"neuroforge/internal/adapter/codingagent/protocol" // §32 taxonomy
)

// Sentinel errors visual harnesses commonly surface.
var (
	// ErrStartupFailed: the emulator/app failed to launch (§33.3 "startup
	// failure").
	ErrStartupFailed = errors.New("visualharness: startup failed")
	// ErrBuildFailed: the build step failed.
	ErrBuildFailed = errors.New("visualharness: build failed")
	// ErrCaptureFailed: the screenshot capture failed.
	ErrCaptureFailed = errors.New("visualharness: capture failed")
	// ErrDeviceNotFound: no device/emulator available.
	ErrDeviceNotFound = errors.New("visualharness: device not found")
)

// DefaultClassify maps a harness error onto the §32 taxonomy.
func DefaultClassify(err error) protocol.FailureClassification {
	if err == nil {
		fc := protocol.DefaultPolicy(protocol.FailureInternalError)
		fc.Reason = "ClassifyFailure called with nil error"
		fc.Retryable = false
		fc.Policy = protocol.PolicyTerminal
		return fc
	}
	switch {
	case errors.Is(err, ErrStartupFailed):
		return protocol.DefaultPolicy(protocol.FailureVisualFailure)
	case errors.Is(err, ErrBuildFailed):
		return protocol.DefaultPolicy(protocol.FailureBuildFailure)
	case errors.Is(err, ErrCaptureFailed):
		return protocol.DefaultPolicy(protocol.FailureVisualFailure)
	case errors.Is(err, ErrDeviceNotFound):
		return protocol.DefaultPolicy(protocol.FailureVisualFailure)
	}
	low := strings.ToLower(err.Error())
	switch {
	case strings.Contains(low, "emulator") && strings.Contains(low, "not found"):
		return protocol.DefaultPolicy(protocol.FailureVisualFailure)
	case strings.Contains(low, "timeout") || strings.Contains(low, "deadline"):
		return protocol.DefaultPolicy(protocol.FailureTimeout)
	case strings.Contains(low, "build"):
		return protocol.DefaultPolicy(protocol.FailureBuildFailure)
	}
	fc := protocol.DefaultPolicy(protocol.FailureVisualFailure)
	fc.Reason = "unclassified visual-harness error: " + err.Error()
	return fc
}
