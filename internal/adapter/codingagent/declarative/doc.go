// Package declarative implements the declarative command coding-agent adapter
// (spec §13.1): a YAML manifest describes a CLI agent, so a new command-line
// engine can be registered with no Go code changes (AC-6).
//
// A [Manifest] describes the detect and run commands plus the engine's
// capabilities. The [Adapter] spawns the run command in its own process group,
// reads newline-delimited JSON normalized events from stdout (spec §13.1
// --output jsonl), parses them with [protocol.ParseEventLine], and forwards
// them to the caller's [EventSink]. Malformed lines are saved as artifacts and
// classified rather than aborting the run (spec: malformed event is saved and
// classified, never fatal). Cancellation terminates the whole process group.
//
// Manifest YAML is parsed by a deliberately minimal, dependency-free parser that
// supports the §13.1 grammar (nested maps, block sequences, scalar values). It
// is NOT a general YAML parser; the trade-off keeps the module dependency-free
// (AGENTS.md, ADR-0010).
package declarative
