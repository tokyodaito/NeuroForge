# NeuroForge

**Autonomous, local-first, multi-model development factory.**

NeuroForge turns free-form task descriptions into reviewed, tested code and
delivers it to a local branch, a remote branch, a Pull/Merge Request, or a merged
target branch — while the user manages a backlog, policies, budgets and autonomy
levels instead of individual agents.

> **Status:** architectural scaffold (milestone **M0 — in progress**).
> Only `forge version` and `forge help` are implemented. Every other capability is
> explicitly tracked as not-yet-implemented — see
> [`docs/spec/COMPLIANCE_MATRIX.md`](docs/spec/COMPLIANCE_MATRIX.md). There are no
> disguised stubs that pretend to work.

The authoritative requirements live in
[`docs/spec/NEUROFORGE_SPEC.md`](docs/spec/NEUROFORGE_SPEC.md). This README is a
navigational summary; when they disagree, **the spec wins**.

---

## Why

- **Local-first.** State, secrets and execution stay on the user's machine.
  Network actions (push / PR / MR / merge) are opt-in and gated by policy.
- **Model-agnostic.** Any coding agent that speaks the adapter protocol can be
  added without touching the scheduler, schema, dashboard or routing core.
- **Safe by default.** A `LOCAL_REVIEW` project performs **zero** Git network
  operations; results land in an isolated worktree + local result branch only.
- **Observable & auditable.** Every task has a full, reconstructable history.

See [`docs/architecture/`](docs/architecture/) for the component model and data
flow, and [`docs/adr/`](docs/adr/) for the architectural decisions.

---

## Quick start

Requirements:

- **Go ≥ 1.23** (developed against Go 1.26). If Go is missing, install it
  explicitly (`brew install go` or https://go.dev/dl/) — NeuroForge never performs
  silent toolchain installs (rule §36.17).
- Git.

```sh
make build              # produces ./forge with version metadata
./forge version         # print version / commit / platform
./forge help            # implemented + planned commands
make check              # fmt-check + go vet + tests (the CI gate)
make test               # unit tests
```

> `make lint` runs `golangci-lint` if installed, otherwise falls back to `go vet`.

There is no daemon, no SQLite storage and no real adapters yet — those arrive in
their milestones (see the implementation plan).

---

## Repository layout

```
cmd/forge/              binary entrypoint
internal/
  cli/                  CLI command dispatch (IMPLEMENTED: version, help)
  version/              build/runtime version info (IMPLEMENTED)
  daemon/               long-running daemon process                (M0, scaffold)
  storage/              SQLite durable store + migrations          (M0, scaffold)
  transport/            loopback TUI<->daemon API (HTTP+SSE)       (M0, scaffold)
  tui/                  interactive terminal UI                     (M0, scaffold)
  project/              project registry + lifecycle               (M1, scaffold)
  task/                 task model, backlog, compiler              (M1, scaffold)
  workgraph/            work DAG + semantic leases                 (M1/M2, scaffold)
  scheduler/            work dispatch                              (M2/M3, scaffold)
  router/               model/engine/account router                (M6, scaffold)
  quota/                quota + circuit breaker                    (M6, scaffold)
  budget/               budget controller                          (M6, scaffold)
  workspace/            git worktree + branches                    (M3, scaffold)
  supervisor/           agent process supervision                  (M3, scaffold)
  merge/                deterministic Merge Governor               (M11, scaffold)
  audit/                tamper-evident audit trail                 (M0, scaffold)
  policy/               security policy + pipeline toggles         (M0/M8, scaffold)
  risk/                 risk classification R0..R4                 (M6, scaffold)
  repoinfo/             repo index / token optimization            (M12, scaffold)
  adapter/
    codingagent/        coding-agent adapter protocol              (M2, scaffold)
    imageprovider/      image-provider adapter protocol            (M9, scaffold)
    visualharness/      visual verification harness protocol       (M10, scaffold)
    vcs/                change-request providers (GitHub/GitLab)   (M11, scaffold)
docs/
  spec/                 NEUROFORGE_SPEC.md (source of truth) + COMPLIANCE_MATRIX.md
  architecture/         COMPONENTS, DATA_FLOW, STATE_MACHINES
  adr/                  architecture decision records
  milestones/           IMPLEMENTATION_PLAN
Makefile                build / test / lint / check
```

Each scaffold package currently contains only a `doc.go` that states its purpose,
the spec section it implements, the owning milestone, and a clear
**"STATUS: not implemented"** marker. No fake logic.

---

## Milestones (summary)

| Milestone | Theme                              | Status       |
|-----------|------------------------------------|--------------|
| M0        | Foundation (module, daemon, SQLite skeleton, CLI, TUI shell) | in progress |
| M1        | Projects and local tasks           | planned      |
| M2        | Agent protocol + fake agent        | planned      |
| M3        | Workspaces (worktree, supervision) | planned      |
| M4–M5     | Coding engines (Codex/Claude/Gemini; Kimi/Grok/OpenCode) | planned |
| M6        | Routing, quota, budget, dashboard  | planned      |
| M7        | Failover + continuation packs      | planned      |
| M8        | Configurable tests & review        | planned      |
| M9        | Image providers                    | planned      |
| M10       | Design + visual verification       | planned      |
| M11       | Remote delivery + Merge Governor   | planned      |
| M12       | Post-merge + context optimization  | planned      |
| M13       | Bootstrap (`forge init`)           | planned      |

Per-milestone breakdown into issues with scope, allowed/forbidden paths,
dependencies, acceptance criteria, checks and Definition of Done:
[`docs/milestones/IMPLEMENTATION_PLAN.md`](docs/milestones/IMPLEMENTATION_PLAN.md).

---

## Contributing

Read [`AGENTS.md`](AGENTS.md) (for humans **and** coding agents) and
[`CONTRIBUTING.md`](CONTRIBUTING.md) before making changes. Notably:

- Do not modify `docs/spec/NEUROFORGE_SPEC.md` — it is the immutable source of
  truth. Architectural deviations are recorded as ADRs.
- Run `make check` before considering work done; every acceptance criterion (AC-1
  … AC-30) must have an automated or integration test (rule §36.22).
