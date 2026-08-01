// Package claude is the in-process NeuroForge adapter for Anthropic's Claude
// Code CLI (spec §12 engines, AC-5, milestone M4). It is a "Path 3" in-process
// adapter (see docs/architecture/ADAPTER_DEV_GUIDE.md): it implements the full
// [codingagent.Adapter] surface (spec §12.2) by spawning the headless
// `claude -p` (print/SDK) mode, streaming its `--output-format stream-json`
// output, and translating each Claude SDK message into the protocol-v1
// [protocol.NormalizedEvent] set.
//
// The adapter is deliberately self-contained: it does not self-register (no
// init / MustRegister). Callers construct it with [New] and register it into a
// [codingagent.Registry] at the daemon wiring layer.
//
// # Hard guarantees
//
//   - Credential-free request boundary (spec §29.2, AC-28): the agent process
//     receives only an allowlisted environment (PATH/HOME/USER/LANG/LC_ALL/TERM
//     plus the caller's allowlist). Merge
//     tokens, the daemon auth token and unrelated API keys are never injected
//     by the adapter; the caller is responsible for allowlisting any provider
//     credential (e.g. an API key) it wants the agent to see.
//   - No dangerous permission bypass: the adapter never emits
//     `--dangerously-skip-permissions` / `--permission-mode bypassPermissions`
//     and rejects those values in [Options]. The default permission mode is
//     `default`.
//   - No hard-coded model names: models are provider-supplied via [Options.Models]
//     / [Adapter.ListModels]; the engine id is the stable string "claude".
//   - Robust streaming: lines longer than bufio.Scanner's 64KiB cap are handled,
//     UTF-8 BOM and CRLF are tolerated, and malformed / unknown future Claude
//     events never abort a run (spec: malformed event is saved + classified, not
//     fatal).
//   - Cancellation ends the whole process group via the shared
//     [neuroforge/internal/adapter/codingagent/proctree] package (setpgid +
//     negative-pgid signal).
//   - No real paid calls in tests (rule §36.5): unit and conformance tests use
//     recorded byte-stream fixtures and stub probes; the real CLI is exercised
//     only by the opt-in `claudesmoke` build-tagged test.
//
// See docs/adapters/claude.md for the command shape, capability matrix, failure
// mapping and the explicitly-not-implemented list.
package claude
