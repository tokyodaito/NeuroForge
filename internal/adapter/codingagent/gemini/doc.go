// Package gemini implements the NeuroForge coding-agent adapter for the
// Google Gemini CLI (spec §12, AC-5). It is an in-process Go adapter ("Path 3"
// of docs/architecture/ADAPTER_DEV_GUIDE.md): it implements every method of
// [codingagent.Adapter] without self-registering. Callers construct it with
// [New] and register it into a [codingagent.Registry] at the wiring site.
//
// The adapter drives the headless Gemini CLI (`gemini -p … --output-format
// json`) in an isolated worktree (spec §17), streams its output into the
// normalized event set, and maps failures onto the §32 taxonomy. It never makes
// a paid call itself; all execution is delegated to the installed Gemini CLI.
//
// Robustness invariants honoured (spec §13, §29, §32):
//
//   - The agent process runs in its own process group (proctree); cancellation
//     terminates the whole group, never orphaning descendants.
//   - The agent environment is a positive allowlist (PATH/HOME/USER/LANG/
//     LC_ALL/TERM + the request allowlist). Merge
//     tokens, API keys and the daemon auth token are never passed (§29.2, AC-28).
//   - Malformed, partial or unknown-future output never aborts a run; it is
//     surfaced as a recoverable warning and persisted to the artifacts dir.
//   - Usage is mapped from Gemini's reported token counts at PROVIDER_REPORTED
//     confidence; absent fields are reported as zero, never fabricated (§36.10).
//   - Unimplemented features are explicitly marked, never disguised as finished
//     stubs (§36.25).
package gemini
