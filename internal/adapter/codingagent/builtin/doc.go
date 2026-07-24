// Package builtin is the central wiring site for NeuroForge's first-party
// coding-agent adapters (spec §12, AC-5).
//
// The package contains no provider-specific behaviour. Its sole responsibility
// is to construct the six built-in engine adapters (Codex, Claude Code, Gemini
// CLI, Kimi Code, Grok Build, OpenCode) with their default options and register
// them into a [codingagent.Registry] at daemon startup. Adding a new engine
// therefore remains purely additive (spec §13.3, AC-6): a new adapter package
// gains one line here and the core never changes.
//
// Every constructor it invokes lives in its own self-contained subpackage under
// internal/adapter/codingagent; this package merely references them.
package builtin
