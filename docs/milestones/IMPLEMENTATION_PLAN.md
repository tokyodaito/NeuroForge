# Implementation plan

Milestone-by-milestone breakdown of `docs/spec/NEUROFORGE_SPEC.md` (§34) into small,
independently verifiable issues. Every issue follows the same template. Status of
each requirement is mirrored in
[`../spec/COMPLIANCE_MATRIX.md`](../spec/COMPLIANCE_MATRIX.md).

> Legend — **Depends on** lists issue IDs that must merge first. AC references
> point to spec §35 acceptance criteria. **Status** of the whole project: only the
> M0 items marked `[DONE]` below are implemented; everything else is planned.

### Issue template

```
### Mx-n — Title
Goal:          one sentence outcome.
Scope:         concrete deliverables.
Allowed paths: packages/files this issue may touch.
Forbidden:     packages/files it must not touch.
Depends on:    issue IDs.
Acceptance:    bullet list; each maps to a spec AC where relevant.
Checks:        how it is verified (tests, commands).
DoD:           Definition of Done (rule §36.25: no fake stubs).
```

---

## M0 — Foundation

> Spec §34 M0: Go repository · SQLite · migrations · daemon · event log · CLI
> skeleton · TUI shell. A demonstrable scenario: the daemon starts, persists
> state, and restarts safely.

### M0-1 — Go module + repo skeleton `[DONE in bootstrap]`
- **Goal:** establish the modular-monolith module and package skeleton.
- **Scope:** `go.mod` (`neuroforge`), `cmd/forge`, `internal/*` doc.go scaffolds,
  `Makefile`, `.gitignore`, `.editorconfig`, AGENTS/README/CONTRIBUTING, ADRs,
  architecture docs, this plan + compliance matrix.
- **Allowed paths:** whole repo (initial scaffold); never `docs/spec/NEUROFORGE_SPEC.md`.
- **Forbidden:** `docs/spec/NEUROFORGE_SPEC.md`; no adapter/storage/daemon logic.
- **Depends on:** —
- **Acceptance:** `make build`/`check` green; `forge` binary exists; packages map
  to spec §10.
- **Checks:** `make check == 0`; `go list ./...` shows expected packages.
- **DoD:** structure present; unimplemented packages marked explicitly (rule §36.25).

### M0-2 — `forge version` + `forge help` + CLI dispatch `[DONE in bootstrap]`
- **Goal:** minimal working CLI with tested dispatch.
- **Scope:** `internal/cli`, `internal/version`, `cmd/forge/main.go`.
- **Allowed paths:** `internal/cli/**`, `internal/version/**`, `cmd/forge/**`.
- **Forbidden:** daemon, transport, adapters, storage.
- **Depends on:** M0-1.
- **Acceptance:** `forge version` prints version/commit/platform; `forge help`
  lists implemented + planned commands; unknown command → exit 1; no-args prints
  TUI-not-implemented notice.
- **Checks:** `go test ./internal/cli ./internal/version`; `make build`.
- **DoD:** unit tests cover every branch; `make check` green.

### M0-3 — SQLite storage + migrations `[DONE]`
- **Goal:** durable, transactional store with versioned schema.
- **Scope:** `internal/storage`: open DB in WAL mode, migration runner, the §31
  table set (incrementally), "state before external action" write helpers.
- **Allowed paths:** `internal/storage/**`, a storage test DB (temp dir).
- **Forbidden:** daemon, adapters, policy logic.
- **Depends on:** M0-1. **Related ADR:** 0003, 0010.
- **Acceptance:** migrations are forward-only and idempotent; concurrent readers
  + single writer works; large artifacts referenced, not stored as BLOBs.
- **Checks:** unit tests (open, migrate from empty, re-migrate is no-op, WAL
  readable during write). — green under `make check`.
- **DoD:** driver choice justified (ADR-0010, pure-Go modernc.org/sqlite); no
  `go vet` issues. Remaining §31 tables land with later milestones (§36.25).

### M0-4 — Daemon process + lifecycle + startup reconciliation `[DONE: lifecycle + reconcile + workspace reconciler + M7 attempt reconciler]`
- **Goal:** single owner of mutable workflow state that recovers on restart.
- **Scope:** `internal/daemon`: start/stop, PID/lockfile, startup reconciliation
  of persisted attempts (resume/finish), no silent resume of forbidden ops.
- **Allowed paths:** `internal/daemon/**`, `internal/storage` (call), `internal/audit`.
- **Forbidden:** adapters, transport (use interfaces), router, scheduler.
- **Depends on:** M0-3, M0-6. **Related ADR:** 0002. **AC:** AC-27.
- **Acceptance:** daemon restart resumes in-flight attempts without double-spend;
  never resumes a `LOCAL_REVIEW` push.
