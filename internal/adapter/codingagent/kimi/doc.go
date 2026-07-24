// Package kimi is the in-process NeuroForge adapter for the Kimi Code coding
// agent (spec §12, §13, AC-5).
//
// Kimi Code is a CLI coding engine. This adapter wraps it as an in-process Go
// adapter (ADAPTER_DEV_GUIDE "Path 3"): it locates the `kimi` runtime, probes
// its version and supported flags, builds a deterministic headless argv, spawns
// the agent in its own process group (via [proctree]) inside an isolated home
// directory, parses its streaming JSONL output into normalized protocol events,
// and reports usage/quota/failure signals using the shared §32 taxonomy.
//
// Security invariants honoured (spec §29, AC-28):
//
//   - The agent process receives only an allowlisted environment. Merge tokens,
//     the daemon auth token, unrelated API keys and the entire host environment
//     are NEVER forwarded. Only PATH/HOME/USERPROFILE/USER/LANG/LC_ALL/TERM and
//     OS essentials (SystemRoot/TEMP/TMP) plus the caller's explicit
//     [protocol.AgentRunRequest.AllowlistEnv] are passed.
//   - Kimi never reads or writes the user's global profile: its home/config is
//     relocated to a per-run directory rooted in the run workspace.
//   - Captured stderr is scrubbed of credential-shaped substrings before it is
//     surfaced in events or logs.
//
// Robustness invariants honoured (spec §13, §32):
//
//   - Unknown/malformed/partial stream-json output never aborts a run; it is
//     surfaced as a recoverable warning and the offending line is saved to the
//     artifacts directory.
//   - Cancellation terminates the entire process group (Windows:
//     CREATE_NEW_PROCESS_GROUP + taskkill /T /F; unix: setpgid + negative-pgid
//     signal) via the shared [proctree] package — this adapter never
//     reimplements process handling.
//   - Failure classification never yields an unbounded retry.
//
// The package does not self-register. Callers construct it via [New] and
// register the returned [*Adapter] with [codingagent.Default] if desired.
package kimi

// adapterVersion is the version of this adapter implementation, independent of
// the wrapped engine version and of [protocol.ProtocolVersion].
const adapterVersion = "kimi-adapter/v1"

// defaultBinaryName is the executable looked up on PATH when no override is set.
const defaultBinaryName = "kimi"

// defaultHomeEnvName is the env var used to relocate Kimi's config/home to an
// isolated per-run directory (see [Options.HomeEnvName]).
const defaultHomeEnvName = "KIMI_HOME"
