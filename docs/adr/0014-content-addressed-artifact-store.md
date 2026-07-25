# ADR-0014: Content-addressed artifact store

- **Status:** Accepted
- **Date:** 2026-07-25
- **Spec refs:** §9.5 (attachment storage), §31 (artifacts on filesystem), §14
  (image generation), §16.4 (visual artifacts), §22.3 (context pack references)

## Context

Attachments (§9.5), generated images (§14), captured screenshots (§16.4), diff
images and other large binaries flow through several subsystems. The spec
mandates content-addressed storage on the filesystem, never as SQLite BLOBs
(§9.5: "~/.neuroforge/artifacts/<hash>"; §31: "Большие artifacts хранятся в
файловой системе, а не BLOB"). Each artifact carries metadata: SHA-256,
original name, MIME type, size, source, project, task, confidentiality label
(§9.5).

Without a single store, each subsystem (image provider, visual harness, task
attachments) would reimplement deduplication and path layout, and the same
bytes written twice could end up at different paths.

## Decision

Add `internal/artifacts` as the single content-addressed artifact store. It:

- keys files by SHA-256 (`<root>/<hash[:2]>/<hash[2:]>`, two-level sharding so
  no single directory grows unbounded);
- writes atomically (temp file + rename) so partial writes are never
  observable;
- is idempotent (the same content written twice resolves to the same hash and
  path);
- stores files read-only (0o400) on POSIX;
- exposes `Write`, `Read`, `Exists`, `Size`, `Hash`.

The store holds bytes only; metadata (original name, MIME, project, task,
confidentiality label) is recorded by the caller (storage layer / image
provider / visual engine) keyed by the returned hash. This keeps the store
concern-free and avoids duplicating SQLite's role.

## Consequences

**Positive**

- Deduplication is automatic: the same reference image attached to two tasks
  is stored once.
- Byte-stable hashes let deterministic checks (§16.3) and fixture tests compare
  artifacts by hash.
- Large binaries stay out of SQLite (§31), keeping the WAL writer fast.

**Negative / trade-offs**

- A new package; mitigated by minimal API surface and stdlib-only deps.

## Alternatives considered

- **Store artifacts as BLOBs in SQLite.** Rejected: §31 explicitly forbids it.
- **One store per subsystem.** Rejected: breaks deduplication and consistency.