- **Checks:** integration test: start → write attempt → kill → restart → reconciled.
- **DoD:** recovery scenario green; AC-27 test exists.
  — **Done:** process lifecycle (start/stop/status/logs/run), single-instance
  guard, stale/corrupted runtime reclaim, durable restart-safe state,
  signal/context graceful shutdown, and a deterministic idempotent startup
  **reconciliation framework** (Reconciler interface + extension point) covering
  the M0 runtime entities (runtime files, DB health), with every decision
  audited and a live-owner conflict abort. **M3** added the workspace reconciler
  (verifies worktree integrity at startup). **M7** added the attempt reconciler:
  an active workspace with a checkpoint + continuation pack is reconciled as
  resumable; an interrupted run is marked failed so it is not treated as live;
  the durable pack survives restart. The `start attempt → checkpoint → kill →
  restart → reconcile` end-to-end scenario now exists (AC-27 satisfied).

### M0-5 — Loopback transport (HTTP JSON + SSE + token) `[DONE]`
- **Goal:** TUI/CLI ↔ daemon command + live-event channel.
- **Scope:** `internal/transport`: loopback-only listener, JSON command API, SSE
  events, random bearer token.
- **Allowed paths:** `internal/transport/**`.
- **Forbidden:** daemon business logic, adapters.
- **Depends on:** M0-1. **Related ADR:** 0004.
- **Acceptance:** refuses non-loopback bind; rejects requests without token; SSE
  delivers events in order; token never logged.
- **Checks:** unit tests (bind refusal, auth, event order). — green.
- **DoD:** transport reusable by both CLI and TUI. — done (CLI uses the client).

### M0-6 — Audit log (append-only) `[DONE]`
- **Goal:** tamper-evident per-task history (AC-30).
- **Scope:** `internal/audit`: append-only event store; record commands, changed
  files, provider/model, attachment transfers, push/PR/merge/revert, policy
  overrides.
- **Allowed paths:** `internal/audit/**`, `internal/storage` (call).
- **Forbidden:** adapters; no delete/update API exposed.
- **Depends on:** M0-3.
- **Acceptance:** entries are append-only and reconstruct a task history; not
  writable by agent processes.
- **Checks:** unit tests (append, no update/delete, history reconstruction). — green.
- **DoD:** audit API documented; AC-30 data shape supported. — done (foundation).

### M0-7 — Policy core (pipeline toggles + dependency rules) `[DONE]`
- **Goal:** deterministic resolution of pipeline switches (§5, §5.1).
- **Scope:** `internal/policy`: toggle model, dependency rules
  (push=false ⇒ no CR/merge/post_merge), prompt-injection priority constants.
- **Allowed paths:** `internal/policy/**`.
- **Forbidden:** adapters, Git.
- **Depends on:** M0-1. **Related ADR:** 0008.
- **Acceptance:** rule §5.1 enforced; task override cannot weaken non-disableable
  security policy (AC-29).
- **Checks:** table-driven unit tests for every dependency rule. — green.
- **DoD:** rule table is the tested source of truth. — done (pure domain package,
  imports only `fmt`); enforcement points (Merge Governor, env allowlist, stage
  wiring) consume it in M3-4/M8-1/M11.

### M0-8 — TUI shell `[DONE]`
- **Goal:** `forge` with no args opens a (minimal) full-screen UI (AC-1 start).
- **Scope:** `internal/tui`: open full-screen layout, connect to daemon via
  transport, show "no projects" placeholder; graceful image fallback (§6.7).
- **Allowed paths:** `internal/tui/**`, `internal/transport` (call).
- **Forbidden:** adapters, storage, daemon.
- **Depends on:** M0-5.
- **Acceptance:** opens full-screen; exits cleanly; degrades on terminals without
  image support.
- **Checks:** integration smoke test (start/exit); manual scenario doc. — green.
- **DoD:** AC-1 partially satisfied (shell only). — done.

### M0-9 — Demonstrable M0 scenario `[DONE]`
- **Goal:** end-to-end proof for M0.
- **Scope:** glue + an integration test script.
- **Allowed paths:** test harness under `internal/daemon` test or `cmd/forge`.
- **Forbidden:** real adapters, real models.
- **Depends on:** M0-3..M0-8.
- **Acceptance:** build → daemon up → persist state → kill → restart → reconciled;
  `forge version`/`help` work.
