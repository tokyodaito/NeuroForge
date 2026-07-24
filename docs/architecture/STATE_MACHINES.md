# State machines

Authoritative states come from the spec; this document makes the **transitions**
explicit and records the package that owns each machine. Where the spec lists
states but not transitions, the transitions below are a faithful interpretation —
any deviation from the spec is a defect.

Legend: `(package)` owns the machine; `[event/condition]` triggers a transition.

---

## 1. Project — `internal/project` (spec §8.4)

```
                        ┌───────────┐
      forge project add │  DISABLED │ ◄──────────────────────────────┐
                        └─────┬─────┘                                │
                         start                                       │
                              ▼                                      │
                        ┌───────────┐  ready   ┌──────┐  error       │
                        │ STARTING  │─────────►│ IDLE │──────────────┤
                        └─────┬─────┘         └──┬───┘              │
                              │ fail              │ start           │
                              ▼                   ▼                  │
                          ┌──────┐ task     ┌─────────┐              │
                          │ERROR │ run  ───►│ RUNNING │              │
                          └──────┘          └────┬────┘              │
                                                │ pause / drain     │
                          ┌──────────────┬──────┴────────┐           │
                       pause▼          drain▼          degraded/error│
                   ┌──────────┐    ┌──────────┐    ┌──────────┐      │
                   │ PAUSING  │    │ DRAINING │    │ DEGRADED │      │
                   └────┬─────┘    └────┬─────┘    └────┬─────┘      │
                        ▼ drained<=0     ▼ empty          ▼ recover   │
                   ┌──────────┐    ┌──────────┐     ┌──────────┐     │
                   │  PAUSED  │    │   IDLE   │     │   IDLE   │─────┘
                   └────┬─────┘    └──────────┘     └──────────┘
                        │ resume                                   │
                        └────► RUNNING

stop ─────► DISABLED      (from any state)
blocked ◄── policy violation / quota exhausted / budget exceeded (from RUNNING/IDLE)
ERROR ◄──── unrecoverable internal failure
```

Notes:

- `DEGRADED`: at least one provider unavailable but work continues on others.
- `BLOCKED`: hard policy/budget block; user action required to resume.
- Transitions persist to `internal/storage` before taking effect (§11.4).

---

## 2. Task — `internal/task` (spec §9, §18.1, §32)

```
DRAFT
  │ task compiled
  ▼
COMPILED        (objective + ACs + scope + risk locked into a specification)
  │ scheduled
  ▼
READY           (waiting for a free route / lease)
  │ scheduler assigns a work package
  ▼
RUNNING
  ├──► PAUSED            (user pause / policy)
  ├──► WAITING_DESIGN_SELECTION   (§15.4, human design pick — other tasks continue)
  ├──► WAITING_QUOTA      (no provider available; §15.5)
  ├──► WAITING_BUDGET     (hard budget exceeded; §23)
  ├──► WAITING_USER       (clarifying question unavoidable, §9.7)
  ├──► NEEDS_REPAIR       (verification/review findings; repair loop)
  │        ▲              │ repaired
  │        └──────────────┘
  ▼
VERIFIED        (required checks passed; Merge Governor input ready)
  │
  ▼
DELIVERING      (push / PR / MR / merge — only if policy allows)
  ▼
COMPLETED       (final status incl. NOT TESTED / NOT REVIEWED labels, §24.4, §25.1)
  │ or
  ▼
REJECTED        (user rejected local result)
  │ or
  ▼
FAILED          (exhausted retries / POLICY_BLOCKED / quarantine, §32)
```

Task overrides cannot weaken non-disableable project security policy (AC-29).

---

## 3. Work package — `internal/workgraph` (spec §18.3, §21)

A task decomposes into a DAG; each node is a work package that runs attempts.

