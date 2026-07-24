package protocol

import "time"

// AgentRunRequest describes a single coding-agent run (spec §12.2 Start). It is
// deliberately credential-free (spec §29.2, AC-28): it references an [Account]
// by name only and never carries merge tokens, the daemon auth token, or
// unrelated API keys. The adapter resolves credentials internally.
type AgentRunRequest struct {
	// RunID is the caller-assigned, stable identifier for this run. Used to
	// correlate events and durable state.
	RunID string
	// Engine is the agent engine id (spec §12.1: engine != model). Required.
	Engine string
	// Model is the provider/model to target (spec §12.1). Opaque to the core;
	// resolved from the model catalog (M6-1), never hard-coded.
	Model string
	// Account is the credentials reference (name only).
	Account Account
	// Workspace is the absolute path to the isolated worktree the agent runs in
	// (spec §17). The primary checkout is never passed.
	Workspace string
	// PromptFile is the path to the compiled task prompt on disk (spec §22.3
	// context pack). Mutually exclusive with Prompt.
	PromptFile string
	// Prompt is an inline prompt, used when no file is written. Mutually
	// exclusive with PromptFile.
	Prompt string
	// Scope is the allowlist of repository paths the agent may modify (spec
	// §22.6 scope check; SCOPE_VIOLATION on departure). Empty means "the whole
	// workspace".
	Scope []string
	// AllowlistEnv is the allowlisted environment variables to expose to the
	// agent process (spec §29.2). The supervisor constructs this; adapters must
	// not augment it with secrets.
	AllowlistEnv []string
	// TurnLimit is the max agent turns for this run (spec §22.7). <=0 means the
	// engine default applies.
	TurnLimit int
	// Timeout is the hard wall-clock limit for the run. Zero means no limit
	// (the supervisor still applies its own).
	Timeout time.Duration
	// SessionID, when set on Start, requests resuming an existing provider
	// session. Normally used via [ResumeRequest] instead.
	SessionID string
}

// ResumeRequest resumes a previously started run (spec §12.2 Resume, §21
// continuation packs). SessionID identifies the provider session to resume;
// CheckpointPath points at the continuation-pack artifact.
type ResumeRequest struct {
	RunID          string
	Engine         string
	Model          string
	Account        Account
	Workspace      string
	SessionID      string
	CheckpointPath string
	Scope          []string
	AllowlistEnv   []string
	TurnLimit      int
	Timeout        time.Duration
}

// RunHandle is the live handle returned by Start/Resume (spec §12.2). Engine
// and Model are carried separately to keep the §12.1 distinction explicit at
// every layer.
type RunHandle struct {
	// RunID matches [AgentRunRequest.RunID].
	RunID string
	// Engine is the agent engine serving this run (spec §12.1).
	Engine string
	// Model is the model being targeted (spec §12.1).
	Model string
	// Account is the credentials reference.
	Account Account
	// SessionID is the provider session id, set when the engine supports resume
	// ([AgentCapabilities.SessionResume]). Used for continuation packs.
	SessionID string
}

// AgentMessage is a user message injected into a running session (spec §12.2
// SendMessage, §6.5 composer). Only meaningful when
// [AgentCapabilities.LiveUserMessages] is true.
type AgentMessage struct {
	// Text is the message body.
	Text string
	// Role defaults to "user"; adapters may pass through other roles if the
	// engine supports them.
	Role string
}