- **Checks:** `make check` includes the scenario.
- **DoD:** M0 ships buildable + demonstrable (rule §36.20).
  — Done: `internal/cli/m0_scenario_test.go` drives the real `forge` binary in an
  isolated temp dir through all 12 steps (config, start, status, loopback bind,
  token auth, audit write/read, stop, restart, durable-state-preserved, no
  second daemon, clean exit). Runs under `make check` / `go test -race`; no
  network, no AI, no user dirs.

---

## M1 — Projects and local tasks

> Spec §8 (projects), §9 (tasks, compiler-lite). Scenario: add a project, manage a
> local backlog, drive the project state machine — no agents yet.

### M1-1 — Project registry `[DONE]`
- **Goal:** register/list/remove projects, persist config.
- **Scope:** `internal/project` (registry), `internal/storage` (projects,
  project_policies).
- **Allowed:** `internal/project/**`, storage tables, `internal/audit`.
- **Forbidden:** adapters, workspace, scheduler.
- **Depends on:** M0-3, M0-6. **AC:** — (enabler).
- **Acceptance:** `forge project add/list` work; idempotent; config versioned.
- **Checks:** unit + CLI tests; `--json` output.
- **DoD:** project row + policy row stored. — Done.

### M1-2 — Project onboarding & detection `[planned]`
- **Goal:** detect languages/build/test/lint/AGENTS.md/README/CI/remote (§8.3),
  confirm before writing.
- **Scope:** `internal/project`, `internal/repoinfo` (early, read-only scan).
- **Allowed:** read-only inspection of the target repo.
- **Forbidden:** writing to the user's checkout.
- **Depends on:** M1-1.
- **Acceptance:** detected commands shown and only persisted after user confirm.
- **Checks:** fixture-repo unit tests.
- **DoD:** onboarding result stored in project config.

### M1-3 — Project state machine `[DONE]`
- **Goal:** implement §8.4 transitions.
- **Scope:** `internal/project` (state), `docs/architecture/STATE_MACHINES.md`
  already documents it.
- **Allowed:** `internal/project/**`.
- **Depends on:** M1-1.
- **Acceptance:** all legal transitions allowed; illegal ones rejected; persisted
  before effect.
- **Checks:** table-driven transition tests.
- **DoD:** state machine matches the doc. — Done (six M1 states + transition table).

### M1-4 — Project lifecycle commands `[DONE]`
- **Goal:** start/pause/drain/stop/settings.
- **Scope:** `internal/cli` (project cmds), `internal/project`.
- **Allowed:** `internal/cli/**`, `internal/project/**`.
- **Depends on:** M1-3.
- **Acceptance:** commands drive the state machine; drain waits for active work.
- **Checks:** CLI/integration tests.
- **DoD:** AC-2 (manage projects without CLI is TUI; CLI parity here). — Done.

### M1-5 — Local backlog (tasks, compiler-lite) `[DONE]`
- **Goal:** `forge task add/list/show` with free-form text + attachments (§9).
- **Scope:** `internal/task` (model, backlog), content-addressed attachments (§9.5),
  redaction hook (§9.6 stubbed to policy).
- **Allowed:** `internal/task/**`, artifacts dir, `internal/audit`.
- **Forbidden:** work graph execution, adapters.
- **Depends on:** M1-1. **AC:** AC-3, AC-4.
- **Acceptance:** free-form task created; image attachment stored with hash+mime;
  metadata recorded.
- **Checks:** unit tests (hashing, mime, metadata).
- **DoD:** AC-3/AC-4 testable. — Done.

### M1-6 — Project/task TUI screens `[DONE]`
- **Goal:** Projects + Tasks panes (§6.2/§6.3 minimal).
- **Scope:** `internal/tui`.
- **Allowed:** `internal/tui/**`, transport.
- **Depends on:** M0-8, M1-1, M1-5. **AC:** AC-2.
- **Acceptance:** add project & create task without CLI; lists update live.
- **Checks:** TUI smoke test; manual scenario.
- **DoD:** AC-2 satisfied for projects/tasks. — Done (MVU architecture with raw
  mode, command palette, status bar, mouse support, live SSE refresh).

### M1-7 — M1 demonstrable scenario `[DONE]`
- **Depends on:** M1-1..M1-6. Build → add project → onboard → add free-form task
  w/ attachment → see it in TUI. **DoD:** AC-2/AC-3/AC-4 green.
  — Done: `internal/cli/m1_scenario_test.go` drives the real `forge` binary
  through 15 steps (add project, list --json, start, add tasks with attachments,
  pause/cancel, daemon restart persistence, audit trail). Plus duplicate-project,
  non-git-repo, and local API tests. Runs under `make check` / `go test -race`.

---

## M2 — Agent protocol

> Spec §12–§13, §33.1. Goal: stabilise the adapter contract; add the fake coding
> agent; run a task end-to-end against the fake. No real engines yet.

