# ADR-0002: Local daemon with durable recovery

- **Status:** Accepted
- **Date:** 2026-07-24
- **Spec refs:** §11.2 (daemon), §11.4 (durable workflow), §27 (AC-27 restart
  recovery)

## Context

Runs are long-lived, multi-step and expensive. A daemon crash, OS reboot or
manual stop must not lose in-flight work, double-spend budget, or leave Git
worktrees and branches in an ambiguous state. The spec requires the daemon to own
state, supervise processes, manage the queue, stream events and — critically —
recover workflows after restart (§11.2, §11.4, AC-27).

## Decision

Run a **local daemon** that is the single owner of mutable workflow state. The
daemon:

1. Writes the intended next state and attempt metadata to durable storage
   (ADR-0003) **before** performing any external action (attempt, PID, workspace,
   checkpoint, route, budget, last event).
2. Supervises agent processes (ADR-0005) and records PID + checkpoint so an
   interrupted attempt can be resumed or safely restarted.
3. On startup, reconciles persisted state: resumes/finishes in-flight attempts,
   re-acquires leases, and never silently resumes a `LOCAL_REVIEW` push (which is
   always forbidden — ADR-0008).
4. Streams normalized events to the TUI over the loopback transport (ADR-0004).

The CLI and TUI are thin clients of the daemon; they do not own workflow state.

## Consequences

**Positive**

- Crash-safe, resumable workflows (AC-27); no silent double-spend.
- Single source of truth simplifies reasoning about concurrency and audit.

**Negative / trade-offs**

- The daemon is a critical single process; its quality bar (tests, recovery
  scenarios in §33.4) is high.
- Requires explicit lifecycle commands (`forge project start/pause/drain/stop`,
  `forge emergency-stop`).

## Alternatives considered

- **Stateless CLI that spawns ad-hoc processes.** Rejected: cannot satisfy
  durable recovery (§11.4) or safe resume; would lose in-flight attempts.
- **External orchestrator (k8s/Temporal).** Rejected: violates local-first/no-ops
  (§36.3) and adds dependencies; the durable-workflow guarantees are achievable
  in-process with SQLite.
