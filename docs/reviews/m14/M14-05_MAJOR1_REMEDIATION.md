# M14-05 MAJOR-1 Remediation — Implementation Report

**Task:** Remediation of MAJOR-1 from the M14-05 independent review
(`docs/reviews/m14/M14-05_REVIEW.md`): cross-task lease isolation defect.
**Implementer actor:** `M14-05-major1-remediation-session`.
**Verdict:** `IMPLEMENTED_TESTED`

This is a focused remediation of a single MAJOR finding from M14-05's
`REVIEW_APPROVED` review. It is **not** M14-06 implementation work (M14-06
rem `BLOCKED` pending independent acceptance of M14-05). The reviewer required
this fix to land before the dispatch hook (FU-M14-05-1) or the claim/release
CLI (FU-M14-05-2) could merge.

## SHAs

- **Starting SHA:** `67a2a83ac1c62619fdf8b27236e764155af63f38`
  (`M14-06: BLOCKED — predecessor M14-05 is REVIEW_APPROVED, not ACCEPTED`).
- **Candidate SHA:** this report's commit.

## Goal and actual scope

### The defect (MAJOR-1)

`ClaimRequest.ProjectID()` returned `req.TaskID` (scheduling.go:219), and the
daemon's `workGraphAPIAdapter.GetWorkGraph` passed `taskID` to
`ListActiveByProject` (workgraph_api.go:58). The lease layer is designed for
`scope="project", scope_id=<projectID>` (spec §18.4, M3 schema migration v4).
The mismatch meant leases were scoped to `(project, taskID)` instead of
`(project, projectID)`, so **two different tasks in the same project could
simultaneously acquire the same file path or semantic resource without
conflicting** — weakening lease isolation from per-project to per-task.

### Actual scope performed

1. **`ClaimRequest` now carries an explicit `ProjectID` field** sourced from
   the task's storage row by the caller. The misleading `ProjectID()` shim
   (which returned `TaskID`) is **deleted** so the mis-naming cannot survive
   (exactly as the reviewer required).
2. **`Scheduler.Claim` validates `ProjectID != ""`** and uses `req.ProjectID`
   directly for `ListActiveByProject`, `AcquirePathTTL`, and
   `AcquireSemanticTTL`.
3. **The daemon adapter resolves `projectID` from `taskID` once** via
   `a.svc.Tasks.Get(ctx, taskID).ProjectID`, then passes it to
   `ListActiveByProject` — so the readiness snapshot uses the same project
   scope `Claim` uses.
4. **All existing tests updated** to pass the real project ID (`"proj"` /
   `proj.ID`) instead of the task ID.
5. **Two new regression tests** prove the fix and guard against regression.

No other behaviour, package boundary, spec, baseline, or security invariant
was touched.

## Changed files

| File | Change |
|---|---|
| `internal/workgraph/scheduling.go` | Added `ProjectID` field to `ClaimRequest`; deleted `ProjectID()` shim; added `project_id` validation; updated 3 call-sites (`ListActiveByProject`, `AcquirePathTTL`, `AcquireSemanticTTL`) to use `req.ProjectID`. |
| `internal/daemon/workgraph_api.go` | `GetWorkGraph` resolves `projectID` from the task row via `a.svc.Tasks.Get` once, then passes it to `ListActiveByProject`. Added nil-guard for `a.svc.Tasks`. |
| `internal/workgraph/store_test.go` | Updated all 10 `ClaimRequest{}` constructions to include `ProjectID`; updated `AcquirePath`/`AcquireSemantic`/`ListActiveByProject` calls to use `"proj"` instead of `taskID`; added `TestScheduler_CrossTaskLeaseConflict_ProjectScoped` and `TestScheduler_ClaimMissingProjectID`. |
| `internal/daemon/workgraph_api_test.go` | `AcquirePath` now seeds the lease at `proj.ID` (the project scope) instead of `taskID`. |
| `internal/cli/workgraph_show_blackbox_test.go` | Same one-line fix: `AcquirePath` uses `proj.ID` instead of `taskID`. |

## Acceptance criterion → code → test mapping

The acceptance criterion is the MAJOR-1 required fix from
`docs/reviews/m14/M14-05_REVIEW.md`:

> "ClaimRequest should carry an explicit ProjectID field ... the daemon adapter
> should resolve projectID from taskID once for both the readiness snapshot and
> the lease scope. Add a regression test that proves cross-task lease conflict
> on the same path ... The current ProjectID() shim should be deleted."

| Required fix element | Code | Test |
|---|---|---|
| `ClaimRequest` carries explicit `ProjectID` field | `scheduling.go:83` (`ProjectID string` in `ClaimRequest`) | `TestScheduler_ClaimMissingProjectID` (rejects empty `ProjectID`); all scheduler tests pass it explicitly |
| `ProjectID()` shim deleted | `scheduling.go` — the method is gone; `grep ProjectID\(\)` returns nothing | compile-time: no caller references the shim |
| Scheduler uses `req.ProjectID` for lease scope | `scheduling.go:140` (`ListActiveByProject`), `:197` (`AcquirePathTTL`), `:202` (`AcquireSemanticTTL`) | `TestScheduler_CrossTaskLeaseConflict_ProjectScoped` (T-A and T-B in same project conflict on `src/shared.go`) |
| Daemon adapter resolves `projectID` from `taskID` | `workgraph_api.go:55-62` (`task, err := a.svc.Tasks.Get(ctx, taskID); projectID := task.ProjectID`) | `TestWorkGraphAdapter_ShowThroughTransport` (seeds lease at `proj.ID`, observes conflict through real transport); `TestWorkGraphShow_BlackBox_CreateShowAndExplainConflict` (compiled `forge` binary + real daemon) |
| Cross-task lease conflict regression test | n/a (test is the proof) | `TestScheduler_CrossTaskLeaseConflict_ProjectScoped`: two tasks (T-A, T-B) in project "proj"; ws-A claims `src/shared.go`; ws-B's claim for the same path fails with `ErrLeaseConflict`/`ErrPackageNotReady` naming the path + ws-A; a non-colliding path (`src/other.go`) does NOT conflict |