### M2-1 — Core interfaces & types `[DONE]`
- **Goal:** `CodingAgentAdapter`, `AgentRunRequest`, `RunHandle`, capabilities
  (§12.2/§12.3).
- **Allowed:** `internal/adapter/codingagent/**`.
- **Forbidden:** scheduler, router, storage schema changes.
- **Depends on:** M0-1. **AC:** AC-5/AC-6 (enabler). **Related ADR:** 0005, 0012.
- **Acceptance:** interfaces compile; capabilities struct complete.
- **Checks:** compile + type tests.
  — Done: versioned `protocol` package (`ProtocolVersion == 1`) holds all stable
  types; `Adapter` interface + `EventSink` + `Registry` in the parent package.

### M2-2 — Normalized events + EventSink `[DONE]`
- **Goal:** the §12.4 event set, typed, ordered.
- **Allowed:** `internal/adapter/codingagent/**`.
- **Depends on:** M2-1.
- **Acceptance:** every §12.4 event representable; sink ordered.
- **Checks:** unit tests on event ordering.
  — Done: `NormalizedEvent` + payloads + `ParseEventLine` (robust to
  malformed/unknown events) + `SliceSink`/`ChannelSink`/`TeeSink`.

### M2-3 — Failure classification (§32) `[DONE]`
- **Goal:** `ClassifyFailure` taxonomy + policies (retry/cooldown/failover/…).
- **Allowed:** `internal/adapter/codingagent/**`, `internal/quota` (states).
- **Depends on:** M2-2.
- **Acceptance:** every §32 class maps to a policy; no infinite retry.
- **Checks:** table-driven classification tests.
  — Done: §32 `FailureClass` taxonomy + bounded `DefaultPolicy` + `DefaultClassify`
  (exit-code/events/stderr heuristics). Retryable classes carry `MaxRetries`.

### M2-4 — Declarative command adapter (§13.1) `[DONE]`
- **Goal:** YAML-defined CLI adapter, no Go changes.
- **Allowed:** `internal/adapter/codingagent/**`, loader.
- **Depends on:** M2-1.
- **Acceptance:** a YAML agent registers and runs via template substitution.
- **Checks:** conformance subset.
  — Done: `declarative` package (zero-dep YAML-subset parser, flow + block
  sequences, template substitution, JSONL streaming, malformed capture, group
  cancel) + worked `example.yaml`.

### M2-5 — Native plugin JSON-RPC (§13.2) `[DONE]`
- **Goal:** stdin/stdout JSON-RPC 2.0 with mandatory methods.
- **Allowed:** `internal/adapter/codingagent/**`.
- **Depends on:** M2-1..M2-2.
- **Acceptance:** all mandatory methods handshake; protocol errors handled.
- **Checks:** plugin round-trip tests.
  — Done: `plugin` client (handshake + version negotiation, `run.event`
  streaming, process-group spawn/reap) + reference server in `fake`.

### M2-6 — Fake coding agent `[DONE]`
- **Goal:** §33.1 scenarios (success, quota before/after edits, rate limit,
  malformed JSON, timeout, crash, scope violation, resume).
- **Allowed:** test-only adapter package.
- **Depends on:** M2-1..M2-3.
- **Acceptance:** each scenario reproducible & deterministic.
- **Checks:** scenario tests.
  — Done: `fake` package (in-process adapter) + `cmd/fake-coding-agent`
  (command + jsonrpc modes); 13 scenarios, one shared script per surface.

### M2-7 — Conformance suite (`forge plugin test`) `[DONE]`
- **Goal:** §13.3 checks (handshake/order/malformed/cancel/timeout/quota/resume/
  crash/version).
- **Allowed:** `internal/cli` (plugin test cmd), conformance harness.
- **Depends on:** M2-1..M2-5. **AC:** AC-6.
- **Acceptance:** 7th agent passes conformance without core changes.
- **Checks:** run suite against the fake adapter.
  — Done: `conformance` package (9 checks) + `forge plugin test <exe>`
  (text/`--json`); passes against the in-process fake adapter AND the fake plugin
  with no core changes.

### M2-8 — Supervisor: run/resume/cancel + streaming
- **Goal:** consume adapters, enforce turn limits (§22.7), capture checkpoints.
- **Allowed:** `internal/supervisor/**`.
- **Depends on:** M2-1..M2-3, M3 (worktree). **Note:** may stub worktree initially.
- **Acceptance:** run lifecycle emits events; checkpoints created; cancellation
  propagates.
- **Checks:** supervisor unit + integration vs fake.
  — **Pending:** blocked on M3 workspaces; the M2 protocol/registry surface is
  complete and is what the supervisor will consume.

