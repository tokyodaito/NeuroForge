# MINIMAL_RUN_FINAL_MERGE_REPORT

Final local integration of the minimal `forge run` vertical slice into `main`.
Mandatory Gates A/B/C passed on the merge commit. Gate D not exercised.
Repository left **MERGED LOCALLY — READY TO PUSH**. No remote publish.

| | |
|--|--|
| Date (local) | 2026-07-26 |
| Role | release/integration agent |
| Final result | **MERGED LOCALLY — READY TO PUSH** |

---

## Final Integration Baseline

| Field | Value |
|-------|--------|
| Feature branch | `fix/runtime-production-adapters` |
| Feature SHA (final, with re-review commit) | `f42134a673900b459d0ef3e59b13904eabcc72d2` |
| Feature tip before re-review commit | `e8cf5a75c07a821e21e3027263be54cf8a6c969f` |
| Main pre-merge SHA | `253c9d1fd68c818f50a60cfb3e115b624e42fde3` |
| Merge SHA | `20991bf0a2e4792517632a8bffa1ac6c284ec149` |
| Report commit SHA | *(recorded after this file is committed)* |
| Previous ephemeral merge SHA (historical) | `80ee2304b52febad165dd818ac1d77e64ef02083` |

---

## Independent Approval

| Field | Value |
|-------|--------|
| Artifact path | `docs/reviews/MINIMAL_RUN_POST_ROLLBACK_REREVIEW.md` |
| Artifact commit | `f42134a673900b459d0ef3e59b13904eabcc72d2` (`docs(review): approve post-rollback minimal-run merge`) |
| Verdict | **READY FOR MERGE** |
| Root cause classification | **VARIANT B — CROSS-TEST PROCESS ATTRIBUTION** |
| max global daemon PID (diff-home) | **2** |
| max same-home daemon PID | **1** |
| max per-home daemon PID | **1** |
| orphan count (DualDaemon / same-home stress) | **0** |
| merge-context `make check` (re-review) | **20/20** |
| Feature Gate A/B/C | PASS / PASS / PASS |
| Merge-context Gate A/B/C (re-review) | PASS / PASS / PASS |
| Gate D | UNPROVEN / NON-BLOCKING |
| Collateral `2be8e6b` UTC day window | UNRELATED BUT VALID |
| Collateral `e7e48ef` declarative timeouts | UNRELATED BUT VALID |
| Blocking issues | **none** |

---

## Merge Method

| Field | Value |
|-------|--------|
| Method | `git merge --no-ff fix/runtime-production-adapters` |
| Message | `merge: stabilize minimal forge run vertical slice` |
| Strategy | ort |
| Conflicts | **none** |
| Squash / rebase / cherry-pick | **not used** |
| First parent | `253c9d1fd68c818f50a60cfb3e115b624e42fde3` (pre-merge main) |
| Second parent | `f42134a673900b459d0ef3e59b13904eabcc72d2` (final feature SHA) |
| Worktree | temporary: `/var/folders/.../T/opencode/neuroforge-main-merge` |

Parents verification:

```
20991bf0a2e4792517632a8bffa1ac6c284ec149
  253c9d1fd68c818f50a60cfb3e115b624e42fde3
  f42134a673900b459d0ef3e59b13904eabcc72d2
```

Independent re-review artifact is included in the merge tree.

---

## Commands Executed

### Baseline (feature worktree)

| Command | Exit | Result |
|---------|------|--------|
| `git status --short` | 0 | allowed untracked reviews only |
| `git branch --show-current` | 0 | `fix/runtime-production-adapters` |
| `git rev-parse HEAD` | 0 | `e8cf5a75…` then `f42134a6…` after artifact commit |
| `git rev-parse main` | 0 | `253c9d1f…` |
| `git worktree list --porcelain` | 0 | main not checked out elsewhere |
| `git show-ref --heads` / `git remote -v` | 0 | expected refs; no network |

### Re-review artifact commit (feature)

