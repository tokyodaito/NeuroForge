// Package opencode is the in-process NeuroForge coding-agent adapter for the
// OpenCode agent engine (spec §12, §13; AC-5).
//
// OpenCode is an Agent Engine, not a model provider (spec §12.1: engine != model).
// The model is supplied separately via [protocol.AgentRunRequest.Model] in the
// form "provider/model"; this adapter drives the OpenCode engine to execute a
// task in an isolated worktree (spec §17) using its headless entry point.
//
// The adapter is a Path-3 in-process Go adapter (see
// docs/architecture/ADAPTER_DEV_GUIDE.md). It wraps the OpenCode CLI's
// non-interactive `run` command, streaming its JSONL output and normalising it
// onto the Protocol-v1 [protocol.NormalizedEvent] set. It does NOT register
// itself: callers construct it via [New] and register it with the daemon's
// [neuroforge/internal/adapter/codingagent.Registry].
//
// # Headless contract
//
// One-shot runs use `opencode run --format json`. The persistent OpenCode server
// (`opencode serve`) is never started by this adapter.
//
// # Security invariants (enforced unconditionally)
//
//   - The agent process receives an ALLOWLISTED environment only (spec §29.2,
//     AC-28): PATH/HOME/USER/LANG/LC_ALL/TERM/TEMP/TMP
//     plus the caller's [protocol.AgentRunRequest.AllowlistEnv]. VCS merge
//     tokens, production credentials, unrelated API keys and the daemon auth
//     token are never forwarded.
//   - `--share` is NEVER passed: NeuroForge-managed runs are never shared.
//   - Secret values are redacted from captured stderr, warning events and
//     malformed-output artifacts.
//   - A workspace-level OpenCode project/plugin config cannot weaken NeuroForge
//     policy: the allowlist, no-share and redaction rules are applied regardless
//     of any plugin or agent config present in the worktree.
//
// # Robustness
//
// Unknown event types and malformed JSONL never abort a run: they are surfaced
// as recoverable warning events and persisted to the artifacts dir for
// forensics (spec: malformed events are saved + classified, never fatal).
// Cancellation terminates the whole process group via
// [neuroforge/internal/adapter/codingagent/proctree].
package opencode