### M2-9 — M2 demonstrable scenario
- **Depends on:** M2-1..M2-8. End-to-end fake run; AC-6 (plugin agent) demoable.
  — AC-6 is demonstrable now via `forge plugin test`; the full end-to-end run
  lands with M2-8/M3.

---

## M3 — Workspaces

> Spec §17, §18.4. Goal: isolated, resumable, checkpointed runs.

### M3-1 — Git worktree manager + branch naming `[DONE]`
- **Allowed:** `internal/workspace/**`. **Forbidden:** vcs adapters, push.
- **Depends on:** M0-3. **Related ADR:** 0007.
- **Acceptance:** worktrees under `~/.neuroforge/workspaces`; branch names per
  §17.3; main checkout never modified (AC-8).
- **Checks:** fs + git assertions.
  — Done: `internal/workspace` manager creates isolated worktrees with
  `forge/<task>/<wp>/attempt-<n>` branches; the safe git runner enforces a
  positive allowlist (AC-7); primary-checkout-untouched is unit + integration
  tested.

### M3-2 — Semantic leases (§18.4) `[DONE]`
- **Allowed:** `internal/workgraph/**`, storage (semantic_leases).
- **Depends on:** M1-5.
- **Acceptance:** file + semantic resources leased; conflicts block (BLOCKED_LEASE).
  — Done: `internal/workgraph` LeaseManager acquires/releases path + semantic
  leases; conflicts return ErrLeaseConflict; tested.

### M3-3 — Checkpoints `[DONE]`
- **Allowed:** `internal/workspace/**`, `internal/audit`.
- **Acceptance:** §21.3 checkpoint moments created; never auto-merge to main.
  — Done: checkpoint commits created at the §21.3 moments inside the attempt
  branch; durable records survive restart.

### M3-4 — Process supervision (allowlisted env) `[DONE]`
- **Allowed:** `internal/supervisor/**`.
- **Acceptance:** agent env is allowlisted; no merge creds/token (AC-28).
- **Checks:** env-leak test.
  — Done: `internal/supervisor` EnvAllowlist strips all forbidden vars;
  AssertEnvSafe verifies; tested with the fake agent.

### M3-5 — Local review result `[DONE]`
- **Allowed:** `internal/workspace/**`, `internal/cli` (open/diff/accept/reject).
- **Depends on:** M3-1. **AC:** AC-8, AC-9, AC-10.
- **Acceptance:** `forge/result/<task>` branch; diff/worktree openable; accept/
  reject/ask-for-changes.
  — Done: result branch, diff, patch export, keep/reject/ask lifecycle; all via
  CLI + API; reject deletes only the managed worktree.

### M3-6 — M3 scenario `[DONE]`
- **Depends on:** M3-1..M3-5. AC-7/AC-8/AC-9/AC-10 demonstrable.
  — Done: `internal/cli/m3_scenario_test.go` drives the real `forge` binary
  through 30+ steps (temp repo → project → task → workspace → fake-agent run →
  checkpoint → result branch → verify primary untouched → verify no network
  ops → daemon restart → verify result accessible → diff → reject). Plus the
  AC-7 security test. Runs under `make check` / `go test -race`.

---

## M4 — Initial coding engines (Codex, Claude Code, Gemini CLI)

> Per engine. Adapters M4 & M5 parallelise after protocol stabilises (§34).

### M4-n — per engine (detect/health/caps → start/resume/cancel → quota/usage → conformance)
- **Allowed:** `internal/adapter/codingagent/<engine>/**`.
- **Forbidden:** core packages, schema.
- **Depends on:** M2-7 (conformance). **AC:** AC-5.
- **Acceptance:** engine passes conformance; quota+usage reported; no hard-coded
  model names in core.
- **Checks:** conformance suite + recorded fixtures (no real paid calls in CI).

---

## M5 — Remaining coding engines (Kimi Code, Grok Build, OpenCode)

Same template as M4. **Depends on:** M2-7. **AC:** AC-5.

---

## M6 — Routing, quota, budget, dashboard

> Spec §19, §20, §23, §26. Goal: pick routes, enforce limits, show usage.

### M6-1 — Model catalog (config, no hard-coded names) — §19.2
- **Allowed:** `internal/router/**`, config. **AC:** AC-16/AC-17 (enabler).

### M6-2 — Complexity classifier C0..C4 — §18.2/§19.3
- **Allowed:** `internal/task` (compiler) / new pkg.
- **Acceptance:** economic cascade; tier mapping per §19.3.

### M6-3 — Risk classifier R0..R4 — §26
- **Allowed:** `internal/risk/**`.
- **Acceptance:** deterministic classification; influences listed in §26.