### Characterization evidence (defect proven before fix)

Before implementing the fix, a throwaway characterization test
(`TestCHAR_CrossTaskLeaseDefect_CURRENT_API`) confirmed the defect against the
unmodified codebase: T-B succeeded leasing `src/shared.go` despite ws-A
already holding it in the same project. The test was removed after confirming
the defect.

After implementing the fix, the production code was **temporarily reverted**
to the task-scoped wiring and `TestScheduler_CrossTaskLeaseConflict_ProjectScoped`
**failed** (`Cross-task defect: T-B claim succeeded ...`), then the fix was
restored and the test passed. This proves the regression test genuinely guards
the defect.

## Test commands and results

Toolchain: `go1.26.5 darwin/arm64`.

| Command | Exit | Result |
|---|---:|---|
| `go test -count=1 -run 'TestScheduler_CrossTaskLeaseConflict_ProjectScoped\|TestScheduler_ClaimMissingProjectID' ./internal/workgraph/` | 0 | both new regression tests PASS |
| `go test -count=1 ./internal/workgraph/` | 0 | all 51 workgraph tests PASS |
| `go test -count=1 ./internal/daemon/` | 0 | all daemon tests PASS (incl. `TestWorkGraphAdapter_ShowThroughTransport` with project-scoped lease) |
| `go test -count=1 -run TestWorkGraph ./internal/cli/ ./internal/daemon/` | 0 | black-box + transport tests PASS (no cache) |
| `go test -race -count=1 ./internal/workgraph/` | 0 | race-clean |
| `make check` | 0 | gofmt clean + `go vet ./...` clean + full suite green (every package `ok`, FAIL_COUNT 0) |
| `go test -race -count=1 ./...` | 0 | all packages `ok`, 0 FAIL, **no race detected** |
| `gofmt -l <changed files>` | 0 | no files listed |
| `go vet ./internal/workgraph/ ./internal/daemon/ ./internal/cli/` | 0 | clean |

## Black-box evidence

`TestWorkGraphShow_BlackBox_CreateShowAndExplainConflict` (compiled `forge`
binary + real daemon + real SQLite + real transport) drives the production
end-to-end path. Step 8 acquires a path lease at `proj.ID` (the project scope)
through the same `LeaseManager` the daemon uses, then `forge workgraph show
--json` reports the first package blocked with a reason naming the path and
`other-ws`. This exercises the daemon adapter's `projectID` resolution
(`a.svc.Tasks.Get` → `task.ProjectID` → `ListActiveByProject(ctx, projectID)`)
against the compiled binary. The test passed against a freshly compiled
`forge` this session.

`TestWorkGraphAdapter_ShowThroughTransport` (in-process transport against real
daemon `Run`) provides the same evidence at the transport layer.

## Known limitations

- **No production caller of `Scheduler.Claim` exists yet.** The dispatch hook
  (FU-M14-05-1) is still unimplemented. This remediation ensures that when it
  lands, the caller will be **forced** to supply a real `ProjectID` (the
  validation rejects an empty one), so the per-task-isolation defect cannot
  silently recur. The latent hazard the reviewer flagged is closed at the API
  boundary.
- **MINOR findings from M14-05_REVIEW are NOT addressed here.** This
  remediation is scoped strictly to MAJOR-1. The five MINOR findings
  (`releaseAllAcquired` unused param, `ComputeReadiness` self-workspace
  exclusion, migration v9 dedup, `IsLeaseUniqueConstraint` broad match,
  manifest SHA) remain tracked as follow-ups in `M14-05_REVIEW.md`.

## Follow-up problems

1. **FU-M14-05-1 (dispatch hook)** can now proceed safely: when it constructs
   `ClaimRequest`, it must resolve `ProjectID` from the task row (e.g. via
   `task.Backlog.Get` or `storage.GetTask`). The validation makes omission a
   hard error.
2. **FU-M14-05-2 (claim/release CLI + endpoint)** similarly must carry
   `ProjectID` through the transport DTO when it lands.
3. **MINOR-2 (self-workspace exclusion in `ComputeReadiness`)** is now slightly
   more relevant: because leases are project-scoped, the readiness verdict for
   a package whose own workspace holds a path lease will report a "self-
   conflict". This is cosmetic (each Claim uses a fresh workspace in the
   production model) and unchanged by this remediation; it remains tracked in
   `M14-05_REVIEW.md`.

## Verdict

**`IMPLEMENTED_TESTED`** — the MAJOR-1 defect is fixed:

- `ClaimRequest.ProjectID` is an explicit field; the misleading `ProjectID()`
  shim is deleted (compile-verified: zero references remain).
- `Scheduler.Claim` uses the real `ProjectID` for all three lease operations
  and rejects an empty one.
- The daemon adapter resolves `projectID` from the task row once and threads
  it into `ListActiveByProject`.
- The regression test (`TestScheduler_CrossTaskLeaseConflict_ProjectScoped`)
  proves cross-task isolation: two tasks in the same project conflict on the
  same path. The test was verified to FAIL when the fix is reverted and PASS
  when applied.
- `make check` is green (FAIL_COUNT 0); `go test -race ./...` is clean (0 FAIL,
  no race); gofmt and go vet are clean; the compiled-binary black-box test
  passes against the real daemon.
- No spec, baseline, security, autonomy, or merge-policy invariant was
  weakened; no fake/stub/demo leakage; package boundaries respected.
