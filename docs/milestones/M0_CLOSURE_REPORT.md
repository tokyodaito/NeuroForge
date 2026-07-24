# M0 closure report

Gap analysis for closing out milestone M0 (Foundation). Source of truth:
[`docs/spec/NEUROFORGE_SPEC.md`](../spec/NEUROFORGE_SPEC.md) (§34, §35, §36) and
[`IMPLEMENTATION_PLAN.md`](IMPLEMENTATION_PLAN.md). Status is mirrored in
[`docs/spec/COMPLIANCE_MATRIX.md`](../spec/COMPLIANCE_MATRIX.md).

> Legend: **DONE** = implemented and covered by an automated test. **PARTIAL** =
> some pieces exist; one or more acceptance scenarios cannot be exercised yet
> because a hard dependency lives in a later milestone. **PENDING** = not started.

---

## What is already DONE in M0 (prior passes)

- M0-1 module/skeleton, M0-2 `forge version`/`help`/dispatch.
- M0-3 SQLite (WAL) + idempotent migrations + `audit_events` append-only table.
- M0-5 loopback transport (event bus, JSON API, SSE, random token, loopback-only).
- M0-6 append-only audit recorder (history reconstruction).
- M0-4 *lifecycle* + restart-safe durable state + single-instance guard +
  stale/corrupted runtime reclaim + graceful shutdown.
- M0-8 full-screen TUI shell (graceful non-TTY degradation).
- `forge daemon {run,start,stop,status,logs}`, `forge doctor` (basic).

---

## Gaps addressed by this closure pass

### M0-7 — Policy core (§5, §5.1, §29, AC-29)

- **Requirement:** deterministic pipeline toggle model, autonomy profiles
  (§4.1–§4.5), dependency validation/normalisation (§5.1), non-disableable
  project security policy (AC-29, §36.15/§36.16), LOCAL_REVIEW network lock
  (AC-7, ADR-0008), prompt-injection priority (§29.3), typed policy decisions.
- **Prior status:** scaffold only (`internal/policy/doc.go`).
- **Implementable now:** the full pure-domain policy core (no execution, no Git,
  no AI). Toggles, profiles, `Normalize` (dependency rules), `Resolve`
  (project+task, AC-29 enforcement), `Allows(action)` gate, injection-priority
  constants/types, violation/decision types.
- **Dependency on future milestones:** none for the core. *Enforcement points*
  (Merge Governor `ALLOW_*` emission in ADR-0008/0009, agent env allowlist in
  M3-4, stage-toggle wiring in M8-1) consume this core later; they are out of
  scope here.
- **Done-when:** table-driven tests cover every §5.1 rule, every profile's
  defaults, the AC-29 "override cannot weaken security" invariant, and the
  LOCAL_REVIEW structural network lock.
- **Tests:** `internal/policy/*_test.go` (profiles, dependency matrix, override
  clamp, mandatory checks, injection priority ordering, action gate).

### M0-4 — Startup reconciliation (§11.4, AC-27)

- **Requirement:** on start, the daemon reconciles persisted state vs OS reality;
  stale process metadata is not treated as live; determinism; idempotency;
  decisions audited; corrupted state never causes silent data loss; no duplicate
  daemon.
- **Prior status:** lifecycle/reclaim done; explicit reconcile step absent.
- **Implementable now:** a generic `Reconciler` framework + an extension point,
  covering the M0 runtime entities that actually exist: daemon-owned runtime
  files (PID/token/addr) and DB schema health. Every decision is audited.
- **Dependency on future milestones (the AC-27 blocker):** real `agent attempts`,
  `work packages`, worktrees and provider sessions do not exist until M2/M3.
  Per the task constraint, no synthetic attempt resume is fabricated.
- **Done-when:** framework + M0 reconcilers tested (stale→reclaim, live→conflict
  refusal, corrupt→no-data-loss, clean→idempotent no-op, decisions audited).
- **AC-27 status:** **PARTIAL** until the M2/M3 end-to-end scenario exists:
  `start attempt → checkpoint → kill daemon → restart → reconcile → resume or
  deterministic restart`.
- **Tests:** `internal/daemon/reconcile_test.go`.

### M0-9 — Demonstrable scenario (§36.20)

- **Requirement:** automated end-to-end M0 proof in an isolated temp dir:
  config → daemon up → health/status → loopback bind → token auth → audit write
  → audit read → stop → restart → durable state preserved → no second daemon →
  clean exit. CI-runnable, in `make check`, no network/AI, no user dirs touched.
- **Prior status:** building blocks tested individually; no single scripted E2E.
- **Implementable now:** fully. Requires a read-only `/audit` API endpoint
  (token-gated) so the scenario can read audit through the daemon.
- **Dependency on future milestones:** none.
- **Done-when:** the scenario runs green under `make check` / `go test -race`.
- **Tests:** `internal/cli/m0_scenario_test.go` (drives the real `forge` binary).

---

## AC status after this pass

| AC | Requirement | Status | Note |
|----|-------------|--------|------|
| AC-1 | `forge` opens TUI | partial | M0 shell; full screens later |
| AC-7 | LOCAL_REVIEW no Git network ops | partial | structurally enforced in `internal/policy` now; wire enforcement (Merge Governor) in M11 |
| AC-27 | Daemon resumes unfinished tasks after restart | **PARTIAL** | reconcile framework + M0 entities done; full attempt resume blocked on M2/M3 |
| AC-29 | Non-disableable security policy | partial→done(core) | policy core enforces; full pipeline wiring in M8-1 |
| AC-30 | Full task history in audit | partial | append-only store + read API; richer kinds per later milestones |

## Readiness verdict

`M0 COMPLETE EXCEPT AC-27 FULL ATTEMPT RECOVERY` — AC-27 cannot be honestly
closed until agent attempts exist (M2/M3). Every other M0 item in the
implementation plan is DONE or honestly PARTIAL with a named blocking milestone.

## Next permitted milestone

Finish the AC-27 remainder is gated on M2 (agent protocol) and M3 (workspaces).
The next milestone in dependency order is **M1 — Projects and local tasks**,
which has no blocker from M0.
