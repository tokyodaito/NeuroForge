// Package repoinfo builds the repository index and token-efficient context for
// coding agents (spec §22).
//
// STATUS: implemented for milestone M12.
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §22): never dump the whole repo into a
// prompt. Instead, build a repository index (file tree, symbol index, imports,
// build/test graph, SQLite FTS, history of related changes) and assemble a
// compact Context Pack (§22.3) that stays within a token budget (§22.1). Log
// slicing (§22.4) and delta repair context (§22.5) keep failure payloads small.
// Prompt-cache fingerprinting (§22.8) gives stable ordering so providers that
// support caching can reuse context.
//
// The package is pure analysis code: it reads the worktree and never mutates
// sources. It never calls an LLM (rule §22.6).
//
// Boundaries: read-only analysis of the worktree; must not mutate sources.
package repoinfo
