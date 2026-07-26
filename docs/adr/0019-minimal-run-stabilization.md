# ADR-0019 — Minimal reliable run (`forge run`) stabilization

- **Status:** Proposed
- **Date:** 2026-07-25
- **Spec refs:** §3.2, §11.4, §12.4, §17, §17.4, §29.2, §29.4, §36.13, §36.14,
  §36.22, §36.25, AC-7, AC-8, AC-27, AC-28
- **Related ADRs:** 0002 (local daemon), 0003 (SQLite durable state), 0005
  (adapter protocol), 0007 (worktree isolation), 0008 (LOCAL_REVIEW), 0009
  (merge governor)
- **Tracking docs:** `../stabilization/minimal-run/` (REQUIREMENTS,
  STATE_MACHINE, OUTCOME_CONTRACT, TEST_PLAN, ACCEPTANCE_MATRIX,
  IMPLEMENTATION_SLICES, REVIEW_CHECKLIST, KNOWN_FAILURES)

## Context

NeuroForge has many subsystems (M0–M13), but the **single** user scenario
"run one task against one production adapter and get a real result" is not
reliable. An independent review of the run path (see KNOWN_FAILURES.md) found
that a `run.completed` adapter event is treated as task success without
verifying the repository state, that workspaces can remain `active` after a
run, that `head_sha` can stay at the base SHA, that tasks stay `NEW`, that
production-adapter usage is not persisted via the workspace-run path, and
that Gemini cancellation can race into `run.failed`. There is also no
top-level `forge run` command; the minimal scenario today requires manually
chaining six commands.

This blocks trust in the product before any autonomous-factory behaviour is
 layered on top.

## Decision

Freeze a **minimal vertical slice** — `forge run "..."` — and stabilize it
end-to-end before extending the platform. The slice is specified in
`docs/stabilization/minimal-run/` and implemented as twelve small, separately
reviewed slices (IMPLEMENTATION_SLICES.md S1–S12).

Architecturally, the stabilization introduces one **new** composition site —
a thin application service (a new `internal/runapp/` package) — that owns the
single correct "finalize" sequence:

```
adapter terminal event
   → post-run Git inspection (FR-9)
   → outcome classification (FR-11)
   → atomic persistence of workspace + task terminal state + audit (FR-12/13)
   → result ref under refs/heads/forge/result/<task-id> (FR-14)
   → structured Result to the CLI (FR-18)
```

Key invariants the service enforces (and that the state machine in
STATE_MACHINE.md makes authoritative):

1. **Process success ≠ task success.** A clean adapter exit is necessary but
   not sufficient; the actual repository state decides the outcome.
2. **Git is the source of truth after a run.** `head_sha` is read from
   `git rev-parse HEAD` inside the worktree, never from a cached field.
3. **Terminal states are absorbing.** No late event or daemon restart may
   move a terminal workspace back to `active`.
4. **Cancellation precedence.** Once `run.cancelled` is accepted, a late
   `run.failed` cannot overwrite it; a timeout is `timed-out`, not
   `cancelled`.

### Scope (frozen)

**In:** simple `forge run`; daemon autostart; task creation; isolated
worktree; one production adapter (OpenCode); prompt/model transport; waiting
for a terminal event; Git inspection; terminal state; result ref;
cancellation; clear CLI result + exit codes; automated regression +
reliability + (opt-in) real-model smoke tests.

**Out (explicitly deferred, not deleted):** new TUI; autonomous backlog;
multi-agent routing; parallel execution; AI review; repair loop; auto-merge;
push; PR; remote merge; dashboards; campaign orchestration. Existing
subsystems implementing these are **bypassed** by the minimal run, not
removed.

## Alternatives considered

- **Fix the existing `forge workspace run` / `forge task dispatch` paths
  in place.** Rejected as the primary path: those entry points are coupled
  to the scheduler/postmerge/quality composition (M12/M13), and rewriting
  them risks regressing the broader platform. The minimal run is a *new*
  thin service that reuses the low-level building blocks (workspace manager,
  supervisor, task backlog, audit) without importing the higher-level
  orchestration. The old commands stay for compatibility (NFR-7).
- **Wait and stabilize "later", after more features.** Rejected: every
  additional subsystem built on an unreliable core inherits the
  reliability gap; the cost compounds.
- **Make `run.completed` authoritative and add a separate "verify" command.**
  Rejected: it preserves the failure mode (operator sees "completed",
  believes it). The verification must be **inside** the run, not optional.

## Consequences

- **Positive:** one reliable scenario end-to-end; a documented, tested
  outcome contract and state machine that later features (retry, failover,
  autonomous delivery) build on; a reusable outcome classifier and Git
  inspector; an anti-false-PASS review process (Gate E) that catches weak
  tests.
- **Positive:** the existing M0–M13 subsystems are untouched, so the
  compliance matrix entries for those milestones remain valid.
- **Negative / cost:** two run paths coexist for a while (`forge workspace
  run` / `forge task dispatch` with their known gaps, and the new
  `forge run`). KNOWN_FAILURES.md documents the gaps of the old path so
  users are not misled. The old paths should not be advertised for the
  minimal scenario.
- **Negative / cost:** a new `internal/runapp/` package is added. It is
  deliberately thin (no provider-specific logic; no LLM; no Git network)
  to keep the surface small and reviewable.
- **Constraint:** any storage change is a forward-only, idempotent
  migration (ADR-0003); Protocol v1 stays frozen (ADR-0005/0012); LOCAL_REVIEW
  remains a hard wall (ADR-0008, AC-7).

## Compliance

This ADR **does not** change `docs/spec/NEUROFORGE_SPEC.md`. It records how
the spec's §3.2 / §17.4 / §29 path is realized for the minimal scenario and
where the stabilization deviates from the *current code* (not from the spec):
the code's "process success == task success" behaviour is the deviation; this
ADR brings the code back into compliance with spec §11.4 ("state before
external action") and §36.25 ("unimplemented requirements explicitly
marked").

## Open questions (for the implementer / reviewer)

1. Should the result ref be created for `completed-with-uncommitted-changes`
   (points at base SHA) or only when there is a commit? This ADR proposes
   **yes, create it**, with honest labelling — the user gets a stable handle
   and the JSON makes clear the result is uncommitted. The reviewer should
   confirm. (OUTCOME_CONTRACT.md §1.3.)
2. Whether to back-port the KF-10 usage-persistence fix to the old
   `forge workspace run` path or only to `forge run`. Proposed: only
   `forge run` (the old path is deprecated for the minimal scenario).
3. Whether the Gemini race fix (KF-09) should be applied uniformly to all
   six adapters now or only to gemini. Proposed: **all six**, audited in S6.
