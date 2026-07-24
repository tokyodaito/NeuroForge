# Component model

This document maps the NeuroForge architecture (spec §10) to concrete Go packages
and records the responsibility, dependencies and owning milestone of each. It is a
navigational companion to the spec; the spec remains authoritative.

- High-level topology: spec §10 and [`DATA_FLOW.md`](DATA_FLOW.md).
- Stateful behaviour: [`STATE_MACHINES.md`](STATE_MACHINES.md).
- Decisions: [`../adr/`](../adr/).
- Implementation order: [`../milestones/IMPLEMENTATION_PLAN.md`](../milestones/IMPLEMENTATION_PLAN.md).

## Principles

1. **Modular monolith.** One `forge` binary; many cohesive packages; no
   microservices (rule §36.1–§36.2; ADR-0001).
2. **Strict package boundaries.** Core never imports adapters; adapters never
   decide policy or routes (AGENTS.md).
3. **Explicit "not implemented".** Every scaffold package currently ships only a
   `doc.go` that states its purpose, spec reference and milestone. There is no
   fake logic (rule §36.25).
4. **Deterministic policy.** Git, quota arithmetic, budget arithmetic and the
   Merge Governor are never delegated to an LLM (rule §36.6, ADR-0009).

## Topology (spec §10)

```
┌─────────────────────────────────────────────┐
│                 Forge TUI                   │   internal/tui
│ Projects · Tasks · Runs · Diff · Settings   │
└──────────────────────┬──────────────────────┘
                       │ loopback HTTP+JSON / SSE   internal/transport
┌──────────────────────▼──────────────────────┐
│                Forge Daemon                 │   internal/daemon
│                                             │
│ Project Registry   (internal/project)       │
│ Task Compiler      (internal/task)          │
│ Repo Intelligence  (internal/repoinfo)      │
│ Work Graph Engine  (internal/workgraph)     │
│ Scheduler          (internal/scheduler)     │
│ Model Router       (internal/router)        │
│ Quota Manager      (internal/quota)         │
│ Budget Controller  (internal/budget)        │
│ Workspace Manager  (internal/workspace)     │
│ Agent Supervisor   (internal/supervisor)    │
│ Design Engine      (internal/design)        │
│ Visual Verification(internal/visual)        │
│ Test Engine        (internal/testengine)    │
│ Review Engine      (internal/review)        │
│ Merge Governor     (internal/merge)         │
│ Post-Merge Sentinel(internal/postmerge)     │
│                                             │
│ Security policy    (internal/policy)        │
│ Risk               (internal/risk)          │
│ Audit              (internal/audit)         │
│ Durable state      (internal/storage)       │
└───────────┬─────────────────────┬───────────┘
            │                     │
   Coding Agent Adapters   Image Provider Adapters
   internal/adapter/       internal/adapter/
     codingagent/            imageprovider/
     (Codex·Claude·Grok·     (GPT Image · Nano Banana)
      Kimi·OpenCode·Gemini)  visualharness/   (visual verification)
                            vcs/             (GitHub/GitLab/local)
```

## Component reference

Columns: **Package** · **Responsibility (spec ref)** · **Owns milestone** ·
**May import** · **Must not import**.

### User-facing surfaces

| Package | Responsibility | Milestone | May import | Must not import |
|---|---|---|---|---|
| `cmd/forge` | binary entrypoint (spec §11.1) | M0 (done) | `internal/cli` | anything else |
| `internal/cli` | non-interactive CLI (spec §30) | M0 (done) | `internal/version`, daemon client (later) | adapters, storage |
| `internal/tui` | interactive TUI (spec §6) | M0 shell → full later | `internal/transport` | adapters, storage |
| `internal/version` | build/runtime version metadata | M0 (done) | stdlib only | — |
| `internal/transport` | loopback TUI↔daemon API (spec §11.3) | M0 | stdlib, daemon transport types | adapters |

