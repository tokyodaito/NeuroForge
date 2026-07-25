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

**Proof levels** (not all "done" items carry the same weight of evidence; an
item may cite one or more):

- `unit-tested` — the pure domain function is tested in-process with deterministic
  fakes (no real daemon, no real binary).
- `integration-tested` — the domain packages are composed together in-process
  (real storage + real services, but no compiled binary / no loopback transport).
- `black-box tested` — the compiled `forge` binary is driven end-to-end through
  the daemon loopback transport; the test has no access to internal state and
  asserts only on observable outputs (HTTP/JSON, filesystem, Git state). This is
  the strongest weight of evidence.

The spec (`NEUROFORGE_SPEC.md`) is authoritative; this matrix is a tracking view.

## Acceptance criteria (spec §35)

| AC | Requirement (abridged) | Status | Milestone | Issue(s) | Package(s) |
|----|------------------------|--------|-----------|----------|------------|
| AC-1 | `forge` (no args) opens interactive TUI | done (M1: full TUI with projects/tasks screens, keyboard nav, command palette, status bar, mouse support, live event refresh via daemon SSE) | M0/M1 | M0-8, M1-6 | `internal/tui`, `internal/cli` |
| AC-2 | Manage projects/tasks without CLI | done (M1: TUI screens for projects and tasks; add project, navigate, start/pause/stop, pause/cancel tasks) | M1 | M1-6 | `internal/tui`, `internal/project` |
| AC-3 | Create a task with free-form text (no template) | done (M1: `forge task add` + TUI; description can be the only user field) | M1 | M1-5 | `internal/task` |
| AC-4 | Attach an image to a task | done (M1: `-a`/`--attach` flag; content-addressed SHA-256 storage under `~/.neuroforge/artifacts/`) | M1 | M1-5 | `internal/task` |
| AC-5 | Codex / Claude Code / Grok Build / Kimi Code / OpenCode / Gemini CLI | done (M4/M5: all six first-party adapters integrated on `integration/adapters`; each implements the full 13-method `codingagent.Adapter` surface at protocol v1 and passes the §13.3 conformance suite offline; central `builtin` registry wires them with no provider-specific core logic) | M4–M5 | M4-n, M5-n | `internal/adapter/codingagent/{codex,claude,gemini,kimi,grok,opencode,builtin}` |
| AC-6 | A 7th agent via plugin, no core changes | done (M2: versioned `protocol` package v1 + declarative + native JSON-RPC plugin + `forge plugin test` conformance suite; the fake agent passes all 9 checks via plugin with no core changes) | M2 | M2-7 | `internal/adapter/codingagent/{protocol,declarative,plugin,conformance}` |
| AC-7 | LOCAL_REVIEW performs no Git network ops | done (M3: structurally enforced by the workspace manager's git allowlist that excludes push/fetch/pull/clone/ls-remote; M11: the delivery-layer Authority refuses every push/PR/merge call in LOCAL_REVIEW, network providers are never invoked, and each refusal is audited — verified end-to-end in `m11integration`) | M0/M3/M11 | M0-7, M3-5, M11-6 | `internal/policy`, `internal/workspace`, `internal/merge`, `internal/adapter/vcs` |
| AC-8 | Code saved in a separate local result branch | done (M3: `forge/result/<task-id>` local branch created by workspace manager; checkpoint commits never auto-merge to main; M11: the result branch is the ONLY delivery artifact in LOCAL_REVIEW — it never appears as a remote ref, and the local-git provider can accept it via merge/squash/cherry-pick/patch) | M3/M11 | M3-5, M11-2 | `internal/workspace`, `internal/adapter/vcs/localgit` |
| AC-9 | Open diff and worktree from TUI | done (M3: `/workspaces/{id}/diff` and workspace path exposed via API; TUI reachable through daemon transport) | M3 | M3-5 | `internal/workspace`, `internal/transport` |
| AC-10 | Accept / reject / ask-for-changes | done (M3: keep/reject/ask review lifecycle via `POST /workspaces/{id}/review`; reject deletes only managed worktree, never user data) | M3/M11 | M3-5, M11-2 | `internal/workspace` |
| AC-11 | Disable test generation | done (M8: tests.generate toggle; when off, test paths become forbidden via the §24.2 scope validator and normalisation R6/R7 force modify_existing/run_generated off) | M8 | M8-1, M8-3 | `internal/policy`, `internal/testengine` |
| AC-12 | Disable running existing tests separately | done (M8: tests.run_existing is an independent toggle; run_generated is separate; both gated by the progressive test engine) | M8 | M8-3 | `internal/policy`, `internal/testengine` |
| AC-13 | Disable AI-review | done (M8: review.ai_review, architecture_review, security_review are independent toggles; §25.1 NOT AI-REVIEWED label when all off) | M8 | M8-4 | `internal/policy`, `internal/review` |
| AC-14 | Push / PR-MR / merge switchable separately | done (M8: independent toggles with §5.1 dependency rules; the Merge Governor decision function returns the highest permitted delivery action; M11: the Authority enforces the resolved policy per-action, so a disabled push automatically forbids PR/MR and remote merge (§5.1 R1/R2) — all toggle combinations covered in `m11integration`) | M8/M11 | M8-1, M11-6 | `internal/policy`, `internal/merge`, `internal/adapter/vcs` |
| AC-15 | Quota failure after edits → continuation via fallback, checkpoint kept | done (M7: cross-engine failover controller writes a continuation pack at the pre-quota-switch checkpoint, opens the circuit on the primary account, selects a fallback route and continues from the current state — the fallback receives ONLY the pack, never the full conversation; completed steps are deduped so they are not repeated; bounded recovery, no infinite retry) | M7 | M7-1, M7-5 | `internal/supervisor`, `internal/workspace` |
| AC-16 | Simple task → cheap route | done (M6: deterministic router maps C0→TINY/C1→SMALL via §19.3 base tiers; table-driven + scenario tests; exhausted accounts excluded) | M6 | M6-4 | `internal/router` |
| AC-17 | Complex task → strong model | done (M6: C3/C4 escalate to HEAVY/FRONTIER; risk R3/R4 floors the tier; fallback chain per §21.1) | M6 | M6-4 | `internal/router` |
| AC-18 | Dashboard shows exact vs ~estimated vs unknown usage distinctly | done (M6: confidence EXACT/PROVIDER_REPORTED/ESTIMATED/INFERRED/UNKNOWN carried end-to-end; `FormatRemaining` prefixes `~` for estimated/inferred; TUI Usage screen + `forge usage` tag totals) | M6 | M6-9 | `internal/quota`, `internal/budget`, `internal/tui`, `internal/cli` |
| AC-19 | GPT Image and Nano Banana adapters | done (M9: both real HTTP adapters implemented on `internal/adapter/imageprovider/{gptimage,nanobanana}`; tier→model catalog swappable without code change; both opt-in (Health=unknown when unconfigured, refuse to generate without a key — rule §33); §14 conformance suite passes against the fake provider) | M9 | M9-1, M9-3, M9-4 | `internal/adapter/imageprovider/{gptimage,nanobanana,fake,conformance}` |
| AC-20 | Generate a visual specification from text | done (M9: design orchestrator turns a text brief into N variants via §14.2 Generate, selects one (HUMAN/AUTOMATIC/FIRST_VALID), and locks a §15.6 visual specification with viewport/theme/locale/density; image quota failover §15.5 falls back to the next provider, then to the attached image, then WAITING_QUOTA) | M9 | M9-6, M9-7 | `internal/design`, `internal/adapter/imageprovider` |
| AC-21 | Create UI implementation task from an attached image | done (M10: REFERENCE_ONLY design mode locks the attached image as the visual specification; the task compiler feeds the locked artifact hash to the coding agent scope; §15.6: once locked, the agent must not arbitrarily change the design) | M10 | M10-6, M10-8 | `internal/design`, `internal/task` |
| AC-22 | Visual Verification captures a real screenshot | done (M10: VisualHarness protocol + generic command harness + first-class Android harness + §33.3 fake harness; Capture writes a content-addressed screenshot; the visual engine consumes it for §16.3 deterministic + multimodal checks) | M10 | M10-1, M10-2, M10-3 | `internal/adapter/visualharness`, `internal/visual` |
| AC-23 | Visual discrepancy triggers repair loop | done (M10: §16.5 bounded repair loop — screenshot → findings → targeted UI repair → rebuild → new screenshot; stops on score≥minimum_score or MaximumIterations; rule §32 no infinite retry) | M10 | M10-5 | `internal/visual` |
| AC-24 | Disabled visual verification never claims UI is verified | done (M10: `visual.Status.IsVerified()` returns true ONLY for `passed`; `skipped` (disabled) and `not_verified` (no screenshot) are never claimable as verified; §16.6 reference-free review sets `PixelPerfect=false` unconditionally) | M10 | M10-7 | `internal/visual`, `internal/policy` |
| AC-25 | `forge init --dry-run` shows a plan, changes nothing | done (M13: `forge init --dry-run` runs scan → profile → plan and renders it WITHOUT touching the filesystem; verified by a directory-snapshot test that asserts zero mutation; AC-25 covered in `m13integration` + CLI) | M13 | M13-3 | `internal/cli`, `internal/bootstrap` |
| AC-26 | `forge init` installs tools, offers official auth, runs doctor | done (M13: the onboarding wizard runs the 8 §7.2 stages — scan, profile, plan+confirmation, guided install, auth wizard (official mechanisms only), toolchain lock — then directs the user to `forge doctor`; the fake detector/installer drive CI per rule §33) | M13 | M13-1..M13-6 | `internal/bootstrap`, `internal/cli` |
| AC-27 | Daemon resumes unfinished tasks after restart | partial→done (M0: startup reconciliation framework; M3: workspace reconciler; M7: attempt reconciler recovers in-flight attempts — an active workspace with a checkpoint + continuation pack is reconciled as resumable; an interrupted run is marked failed so it is not treated as live; the durable pack survives restart so the failover controller can resume. The framework never auto-resumes a delivery operation.) | M0/M3/M7 | M0-4, M3-6, M7-3 | `internal/daemon`, `internal/storage`, `internal/workspace`, `internal/supervisor` |
| AC-28 | Agent has no merge credentials | done (M3: supervisor builds a positive-allowlist environment that strips GITHUB_TOKEN/GITLAB_TOKEN/AWS_SECRET/etc.; AssertEnvSafe verifies no leak; M11: VCS providers resolve tokens ONLY from the daemon-injected CredentialResolver — never from process env — so an agent subprocess cannot merge even with a token in env) | M3/M11 | M3-4, M11-3, M11-4 | `internal/supervisor`, `internal/adapter/vcs/{github,gitlab}` |
| AC-29 | Non-disableable security policy cannot be weakened by task override | done (M0: policy core enforces AC-29 invariant; M8: full pipeline wiring — scope validator, review engine, Merge Governor all consult the resolved policy which restores mandatory checks; M11: the delivery Authority consults the resolved (override-clamped) policy, so a mandatory-review override that enables merge is still blocked by the Governor and refused at delivery) | M0/M8/M11 | M0-7, M8-1, M11-5 | `internal/policy`, `internal/merge` |
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
| `forge task dispatch` | done (M12 wiring: dispatches through the production scheduler → dispatcher → supervisor, recording usage + memory + quality stats; --context-pack builds a token-budgeted pack; --json) | M12 |
| `forge task post-merge` | done (M12 wiring: runs the post-merge sentinel via the daemon; inject deterministic smoke checks; --json) | M12 |
| `forge task reopen` | done (M12 wiring: idempotent task reopen §37) | M12 |
| `forge memory list/learn` | done (M12 wiring: read/learn structured project memory §22.9 via daemon; --json) | M12 |
| `forge quality` | done (M12 wiring: token accounting + per-model success rates §6.1/§19.1 via daemon; --json) | M12 |
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
| `forge agent ...` / `forge model ...` | planned | M2, M6+ |
| `forge route explain` | done (M6: deterministic route decision, alternatives, fallback chain, quota, budget, exclusion reasons; --json) | M6 |
| `forge quota` | done (M6: per-account quota states + confidence; --json) | M6 |
| `forge usage` | done (M6: included vs paid separated, confidence-tagged totals; --json) | M6 |
| `forge cost` | done (M6: cost across daily/monthly/project scopes; --json) | M6 |
| `forge image-provider list` | done (M9: lists registered image providers; --json) | M9 |
| `forge image-provider doctor` | done (M9: runs the §14 conformance suite; --json) | M9 |
| `forge plugin ...` | done (M2: `forge plugin test <exe>` runs the §13.3 conformance suite; `forge plugin list` stub) | M2-7 |
| `forge audit` | partial (read-only `/audit` API available; `forge audit` CLI command in M1+) | M0/M1+ |
| `forge emergency-stop` / `forge cleanup` | planned | M1+ |
| `forge init` | done (M13: onboarding wizard — scan/profile/plan/confirm/install/auth/lock; `--dry-run` AC-25, `--yes`, `--profile`, `--no-global`, `--offline`, `--skip-agents`, `--repair`; `--json`) | M13 |
| `forge update` | done (M13: compatibility check, plan, apply, conformance re-run, rollback §7.5; blocked during active task §36.19) | M13 |

## Hard rules (spec §36) — current enforcement

| # | Rule | Status / mechanism |
|---|------|--------------------|
| 1–2 | Modular monolith, not microservices / one giant package | done — ADR-0001 + package layout |
| 3 | No Kubernetes | done — not used |
| 4 | No web UI before TUI | done — TUI-first in plan |
| 5 | No real paid models in CI | done (M2/M9) — fake coding agent (§33.1) + fake image provider (§33.2) + fake visual harness (§33.3) drive all orchestration/conformance tests; real image providers (GPT Image/Nano Banana) are opt-in and refuse to generate without a configured key; tested |
| 6 | Fake coding agent first | done (M2) — `internal/adapter/codingagent/fake` + `cmd/fake-coding-agent`, 13 scenarios |
| 7 | Stabilise adapter protocol, then adapters | done (M2) — protocol v1 stabilised; concrete engines in M4/M5 |
| 8 | No hard-coded model names in core | done (M2) — enforced by `TestCoreHasNoHardcodedModelNames`; models are provider-supplied |
| 9 | Separate coding agents from image providers | done (M9) — separate adapter families with distinct interfaces (`codingagent.Adapter` vs `imageprovider.Adapter`); separate registries; image quota/budget tracked separately (§14.4); enforced by the type system (a coding agent cannot be registered as an image provider); ADR-0005/0006/0013 |
| 10 | Quota not reported as exact unless provider says so | done (M6/M9) — `quota.Confidence` + `FormatRemaining` prefix `~` for ESTIMATED/INFERRED; aggregates take the coarsest confidence; image providers (GPT Image/Nano Banana) default to UNKNOWN confidence (no per-account quota API); tested |
| 11 | No full repo in prompt | done (M12) — `internal/repoinfo` builds the §22.2 index (file tree, symbols, imports, build/test graph, FTS-like search, related-changes) and assembles a compact Context Pack (§22.3) that is trimmed to a token budget (§22.1); log slicing (§22.4) + delta repair context (§22.5) keep failure payloads small; never dumps the whole repo (tested) |
| 12 | No LLM for Git/policy/quota/budget arithmetic | done (policy) — ADR-0009; code-only; repoinfo/quality/postmerge/memory/bootstrap are all pure Go (no LLM) |
| 13 | No push in LOCAL_REVIEW | done (M3) — git runner allowlist structurally excludes push/fetch/pull/clone/ls-remote; integration-tested (AC-7) |
| 14 | Never modify primary checkout | done (M3: worktree isolation verified by integration test — HEAD SHA + working tree files unchanged after workspace create/run/checkpoint/result) | ADR-0007 |
| 15 | Agent cannot change project security policy | done — `internal/policy` AC-29 enforcement; M8 wiring in testengine/review/merge |
| 16 | Agent cannot disable checks that validate its output | done — `internal/policy` mandatory checks + M8 review/merge enforcement |
| 17–18 | No silent install / privilege escalation | done (M13) — `internal/bootstrap` Executor requires an explicit `Confirmer`; plan-level, per-step sudo (§36.18) and shell-profile-diff (§7.2 stage 4) confirmations are each enforced with dedicated sentinel errors (`ErrNotConfirmed`/`ErrShellProfileNotApproved`); `--dry-run` is a pure no-op (AC-25); installer tests use the `FakeInstaller` (rule §33) |
| 19 | No provider CLI update during active run | done (M13) — `ToolchainLock.Update` consults an `ActiveTaskGuard` and returns `ErrActiveTask` before any update runs (§36.19); `forge update` honours it |
| 20 | App builds + demonstrable scenario after each milestone | enforced — `make check` gate |
| 21 | Record deviations as ADR | done — `docs/adr/` |
| 22 | Every AC has an automated/integration test | enforced — per-issue Checks |
| 23 | Spec is source of truth | done — referenced everywhere |
| 24 | Agent may not self-reduce project scope | done (M11: the Governor `scope_valid` gate is consulted by the Authority; a delivery whose changes fall outside the task scope is refused before any provider call) |
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

## Milestone M4/M5 — coding-agent adapters (integrated)

All six first-party coding engines (§12, AC-5) integrated on branch
`integration/adapters`. Each is a self-contained in-process Go adapter ("Path
3" of the adapter dev guide) under `internal/adapter/codingagent/<engine>/`.

- **Engines** — Codex (`codex`), Claude Code (`claude`), Gemini CLI (`gemini`),
  Kimi Code (`kimi`), Grok Build (`grok`), OpenCode (`opencode`).
- **Interface coverage** — every adapter satisfies `codingagent.Adapter` (13
  methods, §12.2), enforced at compile time by a `var _ codingagent.Adapter`
  assertion.
- **Protocol v1 frozen** — no event types or shared types were added to
  `internal/adapter/codingagent/protocol`. Each adapter translates its engine's
  native schema onto the existing §12.4 normalized event set.
- **No core changes** — each adapter lives in its own subpackage; no shared/core
  package was modified. `go.mod`/`go.sum` untouched (stdlib + existing internal
  packages only).
- **No self-registration** — adapters expose `New(opts)`; the daemon wires them.
- **Security invariants** — allowlisted environment only (§29.2, AC-28);
  `--share`/YOLO/bypass modes never enabled; secret redaction in
  stderr/events/artifacts; credentials never cross the boundary.
- **Cancellation/timeout** — terminate the whole process group via the shared
  `proctree` package (Windows-safe: `CREATE_NEW_PROCESS_GROUP` + `taskkill /T /F`).
- **Windows correctness** — PATHEXT-aware discovery (`.exe`/`.cmd`/`.bat`/npm
  shims; `.ps1` skipped where not spawnable), paths with spaces/Unicode, CRLF +
  UTF-8 BOM tolerance in stream parsers, no Unix-only assumptions.
- **Offline conformance** — all nine §13.3 checks (handshake, version
  compatibility, event ordering, malformed output, cancellation, timeout, quota
  failure, resume, process crash) honoured through each adapter's real run
  pipeline against recorded byte-stream fixtures — no paid calls (rule §36.5).
  Authenticated model enumeration / live quota / live health are deferred to
  each adapter's opt-in build-tagged smoke test (skipped in CI).
- **Central registry** — `internal/adapter/codingagent/builtin` constructs and
  registers all six adapters into a `codingagent.Registry` with default options.
  It holds no provider-specific logic (§13.3); canonical engine ids live there
  as the integration contract, verified against each adapter's `ID()`.
- **Tests** — per-adapter unit + conformance + (opt-in) smoke suites, plus
  `builtin` integration tests: discovery of all six, id uniqueness, duplicate
  rejection, and dispatch through the common `codingagent.Adapter` interface
  (the only surface the scheduler/supervisor core may use — ADR-0005).
- **Docs** — `docs/adapters/<engine>.md` per engine, `docs/adapters/README.md`
  index, per-adapter review notes under `docs/reviews/adapters/`, and the
  `docs/reviews/ADAPTER_INTEGRATION_REPORT.md` integration report.

### Explicitly not implemented (rule §36.25 — never faked)

- `ListModels` returns empty for engines with no offline catalogue (no
  hard-coded model names, §36.8); the model catalogue arrives in M6-1.
- `InspectQuota` reports UNKNOWN where the headless CLI exposes no live quota
  API (§20.1, §36.10); per-run usage flows via `usage.updated`.
- `SendMessage`/`Resume` return explicit errors where the engine has no
  headless live-message or resumable-session contract.

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

## Milestone M6 — what is implemented

- **Risk classifier** (`internal/risk`, §26): deterministic R0..R4 mapping from
  structural signals (auth/payments/permissions/destructive → R4; migrations/
  concurrency/subscriptions → R3; public API/integration → R2) plus conservative
  keyword/path hints. Highest band wins; reasons returned for §19.6 explanation.
  Never uses an LLM (rule §22.6).
- **Complexity classifier + model tiers** (`internal/router`, §18.2/§19.2/§19.3):
  C0..C4 from additive scoring (file count, turns, context size, role, cross-
  package, architectural decision, conflicting cheap results). Tiers TINY/SMALL/
  STANDARD/HEAVY/FRONTIER; `BaseTier` implements the §19.3 economic cascade;
  `Escalate`/`Deescalate` implement §19.4/§19.5.
- **Model catalog** (`internal/router`, §19.2, rule §36.8): maps abstract tiers to
  provider-supplied (engine, model, account) triples with cost/context/capability
  facts. **No model name is hard-coded in core** — enforced by
  `TestNoHardcodedModelNames` scanning router + fakes sources. Engine, model and
  account are kept distinct (§12.1/§19). Fakes use clearly-non-real identifiers
  (alpha-*/beta-*) so CI never touches a paid model (rule §36.5).
- **Deterministic router** (`internal/router`, §19): pure scoring — tier match,
  cost, subscription-included bonus, priority, provider-diversity penalty; never
  calls an LLM. Risk floors the tier (§26). Builds a §21.1 fallback chain
  (target → escalation → de-escalation). Exhausted accounts are excluded (§20.3);
  hard-budget blocks restrict to subscription-included routes (§23). Every
  decision is fully explainable: selected route, ranked alternatives, excluded
  candidates with reasons, quota summary, budget decision (`RenderExplanation`).
- **Quota manager + circuit breaker** (`internal/quota`, §20): confidence levels
  EXACT/PROVIDER_REPORTED/ESTIMATED/INFERRED/UNKNOWN (aliased from the stabilized
  protocol) and states AVAILABLE/LOW/EXHAUSTED/RATE_LIMITED/AUTH_REQUIRED/DEGRADED/
  UNKNOWN. Circuit breaker CLOSED/OPEN/HALF_OPEN. **Rate-limit ≠ exhaustion**
  (rate-limit recovers after retry-after; exhaustion blocks until reset).
  **Auth failure stops auto-retry** (only a healthy snapshot clears it).
  `FormatRemaining` prefixes `~` for estimated/inferred — estimated is never shown
  as exact (rule §36.10, AC-18). Safe for concurrent use.
- **Budget controller** (`internal/budget`, §23): global/daily/monthly/project/
  task/image budgets; soft (cheaper route) and hard (forbid paid run) limits.
  **Subscription-included usage accounted separately from paid API cost** —
  included usage never counts against a paid hard limit, and a hard block still
  permits included routes (§23). Aggregates carry the coarsest confidence (AC-18).
  Deterministic arithmetic, never an LLM (rule §22.6).
- **CLI** (`internal/cli`): `forge route explain` (text + `--json`), `forge quota`,
  `forge usage` (included vs paid lines, confidence-tagged totals), `forge cost`.
  All deterministic, offline (no daemon round-trip for the M6 surface).
- **TUI screens** (`internal/tui`, §6.1/§6.2/§19.6): Usage, Quotas and Route
  Decision screens reachable via the command palette. MVU architecture preserved;
  confidence-distinct rendering; rate-limit/exhaustion shown as distinct states.
- **Tests**: risk (table-driven + ordering), complexity (table-driven), router
  (tier mapping, scoring, fallback chain, exhausted-account exclusion, hard-budget
  restriction, image/context filters, determinism property, invalid-input
  rejection, no-hardcoded-names invariant, fallback-chain property), quota (state
  distinctness, breaker transitions, auth-stops-retry, low water mark, format,
  concurrency), budget (included/paid separation, soft/hard, most-restrictive
  scope, image separate, task-by-risk, aggregate confidence, concurrency), CLI
  (text/json for all four commands, invalid input), TUI (palette navigation, all
  three screens render, included/paid separation, snapshot population), and the
  **M6-10 scenario** demonstrating AC-16/AC-17/AC-18 together. All run under
  `go test -race` and `make check`.

### Adapter protocol unchanged

M6 adds **no** types to `internal/adapter/codingagent/protocol` and modifies no
adapter. The `quota` package aliases the protocol's `QuotaConfidence` so the
stabilized boundary remains the single source of truth.

### Remaining in M6 (explicitly not faked, rule §36.25)

- Live quota/usage snapshots are not yet streamed from the daemon into the
  router/quota/budget at runtime — the M6 commands/TUI operate on deterministic
  in-process fixtures (the default catalog + seeded demo usage). Daemon-owned
  catalog/usage persistence and per-task router invocation land with the
  supervisor wiring (M2-8 follow-up) and task execution. The pure decision
  functions are complete and fully tested.
- A pre-existing race in `internal/adapter/codingagent/claude.TestRunConcurrent`
  exists on `main` independent of M6; it is out of scope (adapter protocol is
  frozen, §36.7) and tracked separately.

## Milestone M7 — Failover — what is implemented

- **Continuation packs** (`internal/supervisor`, spec §21.2): durable artifacts
  written at provider switches / crash recovery. The pack captures
  base/current SHA, completed steps (deduped), remaining work, the triggering
  failures, verification status, and the next objective. `BuildPackFromRun`
  extracts the pack from a run's events; `MergePacks` accumulates progress
  across a multi-hop failover; `RenderFallbackPrompt` renders the prompt the
  fallback agent receives (the FULL conversation history of the failed run is
  deliberately NOT transferred — spec §21.2). Packs are persisted on disk
  (mode 0o600) and recorded in `continuation_packs` (durable recovery substrate).
- **Recovery classifier** (`internal/supervisor/recovery.go`, spec §21/§32):
  deterministic mapping of a §32 failure class to a bounded recovery action
  (retry / failover / wait_quota / quarantine / terminal / pause). Honours the
  per-class retry budget from `protocol.DefaultPolicy` so **no class triggers an
  infinite retry** (§32). Distinguishes "all routes quota-exhausted"
  (WAITING_QUOTA) from "unrecoverable" (QUARANTINE). Never an LLM (§22.6).
- **Resume / clean-restart policy** (`internal/supervisor/resume.go`, spec §21):
  decides whether to resume an existing provider session (transient failure +
  engine supports `SessionResume` + budget remains) or do a clean restart on a
  fallback route from the continuation pack. Failover always means a clean
  restart — the old session is irrelevant when switching provider.
- **Cross-engine failover controller** (`internal/supervisor/failover.go`,
  spec §21, AC-15): runs an agent across a route chain. On a provider-side
  failure it checkpoints (pre-quota-switch, §21.3), builds + persists a
  continuation pack, opens the circuit via the QuotaHook, selects the next
  fallback route, and continues with the pack-derived prompt. Transient
  failures (rate-limit/crash/timeout) get a bounded same-route retry with
  cooldown + jitter before failover. Terminal failures (build/test/scope/policy)
  are surfaced immediately — never retried or failed over.
- **Provider cooldown + jitter** (`internal/supervisor/jitter.go` + recovery
  classifier, §20.3): cooldowns carry a configurable jitter fraction; the
  quota manager already applied jitter to retry-after windows. Bounded retries
  apply the cooldown before each attempt.
- **Retry limits** — enforced by the recovery classifier honouring
  `protocol.DefaultPolicy`'s bounded `MaxRetries` per class; property-tested to
  guarantee no infinite retry (§32).
- **Checkpoint retention** (`internal/workspace/recovery.go`, spec §21.3):
  `RetainCheckpoints` prunes the oldest checkpoint records beyond a retention
  window once a run settles; the underlying commits stay in the attempt branch
  (the recovery substrate), only the bookkeeping rows are bounded.
- **WAITING_QUOTA + QUARANTINED** (`internal/workspace/recovery.go`, spec
  §15.5/§20.3/§28/§32): new workspace lifecycle states. WAITING_QUOTA parks a
  work package when every route is quota-exhausted (resumes automatically when a
  route resets). QUARANTINE marks an unrecoverable failure for human review.
  Both are re-entrant (a parked/quarantined workspace can return to active).
- **Crash/restart recovery** (`internal/daemon/attempt_reconcile.go`, AC-27,
  spec §11.4): a new startup reconciler recovers in-flight agent attempts after
  a daemon restart. An active workspace with a checkpoint + continuation pack
  is reconciled as resumable; an interrupted run is marked failed so it is not
  treated as live. Never auto-resumes a delivery operation (§36.13).
- **Daemon wiring** (`internal/daemon/failover_hook.go`): thin adapters bridge
  the real `workspace.Manager` and `quota.Manager` onto the supervisor's
  `WorkspaceHook`/`QuotaHook` interfaces (the supervisor stays free of a
  workspace import — the daemon is the composition root). The attempt
  reconciler is registered in the startup reconciliation chain.
- **Tests**: recovery classifier (table-driven: quota→failover, rate-limit
  bounded retry, auth→quarantine, terminal, protocol-error quarantine, no-
  infinite-retry property, jitter), resume policy (failover=clean-restart,
  retry+capability), continuation pack (fallback prompt has no transcript,
  completed dedup, multi-hop merge, build-from-run dedupe), failover controller
  (AC-15 quota-after-edits→fallback keeps checkpoint, all-routes-exhausted→
  WAITING_QUOTA, terminal-not-retried, rate-limit retry-then-failover), real
  hook integration (workspace checkpoint/state + quota feedback), workspace
  recovery states + checkpoint retention, attempt reconciler (resumable / no-
  pack-stale / waiting-quota-kept), **M7 scenario** (12-step AC-15 proof: two
  attempts, circuit opened, pack written, pre-quota-switch checkpoint,
  fallback got pack not conversation, pack durable), and a **crash/restart**
  integration test (checkpoint + pack survive DB close/reopen). All run under
  `go test -race` and `make check`; no real paid providers (§36.5).

### Adapter protocol unchanged

M7 adds **no** types to `internal/adapter/codingagent/protocol` and modifies no
adapter. The failover controller consumes the stabilized §32 taxonomy +
`DefaultPolicy` that M2 defined.

### Remaining in M7 (explicitly not faked, rule §36.25)

- The failover controller is exercised in-process (full real storage + workspace
  + quota manager + fake agent). It is not yet wired behind a `forge workspace`
  CLI subcommand / transport endpoint — that wiring lands when the scheduler
  (M6 follow-up) dispatches tasks, since the scheduler is what invokes the
  controller per task with a router-derived route chain. The pure failover
  logic is complete and tested end-to-end against the fake agent.
- Live quota reset detection (auto-unparking a WAITING_QUOTA workspace the moment
  a provider reports a fresh quota window) depends on the daemon-owned quota
  polling that arrives with the scheduler wiring; the recovery classifier +
  reconciler already handle the decision correctly once a route becomes
  available.

## Milestone M8 — Configurable tests and review — what is implemented

- **Stage toggles + dependency enforcement** (`internal/policy`, §5/§5.1, AC-29):
  extended the policy core with the §24.1 test toggles (generate,
  modify_existing, run_existing, run_generated, require_for_local_result,
  require_for_remote_merge) and new Actions. Normalisation rules R6 (generate=
  false → modify_existing=false) and R7 (generate=false → run_generated=false)
  enforce the §24.2 cascade. The AC-29 invariant (task override cannot weaken
  mandatory security policy) is wired through the full pipeline.
- **Test-path scope validator** (`internal/policy/scope.go` + `internal/testengine/
  scope.go`, §24.2): when test generation is disabled, test file changes are
  structurally forbidden. The validator recognises test paths across Go, JS/TS,
  Python, Java, Ruby, and generic `test/`/`__tests__/`/`src/test/` conventions.
- **Pipeline stage status** (`internal/policy/stages.go`): an explicit, human-
  readable breakdown showing which stages are active, skipped, or locked. Renders
  the §24.4/§25.1 local-result labels (IMPLEMENTED / NOT TESTED / NOT REVIEWED /
  LOCAL BRANCH ONLY). Locked stages (push/CR/merge under LOCAL_REVIEW) are
  distinguished from merely skipped ones.
- **Test engine** (`internal/testengine`, §24.3, M8-2): progressive verification
  (syntax → compile → targeted → module → full); stops after the first failure;
  skips test levels entirely when tests are disabled; deterministic FakeRunner
  for offline testing (rule §36.5).
- **Review engine** (`internal/review`, §25, M8-4, AC-13): three independent
  review roles (correctness/AI, architecture, security); each toggleable; the
  Finding model (blocker/major/minor/info) is consumed by the Merge Governor;
  deterministic FakeReviewer.
- **Verification evidence** (`internal/evidence`, §27, M8-5): each acceptance
  criterion linked to typed evidence (test/visual/static/manual/review);
  confidence is lowered when tests are disabled (§27); completeness gate
  consumed by the Merge Governor.
- **Repair loop** (`internal/repair`, §22.5/§25/§16.5, M8-6): bounded loop
  collecting findings from the test + review engines; builds a targeted repair
  context per §22.5 (finding + diff + failing test — NOT the full conversation);
  bounded by MaxIterations (rule §32: no infinite retry); iteration history
  recorded for audit.
- **Merge Governor** (`internal/merge`, §28, ADR-0009, M8-1): the deterministic
  decision function [Decide] evaluates all §28 gates and returns one of
  ALLOW_LOCAL_RESULT/ALLOW_PUSH/ALLOW_CHANGE_REQUEST/ALLOW_MERGE/REQUIRE_REBASE/
  REQUIRE_REPAIR/POLICY_BLOCKED/QUARANTINE. The §24.5 enforcement: a task
  override disabling tests cannot bypass the mandatory merge rule (tests ran but
  failed → REQUIRE_REPAIR; tests disabled entirely → POLICY_BLOCKED).
- **Integration tests** (`internal/m8integration`, M8-7): comprehensive table-
  driven tests for all main flag combinations + dedicated critical-path tests
  for §24.2 test-paths-forbidden, AC-29 override-clamp, §24.5 merge-bypass-
  prevention, pipeline-status visibility, repair-loop resolution, evidence-
  confidence lowering, and independent push/PR/merge toggles.

### Remaining in M8 (explicitly not faked, rule §36.25)

- The M8 engines are exercised in-process with deterministic fakes. They are not
  yet wired behind daemon transport endpoints or CLI subcommands — that wiring
  lands with the scheduler (M6 follow-up) that dispatches tasks through the full
  pipeline. The pure decision functions and their composition are complete and
  tested.
- Image providers and visual verification (M9/M10) are implemented — the
  `ImageProviderAdapter` protocol + GPT Image + Nano Banana + fake image
  provider (§14, §33.2), the design orchestrator with image quota failover
  (§15), the `VisualHarness` protocol + generic/Android/fake harnesses (§16,
  §33.3), and the Visual Verification Engine with deterministic checks,
  multimodal evaluator interface, reference-free review (§16.6) and the bounded
  repair loop (§16.5) are all in place. The `visual_policy_satisfied` gate can
  now be computed from a `visual.Result`. Real image/device calls remain opt-in
  (rule §33): CI exercises only the fake provider/harness.
- Remote merge (GitHub/GitLab PR providers, M11) is now implemented: the
  `vcs.ChangeRequestProvider` interface (Local Git, GitHub, GitLab) is the §17.6
  surface, and `merge.Authority` is the single merge-authority chokepoint that
  authorises every delivery call against a Governor decision + resolved policy +
  the network lock. A deterministic merge queue serialises merges with a
  local-merge fallback (§5.1 R5). GitHub/GitLab are covered by fake HTTP/fixture
  tests; real network tests are opt-in (`network` build tag). Audit records
  push/PR/MR/merge/denied-delivery (§29.4). See ADR-0015.

## Milestone M12 — Post-merge and optimization — what is implemented

- **Repo index + Context Packs** (`internal/repoinfo`, §22.2/§22.3, rule §36.11):
  builds the repository index — file tree, symbol index (Go/Python/JS/TS/JVM),
  imports, build/test/lint command detection, an in-memory ranked search (the
  SQLite-FTS analogue), and a related-changes co-change graph. `AssemblePack`
  builds a compact Context Pack (specification + allowed scope + repo map +
  relevant file slices + architectural rules + commands + recent failures +
  artifact links) that is **trimmed to a token budget** (§22.1) — it never dumps
  the whole repo (rule §36.11, tested). A `MaxRepoFiles` walk cap bounds memory.
- **Log slicing** (`internal/repoinfo`, §22.4): `SliceLog` reduces a raw build/
  test log to the exit code + failing command + first error + relevant stack
  trace + deduped summary of other errors + a link to the full log — bounded to
  `MaxLogTokens` so a log can never blow the context budget. Models never receive
  the full log.
- **Delta repair context** (`internal/repoinfo`, §22.5): `AssembleDelta` hands a
  repair agent ONLY the finding + current diff + failing test + implicated files
  — it deliberately does NOT replay the full research history (tested).
- **Prompt-cache fingerprinting** (`internal/repoinfo`, §22.8): `StablePrefix`
  renders the cacheable parts (rules/commands/repo map) in a deterministic order,
  and `FingerprintPrompt` produces a stable hash so providers that support prompt
  caching can detect a cache hit (byte-stable ordering, tested).
- **Project memory** (`internal/memory`, §22.9): typed records
  (architecture_fact/build_command/design_system_rule/known_failure/
  accepted_decision/provider_quirk) with source/confidence/scope/commit-SHA/
  expiration. Re-learning bumps the version; TTL + on-commit-change pruning; the
  high-confidence rules feed the Context Pack's "architectural rules". Pure
  domain logic; persisted by the daemon via the §31 `project_memory` table
  (migration v5).
- **Post-merge sentinel + auto-revert + task reopening** (`internal/postmerge`,
  §4.4/§37): after a merge, the sentinel runs smoke checks; on regression it
  auto-reverts through the Authority (the single merge-authority chokepoint,
  §28) AND reopens the task — but **only when the resolved policy enables
  `post_merge.auto_revert`** (only ever true for AUTONOMOUS, §4.4). Outside
  AUTONOMOUS the sentinel is a structural no-op (the merge would already have
  been refused, AC-7). A failed revert downgrades to ALERT_ONLY and reopens for
  human review (never silent). Tested end-to-end in `m12integration`.
- **Token accounting + quality statistics** (`internal/quality`, §6.1/§14.4/
  §19.1): records per-run usage (coding input/output/cached + image generations)
  with cached input accounted separately (§22.8 cache benefit); aggregates by
  task/project/provider/day; records per-task outcomes and computes success
  rates per model/engine/route (the §19.1 routing feedback signals). Pure
  deterministic arithmetic (rule §22.6); the durable substrate is the §31
  `usage_events` table (migration v5).
- **Storage** (migration v5): `post_merge_checks`, `usage_events`,
  `project_memory` tables (§31).
- **Scenario** (`internal/m12integration`, M12-8): post-merge regression →
  auto-revert + task reopen; auto-revert structurally disabled outside
  AUTONOMOUS; failed-revert downgrade; context budget respected (§22.1); log
  slice + delta repair small (§22.4/§22.5); prompt-cache fingerprint stable
  (§22.8); token accounting + quality stats. All run under `go test -race`.

### Remaining in M12 (explicitly not faked, rule §36.25)

- ~~The M12 engines are exercised in-process with deterministic fakes. They are
  not yet wired behind daemon transport endpoints / driven by the scheduler —
  that wiring lands with the scheduler (M2-8 follow-up) that dispatches tasks
  through the full pipeline + post-merge sentinel. The pure decision functions
  and their composition are complete and tested.~~
- **M12/M13 production wiring complete.** The scheduler package
  (`internal/scheduler`) is the production composition root: task dispatch flows
  scheduler → dispatcher (workspace) → supervisor, with usage events persisted
  to SQLite (§6.1/§14.4), token-budgeted Context Packs built and prepended to
  the agent prompt (§22.3), project memory read/updated (§22.9), and quality
  statistics recorded (§19.1). The post-merge sentinel is wired behind the
  daemon transport and driven after a merge through the Authority reverter path
  (ADR-0017). New daemon endpoints: `POST /tasks/{id}/dispatch`,
  `POST /tasks/{id}/post-merge`, `POST /tasks/{id}/reopen`,
  `GET /projects/{id}/usage`, `GET|POST /projects/{id}/memory`,
  `GET /quality/stats`, `GET /tasks/{id}/post-merge`. New CLI: `forge task
  dispatch`, `forge task post-merge`, `forge task reopen`, `forge memory`, `forge
  quality`.
- **Proof levels now distinguished honestly:**
  - M12 domain packages (`postmerge`, `memory`, `quality`, `repoinfo`) are
    **unit-tested** (in-process) AND **integration-tested** (`m12integration`).
  - The production scheduler wiring is **unit-tested** (`scheduler` package) AND
    **black-box tested** (`m12_m13_e2e_test.go` drives the compiled `forge`
    binary through the daemon loopback transport for all 15 required scenarios:
    dispatch, attempt creation, adapter execution, usage persistence, Context
    Pack, memory, quality stats, restart recovery, post-merge, sentinel,
    auto-revert, idempotent reopen, ALERT_ONLY downgrade, LOCAL_REVIEW no-op,
    init/update production services).

## Milestone M13 — Bootstrap — what is implemented

- **System scan** (`internal/bootstrap/scan.go`, §7.2 stage 1): detects OS/arch/
  shell/git/package-manager/docker/podman/gh/glab/JDK/Node/Android-SDK + the six
  coding agents, all via a read-only `Detector` abstraction (production
  `CommandDetector` shells out to `--version` only; tests use `FakeDetector`).
  Flags elevation (warns, never escalates). Never installs/mutates.
- **Profiles** (`internal/bootstrap/profiles.go`, §7.2 stage 2):
  Minimal/Standard/Android/Web/Full/Custom, each a declarative set of tool
  requests with sudo flags + shell-profile-change annotations.
- **Installation plan + confirmation** (`internal/bootstrap/plan.go`, §7.2
  stage 3/4, AC-25): `ComputePlan` diffs the profile against the scan — present
  tools are NEVER reinstalled (§7.2 "удалять существующие версии" forbidden);
  `--no-global`/`--skip-agents` honoured. The plan renders the §7.2 stage-3
  view AND the shell-profile diff (shown BEFORE applying). `--dry-run` is a pure
  no-op.
- **Platform-specific installer abstraction** (`internal/bootstrap/installer.go`,
  §7.2 stage 5): the `Installer` interface + `Registry` + `Executor`. The
  `Executor` is the ONLY thing that mutates the system, and it enforces every
  safety rule via the `Confirmer`: plan approval (§36.17), per-step sudo
  approval (§36.18), and shell-profile-diff approval (§7.2 stage 4) — each with
  a dedicated sentinel error. CI uses the `FakeInstaller` (rule §33: installer
  tests never install real system packages); production uses the `guidedInstaller`
  that prints each step and never escalates silently.
- **Authentication wizard** (`internal/bootstrap/auth.go`, §7.2 stage 6): runs
  after install, launching each provider's OFFICIAL login mechanism via
  `LoginLauncher`. NeuroForge NEVER collects a provider password (tested: no
  password field crosses the boundary).
- **Toolchain lock** (`internal/bootstrap/toolchain.go`, §7.4): persists
  detected + installed versions; `Update` consults an `ActiveTaskGuard` and
  returns `ErrActiveTask` before any update during an active run (§36.19); drift
  detection.
- **`forge update`** (`internal/bootstrap/update.go`, §7.5): compatibility check
  → plan → apply → conformance re-run → rollback. On conformance failure the
  previous lock is restored (`ErrConformanceFailed`, §7.5 step 5, tested).
- **`forge init --repair`**: reconciles the toolchain with the lock — reinstalls
  only missing tools through the confirmation gate (never silent).
- **CLI** (`internal/cli/init_cmd.go`): `forge init` (with `--dry-run`,
  `--yes`, `--profile`, `--no-global`, `--offline`, `--skip-agents`, `--repair`,
  `--json`) and `forge update` (`--yes`, `--json`). Both are testable offline via
  injectable deps.
- **Scenario** (`internal/m13integration`, M13-8): AC-25 (dry-run changes
  nothing — directory-snapshot assertion), AC-26 (full init installs + locks),
  §36.17 (no silent install), §36.18 (no silent sudo), §7.2 stage 4 (shell diff
  shown first), §7.2 stage 6 (auth never asks passwords), §36.19 (no update
  during active task), §7.5 (rollback on conformance failure), all six profiles
  produce a plan, and CI uses the fake installer. All run under `go test -race`.

### Remaining in M13 (explicitly not faked, rule §36.25)

- ~~The production `guidedInstaller` prints each install step and the official
  command; it does not silently invoke `sudo`/system package managers (a
  deliberate safety choice).~~
- The production `guidedInstaller` prints each install step and the official
  command; it does not silently invoke `sudo`/system package managers (a
  deliberate safety choice — a platform-specific installer that actually runs
  `brew`/`apt`/`winget` would be registered into the `Registry` when the user
  opts in, always behind the confirmation gate).
- **M13 black-box proof added.** `forge init --dry-run`, `forge init --yes`,
  `forge init --repair`, and `forge update` are now **black-box tested** against
  the compiled `forge` binary (`TestM12_M13_E2E_InitDryRunAndRepair`): the
  dry-run is proven to change nothing (AC-25, directory-snapshot assertion), the
  full init writes the toolchain lock (§7.4), repair reconciles with the lock,
  and update runs the production conformance+rollback path. These are no longer
  only in-process/unit-tested — they are proven through the real CLI binary.
