// Package grok implements the in-process NeuroForge coding-agent adapter for
// Grok Build (spec §12, AC-5). It is a "Path 3 — in-process Go adapter" (see
// docs/architecture/ADAPTER_DEV_GUIDE.md): it implements every method of
// [codingagent.Adapter] and speaks coding-agent protocol version 1
// ([protocol.ProtocolVersion]) without modifying the shared protocol package.
//
// The adapter spawns the headless Grok CLI in its own process group
// ([proctree.NewGroupCommand]) with an allowlisted environment (spec §29.2,
// AC-28), reads its incremental `streaming-json` output line by line, maps each
// item onto the §12.4 normalized event set, and streams the events to the
// caller's [codingagent.EventSink]. It never makes paid API calls itself; all
// billing happens inside the spawned Grok process against the resolved account.
//
// # Wire format
//
// Grok's headless entrypoint used here is:
//
//	grok --no-auto-update -p --output-format streaming-json [--model M] [--resume S] [prompt]
//
// `--no-auto-update` is always passed (rule §36.19: never update a provider CLI
// during an active run). The working directory is mapped to the run workspace via
// the child process CWD (spec §17). Output is parsed incrementally; unknown /
// malformed lines NEVER abort a run — they are surfaced as `warning` events and
// the offending bytes are persisted to the artifacts directory (spec §13.1,
// §36.25 robustness contract).
//
// The exact Grok `streaming-json` item schema is not fully documented upstream.
// The set of item shapes this adapter understands is defined in parser.go and is
// explicitly an ASSUMPTION (marked per rule §36.25): the parser is deliberately
// tolerant — unknown item types and unknown fields are ignored or warned on, so
// the adapter degrades gracefully when the real CLI emits something not yet
// modelled here. See docs/adapters/grok.md for the mapping table and the list of
// explicitly-not-implemented / pending-confirmation items.
//
// # Not implemented / pending confirmation (rule §36.25)
//
//   - LiveUserMessages (SendMessage): Grok's headless `-p` mode has no stdin
//     message channel; SendMessage returns a sentinel error and the capability is
//     reported as false. Deferred until a Grok surface for live messages exists.
//   - ListModels: Grok has no confirmed offline model-listing command, so a
//     single opaque placeholder descriptor is returned (no real model names are
//     hard-coded, rule §36.8). Replace once a `grok models` surface is confirmed.
//   - Version-gated capability thresholds (session resume, cached usage) are
//     ASSUMED; see capabilities.go.
//   - InspectQuota: Grok exposes no authoritative quota probe, so it reports
//     UNKNOWN (rule §36.10).
package grok
