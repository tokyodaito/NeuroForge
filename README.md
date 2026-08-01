# NeuroForge

**Autonomous, local-first, multi-model development factory.**

NeuroForge turns free-form task descriptions into reviewed, tested code and
delivers it to a local branch, a remote branch, a Pull/Merge Request, or a merged
target branch — while the user manages a backlog, policies, budgets and autonomy
levels instead of individual agents.

> **Status:** self-hosting alpha. The domain logic for all fourteen milestones
> (M0–M13) is implemented and covered by automated tests — unit tests, in-process
> integration tests, and black-box tests that drive the compiled `forge` binary
> through the daemon loopback transport. `forge run "<description>"` is the
> production self-hosting vertical slice: it creates a task, opens an isolated
> worktree, runs one real coding-agent adapter (opencode by default), and
> finalizes a local result ref (reliability is being actively stabilized — see
> [`docs/stabilization/`](docs/stabilization/)). What stays **opt-in** (rule §33,
> no paid calls in CI): live paid models, real image generation, and real device
> harnesses — CI exercises only the fake coding agent, fake image provider and
> fake visual harness. Per-requirement status lives in
> [`docs/spec/COMPLIANCE_MATRIX.md`](docs/spec/COMPLIANCE_MATRIX.md); there are no
> disguised stubs that pretend to work (rule §36.25).

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
./forge daemon start    # start the background daemon (loopback HTTP + SSE)
./forge daemon status   # lifecycle status (--json)
./forge run "<desc>"    # one-shot run: task -> worktree -> adapter -> result ref
make check              # fmt-check + go vet + tests (the CI gate)
make test               # unit tests
```

> `make lint` runs `golangci-lint` if installed, otherwise falls back to `go vet`.

The daemon owns all mutable state (SQLite/WAL via the pure-Go
`modernc.org/sqlite` driver, ADR-0010); the CLI and TUI reach it only over a
loopback, token-protected HTTP+SSE transport (ADR-0004). Real paid models,
image generation and device harnesses are **opt-in** and never invoked in CI —
orchestration and conformance tests use the fake coding agent (§33.1), fake
image provider (§33.2) and fake visual harness (§33.3) instead (rule §36.5).

### Linux, macOS and Windows hosts

NeuroForge is Linux-only. On a Windows host, run it inside **WSL2** (install
and build Linux-side — never from a `/mnt/c` checkout). See the full
[WSL2 setup guide](docs/platforms/WSL2.md). macOS may work via the generic Unix
code paths but receives no dedicated support.

### Security model

Understand what NeuroForge does and does not isolate before trusting it with a
repository:

- **The worktree is an organizational boundary, not a security boundary.** The
  coding agent runs **unsandboxed, as your user**, with access to your `HOME`
  (required for the agent CLI's own auth, e.g. OpenCode's
  `~/.local/share/opencode/auth.json`). Code the agent writes or commands it
  runs can read anything your user can read. Run NeuroForge only on code and
  task descriptions you would run yourself.
- **Review is a quality gate, not a security or adversarial gate.** The review
  stage reduces the chance of bad changes landing; it does not make running an
  untrusted agent safe.
- **Do not paste secrets into task descriptions.** The prompt is visible in
  local process arguments while the agent runs, and is persisted in local
  run history.
- **Multi-tenant use is unsupported.** One NeuroForge home, one user; the
  loopback daemon token protects the API from other local processes, not one
  user's runs from another user's agent.

---

## Repository layout

```
cmd/forge/              binary entrypoint (+ cmd/fake-coding-agent fixture)
internal/
  cli/                  CLI command dispatch (version, help, daemon, project,
                        task, workspace, run, plugin, route/quota/usage/cost,
                        image-provider, init, update, doctor, memory, quality)
  version/              build/runtime version info
  daemon/               long-running daemon: lifecycle, lockfile, startup
                        reconciliation framework + attempt reconciler (AC-27)
  storage/              SQLite/WAL durable store + forward-only migrations
  transport/            loopback TUI<->daemon API (HTTP JSON + SSE + token)
  tui/                  interactive terminal UI (MVU, projects/tasks/usage/
                        quotas/route screens, command palette, live SSE)
  project/              project registry + state machine (§8)
  task/                 task model, backlog, attachments, compiler (§9)
  workgraph/            work DAG + path/semantic leases (§18.4)
  workspace/            git worktrees, branches, checkpoints, review (§17)
  supervisor/           agent process supervision, env allowlist (AC-28),
                        continuation packs, failover controller, recovery (§21)
  scheduler/            production dispatch root: task -> dispatcher ->
                        supervisor, usage/memory/quality wiring (§22)
  runapp/               one-shot `forge run` service (task+worktree+adapter+
                        finalize, outcome contract, terminal arbitration)
  router/               complexity C0..C4, model catalog, deterministic route
                        selection + fallback chain (§18.2/§19; no hard-coded
                        model names, §36.8)
  quota/                quota confidence + circuit breaker (§20)
  budget/               budget controller: included vs paid, soft/hard (§23)
  risk/                 risk classification R0..R4 (§26)
  policy/               pipeline toggles, dependency rules, AC-29 invariant,
                        stage status + labels (§4/§5/§5.1/§24/§25/§29)
  testengine/           progressive verification (syntax->compile->...->full, §24)
  review/               correctness/architecture/security review roles (§25)
  evidence/             acceptance-criterion -> typed evidence linking (§27)
  repair/               bounded repair loop, targeted context (§22.5/§25/§16.5)
  merge/                deterministic Merge Governor + Authority + queue (§28)
  postmerge/            post-merge sentinel + auto-revert + reopen (§4.4/§37)
  repoinfo/             repo index, context packs, log slicing, prompt-cache
                        fingerprint (§22.1-§22.5/§22.8; rule §36.11)
  memory/               typed project memory (§22.9)
  quality/              token accounting + per-model success rates (§6.1/§19.1)
  artifacts/            content-addressed (SHA-256) artifact store (§9.5, §31)
  design/               design orchestrator: brief -> variants -> spec (§15)
  visual/               visual verification engine + reference-free review (§16)
  audit/                tamper-evident append-only audit trail (§29, AC-30)
  bootstrap/            `forge init` / `forge update` wizard + toolchain lock (§7)
  adapter/
    codingagent/        protocol v1 + Registry + fake + declarative + native
                        JSON-RPC plugin + conformance (§12/§13/§33.1)
      codex/claude/gemini/kimi/grok/opencode/   six first-party engines (AC-5)
      builtin/          central registry wiring all six (§13.3)
    imageprovider/      protocol + Registry + fake + conformance (§14/§33.2)
      gptimage/         OpenAI Images adapter (opt-in)
      nanobanana/       Gemini generateContent adapter (opt-in)
    visualharness/      protocol + generic + Android + fake harnesses (§16/§33.3)
    vcs/                ChangeRequestProvider: localgit + GitHub + GitLab (§17.6)