| Command | Exit | Result |
|---------|------|--------|
| strip trailing whitespace (check hygiene only) | 0 | docs-only |
| `git add -- docs/reviews/MINIMAL_RUN_POST_ROLLBACK_REREVIEW.md` | 0 | |
| `git diff --cached --check` | 0 | |
| `git commit -m "docs(review): approve post-rollback minimal-run merge"` | 0 | single file |

### Pre-merge baseline (main worktree)

| Command | Exit | Duration | Result |
|---------|------|----------|--------|
| `git diff --check` | 0 | <1s | PASS |
| `gofmt -l .` | 0 | <1s | empty |
| `go vet ./...` | 0 | ~1s | PASS |
| `go test -count=1 ./...` | 0 | ~90s | PASS |

### Merge

| Command | Exit | Result |
|---------|------|--------|
| `git merge --no-ff fix/runtime-production-adapters -m "…"` | 0 | merge SHA `20991bf` |

---

## Gate A

Run on merge commit `20991bf`.

| Command | Exit | Duration | Result |
|---------|------|----------|--------|
| `git diff --check HEAD^1..HEAD` | 0 | <1s | PASS |
| `gofmt -l .` | 0 | <1s | empty |
| `go vet ./...` | 0 | ~1s | PASS |

**Gate A: PASS**

---

## Gate B

| Command | Exit | Duration | Result |
|---------|------|----------|--------|
| `go test -count=1 ./...` | 0 | ~142s | PASS |
| `go test -race -count=1 ./...` | 0 | ~150s | PASS |
| `GOFLAGS=-count=1 make check` | 0 | ~145s | PASS |

**Gate B: PASS**

---

## Gate C

### DualDaemon / different-home / isolation

| Command | Exit | Duration | Result |
|---------|------|----------|--------|
| `go test -count=20 ./internal/cli -run 'DualDaemon\|DifferentHome\|Isolation\|ForeignHome'` | 0 | ~189s | PASS |
| `go test -race -count=20 ./internal/cli -run 'DualDaemon\|DifferentHome\|Isolation\|ForeignHome'` | 0 | ~189s | PASS |

Interpretation (from independent re-review + green Gate C):

- max same-home daemon PID = **1**
- different homes may have global count **2**
- max per-home = **1**
- orphan count = **0**
- foreign-home daemon excluded by home-scoped sampler
- same-home overlap not hidden

### Reliability

| Command | Exit | Duration | Result |
|---------|------|----------|--------|
| `go test -count=3 ./internal/cli -run Reliability` | 0 | ~57s | PASS |
| `go test -race -count=3 ./internal/cli -run Reliability` | 0 | ~59s | PASS |

### BF-03 (restart / crash / late / repeat / SIGINT)

| Command | Exit | Duration | Result |
|---------|------|----------|--------|
| `go test -count=3 ./internal/cli -run 'Restart\|DaemonCrash\|LateTerminal\|RepeatedFinalization\|SIGINT'` | 0 | ~100s | PASS |
| `go test -race -count=3 ./internal/cli -run 'Restart\|DaemonCrash\|LateTerminal\|RepeatedFinalization\|SIGINT'` | 0 | ~102s | PASS |

### BF-07 (finalize recovery / concurrent / conflict)

| Command | Exit | Duration | Result |
|---------|------|----------|--------|
| `go test -count=10 ./internal/runapp -run 'Recovery\|Concurrent\|Conflict\|Finalize'` | 0 | ~60s | PASS |
| `go test -race -count=10 ./internal/runapp -run 'Recovery\|Concurrent\|Conflict\|Finalize'` | 0 | ~80s | PASS |

### Cancellation / process-tree

| Command | Exit | Duration | Result |
|---------|------|----------|--------|
| `go test -race -count=50 ./internal/adapter/codingagent/gemini` | 0 | ~58s | PASS |
| `go test -race -count=10 ./internal/adapter/codingagent/proctree` | 0 | ~4s | PASS |

