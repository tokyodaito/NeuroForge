// Package supervisor runs and supervises coding-agent processes.
//
// STATUS: implemented (milestone M3).
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §10, §12): start/resume agent runs through
// the adapter protocol, stream normalized events, enforce turn limits (§22.7),
// capture checkpoints, classify failures (§32) to drive retry/failover, and
// write continuation packs (§21.2).
//
// Security (§29.2, AC-28): agent processes run with a restricted allowlisted
// environment and never receive merge credentials, the daemon auth token,
// production secrets, or unrelated API keys. The environment is built by the
// positive-allowlist EnvAllowlist function and verified by AssertEnvSafe.
package supervisor