### M6-4 — Route selection (engine+model+account+runtime) — §19
- **Allowed:** `internal/router/**`. **Depends on:** M6-1..M6-3, M2.
- **Acceptance:** AC-16 (cheap route), AC-17 (escalation); `forge route explain`.

### M6-5 — Circuit breaker — §20.3
- **Allowed:** `internal/quota/**`. **Depends on:** M2-3.

### M6-6 — Quota manager (confidence levels) — §20.1/§20.2
- **Allowed:** `internal/quota/**`.

### M6-7 — Budget controller — §23
- **Allowed:** `internal/budget/**`. **Acceptance:** soft/hard limits; BUDGET_EXCEEDED.

### M6-8 — Usage/cost tracking — §14.4
- **Allowed:** `internal/budget`/`internal/quota`.

### M6-9 — Dashboard (TUI) + usage/quota views — §6.1/§20.1
- **Allowed:** `internal/tui/**`. **Depends on:** M6-6/M6-8. **AC:** AC-18.
- **Acceptance:** exact/~estimated/unknown rendered distinctly.

### M6-10 — M6 scenario
- **Depends on:** M6-1..M6-9. AC-16/AC-17/AC-18 demonstrable.

---

## M7 — Failover

> Spec §21. Goal: switch providers without losing progress.

- **M7-1 — Continuation packs** (§21.2): `internal/supervisor`/storage. **AC:** AC-15. `[DONE]`
  — `ContinuationPack` with completed/remaining/verification; `BuildPackFromRun`
  (deduped), `MergePacks` (multi-hop), `RenderFallbackPrompt` (no full conversation),
  persisted on disk + `continuation_packs`.
- **M7-2 — Provider switching** using packs. `[DONE]` — `FailoverController` runs a
  route chain; on a provider-side failure it checkpoints (pre-quota-switch),
  writes a pack, opens the circuit, selects the fallback, continues with the
  pack-derived prompt. Recovery classifier + resume policy bound the behaviour.
- **M7-3 — Recovery on restart** (ties to M0-4). **AC:** AC-27. `[DONE]` —
  `attemptReconciler` recovers in-flight attempts at daemon startup; a
  checkpoint+pack-backed active workspace is reconciled as resumable, an
  interrupted run is marked failed; the durable pack survives restart.
- **M7-4 — Session policy.** `[DONE]` — `ResumePolicy` decides resume (same
  engine+session) vs clean restart (failover from pack); clean restart policy
  is enforced for all provider-side failures.
- **M7-5 — Scenario:** fake quota failure after edits → fallback keeps checkpoint (AC-15). `[DONE]`
  — `TestM7_AC15_PrimaryQuotaAfterEdits_FallbackContinuesFromCheckpoint` (12-step
  proof) + crash/restart integration test.

---

## M8 — Configurable tests and review

> Spec §5, §24, §25, §27.

- **M8-1 — Stage toggles + dependency enforcement** (§5/§5.1). **Allowed:** `internal/policy`. **AC:** AC-11..AC-14, AC-29. `[DONE]`
  — Extended the policy core with the §24.1 test toggles (generate,
  modify_existing, run_existing, run_generated, require_for_local_result,
  require_for_remote_merge), new Actions (ActModifyExistingTests,
  ActRunGeneratedTests, ActSecurityReview, ActArchReview), normalisation rules
  R6 (generate=false → modify_existing=false) and R7 (generate=false →
  run_generated=false), the §24.2 test-path scope validator (CheckTestScope /
  CheckFileChanges — test paths forbidden when generation is off), and the
  pipeline stage status (StageStatus / LocalResultLabels) that explicitly shows
  skipped/locked stages and renders the §24.4/§25.1 labels.
- **M8-2 — Test engine** (progressive verification §24.3). **Allowed:** `internal/testengine` (new). `[DONE]`
  — Progressive verification levels (syntax → compile → targeted → module →
  full); stops after the first failure; skips test levels entirely when tests
  are disabled. Deterministic FakeRunner for offline testing (rule §36.5).
- **M8-3 — Tests generate/run toggles + scope validator** (§24.2). **AC:** AC-11, AC-12. `[DONE]`
  — testengine.ScopeValidator enforces the §24.2 rule: when generation is off,
  test file changes are rejected. generate/modifying/run-existing/run-generated
  are all independent toggles subject to the R6/R7 dependency rules.
- **M8-4 — Review engine** (§25). **Allowed:** `internal/review` (new). **AC:** AC-13. `[DONE]`
  — Three independent review roles (correctness/AI, architecture, security);
  each toggleable; AC-29 mandatory enforcement via policy.Resolve; Finding
  model (blocker/major/minor/info) consumed by the Merge Governor; deterministic
  FakeReviewer.