### 20× merge-context `make check`

```sh
for i in $(seq 1 20); do
  GOFLAGS=-count=1 make check || exit 1
done
```

| Metric | Value |
|--------|--------|
| Iterations | 20 |
| PASS | **20** |
| FAIL | **0** |
| Typical duration | ~140–147s each |

**Gate C: PASS**

---

## Gate D

| Field | Value |
|-------|--------|
| `NEUROFORGE_SMOKE` | unset (not set by this agent) |
| Real OpenCode smoke | **not run** |
| Result | **UNPROVEN / NOT EXERCISED / NON-BLOCKING** |

Do not report PASS for a skipped smoke test.

---

## Process Isolation

| Metric | Value |
|--------|--------|
| max global daemon PID (diff-home isolation) | **2** |
| max same-home daemon PID | **1** |
| max per-home daemon PID | **1** |
| orphan count (DualDaemon / same-home stress) | **0** |
| Live `forge`/`cli.test` `daemon run` after gates | **0** |
| Stale pidfiles from prior reviews | DEAD (not this execution) |
| Cleanup actions this pass | none required (no owned live orphans) |
| Unrelated processes killed | **none** |

---

## Remaining Non-blocking Issues

1. **`TestWorkspaceRun_PromptFileReadSucceeds` may leave a `cli.test` daemon process**

   Full-suite runs can autostart a daemon via `os.Executable()` (`cli.test`)
   when the test assumes “no daemon” and never stops it. Classified as a
   **pre-existing non-blocking suite hygiene leak**, not a same-home DualDaemon
   regression:

   - Does not share the DualDaemon `forge-it-*/forge` binary identity path
   - DualDaemon / different-home orphan count remains **0**
   - Not introduced by post-rollback BF-F-01 commits
   - **Not fixed in this integration pass** (explicitly out of scope)

2. Sampler residual on spaced paths / macOS `/private` cwd (non-blocking; TempDir fixtures OK).

3. Symlink `--runtime-home` fail-closed vs real path (production Start uses absolute `dirs.Root`).

4. Theoretical sub-poll same-home miss; stress did not observe.

5. T4 residual (owner SIGKILL before healthy) unchanged; non-blocking.

---

## Merge Integrity

| Check | Status |
|-------|--------|
| Tracked working tree clean after gates | **yes** |
| Tests did not modify source files | **yes** |
| Re-review artifact present on main | **yes** |
| All feature commits reachable from main | **yes** (`f42134a` second parent) |
| Unexpected runtime files in repo | **none** |
| Result refs created in main repo by tests | **none** |
| Owned orphan daemons remain | **none** |
| `git fsck --no-dangling` | clean (no errors) |
| Remote refs changed | **no** |
| Rollback performed | **no** |

---

## Network Safety

This final integration pass performed **no** Git network operations
(`push` / `pull` / `fetch` / `clone` / `git ls-remote` / remote mutation).

An earlier historical coding pass (outside this final integration) ran
read-only `git ls-remote`; therefore the **full historical process** was not
entirely offline. This release/integration session itself stayed local-only.

---

## Final Result

# MERGED LOCALLY — READY TO PUSH

| Field | Value |
|-------|--------|
| Gate A | **PASS** |
| Gate B | **PASS** |
| Gate C | **PASS** |
| Gate D | **UNPROVEN / NON-BLOCKING** |
| Gate E | N/A (not defined as mandatory for this pass) |
| `make check` ×20 | **20/20 PASS** |
| Conflicts | none |
| Rollback | not performed |
| Final main HEAD (merge) | `20991bf0a2e4792517632a8bffa1ac6c284ec149` |
| Final report path | `docs/reviews/MINIMAL_RUN_FINAL_MERGE_REPORT.md` |
| Push | **no** |
| PR | **no** |
| fetch/pull/clone/ls-remote | **no** (this pass) |
| Remote mutation | **no** |

(End of final merge report.)
