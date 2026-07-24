package codingagent

import (
	"context"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// CodingAgentAdapter is the unified surface every coding engine implements
// (spec §12.2). Methods are grouped:
//
//   - metadata: [Adapter.ID], [Adapter.Detect], [Adapter.Version],
//     [Adapter.Health], [Adapter.Capabilities], [Adapter.ListModels],
//     [Adapter.InspectQuota];
//   - run lifecycle: [Adapter.Start], [Adapter.Resume], [Adapter.SendMessage],
//     [Adapter.Cancel];
//   - diagnostics: [Adapter.ClassifyFailure].
//
// The interface references the versioned types in package protocol so the
// stability boundary is explicit. Concrete engine adapters land in milestones
// M4/M5; the fake adapter (M2) and the declarative/plugin adapters (M2) are the
// first implementations.
type Adapter interface {
	// ID is the stable engine identifier (spec §12.1), e.g. "fake", "codex". It
	// is independent from any model name.
	ID() string

	// Detect reports whether the engine runtime is present/usable (spec §12.2).
	Detect(context.Context) protocol.DetectionResult

	// Version reports adapter, engine and protocol versions (spec §12.2).
	Version(context.Context) protocol.VersionResult

	// Health probes one account's reachability (spec §12.2).
	Health(ctx context.Context, account protocol.Account) protocol.HealthResult

	// Capabilities returns the static capability profile (spec §12.3).
	Capabilities(context.Context) protocol.AgentCapabilities

	// ListModels returns the models this engine can target for an account
	// (spec §12.2). Model names are provider-supplied; the core never
	// hard-codes them (rule §36.8).
	ListModels(ctx context.Context, account protocol.Account) ([]protocol.ModelDescriptor, error)

	// InspectQuota returns the current quota snapshot for an account (spec
	// §12.2, §20.1).
	InspectQuota(ctx context.Context, account protocol.Account) protocol.QuotaSnapshot

	// Start begins a run (spec §12.2). It streams normalized events to sink and
	// returns a live handle. The request never carries credentials (AC-28).
	Start(ctx context.Context, req protocol.AgentRunRequest, sink EventSink) (protocol.RunHandle, error)

	// Resume continues a previous run via a continuation pack/session (spec
	// §12.2, §21).
	Resume(ctx context.Context, req protocol.ResumeRequest, sink EventSink) (protocol.RunHandle, error)

	// SendMessage injects a user message into a running session (spec §12.2,
	// §6.5). Only valid when [Adapter.Capabilities] reports LiveUserMessages.
	SendMessage(ctx context.Context, handle protocol.RunHandle, msg protocol.AgentMessage) error

	// Cancel terminates a run. Implementations MUST terminate the entire agent
	// process group (spec: cancellation ends the whole process group), not just
	// the parent process.
	Cancel(ctx context.Context, handle protocol.RunHandle) error

	// ClassifyFailure maps a native failure signal onto the §32 taxonomy (spec
	// §12.2, §32). Adapters without a specific signal should defer to
	// [DefaultClassify].
	ClassifyFailure(exitCode int, events []protocol.NormalizedEvent, stderr string) protocol.FailureClassification
}

// CodingAgentAdapter is an alias for [Adapter], kept for spec readability
// (§12.2 names the interface CodingAgentAdapter). New code should use Adapter.
type CodingAgentAdapter = Adapter

// EventSink consumes the normalized event stream produced by an adapter run
// (spec §12.4). Implementations must be safe for concurrent use when the
// adapter emits from multiple goroutines. Returning a non-nil error signals the
// adapter to abort the run (e.g. on caller cancellation); otherwise the event
// is delivered in order and the run continues.
type EventSink interface {
	OnEvent(ctx context.Context, ev protocol.NormalizedEvent) error
}

// SinkFunc is an adapter allowing ordinary functions to satisfy [EventSink].
type SinkFunc func(ctx context.Context, ev protocol.NormalizedEvent) error

// OnEvent implements [EventSink].
func (f SinkFunc) OnEvent(ctx context.Context, ev protocol.NormalizedEvent) error { return f(ctx, ev) }
