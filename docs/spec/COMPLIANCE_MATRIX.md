# Compliance matrix

Maps the spec's acceptance criteria (§35) and a selection of hard rules (§36) to
their implementation status, owning milestone, issue (see
[`../milestones/IMPLEMENTATION_PLAN.md`](../milestones/IMPLEMENTATION_PLAN.md)),
and the package(s) responsible.

Statuses:

- `done` — implemented and covered by an automated test.
- `partial` — some pieces exist; not yet acceptance-complete.
- `planned` — not started; milestone/issue assigned.
- `n/a` — not applicable at this stage.

The spec (`NEUROFORGE_SPEC.md`) is authoritative; this matrix is a tracking view.

## Acceptance criteria (spec §35)

| AC | Requirement (abridged) | Status | Milestone | Issue(s) | Package(s) |
|----|------------------------|--------|-----------|----------|------------|
| AC-1 | `forge` (no args) opens interactive TUI | done (M1: full TUI with projects/tasks screens, keyboard nav, command palette, status bar, mouse support, live event refresh via daemon SSE) | M0/M1 | M0-8, M1-6 | `internal/tui`, `internal/cli` |
| AC-2 | Manage projects/tasks without CLI | done (M1: TUI screens for projects and tasks; add project, navigate, start/pause/stop, pause/cancel tasks) | M1 | M1-6 | `internal/tui`, `internal/project` |
| AC-3 | Create a task with free-form text (no template) | done (M1: `forge task add` + TUI; description can be the only user field) | M1 | M1-5 | `internal/task` |
| AC-4 | Attach an image to a task | done (M1: `-a`/`--attach` flag; content-addressed SHA-256 storage under `~/.neuroforge/artifacts/`) | M1 | M1-5 | `internal/task` |
| AC-5 | Codex / Claude Code / Grok Build / Kimi Code / OpenCode / Gemini CLI | planned (M2: stable protocol + conformance suite ready; concrete engines M4–M5) | M4–M5 | M4-n, M5-n | `internal/adapter/codingagent` |
| AC-6 | A 7th agent via plugin, no core changes | done (M2: versioned `protocol` package v1 + declarative + native JSON-RPC plugin + `forge plugin test` conformance suite; the fake agent passes all 9 checks via plugin with no core changes) | M2 | M2-7 | `internal/adapter/codingagent/{protocol,declarative,plugin,conformance}` |
| AC-7 | LOCAL_REVIEW performs no Git network ops | done (M3: structurally enforced by the workspace manager's git allowlist that excludes push/fetch/pull/clone/ls-remote; integration test verifies no remote refs are ever created) | M0/M3 | M0-7, M3-5 | `internal/policy`, `internal/workspace` |
| AC-8 | Code saved in a separate local result branch | done (M3: `forge/result/<task-id>` local branch created by workspace manager; checkpoint commits never auto-merge to main) | M3 | M3-5 | `internal/workspace` |
| AC-9 | Open diff and worktree from TUI | done (M3: `/workspaces/{id}/diff` and workspace path exposed via API; TUI reachable through daemon transport) | M3 | M3-5 | `internal/workspace`, `internal/transport` |
| AC-10 | Accept / reject / ask-for-changes | done (M3: keep/reject/ask review lifecycle via `POST /workspaces/{id}/review`; reject deletes only managed worktree, never user data) | M3/M11 | M3-5, M11-2 | `internal/workspace` |
| AC-11 | Disable test generation | planned | M8 | M8-3 | `internal/policy`, `internal/testengine` |
| AC-12 | Disable running existing tests separately | planned | M8 | M8-3 | `internal/policy`, `internal/testengine` |
| AC-13 | Disable AI-review | planned | M8 | M8-4 | `internal/policy`, `internal/review` |
| AC-14 | Push / PR-MR / merge switchable separately | planned | M8/M11 | M8-1, M11-6 | `internal/policy`, `internal/adapter/vcs` |
| AC-15 | Quota failure after edits → continuation via fallback, checkpoint kept | planned | M7 | M7-1, M7-5 | `internal/supervisor`, `internal/adapter/codingagent` |
| AC-16 | Simple task → cheap route | planned | M6 | M6-4 | `internal/router` |
| AC-17 | Complex task → strong model | planned | M6 | M6-4 | `internal/router` |
| AC-18 | Dashboard shows exact vs ~estimated vs unknown usage distinctly | planned | M6 | M6-9 | `internal/tui`, `internal/quota` |
| AC-19 | GPT Image and Nano Banana adapters | planned | M9 | M9-3, M9-4 | `internal/adapter/imageprovider` |
| AC-20 | Generate a visual specification from text | planned | M9/M10 | M9-6 | `internal/design`, `internal/adapter/imageprovider` |
| AC-21 | Create UI implementation task from an attached image | planned | M10 | M10-8 | `internal/task`, `internal/visual` |
| AC-22 | Visual Verification captures a real screenshot | planned | M10 | M10-3 | `internal/adapter/visualharness`, `internal/visual` |
| AC-23 | Visual discrepancy triggers repair loop | planned | M10 | M10-5 | `internal/visual` |
| AC-24 | Disabled visual verification never claims UI is verified | planned | M10 | M10-7 | `internal/visual`, `internal/policy` |
| AC-25 | `forge init --dry-run` shows a plan, changes nothing | planned | M13 | M13-3 | `internal/cli`, bootstrap |
| AC-26 | `forge init` installs tools, offers official auth, runs doctor | planned | M13 | M13-1..M13-6 | bootstrap |
| AC-27 | Daemon resumes unfinished tasks after restart | partial (M0: startup reconciliation framework + M0 entities done; M3: workspace reconciler verifies worktree integrity at startup; full agent-attempt resume blocked on M7 continuation packs) | M0/M3/M7 | M0-4, M3-6, M7-3 | `internal/daemon`, `internal/storage`, `internal/workspace` |
| AC-28 | Agent has no merge credentials | done (M3: supervisor builds a positive-allowlist environment that strips GITHUB_TOKEN/GITLAB_TOKEN/AWS_SECRET/etc.; AssertEnvSafe verifies no leak; tested) | M3 | M3-4 | `internal/supervisor` |
| AC-29 | Non-disableable security policy cannot be weakened by task override | partial (M0: policy core enforces AC-29 invariant; full pipeline wiring in M8-1) | M0/M8 | M0-7, M8-1 | `internal/policy` |
| AC-30 | Full task history available in audit | done (M1: project/task lifecycle events — added/removed/state_changed — all recorded in append-only audit store; `forge daemon logs -f` for live events) | M0+ | M0-6, M1-1, M1-5 | `internal/audit` |

## CLI surface (spec §30)

| Command | Status | Issue |
|---------|--------|-------|
| `forge version` | done | M0-2 |
| `forge help` | done | M0-2 |
| `forge` (TUI) | done (M1 full TUI with projects/tasks screens) | M0-8, M1-6 |
| `forge dashboard` | done (alias for TUI) | M1 |
| `forge daemon run` | done | M0-4 |
| `forge daemon start` | done | M0-4 |
| `forge daemon stop` | done | M0-4 |
| `forge daemon status` | done | M0-4 |
| `forge daemon logs` | done | M0-4 |
| `forge doctor` | done (basic M0 checks; full onboarding doctor in M13) | M0 |
| `forge project add` | done (--name, --json) | M1-1 |
| `forge project list` | done (--json) | M1-1 |
| `forge project show` | done (--json) | M1-1 |
| `forge project start` | done | M1-4 |
| `forge project pause` | done | M1-4 |
| `forge project stop` | done | M1-4 |
| `forge project remove` | done (files NOT deleted) | M1-1 |
| `forge task add` | done (-p/--project, --title, --priority, -a/--attach, --json) | M1-5 |
| `forge task list` | done (-p/--project, --json) | M1-5 |
| `forge task show` | done (--json) | M1-5 |
| `forge task pause` | done | M1-5 |
| `forge task cancel` | done | M1-5 |
| `forge workspace create` | done (-t/--task, --wp, --base, --json) | M3 |
| `forge workspace list` | done (-t/--task, --project, --json) | M3 |
| `forge workspace show` | done (--json) | M3 |
| `forge workspace run` | done (--engine, --json) | M3 |
| `forge workspace checkpoint` | done (--moment, --message) | M3 |
| `forge workspace result` | done (--json) | M3 |
| `forge workspace review` | done (-a keep\|reject\|ask, --json) | M3 |
| `forge workspace diff` | done | M3 |
| `forge workspace patch` | done | M3 |
| `forge workspace delete` | done | M3 |
| `forge workspace checkpoints` | done (--json) | M3 |
| `forge agent ...` / `forge model ...` / `forge route ...` | planned | M2, M6 |
| `forge image-provider ...` | planned | M9 |
| `forge quota` / `usage` / `cost` | planned | M6 |
| `forge plugin ...` | done (M2: `forge plugin test <exe>` runs the §13.3 conformance suite; `forge plugin list` stub) | M2-7 |
| `forge audit` | partial (read-only `/audit` API available; `forge audit` CLI command in M1+) | M0/M1+ |
| `forge emergency-stop` / `forge cleanup` | planned | M1+ |
| `forge init` / `update` | planned | M13 |

## Hard rules (spec §36) — current enforcement

| # | Rule | Status / mechanism |
|---|------|--------------------|
| 1–2 | Modular monolith, not microservices / one giant package | done — ADR-0001 + package layout |
| 3 | No Kubernetes | done — not used |
| 4 | No web UI before TUI | done — TUI-first in plan |
| 5 | No real paid models in CI | done (M2) — fake coding agent (§33.1) drives all orchestration/conformance tests; no AI/network calls |
| 6 | Fake coding agent first | done (M2) — `internal/adapter/codingagent/fake` + `cmd/fake-coding-agent`, 13 scenarios |
| 7 | Stabilise adapter protocol, then adapters | done (M2) — protocol v1 stabilised; concrete engines in M4/M5 |
| 8 | No hard-coded model names in core | done (M2) — enforced by `TestCoreHasNoHardcodedModelNames`; models are provider-supplied |
| 9 | Separate coding agents from image providers | done (structure) — ADR-0005/0006 |
| 10 | Quota not reported as exact unless provider says so | planned — M6-6 |
| 11 | No full repo in prompt | planned — M12-3 |
| 12 | No LLM for Git/policy/quota/budget arithmetic | done (policy) — ADR-0009; code-only |
| 13 | No push in LOCAL_REVIEW | done (M3) — git runner allowlist structurally excludes push/fetch/pull/clone/ls-remote; integration-tested (AC-7) |
| 14 | Never modify primary checkout | done (M3: worktree isolation verified by integration test — HEAD SHA + working tree files unchanged after workspace create/run/checkpoint/result) | ADR-0007 |
| 15 | Agent cannot change project security policy | done (core) — `internal/policy` AC-29 enforcement; full wiring in M8-1 |
| 16 | Agent cannot disable checks that validate its output | done (core) — `internal/policy` mandatory checks; full wiring in M8-1 |
| 17–18 | No silent install / privilege escalation | planned — M13-3 |
| 19 | No provider CLI update during active run | planned — M13-5 |
| 20 | App builds + demonstrable scenario after each milestone | enforced — `make check` gate |
| 21 | Record deviations as ADR | done — `docs/adr/` |
| 22 | Every AC has an automated/integration test | enforced — per-issue Checks |
| 23 | Spec is source of truth | done — referenced everywhere |
| 24 | Agent may not self-reduce project scope | planned — M11-5 (scope_valid) |
| 25 | Unimplemented requirements explicitly marked | done — scaffold `doc.go` markers, this matrix, help text |

## Durable-workflow & security enforcement (M0)

| Requirement | Status / mechanism |
|-------------|--------------------|
| State not held only in RAM (§11.4) | done — SQLite/WAL in `internal/storage` |
| Daemon listens only on loopback (§11.3) | done — transport refuses non-loopback bind (tested) |
| Local API protected by a random local token | done — per-run crypto-random token, constant-time checked |
| Token / runtime files have safe fs permissions | done — dirs 0o700, pid/token/addr files 0o600 (tested) |
| Migrations are idempotent and forward-only | done — `schema_migrations` + transactional steps (tested) |
| Repeated `daemon start` never spawns a second daemon | done — single-instance guard (tested) |
| `stop` gracefully terminates the child process | done — `/shutdown` + SIGTERM fallback (tested) |
| No external network calls | done — loopback only, no outbound calls |
| Structured logging | done — `log/slog` JSON |
| Context-cancellation shutdown | done — signal/`/shutdown`/ctx → graceful drain (tested) |

## Milestone M0 — what is implemented

- **Storage** (`internal/storage`, ADR-0003/0010): SQLite opened in WAL mode via
  the pure-Go `modernc.org/sqlite` driver; forward-only, idempotent, versioned
  migration runner (`schema_migrations`); the `audit_events` append-only table
  with DB-level triggers rejecting UPDATE/DELETE. Large artifacts stay on disk.
- **Audit** (`internal/audit`, AC-30): append-only Recorder reconstructing
  per-scope history; no update/delete API exposed to callers.
- **Transport** (`internal/transport`, ADR-0004): in-memory broadcast event bus;
  loopback-only HTTP server (JSON command API `/healthz`, `/status`, `/audit`,
  `/shutdown` + SSE `/events`); random per-run bearer token, constant-time
  checked, never logged. Repeated start refuses a non-loopback bind.
- **Daemon** (`internal/daemon`, ADR-0002): global runtime dir layout
  (`NEUROFORGE_HOME` / `~/.neuroforge`); the Run loop wires storage→migrate→
  **reconcile**→audit→bus→transport and shuts down cleanly on ctx/SIGTERM/
  SIGINT/`/shutdown`; single-instance guard + stale/corrupted runtime reclaim;
  startup reconciliation framework (every decision audited, idempotent, with an
  extension point for M2/M3 attempt reconcilers); `start`/`stop`/`status`/
  `logs`/`run`.
- **Policy core** (`internal/policy`, §4/§5/§5.1/§29): typed pipeline toggles,
  autonomy profiles, `Normalize` (§5.1 dependency rules), `Resolve` (AC-29
  invariant + LOCAL_REVIEW network lock + UI-merge visual-verification rule),
  typed Action gate, and the prompt-injection priority order. Pure domain
  package (imports only `fmt`).
- **Structured logging** (`log/slog` JSON) and **context cancellation** wired
  through the daemon lifecycle.
- **TUI shell** (`internal/tui`, AC-1): full-screen alternate-buffer shell that
  renders a banner, placeholders and daemon status, exits on q/Esc/Ctrl-C, and
  degrades gracefully on a non-TTY.
- **CLI**: `forge daemon {run,start,stop,status,logs}`, `forge doctor` (build,
  platform, runtime home + permissions, DB schema version, daemon status).
- **Tests**: storage/migrations (WAL, idempotency, concurrent readers,
  append-only), audit (history reconstruction), transport (loopback refusal,
  token auth, SSE ordering, `/audit`, bus), policy (table-driven profiles, §5.1
  rules, AC-29, network lock, injection ordering), daemon Run + reconciliation
  (health, private files, audit persistence, API+context shutdown, stale/
  corrupted reclaim, live-pid conflict abort, extension point), and
  **integration tests** that build the real `forge` binary and exercise
  lifecycle / repeated-start / corrupted-state / SIGTERM cancellation / restart
  plus the **automated M0 demonstrable scenario** (12 steps). All run under
  `go test -race` and `make check`.

### Remaining in M0

- **AC-27 full agent-attempt recovery** is the only honest gap. The startup
  reconciliation framework + M0 entity coverage are done, but real agent
  attempts / work packages / worktrees do not exist until **M2/M3**, so the
  end-to-end `start attempt → checkpoint → kill → restart → reconcile → resume
  or deterministic restart` scenario cannot be exercised yet. It is not faked
  (rule §36.25). AC-27 stays **PARTIAL**, blocked on M2/M3; AC-27 completion
  also touches M7-3.

## Milestone M1 — what is implemented

- **Project Registry** (`internal/project`, §8): register Git repositories with
  read-only validation (never modifies checkout §17.1), list/show/remove
  projects, auto-generated slug IDs with uniqueness handling, duplicate-path
  rejection. Every mutation audited (§29.4).
- **Project State Machine** (`internal/project`, §8.4): the six M1 states
  (DISABLED/IDLE/RUNNING/PAUSED/DRAINING/ERROR) with validated transitions;
  illegal transitions rejected with `ErrInvalidTransition`. State persisted to
  SQLite before any effect (§11.4).
- **Local Backlog** (`internal/task`, §9): free-form task creation (AC-3 —
  description can be the only user field), optional title/priority, project
  association. Content-addressed attachments (SHA-256, §9.5, AC-4) stored under
  `~/.neuroforge/artifacts/<hash>`.
- **Task State Machine** (`internal/task`): NEW/INGESTED/PAUSED/CANCELLED with
  validated transitions; terminal states cannot be left.
- **Daemon API** (`internal/transport`, `internal/daemon`): RESTful project and
  task endpoints (`GET/POST/DELETE /projects`, `GET/POST /tasks`, lifecycle
  actions). The daemon is the single owner of mutable state (ADR-0002); both
  CLI and TUI reach it only through the loopback HTTP API — the TUI never
  touches SQLite directly.
- **CLI Commands** (`internal/cli`): `forge project {add,list,show,start,pause,
  stop,remove}`, `forge task {add,list,show,pause,cancel}`, `forge dashboard`.
  All read commands support `--json`.
- **TUI** (`internal/tui`, ADR-0011): Model-View-Update architecture with raw
  terminal mode (`golang.org/x/term`). Projects screen (list + navigation +
  state colours), Tasks screen (project-filtered), detail views, command palette
  (Ctrl-P fuzzy search), status bar (daemon status + key hints), mouse tracking,
  and live event refresh via daemon SSE subscription. Model/update logic is
  fully unit-tested without a terminal.
- **Durable state** (`internal/storage`, §11.4): migration v3 adds `projects`,
  `tasks`, `task_attachments` tables. State survives daemon restart (verified by
  integration test).
- **Tests**: project state machine (table-driven valid/invalid transitions),
  task state machine, project registry (duplicate rejection, non-Git rejection,
  git validation, audit recording, state persistence), task backlog (free-form,
  attachment hashing, content-addressed storage, state transitions), transport
  API endpoints (HTTP round-trip with mock API, auth, empty-list-as-array,
  POST body), TUI model/update (keyboard navigation, screen switching, command
  palette, daemon event handling, view rendering), and the **M1 demonstrable
  scenario** (builds real `forge` binary, exercises 15-step end-to-end flow:
  add project → list/start → add tasks with attachment → pause/cancel →
  restart daemon → verify persistence → audit trail). Plus duplicate-project
  rejection, nonexistent-repo rejection, and local API invalid-transition tests.
  All run under `go test -race` and `make check`.

## Milestone M2 — what is implemented

- **Versioned protocol (§12, §13, ADR-0005/0012)** — `internal/adapter/codingagent/protocol`:
  `ProtocolVersion == 1` (the stability boundary), `AgentCapabilities` (§12.3),
  `AgentRunRequest`/`ResumeRequest`/`RunHandle` (engine ≠ model, §12.1),
  `Account` (name-only — no credentials, AC-28), `ModelDescriptor`/quota types
  (§20.1 confidence levels), the §12.4 normalized event set, the §32 failure
  taxonomy with bounded `DefaultPolicy`, and the JSON-RPC 2.0 envelope + method
  constants + handshake/version-negotiation types.
- **Adapter interface + registry (§12.2)** — `internal/adapter/codingagent`:
  `Adapter` (`CodingAgentAdapter`), `EventSink` (+ `SliceSink`/`ChannelSink`/
  `TeeSink`/`SinkFunc`), `Registry` (purely-additive registration, AC-6), and
  `DefaultClassify` (deterministic exit-code/events/stderr → §32 class, no LLM).
- **Fake coding agent (§33.1)** — `internal/adapter/codingagent/fake` +
  `cmd/fake-coding-agent`: deterministic, network-free; 13 scenarios (success,
  quota before/after edits, rate limit, auth failure, malformed JSON, timeout,
  crash, partial output, resume, cancellation, scope violation, usage events).
  One scenario script drives the in-process adapter, the JSONL command mode and
  the JSON-RPC plugin mode identically.
- **Declarative command adapter (§13.1)** — `internal/adapter/codingagent/declarative`:
  zero-dependency YAML-subset manifest parser (flow + block sequences), template
  substitution, JSONL streaming with process-group spawn, malformed-output
  capture to artifacts + classification, cancellation that kills the whole
  group. Ships a worked `example.yaml`.
- **Native JSON-RPC plugin (§13.2)** — `internal/adapter/codingagent/plugin`:
  client spawns the plugin, handshakes with version negotiation, exposes it as
  an `Adapter`, routes `run.event` notifications to per-run sinks, reaps the
  process group on close. Reference server in the fake package.
- **Process-group termination** — `internal/adapter/codingagent/proctree`:
  setpgid (unix) / new process group (windows) + tree kill, so cancellation ends
  the whole group (spec requirement).
- **Conformance suite (§13.3)** — `internal/adapter/codingagent/conformance` +
  `forge plugin test`: handshake, version compatibility, event ordering,
  malformed output, cancellation, timeout, quota failure, resume, process crash.
  Passes against both the in-process fake adapter and the fake plugin.
- **`forge plugin test` CLI** — runs the suite against any plugin executable
  (text + `--json`), all 9 checks green against the fake agent. AC-6 satisfied.
- **Invariants** — `internal/adapter/codingagent/invariants_test.go`: no
  credential fields cross the boundary (AC-28), no hard-coded model names in core
  (§36.8), engine ≠ model (§12.1), protocol pinned to v1, §12.4 event set exact.
- **Docs** — `docs/architecture/ADAPTER_DEV_GUIDE.md` (3 adapter paths), ADR-0012,
  updated ADR-0005 + this matrix + the implementation plan.
- **Tests** — protocol (events/failure/caps/version/handshake round-trips),
  adapter (registry/event-sinks/classifier, table-driven), fake (all scenarios),
  declarative (manifest parsing incl. flow sequences, end-to-end runs, malformed
  capture, crash synthesis, cancellation kills group, no creds in env,
  concurrency), plugin (handshake/metadata/run/cancel/resume/quota/malformed/
  usage/process-group reaping), conformance (suite vs in-process fake + fake
  plugin), CLI (`forge plugin test` text/JSON/usage/missing-exe), invariants.
  All run under `go test -race` and `make check`.

### Remaining in M2

- **AC-5** (concrete Codex/Claude/Gemini/Grok/Kimi/OpenCode adapters) is
  **planned for M4/M5** — deliberately not implemented (rule §36.7: stabilise the
  protocol first).
- **M2-8 supervisor** (consume adapters, enforce turn limits, checkpoints) and
  **M2-9 demonstrable scenario** depend on M3 workspaces; left to the M2-8/M3
  follow-up. The protocol, registry and conformance suite — the M2 acceptance
  surface (AC-6) — are complete and tested.

## Milestone M3 — what is implemented

- **Git worktree manager** (`internal/workspace`, ADR-0007, §17): creates
  isolated worktrees under `~/.neuroforge/workspaces/<project>/<task>/<wp>/
  attempt-<n>/` with branch naming per §17.3
  (`forge/<task>/<wp>/attempt-<n>`, `forge/result/<task>`). The user's primary
  checkout is NEVER modified — verified by integration test (HEAD SHA +
  working-tree files unchanged). The worktree path is persisted for restart
  recovery (AC-27, AC-9).
- **Safe git runner** (AC-7, §36.13): every git invocation is validated against
  a positive allowlist that structurally excludes `push`, `fetch`, `pull`,
  `clone`, `ls-remote`, `send-pack`, `fetch-pack`, and `archive`. LOCAL_REVIEW
  performs zero Git network operations by construction. Integration-tested
  (no remote refs are ever created during the full workspace lifecycle).
- **Checkpoints** (§5.2, §21.3): checkpoint commits created inside the attempt
  branch at the defined moments (plan, first-diff, compile, tests, screenshot,
  pre-quota-switch, pre-repair, pre-integration, manual). Checkpoint commits
  NEVER auto-merge into the user's main branch. Checkpoint records are durable
  (survive restart).
- **Local result branch** (AC-8, §17.4): `forge/result/<task-id>` is created
  locally pointing at the workspace HEAD. Never pushed. The TUI/CLI can open
  the diff, export a patch, and review (keep/reject/ask-for-changes).
- **Review lifecycle** (AC-10, §17.5): keep (retain), reject (delete managed
  worktree + attempt branch — NEVER user data outside the managed path), ask
  (retain for next attempt). Every action is audited.
- **Semantic + path leases** (`internal/workgraph`, §18.4): advisory locks on
  file paths and the five §18.4 semantic resources (database_schema,
  navigation_graph, subscription_contract, design_system, build_configuration).
  Conflicts block concurrent work packages (BLOCKED_LEASE).
- **Process supervisor** (`internal/supervisor`, §10/§12, AC-28): runs coding
  agents with a **positive-allowlist environment** that strips merge tokens,
  production credentials, API keys, and the daemon auth token. `AssertEnvSafe`
  verifies no leak. The supervisor enforces turn limits, a hard timeout, and
  streams normalized events.
- **Continuation packs** (§21.2): durable artifacts for provider switching and
  crash recovery, written to disk + recorded in storage.
- **Daemon wiring**: workspace service wired to the loopback API; workspace
  reconciler verifies worktree integrity at startup (stale worktrees marked,
  never silently resumed). Fake coding agent registered by default.
- **Doctor orphan detection**: `forge doctor` scans the managed workspaces
  directory for worktrees with no matching workspace record.
- **CLI commands** (`internal/cli`): `forge workspace {create,list,show,run,
  checkpoint,result,review,diff,patch,delete,checkpoints}` with `--json`.
- **Tests**: workspace unit tests (primary-checkout-untouched, branch naming,
  checkpoint commits, result branch, reject deletes only managed worktree,
  multiple attempts, diff/patch), workgraph lease tests (path + semantic
  conflicts, release, invalid), supervisor tests (env allowlist strips
  forbidden vars, fake agent runs in worktree, audit recording), and the
  **M3 demonstrable integration scenario** (30+ steps: temp repo → project →
  task → workspace → fake-agent run → checkpoint → result branch → verify
  primary untouched → verify no network ops → daemon restart → verify result
  accessible → diff → reject). Plus the AC-7 security test. All run under
  `go test -race` and `make check`.

## Bootstrap (original scaffold) — context

- `forge version` / `forge help` implemented + unit-tested (`internal/cli`,
  `internal/version`).
- Modular-monolith package skeleton created (25 packages build & vet clean); every
  not-yet-implemented package carries a `STATUS: scaffold — not implemented` doc
  comment.
- `make build` / `test` / `lint` / `check` configured and green (the original
  scaffold had zero deps; M0 added `modernc.org/sqlite`, ADR-0010).
- ADRs 0001–0009, architecture docs (COMPONENTS / DATA_FLOW / STATE_MACHINES),
  AGENTS/README/CONTRIBUTING, and this matrix + implementation plan in place.
- `docs/spec/NEUROFORGE_SPEC.md` **untouched**.
