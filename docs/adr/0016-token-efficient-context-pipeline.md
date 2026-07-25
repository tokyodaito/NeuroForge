# ADR-0016: Token-efficient context pipeline (repo index + Context Pack)

- **Status:** Accepted
- **Date:** 2026-07-25
- **Spec refs:** §22 (Token Optimization Engine), §22.1 (no full repo dump),
  §22.2 (repo index), §22.3 (Context Pack), §22.4 (log slicing), §22.5 (delta
  repair context), §22.6 (no LLM for deterministic ops), §22.8 (prompt cache),
  §22.9 (project memory), rule §36.11 (no full repo in prompt)

## Context

M12 delivers the token-optimisation engine (spec §22). The spec forbids dumping
the whole repository into a prompt (rule §36.11) and demands that models never
receive full enormous logs (§22.4) or the full research history during repair
(§22.5). At the same time, deterministic operations must never use an LLM
(rule §22.6), and prompt-cache-friendly providers need byte-stable prefixes
(§22.8).

## Decision

A single pure-Go package, `internal/repoinfo`, owns the entire pipeline:

1. **Repo index** (`Build`) — a single read-only walk produces the §22.2 graph:
   file tree, a conservative language-agnostic symbol index, imports, and
   detected build/test/lint commands. Vendor/generated trees are mapped but not
   symbol-indexed; a `MaxRepoFiles` cap bounds memory for giant monorepos.
2. **Context Pack** (`AssemblePack`) — assembles spec + scope + repo map +
   relevant file slices + architectural rules + commands + recent failures +
   artifact links, **trimmed to a token budget** (§22.1). Least-relevant files
   are dropped first; the core (spec/scope/rules) is non-trimmable. A
   deterministic token estimator (`EstimateTokens`) bounds the pack without a
   provider tokenizer (rule §22.6).
3. **Log slicing** (`SliceLog`) — reduces a raw log to exit code + failing
   command + first error + relevant stack trace + deduped other-error summary +
   full-log link, bounded to `MaxLogTokens`.
4. **Delta repair** (`AssembleDelta`) — finding + diff + failing test +
   implicated files only; never the full conversation (§22.5).
5. **Prompt-cache fingerprinting** (`StablePrefix` + `FingerprintPrompt`) — the
   cacheable parts are rendered in deterministic order and hashed so providers
   can detect cache hits (§22.8).

`internal/memory` (§22.9) supplies the structured project memory whose
high-confidence records feed the pack's "architectural rules".

The package is pure analysis code; it never mutates sources and never calls an
LLM.

## Consequences

**Positive**

- Rule §36.11 is enforced structurally: there is no code path that places the
  whole repo into a prompt — the pack is budget-trimmed by construction.
- §22.4/§22.5 keep failure/repair payloads small and deterministic.
- §22.8 prompt-cache reuse is detectable (stable fingerprint).

**Negative / trade-offs**

- The in-memory search is a ranked substring/term scan (the SQLite-FTS analogue)
  rather than a real FTS5 index. Acceptable for v1: the index is bounded and
  deterministic; the daemon can persist/upgrade to FTS5 later without changing
  the pack API.

## Alternatives considered

- **Vector DB.** Rejected for v1 (spec §22.2: "не обязательна для первой
  версии"). The symbol/import/co-change graph covers relevance ranking.
- **Provider tokenizer for budgeting.** Rejected (rule §22.6): the estimator is
  deterministic and model-independent.
