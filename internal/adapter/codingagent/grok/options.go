package grok

// AdapterVersion is the version of this adapter implementation (independent of
// [protocol.ProtocolVersion] and of the Grok CLI version).
const AdapterVersion = "grok-adapter-v1"

// defaultBinaryName is the executable resolved by [detect] when Options.Binary
// is empty. It is intentionally a generic name, never a model name (rule §36.8).
const defaultBinaryName = "grok"

// Options configures a [Adapter]. The zero value is usable: the adapter will
// resolve "grok" via PATH, parse its version, derive capabilities, and save
// malformed-output artifacts to the system temp directory.
type Options struct {
	// Binary overrides the Grok executable name or path resolved by detection
	// and used as the headless command (argv[0]). Empty → "grok" (resolved via
	// PATH/PATHEXT). Use an absolute path to tolerate spaces/Unicode.
	Binary string

	// ArtifactsDir is where malformed agent output lines are persisted for
	// forensics (spec §13.1). Empty → [os.TempDir].
	ArtifactsDir string

	// EnableTurnLimit maps AgentRunRequest.TurnLimit to Grok's turn-limit flag
	// when true. Disabled by default because the exact flag is pending
	// confirmation against the installed CLI (rule §36.25): an unknown flag
	// could break a real run. The supervisor's wall-clock Timeout always binds.
	EnableTurnLimit bool

	// EnableACP reports ACP support in capabilities when true. Off by default:
	// Agent Client/Server Protocol support is optional and version-dependent.
	// This does NOT change protocol v1 (rule: do not change the protocol to
	// accommodate it); it only toggles the declared capability bit.
	EnableACP bool

	// ResumeEnabled forces [Capabilities].SessionResume on (or off) regardless
	// of the version gate. nil → derive from the detected version. Useful for
	// integration tests against a stub binary.
	ResumeEnabled *bool

	// ExtraEnv is appended to the allowlisted child environment. It is intended
	// for adapter integration tests that drive a stub binary (e.g. selecting a
	// recorded scenario). The daemon MUST NOT populate this with secrets — it
	// bypasses the per-request allowlist by design (spec §29.2, AC-28).
	ExtraEnv []string
}
