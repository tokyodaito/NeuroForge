# ADR-0010: Pure-Go SQLite driver (modernc.org/sqlite)

- **Status:** Accepted
- **Date:** 2026-07-24
- **Spec refs:** §10 (primary store), §11.4 (durable workflow), §31 (storage)
- **Supplements:** [ADR-0003](0003-sqlite-durable-state.md) (records the driver
  choice anticipated by its "Not yet implemented" note)

## Context

ADR-0003 fixes SQLite (WAL) as the durable store but left the concrete driver —
and the CGO-vs-pure-Go trade-off — as an open decision to be made when M0
storage landed. `AGENTS.md` requires that any new dependency be justified.

NeuroForge targets Windows, macOS and Linux with a single `go build`, ideally
with no CGO so cross-compilation and CI are trivial.

## Decision

Use **`modernc.org/sqlite`** (the pure-Go, CGO-free SQLite implementation) as
the `database/sql` driver for all durable state.

- Driver name registered with `database/sql`: `sqlite`.
- Connection pragmas (WAL, `busy_timeout`, `synchronous=NORMAL`, `foreign_keys`)
  are applied per pooled connection via the DSN `_pragma` query parameters.
- All access is mediated by `internal/storage`; no other package imports the
  driver directly.

## Justification (why a dependency, why this one)

- SQLite is **mandated** by the spec (§10/§31) and is not implementable from the
  standard library.
- A pure-Go driver avoids CGO entirely, keeping `make build`/`make check`
  cross-platform and CGO-toolchain-free (critical for Windows targets and CI).
- `modernc.org/sqlite` supports WAL and FTS5, satisfying ADR-0003's "pure-Go
  driver preferred if it meets the WAL/FTS requirements".
- It is ISC-licensed (permissive), actively maintained, and is the de-facto
  CGO-free SQLite for Go.

## Consequences

**Positive**

- One `go build` produces the binary on every target OS with no C toolchain.
- WAL + FTS requirements from ADR-0003 are met without CGO.

**Negative / trade-offs**

- The driver pulls in a transitive set (`modernc.org/libc`, `mathutil`,
  `memory`, `golang.org/x/sys`, etc.), increasing module size and binary size.
- Pure-Go SQLite is somewhat slower than a CGO build; acceptable for a
  local-first single-writer workload with modest volume.
- The driver is a single-vendor implementation; mitigated by it being widely
  adopted and by isolating all SQL behind `internal/storage`.

## Alternatives considered

- **`github.com/mattn/go-sqlite3` (CGO).** Faster and very mature, but CGO
  complicates Windows builds and cross-compilation, violating the
  build-simplicity goal for no functional gain at M0's scale.
- **bbolt / an embedded KV.** Rejected in ADR-0003: lacks relational queries and
  FTS required by §31/§22.2.
