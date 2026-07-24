// Package repoinfo builds the repository index for token-efficient context.
//
// STATUS: scaffold — not implemented (planned for milestone M12).
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §22): never dump the whole repo into a
// prompt; provide Git/file-tree/symbol/imports/build/test graphs plus SQLite FTS
// and history of related changes, build Context Packs, slice logs, and assemble
// delta repair context. A vector DB is optional for v1.
//
// Boundaries: read-only analysis of the worktree; must not mutate sources.
package repoinfo
