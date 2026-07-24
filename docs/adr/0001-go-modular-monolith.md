# ADR-0001: Go modular monolith

- **Status:** Accepted
- **Date:** 2026-07-24
- **Spec refs:** §10 (architecture), §11.1 (one binary), §36.1–§36.2 (no giant
  package, no microservices)

## Context

NeuroForge orchestrates many cooperating subsystems (task compiler, work graph,
scheduler, router, quota, budget, workspaces, supervisor, design/visual/test/
review engines, merge governor, multiple adapter families). We need a code
organisation that keeps concerns separable and testable without paying the
operational cost of multiple deployable services, and without one unmanageable
"god package".

The spec mandates (§36.1–§36.2): do not implement the product as a single giant
package; do not turn it into microservices. §11.1 requires a single `forge`
binary containing CLI, TUI, daemon, worker and plugin host.

## Decision

Implement NeuroForge as a **single Go binary structured as a modular monolith**:

- One module path (`neuroforge`), one binary `cmd/forge`.
- Cohesive packages under `internal/`, each with one responsibility and explicit
  boundaries (see `docs/architecture/COMPONENTS.md` and `AGENTS.md`).
- Core packages never import adapter packages directly for execution decisions;
  adapters are plugged in through interfaces and consumed by the supervisor only.
- All inter-package interaction happens in-process (function calls, interfaces,
  channels). There is exactly one process boundary that matters: the daemon ↔
  agent adapter processes (ADR-0005) and daemon ↔ TUI (ADR-0004).

## Consequences

**Positive**

- Simple build, deploy, test, and versioning: `make build` → `forge`.
- In-process calls are fast and trivially testable; no serialisation tax on
  internal flows.
- Clear package boundaries still enforce modularity and enable parallel work.
- Single binary matches the local-first, no-ops product story (no Kubernetes,
  rule §36.3).

**Negative / trade-offs**

- A dependency cycle or leaky boundary can quietly erode modularity — mitigated
  by the import rules in `COMPONENTS.md` and `go vet`/lint.
- All subsystems share one process lifecycle; a crash in one area can affect the
  daemon. Mitigated by supervised agent subprocesses (ADR-0005) and durable state
  (ADR-0002/0003).

## Alternatives considered

- **Microservices per subsystem.** Rejected: violates spec §36.2 and the
  local-first/no-ops goal; adds latency, config and ops burden.
- **One flat `main` package.** Rejected: violates spec §36.1; untestable and
  unmaintainable.
- **Plugin-only architecture (everything is a plugin).** Rejected as the primary
  shape: adapters are pluggable (ADR-0005/0006), but the core orchestration must
  be a coherent, versioned whole to guarantee durable workflow and policy
  invariants.
