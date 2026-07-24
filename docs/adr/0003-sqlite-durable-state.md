# ADR-0003: SQLite (WAL) for durable state

- **Status:** Accepted
- **Date:** 2026-07-24
- **Spec refs:** §11.4 (durable workflow), §31 (storage schema), §22.2 (SQLite
  FTS), §10 (primary store)

## Context

The daemon (ADR-0002) needs an embedded, transactional, zero-ops store that
survives restarts and supports structured queries (projects, tasks, attempts,
quota snapshots, audit) plus full-text search over the repo index (§22.2). The
spec fixes the primary store as SQLite (§10) and lists the minimal table set
(§31). Large artifacts must live on the filesystem, not as BLOBs (§31).

> Not yet implemented — `internal/storage` is currently a scaffold (M0). The
> concrete driver choice will be a follow-up ADR amendment recorded here when M0
> storage lands, including CGO vs pure-Go trade-off justification (a dependency,
> so it will be justified per `AGENTS.md`).

## Decision

Use **SQLite in WAL mode** as the single durable store, owned exclusively by
`internal/storage`. Concretely (when implemented):

- WAL journal mode for concurrent read + single writer.
- Explicit schema migrations (`schema_migrations` table, §31) — forward-only,
  versioned.
- Persist the "before" state of any external action transactionally (§11.4).
- Store large artifacts content-addressed on disk (§9.5, §31) and keep only
  references/hashes in SQLite.
- Use SQLite FTS for the repo index (§22.2); a vector DB is explicitly optional
  for v1.

## Consequences

**Positive**

- Zero-ops, single-file, transactional; perfect fit for local-first.
- WAL gives good read concurrency for the TUI/dashboard.
- FTS covers the repo-index use case without a separate search server.

**Negative / trade-offs**

- Write concurrency is serialised to one writer; mitigated by short, scoped
  transactions.
- CGO dependency risk for the most portable driver — to be resolved in M0 with a
  documented choice (pure-Go driver preferred if it meets the WAL/FTS
  requirements, else a CGO driver with a build-tag fallback).

## Alternatives considered

- **Embedded KV (e.g., bbolt).** Rejected: lacks relational queries and FTS
  required by §31/§22.2 without building that layer ourselves.
- **A separate server (Postgres).** Rejected: violates local-first/no-ops
  (§36.3).
- **Files only.** Rejected: cannot provide transactional, crash-safe "state
  before external action" semantics (§11.4).