- **M8-5 — Verification evidence linking** (§27). `[DONE]` — `internal/evidence`
  (new): each acceptance criterion linked to typed evidence (test/visual/static/
  manual/review); confidence lowered when tests are disabled (§27); completeness
  gate consumed by the Merge Governor (§28).
- **M8-6 — Repair loops.** `[DONE]` — `internal/repair` (new): bounded repair loop
  collecting findings from test + review engines; builds a targeted repair
  context per §22.5 (finding + diff + failing test — NOT the full conversation);
  bounded by MaxIterations (rule §32: no infinite retry); history recorded for
  audit.
- **M8-7 — Scenario:** no-test/no-review task; LOCAL result labels (§24.4/§25.1). `[DONE]`
  — `internal/m8integration`: comprehensive table-driven integration tests for
  all main flag combinations (full pipeline, no-test, no-review, tests-off/
  review-on, tests-on/review-off, remote-review, autonomous with failures,
  gen-off auto-disables run-generated, partial reviews). Plus dedicated critical
  tests: test-paths-forbidden (§24.2), override-cannot-weaken-mandatory (AC-29),
  automatic-merge-cannot-bypass-mandatory-checks (§24.5), pipeline-status-shows-
  skipped-stages, repair-loop-resolves, evidence-confidence-lowered, independent
  push/PR/merge.

---

## M9 — Image providers (DONE)

> Spec §14, §15.4–§15.6, §33.2.

- **M9-1 — `ImageProviderAdapter` interface + registry** (§14.2). **AC:** AC-19 (enabler). **Related ADR:** 0006. ✅ `internal/adapter/imageprovider` + `protocol` sub-package.
- **M9-2 — Fake image provider** (§33.2). ✅ `internal/adapter/imageprovider/fake` (success/quota/invalid-image/timeout/failover/deterministic-fixture/auth).
- **M9-3 — GPT Image adapter.** **AC:** AC-19. ✅ `internal/adapter/imageprovider/gptimage` (OpenAI Images API; opt-in via CredentialResolver; tier→model catalog swappable).
- **M9-4 — Nano Banana adapter.** **AC:** AC-19. ✅ `internal/adapter/imageprovider/nanobanana` (Gemini generateContent; same opt-in/catalog shape).
- **M9-5 — Image budgets + quota** (§23 image, §14.4). ✅ image quota tracked separately on `quota.Manager` (image accounts); image spend bounded by `budget.Limits.ImageDailyUSD` separate from coding; tested.
- **M9-6 — Design generation orchestration** (variants + selection §15.4). **AC:** AC-20. ✅ `internal/design` (Brief/Variant/Specification; HUMAN/AUTOMATIC/FIRST_VALID; §15.1 modes; MaxVariants cap).
- **M9-7 — Scenario:** text → visual specification (AC-20); image quota failover (§15.5). ✅ `internal/m9integration` (AC-19/AC-20/quota-failover/cross-provider/rule §36.9 separation).
- **M9-8 — Artifacts store** (§9.5, §31). ✅ `internal/artifacts` (content-addressed SHA-256, atomic+idempotent writes; ADR-0014).
- **M9-9 — Conformance + CLI.** ✅ `internal/adapter/imageprovider/conformance` (10 checks); `forge image-provider list|doctor`.

---

## M10 — Design and visual verification (DONE)

> Spec §15, §16, §33.3.

- **M10-1 — `VisualHarness` interface + generic command harness** (§16.1/§16.2). ✅ `internal/adapter/visualharness` + `generic`. **Related ADR:** 0013.
- **M10-2 — Android harness** (§16.2). ✅ `internal/adapter/visualharness/android` (emulator/AVD/APK/Activity/locale/theme/font-scale/fixed-resolution/screencap).
- **M10-3 — Screenshot capture + comparison** (§16.3). **AC:** AC-22. ✅ Capture content-addressed; `internal/visual.DeterministicChecks` (size/viewport/blank/clipping/overflow/contrast/similarity).
- **M10-4 — Visual findings model** (§16.4). ✅ `visual.Finding`/`Result`/`ResultArtifacts` (severity blocker/major/minor/info; reference/actual/diff hashes).
- **M10-5 — Visual repair loop** (§16.5). **AC:** AC-23. ✅ `visual.RunRepairLoop` bounded (rule §32); stops on score≥minimum_score or MaximumIterations.
- **M10-6 — Visual specification lock** (§15.6). ✅ `design.Specification.IsLocked`; viewport/theme/locale/density carried end-to-end.
- **M10-7 — Reference-free quality review** (§16.6). **AC:** AC-24 (no false "verified"). ✅ `visual.ReferenceFreeReview` (integrity/overflow/readability/broken states); `PixelPerfect=false` unconditionally without a reference; `Status.IsVerified()` true only for `passed`.
- **M10-8 — Scenario:** screenshot-from-attachment → UI → verify → repair (AC-21/AC-22/AC-23). ✅ `internal/m10integration` (AC-21..AC-24 + end-to-end + all §33.3 fake harness scenarios).
- **M10-9 — Multimodal evaluator interface** (§16.3). ✅ `visual.MultimodalEvaluator` + deterministic default; production wires a vision-model-backed evaluator via a coding agent (rule §36.9: analysis by coding agent, generation by image provider).