### Daemon core (spec §10 block)

| Package | Responsibility | Milestone | May import | Must not import |
|---|---|---|---|---|
| `internal/daemon` | process lifecycle, recovery (§11.2, §11.4) | M0 | storage, scheduler, supervisor, transport, audit | adapters directly (via supervisor only) |
| `internal/project` | project registry + state machine (§8) | M1 | storage, policy, audit, repoinfo | adapters |
| `internal/task` | task model + compiler (§9, §18.1) | M1 | storage, workgraph, policy, audit | adapters |
| `internal/workgraph` | work DAG + semantic leases (§18.3–§18.4) | M1/M2 | storage, workspace (lease records) | adapters |
| `internal/scheduler` | dispatch work packages (§10) | M2/M3 | router, quota, budget, workspace, supervisor | adapters |
| `internal/router` | engine+model+account+runtime (§19) | M6 | quota, budget, model catalog, risk | adapters, storage write |
| `internal/quota` | quota + circuit breaker (§20) | M6 | storage (snapshots) | adapters |
| `internal/budget` | budget controller (§23) | M6 | storage, usage | adapters, LLM |
| `internal/workspace` | git worktree + branches (§17) | M3 | git (deterministic ops) | vcs adapters, network push |
| `internal/supervisor` | run + supervise agents (§12) | M3 | `adapter/codingagent`, workspace, audit | router, scheduler |
| `internal/policy` | security policy + pipeline toggles (§5, §29) | M0/M8 | storage | adapters |
| `internal/risk` | risk classification R0..R4 (§26) | M6 | — | adapters |
| `internal/repoinfo` | repo index / context (§22) | M12 | git, storage (FTS) | adapters |
| `internal/audit` | tamper-evident audit (§29.4) | M0 | storage | adapters |
| `internal/storage` | SQLite WAL + migrations (§11.4, §31) | M0 | SQLite driver (ADR-0003) | adapters, daemon logic |

### Daemon sub-engines (later milestones)

| Package | Responsibility | Milestone |
|---|---|---|
| `internal/design` | design-to-code pipeline, visual spec (§15) | M10 |
| `internal/visual` | visual verification engine (§16) | M10 |
| `internal/testengine` | progressive test execution (§24) | M8 |
| `internal/review` | AI review engine (§25) | M8 |
| `internal/merge` | deterministic Merge Governor (§28) | M11 |
| `internal/postmerge` | post-merge sentinel + auto-revert | M12 |

> These six packages are documented here but are **not** created as directories
> yet; they will be added in their owning milestone to avoid empty scaffolding.

### Adapters (plug-in surface)

| Package | Responsibility | Milestone |
|---|---|---|
| `internal/adapter/codingagent` | `CodingAgentAdapter` protocol + registry (§12–§13) | M2 (protocol) / M4–M5 (adapters) |
| `internal/adapter/imageprovider` | `ImageProviderAdapter` protocol + registry (§14) | M9 |
| `internal/adapter/visualharness` | `VisualHarness` protocol + generic/Android harnesses (§16) | M10 |
| `internal/adapter/vcs` | `ChangeRequestProvider` (local/GitHub/GitLab) (§17.6) | M11 |

Adapter rules: adding a coding agent must not change scheduler, schema, dashboard
or routing core (rule §13.3). Adapters never receive merge credentials (§28).

## Status legend

Each package directory contains a `doc.go` whose header records its status.
`internal/cli`, `internal/version`, `internal/storage`, `internal/audit`,
`internal/transport`, `internal/daemon`, `internal/policy` and `internal/tui`
carry implemented foundations (M0) or full implementations; every
not-yet-implemented package keeps a `STATUS: scaffold — not implemented (planned
for M<n>)` marker. The cumulative status of spec requirements is in
[`../spec/COMPLIANCE_MATRIX.md`](../spec/COMPLIANCE_MATRIX.md).