```
PENDING         (lease not yet acquired)
  │ leases acquired (files + semantic resources, §18.4)
  ▼
READY
  │ route assigned
  ▼
RUNNING ──► attempt lifecycle (see §4)
  │
  ├──► SUSPENDED         (provider switch; continuation pack written, §21.2)
  │        │ fallback route ready
  │        ▼
  │     RUNNING
  │
  ├──► BLOCKED_LEASE     (conflicting semantic lease held by another package)
  ▼
DONE            (node's objective met, checkpoint persisted)
  │ all DAG predecessors DONE
  ▼
(upstream node becomes READY)
```

---

## 4. Agent run / attempt — `internal/supervisor` (spec §12.4, §21.3, §32)

```
attempts (n=1..)
  │ start / resume
  ▼
run.started
  │  message/tool/command/file.changed/usage.updated (streamed events)
  │  checkpoints at: after plan, first useful diff, compile, targeted tests,
  │                  screenshot, before quota switch, before repair, before integration (§21.3)
  ▼
run.completed ──► ATTEMPT_OK
run.failed    ──► classify (ClassifyFailure, §32)
                  ├ PROVIDER_QUOTA/RATE_LIMIT → failover (continuation pack)
                  ├ BUILD/TEST/VISUAL_FAILURE  → repair loop
                  ├ SCOPE/POLICY_VIOLATION     → POLICY_BLOCKED
                  ├ ENGINE_CRASH/PROTOCOL      → retry w/ backoff (bounded)
                  └ timeout / cancelled        → CANCELLED
run.cancelled ──► CANCELLED
```

Turn limits per complexity (§22.7: C0=4 … C4=40); a checkpoint is created before
the limit is hit. Infinite retries are forbidden (§32).

---

## 5. Circuit breaker — `internal/quota` (spec §20.3)

```
        ┌─────────────────────────────────────────────┐
        ▼                                             │
     CLOSED ──quota exhausted──► OPEN ──reset timer──► (probe)
        ▲                          │                  │
        │                          ▼                  │
        └──── success ◄──────── HALF_OPEN             │
                      │                               │
                      └── failure ───────────────────►│ (back to OPEN)
```

- `OPEN`: account blocked until reset; other tasks not assigned to it; work
  package gets a fallback route.
- Rate limit ≠ exhaustion: use `retry-after` + jitter, keep account usable.
- `AUTH_REQUIRED`: stop automatic retry, surface a "Log in" action.

---

## 6. Delivery — `internal/merge` + `internal/adapter/vcs` (spec §28, §3.1–§3.4)

The Merge Governor is deterministic and never holds merge credentials.

```
VERIFIED (task)
  │
  ▼
Merge Governor checks (§28):
  specification_locked · scope_valid · required_checks_passed ·
  acceptance_evidence_complete · blocker_findings==0 · major_findings==0 ·
  target_allowed · branch_current · budget_policy_satisfied · visual_policy_satisfied
  │
  ├── POLICY_BLOCKED   (a required check failed / policy forbids)
  ├── REQUIRE_REBASE   (target moved)
  ├── REQUIRE_REPAIR   (findings require another repair loop)
  ├── QUARANTINE       (severe/suspicious result, manual review)
  ├── ALLOW_LOCAL_RESULT  ──► forge/result/<task>   (LOCAL_REVIEW, AC-7/AC-8)
  ├── ALLOW_PUSH          ──► adapter/vcs.PushBranch (REMOTE_REVIEW, no merge)
  ├── ALLOW_CHANGE_REQUEST──► adapter/vcs.Create/UpdateChangeRequest
  └── ALLOW_MERGE         ──► adapter/vcs.Merge (+ optional EnableAutoMerge)
                              │
                              ▼
                        Post-Merge Sentinel (M12)
                              │ regression detected
                              ▼
                        adapter/vcs.Revert (auto-revert, AUTONOMOUS only)
```

In `LOCAL_REVIEW`, the only reachable decision is `ALLOW_LOCAL_RESULT`; all
network transitions are unreachable by policy.