---

## M11 — Remote delivery + Merge Governor

> Spec §17.6, §28, §3.3. Goal: push/PR/MR and deterministic merge.

- **M11-1 — `ChangeRequestProvider` interface** (§17.6). `[DONE]` — `internal/adapter/vcs` (interface + types + Capabilities + Registry + sentinel errors; ADR-0015).
- **M11-2 — Local Git provider** (accept/merge/squash/cherry-pick/patch §17.5). **AC:** AC-10. `[DONE]` — `internal/adapter/vcs/localgit` (§17.5 accept-into-current-branch with backup ref + dirty-checkout refusal; non-network, IsNetwork=false).
- **M11-3 — GitHub PR provider.** `[DONE]` — `internal/adapter/vcs/github` (REST API + auto-merge; fake httptest.Server fixture tests; opt-in `network` build-tag tests for real calls, rule §33).
- **M11-4 — GitLab MR provider.** `[DONE]` — `internal/adapter/vcs/gitlab` (REST API v4; fake fixture tests; opt-in `network` tests).
- **M11-5 — Merge Governor decision function + exhaustive tests** (§28). **Related ADR:** 0009, 0015. **AC:** AC-28, AC-29. `[DONE]` — Governor (M8) + `merge.Authority` (single merge-authority chokepoint) + `merge.Queue` (deterministic FIFO merge queue with local fallback for §5.1 R5 local-merge mode).
- **M11-6 — Push/PR/MR wiring under policy.** **AC:** AC-14. `[DONE]` — the Authority consults `policy.Resolve` per action: disabled push auto-forbids PR/MR and remote merge (§5.1 R1/R2).
- **M11-7 — Scenario:** REMOTE_REVIEW PR; AUTONOMOUS merge; LOCAL_REVIEW stays silent (AC-7). `[DONE]` — `internal/m11integration` (AC-7/AC-8/AC-14/AC-28/AC-29 + §3.3 REMOTE_REVIEW push+PR-no-merge + §28 sole-merge-authority + §29.4 audit + GitHub/GitLab end-to-end vs fake HTTP + merge queue determinism).

---

## M12 — Post-merge and optimization

> Spec §22, §37 (post-merge), §22.9.

- **M12-1 — Post-merge sentinel.**
- **M12-2 — Auto-revert** (AUTONOMOUS only).
- **M12-3 — Repo index (FTS, context pack)** (§22.2/§22.3). **Allowed:** `internal/repoinfo`.
- **M12-4 — Log slicing + delta repair context** (§22.4/§22.5).
- **M12-5 — Prompt-cache fingerprinting** (§22.8).
- **M12-6 — Project memory** (§22.9).
- **M12-7 — Quality statistics.**
- **M12-8 — Scenario:** post-merge regression → auto-revert; context budget respected (§22.1).

---

## M13 — Bootstrap

> Spec §7. `forge init` may start earlier but completes after all adapters (§34).

- **M13-1 — `forge init` onboarding wizard.** **AC:** AC-26.
- **M13-2 — System scan** (§7.2 stage 1).
- **M13-3 — Installation plan + confirmation** (no silent install/escalation §7.2/§36.17/§36.18). **AC:** AC-25.
- **M13-4 — Authentication wizard** (official provider mechanism §7.2 stage 6).
- **M13-5 — Toolchain lock** (§7.4; never update provider CLI mid-run §36.19).
- **M13-6 — `forge doctor`.**
- **M13-7 — `forge update`** (compatibility check, conformance re-run, rollback §7.5).
- **M13-8 — Scenario:** `forge init --dry-run` shows plan, changes nothing (AC-25); full init + doctor (AC-26).

---

## Cross-cutting rules for every issue

- Every AC has an automated or integration test (rule §36.22).
- No real paid models in CI — use fake agents/providers (rule §36.5, §33).
- After each milestone: app builds + has a demonstrable scenario (rule §36.20).
- Never disguise unimplemented behaviour as finished stubs (rule §36.25).
- Architectural deviations → ADR (rule §36.21).
