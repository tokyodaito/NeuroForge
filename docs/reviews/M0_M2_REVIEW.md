# M0_M2_REVIEW.md

**Reviewer:** independent staff-level reviewer  
**Date:** 2026-07-24  
**Scope:** NeuroForge M0-M2 (foundation, daemon, storage, adapter protocol, fake agent, TUI skeleton)  
**Status:** Implementation review against spec, AGENTS.md, ADRs, Compliance Matrix, Milestones M0-M2.  
**Tests executed:** `make test` (all pass, cached), `make race` (no races), `go vet` (clean), plugin conformance suite (passes), static analysis (green). No data races, no secret leaks detected.

**Summary:**  
- Durable state (SQLite) and crash recovery are **PARTIAL** per Compliance Matrix (AC-27).  
- Runtime state mutations (projects, tasks) now wrap SQLite operations + audit in
  single transactions (MAJOR-1/MAJOR-3 FIXED); agent-attempt resume and full
  worktree recovery remain stubbed (M2/M3 pending, MAJOR-2 deferred).  
- All other checked areas (adapter protocol extensibility, engine/model separation, fake-agent realism, no real providers, version negotiation, process-group cleanup, FS permissions, local API auth, data races) are **compliant** or **done**.  
- No production code changed. No new requirements added.

## Resolution log (2026-07-24 follow-up)

Only confirmed BLOCKER/MAJOR findings were acted on. No BLOCKERs were raised.
Status now attached to each finding below. Verification: `make check` green,
`make race` clean.

| Finding  | Severity | Status      | Notes |
|----------|----------|-------------|-------|
| MAJOR-1  | MAJOR    | **FIXED**   | State mutation + audit now share one SQLite tx. |
| MAJOR-2  | MAJOR    | **DEFERRED (M2/M3)** | Agent-attempt recovery; entities to recover do not exist yet (no `agent_attempts`/`work_packages` tables). The code explicitly marks this as a placeholder and forbids faking it (rule §36.25). Implementing it now would be new M2/M3 feature work, outside the scope of a "fix confirmed findings" pass. AC-27 remains tracked PARTIAL. |
| MAJOR-3  | MAJOR    | **FIXED**   | Same root cause as MAJOR-1: runtime mutations now use `storage.Tx`. |
| MINOR-*  | MINOR    | Not in scope | Task targeted BLOCKER/MAJOR only. |

**MAJOR-1 / MAJOR-3 fix.** `internal/storage` now exposes a `Tx`
(`DB.BeginTx`/`Tx.Commit`/`Tx.Rollback`) with transactional variants of every
compound-involved mutation (`CreateProject`, `UpdateProjectState`, `DeleteProject`,
`CreateTask`, `CreateAttachment`, `UpdateTaskState`) and of `AppendAuditEvent`.
`internal/audit` gained `AuditAppender` + `Recorder.RecordTx` so an audit event
is appended into the caller's transaction. `Registry` (`Add`/`Transition`/`Remove`)
and `Backlog` (`Add`/`Transition`) now wrap state change + audit in a single tx;
an audit failure aborts the mutation (audit is no longer a swallowed side-effect,
per §29.4). Regression tests cover tx commit/rollback atomicity at the storage
layer and state+audit commit-together at the registry/backlog layer.

## Remarks

### MAJOR
**File:** internal/project/registry.go:118  
**Status:** **FIXED** (2026-07-24). State mutation + audit now run inside one `storage.Tx`.  
**Problem:** State mutation (e.g. CreateProject) + audit recording are separate calls; no SQLite tx boundary.  **Evidence:** `if err := r.db.CreateProject(ctx, ...); err != nil { ... }` followed by `r.auditProject(ctx, ... Record(...))` (lines 118-131); identical pattern in Transition (192), Add in task/backlog.go (157), etc.  
**Required property:** All durable mutations must execute inside one SQLite transaction so "intended next state" is written before any external action (spec §11.4, ADR-0003).  
**Verification:** Integration test simulating SIGKILL mid-mutation; on restart verify state + full audit trail consistent.

### MAJOR
**File:** internal/daemon/reconcile.go:78  
**Status:** **DEFERRED (M2/M3)** — confirmed but not fixable in this pass. The
entities that would need recovery (agent attempts / work packages / worktrees)
do not exist yet in M0-M2 (no `agent_attempts` or `work_packages` tables; only
`schema_migrations`, `audit_events`, `projects`, `tasks`, `task_attachments`).
The placeholder is explicitly marked and the code forbids faking it (rule
§36.25). This is tracked M2/M3 feature work, not a defect in shipped code;
AC-27 remains PARTIAL.  
**Problem:** Agent-attempt / work-package recovery is placeholder only.  
**Evidence:** `TestReconcile_ExtensionPoint` uses "placeholder — real attempt recovery arrives in M2/M3" (line 196); Compliance Matrix AC-27 marked PARTIAL; no worktree/branch reclaim in DefaultReconcilers.  
**Required property:** Daemon must deterministically resume or safely restart in-flight agent attempts after crash/restart without duplicates or lost state (AC-27, ADR-0002 §11.2).  
**Verification:** Full agent-run reconciler + integration test with simulated crash during active run.

### MAJOR
**File:** internal/storage/migrate.go:172  
**Status:** **FIXED** (2026-07-24). Runtime mutations now use `storage.Tx`
(`DB.BeginTx`); same root cause and fix as MAJOR-1.  
**Problem:** Only migrations use BeginTx; runtime state changes (projects/tasks) have none.  
**Evidence:** `tx, err := d.db.BeginTx...` (172) used solely for schema_migrations table; all other DB ops in project/ and task/ are outside tx.  
**Required property:** Compound operations (DB write + audit append) must be atomic under SQLite tx for crash recovery and no partial state (spec §31, ADR-0003).  
**Verification:** Add tx wrapper in registry and backlog packages; test crash after mutation but before commit.

### MINOR
**File:** internal/transport/server.go:336  
**Problem:** loggingMiddleware scrubs token from path/status but does not explicitly redact if token appears in request body or headers in other logs.  
**Evidence:** Debug log omits token; no redaction in other slog calls that might reach audit or events.  
**Required property:** No secret leakage from tokens in logs or audit events (AGENTS.md §36.6, local security model).  
**Verification:** Run `forge daemon logs` during authenticated API calls and confirm token never appears.

### MINOR
**File:** internal/daemon/runtime.go:62  
**Problem:** writeFileSecret performs WriteFile then explicit Chmod (redundant on most systems).  
**Evidence:** `os.WriteFile(..., 0o600)` followed by `os.Chmod(..., 0o600)` (lines 62-65).  
**Required property:** Runtime files (PID, token, addr) must be owner-only readable (0o600).  
**Verification:** Inspect file modes on disk after `forge daemon run`.

### MINOR
**File:** internal/adapter/codingagent/protocol/protocol.go (and tests)  
**Problem:** Version negotiation and extensibility work via protocol v1, but no runtime version check in declarative/plugin beyond handshake.  
**Evidence:** ProtocolVersion == 1 hard-coded; conformance suite tests compatibility but no backward/forward negotiation logic exercised in M0-M2.  
**Required property:** Adapters must negotiate version and ignore unknown fields (ADR-0012, spec §12.4).  
**Verification:** Add version negotiation test in conformance suite.

All remarks are factual observations from code vs source of truth (spec, AGENTS.md, ADRs, Compliance Matrix, M0-M2 plan). MAJOR-1/MAJOR-3 fixes and regression tests applied 2026-07-24 (`make check` + `make race` green); MAJOR-2 deferred as tracked M2/M3 work; MINORs unactioned (out of scope).