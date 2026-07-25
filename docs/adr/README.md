# Architecture Decision Records (ADR)

An ADR captures a single, significant architectural decision: the context, the
choice, the alternatives, and the consequences. ADRs are append-only — supersede
an ADR with a new one rather than rewriting it.

- The spec (`../spec/NEUROFORGE_SPEC.md`) is the source of truth. An ADR records
  *how* a spec requirement is realised or where a deviation is introduced
  (spec §36.21).
- Format: `NNNN-kebab-title.md`. `Status:` is one of `Proposed`, `Accepted`,
- `Deprecated`, `Superseded by NNNN`.

## Index

| # | Title | Status | Spec ref |
|---|---|---|---|
| [0001](0001-go-modular-monolith.md) | Go modular monolith | Accepted | §36.1–§36.2, §10, §11.1 |
| [0002](0002-local-daemon.md) | Local daemon with durable recovery | Accepted | §11.2, §11.4 |
| [0003](0003-sqlite-durable-state.md) | SQLite (WAL) for durable state | Accepted | §11.4, §31 |
| [0004](0004-tui-daemon-transport.md) | Loopback HTTP JSON + SSE transport | Accepted | §11.3 |
| [0005](0005-coding-agent-adapter-protocol.md) | Coding-agent adapter protocol | Accepted | §12–§13 |
| [0006](0006-image-provider-adapter-protocol.md) | Image-provider adapter protocol | Accepted | §14 |
| [0007](0007-git-worktree-isolation.md) | Git worktree isolation for agent runs | Accepted | §17 |
| [0008](0008-local-review-security-model.md) | LOCAL_REVIEW security model | Accepted | §3.2, §29 |
| [0009](0009-deterministic-merge-governor.md) | Deterministic Merge Governor | Accepted | §28 |
| [0010](0010-sqlite-driver-modernc.md) | Pure-Go SQLite driver (modernc.org/sqlite) | Accepted | §10, §31 |
| [0011](0011-terminal-raw-mode-golang-x-term.md) | Terminal raw-mode via golang.org/x/term | Accepted | §6, M1 |
| [0012](0012-versioned-coding-agent-protocol.md) | Versioned coding-agent protocol package (v1) | Accepted | §12–§13, §32, §33.1 |
| [0013](0013-visual-harness-protocol.md) | Visual verification harness protocol | Accepted | §16, §15.2, §33.3 |
| [0014](0014-content-addressed-artifact-store.md) | Content-addressed artifact store | Accepted | §9.5, §31, §14, §16.4 |
| [0015](0015-change-request-provider-protocol.md) | Change-request provider protocol & merge authority | Accepted | §17.6, §28 |
| [0016](0016-token-efficient-context-pipeline.md) | Token-efficient context pipeline (repo index + Context Pack) | Accepted | §22 |
| [0017](0017-postmerge-sentinel-and-auto-revert.md) | Post-merge sentinel & auto-revert behind the merge authority | Accepted | §4.4, §37, §28 |
| [0018](0018-bootstrap-confirmation-gate.md) | Bootstrap safety — explicit confirmation over the installer abstraction | Accepted | §7, §36.17–§36.19 |
| [0019](0019-minimal-run-stabilization.md) | Minimal reliable run (`forge run`) stabilization | Proposed | §3.2, §11.4, §17, §29, §36.13–§36.14, AC-7, AC-8, AC-27, AC-28 |