docs/
  spec/                 NEUROFORGE_SPEC.md (source of truth) + COMPLIANCE_MATRIX.md
  architecture/         COMPONENTS, DATA_FLOW, STATE_MACHINES, ADAPTER_DEV_GUIDE
  adr/                  architecture decision records (ADR-0001..0020)
  milestones/           IMPLEMENTATION_PLAN + milestone closure reports
  adapters/             per-engine adapter docs
  stabilization/        minimal reliable run (`forge run`) spec + test plan
  platforms/            WSL2 setup guide (Windows hosts)
Makefile                build / test / lint / check
```

Packages that are not yet acceptance-complete carry an explicit
`STATUS: not implemented` marker in their `doc.go`, and every unmet requirement
is tracked in the compliance matrix — there are no fake stubs that look finished
(rule §36.25).

---

## Milestones (summary)

| Milestone | Theme                              | Status       |
|-----------|------------------------------------|--------------|
| M0        | Foundation (module, daemon, SQLite skeleton, CLI, TUI shell) | done |
| M1        | Projects and local tasks           | done |
| M2        | Agent protocol + fake agent        | done |
| M3        | Workspaces (worktree, supervision) | done |
| M4–M5     | Coding engines (Codex/Claude/Gemini; Kimi/Grok/OpenCode) | done (integrated; offline conformance; live calls opt-in) |
| M6        | Routing, quota, budget, dashboard  | done |
| M7        | Failover + continuation packs      | done |
| M8        | Configurable tests & review        | done |
| M9        | Image providers                    | done (GPT Image + Nano Banana; real calls opt-in) |
| M10       | Design + visual verification       | done (real device harness opt-in) |
| M11       | Remote delivery + Merge Governor   | done (real GitHub/GitLab network opt-in) |
| M12       | Post-merge + context optimization  | done |
| M13       | Bootstrap (`forge init`)           | done |

"done" means the milestone's domain logic is implemented and covered by automated
tests (unit / in-process integration / black-box against the compiled binary).
It is **not** a production-maturity claim: the project is a self-hosting alpha.
Capabilities that touch live paid models, real image generation, real devices or
real network VCS providers remain **opt-in** (rule §33) and are never exercised
in CI; live quota/usage streaming into the router at runtime and the remaining
planned CLI surface (`forge agent …`, `forge model …`, `forge audit`,
`forge emergency-stop`) are tracked in the compliance matrix.

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
