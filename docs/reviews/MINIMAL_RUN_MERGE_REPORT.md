# MINIMAL_RUN_MERGE_REPORT

Local integration verification for minimal `forge run` vertical slice.

| | |
|--|--|
| Date (local) | 2026-07-26 |
| Integration agent | release/integration (local only) |
| Final result | **MERGE ROLLED BACK** |

---

## Integration Baseline

| Item | Value |
|------|--------|
| Feature branch | `fix/runtime-production-adapters` |
| Feature SHA (pre-review commit) | `a6e92ae02993b0fc1d0fa4f4fe0d15228675c1cb` |
| Feature review-artifact commit | `904cf77915b37c0b180e7b9c926239eadbb1580b` |
| Integration branch | `main` |
| Pre-merge SHA | `253c9d1fd68c818f50a60cfb3e115b624e42fde3` |
| Merge SHA (ephemeral; rolled back) | `3f6677232d8daf00be4dc2cc3a49901ec907cd9d` |
| Post-rollback `main` HEAD | `253c9d1fd68c818f50a60cfb3e115b624e42fde3` |

Integration branch selection evidence (no network):

- local branch `main` exists
- `refs/remotes/origin/HEAD` → `origin/main`
- `init.defaultBranch` = `main`
- no local `master`

---

## Review Approval

| Item | Value |
|------|--------|
| Final re-review artifact | `docs/reviews/MINIMAL_RUN_FINAL_REREVIEW.md` |
| Review commit | `904cf77915b37c0b180e7b9c926239eadbb1580b` |
| Verdict | **READY FOR MERGE** |
| BF-F-01 | **PASS** |
| Gate A (review) | PASS |
| Gate B (review) | PASS |
| Gate C (review) | PASS |
| Gate D (review) | UNPROVEN (non-blocking) |
| Gate E (review) | ACCEPT |
| Residual T4/T5 | Documented non-blocking (T4 transient dual under owner-kill before healthy) |
| Production code changed by reviewer | No |

Note: trailing whitespace on one markdown line in the re-review file was stripped before commit so `git diff --cached --check` would pass. Content/verdict unchanged.

---

## Merge Method

| Item | Value |
|------|--------|
| Method | `git merge --no-ff fix/runtime-production-adapters` |
| Message | `merge: stabilize minimal forge run vertical slice` |
| Conflicts | **none** |
| Squash / rebase / cherry-pick | **not used** |
| Worktree | temporary: `/var/folders/7g/krzt39vs5pq1rmmvql2jrs4m0000gn/T/opencode/neuroforge-merge-84737` |
| Parent 1 (main) | `253c9d1fd68c818f50a60cfb3e115b624e42fde3` |
| Parent 2 (feature) | `904cf77915b37c0b180e7b9c926239eadbb1580b` |
| Merge commit parents verified | `git rev-list --parents -n 1` → both parents present |

Pre-merge baseline on `main`:

```
go test -count=1 ./...  → exit 0 (~baseline green)
```

---

## Commands Executed

| Command | Exit | Duration | Result |
|---------|------|----------|--------|
| Baseline git suite (status/branch/HEAD/log/worktree/remote) | 0 | <1s | feature HEAD match `a6e92ae…`; clean production tree |
| `git add` + `git commit` review artifact | 0 | <1s | `904cf77` single file |
| `git worktree add <tmp> main` | 0 | <1s | pre-merge `253c9d1` |
| `go test -count=1 ./...` (pre-merge main) | 0 | ~ok | baseline green |
| `git merge --no-ff fix/runtime-production-adapters` | 0 | <1s | merge `3f66772`, no conflicts |
| `git diff --check HEAD^1..HEAD` | 0 | <1s | Gate A |
| `gofmt -l .` | 0 | <1s | empty output |
| `go vet ./...` | 0 | ~1s | Gate A |
| `go test -count=1 ./...` (merge commit) | 0 | ~140s | Gate B part 1 PASS |
| `go test -race -count=1 ./...` (merge commit) | 0 | ~147s | Gate B part 2 PASS |
| `make check` (merge commit) | **2** | ~141s | **FAIL** — DualDaemonRace |
| Gate C DualDaemon / Reliability / Restart / runapp / gemini / proctree | — | — | **NOT RUN** (stopped after Gate B fail) |
| Gate D `NEUROFORGE_SMOKE=opencode` smoke | — | — | **NOT EXERCISED** (`NEUROFORGE_SMOKE` unset) |
| `git reset --hard 253c9d1…` (integration worktree) | 0 | <1s | merge rolled back |
| `git worktree remove` + `prune` | 0 | <1s | temp worktree removed |

### Gate B failure detail (`make check`)

```
--- FAIL: TestForgeRun_DualDaemonRace (5.86s)
    --- FAIL: TestForgeRun_DualDaemonRace/repeat#01 (1.90s)
        run_dualdaemon_test.go:135: daemon log: 1 'daemon starting' events
        run_dualdaemon_test.go:135: pid file: 99887
        run_dualdaemon_test.go:135: addr file: http://127.0.0.1:53089
        run_dualdaemon_test.go:136: repeat 1: process table saw 2 concurrent
            daemon processes (want <= 1) — transient dual daemon
FAIL
FAIL neuroforge/internal/cli 135.050s
make: *** [test] Error 1
```

Observations at failure:

- pidfile owner single (`99887`)
- daemon log reports **1** `'daemon starting'` event
- process-table sampler reported max concurrent daemon PIDs = **2**
- prior same-merge `go test -count=1 ./...` and `-race -count=1 ./...` had passed

Evidence preserved under `/tmp/neuroforge-merge-evidence/` (local temp; not committed).

No code was modified on `main` to “fix” the failure.

---

## Gate Results

| Gate | Status | Notes |
|------|--------|-------|
| **A** Static | **PASS** | `git diff --check`, `gofmt -l .` empty, `go vet ./...` |
| **B** Tests + race + make check | **FAIL** | full `./...` and `-race ./...` PASS; `make check` FAIL on DualDaemonRace |
| **C** Reliability / dual-daemon stress | **NOT RUN** | blocked by Gate B failure policy |
| **D** Real OpenCode smoke | **UNPROVEN / NOT EXERCISED** | `NEUROFORGE_SMOKE` unset; no credentials injected; non-blocking |
| **E** Final re-review | **ACCEPT** (pre-existing artifact) | does not override integration Gate B fail |

---

## Merge Integrity

| Check | Result |
|-------|--------|
| Working tree after rollback | clean on `main` at pre-merge SHA |
| Final review artifact on feature | present at `904cf77` |
| Merge retained on `main` | **No** — hard reset to `253c9d1` |
| Feature branch retained | **Yes** `fix/runtime-production-adapters` @ `904cf77` |
| Stabilization / review commits on feature | retained |
| Unexpected runtime files committed | No (merge never published) |
| Orphan test daemon processes after cleanup | None observed (`pgrep` empty) |
| User worktrees | not touched |
| Temp integration worktree | removed |

---

## Network Safety

| Claim | Status |
|-------|--------|
| This integration pass performed Git network ops (`fetch`/`pull`/`push`/`clone`/`ls-remote`) | **No** |
| Remote refs mutated | **No** |
| PR created | **No** |
| Prior coding-agent ran read-only `git ls-remote` | **Yes** (documented in final re-review §13) |
| Entire historical process was fully offline | **Cannot claim** — implementer network git op exists in history |

---

## Rollback

| Item | Value |
|------|--------|
| Reason | Mandatory Gate B (`make check`) failed on merge commit |
| Action | `git reset --hard 253c9d1fd68c818f50a60cfb3e115b624e42fde3` in temp integration worktree only |
| Feature branch / review commit deleted | **No** |
| Code fixes attempted on integration branch | **No** |

---

## Final Result

# **MERGE ROLLED BACK**

`main` remains at `253c9d1fd68c818f50a60cfb3e115b624e42fde3`.

Feature branch `fix/runtime-production-adapters` remains at
`904cf77915b37c0b180e7b9c926239eadbb1580b` with final re-review approval artifact.

Re-attempt merge only after DualDaemonRace is stable under `make check` on the
feature tip (or after a focused flake investigation). Do not treat review Gate B
PASS alone as sufficient if integration `make check` fails.

---

## Post-Rollback BF-F-01 Investigation

| | |
|--|--|
| Date (local) | 2026-07-26 |
| Agent | concurrency/release-fix (local only) |
| Classification | **VARIANT B — CROSS-TEST PROCESS ATTRIBUTION** |
| Feature tip after fix | `e7e48efa4bfadd1fcd7a189ecb4a04b6ae754607` |
| `main` after investigation | `253c9d1fd68c818f50a60cfb3e115b624e42fde3` (unchanged; no publish) |
| Ephemeral verify merge SHA | `47e2dfbf585e970cad74b208411ec4fbe95e0b40` (worktree only; rolled back) |
| Final status | **READY FOR INDEPENDENT RE-REVIEW** |

### True root cause

`TestForgeRun_DualDaemonRace` sampled the OS process table with:

```text
pgrep -f <package-forge-binary> + " daemon run"
```

All `internal/cli` integration tests share one `forgeBinary` path. A live
daemon for **another temp home** (prior subtest/test cleanup lag, or any
concurrent same-binary daemon) inflated `maxConcurrentDaemons` even when the
home under test had exactly one owner.

This matches the original Gate B evidence exactly:

| Probe | Original failure | Interpretation |
|-------|------------------|----------------|
| `'daemon starting'` log events | **1** | single `daemon.Start` for the home |
| pidfile owner | **single** | single claimed owner |
| process-table max | **2** | binary-global count, not same-home |

**Not** Variant A (same-runtime dual): a real same-home double spawn would
append a second `--- daemon starting ---` line and/or flip the pidfile.

### Process timeline (diagnostic reproduction)

Isolated harness: foreign daemon on `home-A`, DualDaemon-shaped 8-client burst
on `home-B`, same forge binary, sampler attributes via `NEUROFORGE_HOME`:

| Time | PID | Runtime Home | Global N | Home-A N | Home-B N |
|------|-----|--------------|----------|----------|----------|
| t0 | — | — | 0 | 0 | 0 |
| t1 | 3099 | home-A (foreign contaminant) | 1 | 1 | 0 |
| t2 | 3099, 3199 | home-A + home-B | **2** | 1 | 1 |
| t3… | same | same | 2 | 1 | 1 |

Result: `max_global=2`, `max_A=1`, `max_B=1`. Old sampler → false FAIL.
Home-scoped sampler → PASS. Same-home invariant holds.

### Fix (does not hide same-home overlap)

1. **Production identity:** `daemon.Start` spawns
   `forge daemon run --runtime-home <dirs.Root>` so argv attributes a PID to
   exactly one home. `daemon run` validates the flag against resolved home.
2. **Test sampler:** `daemonProcCount(bin, home)` counts only PIDs whose
   `--runtime-home` / `NEUROFORGE_HOME` / cwd equals `home`.
3. **Regressions:**
   - `TestForgeRun_DaemonProcCount_DifferentHomesIsolation` (R2)
   - `TestForgeRun_DaemonProcCount_ForeignHomeNotCounted` (R3 / merge shape)
4. Same-home DualDaemonRace still fails if `maxConcurrentDaemons > 1` **for
   that home** — assertion ceiling remains 1; only foreign homes are excluded.
5. **Collateral (make-check stability, not BF-F-01):**
   - demo usage seeds clamped into UTC day window (midnight empty-window flake)
   - declarative adapter test timeouts 5s/4s → 30s under full-suite load

### Files changed

| Commit | Files |
|--------|--------|
| `e4f812e` | `internal/daemon/lifecycle.go`, `internal/cli/daemon_cmd.go`, `internal/cli/run_dualdaemon_test.go` |
| `2be8e6b` | `internal/cli/usage_cost_cmd.go`, `internal/tui/m6_snapshot.go` |
| `e7e48ef` | `internal/adapter/codingagent/declarative/adapter_test.go` |

### Merge-context evidence (post-fix)

Ephemeral worktree from `main` @ `253c9d1` + `git merge --no-ff fix/runtime-production-adapters`
→ `47e2dfb` (local only; branch `main` force-reset back to `253c9d1` after).

| Command | Exit | Notes |
|---------|------|--------|
| `go test -count=1 ./...` × **20** | **0/0 fails** | ~140–145s each; no cache |
| `go test -race -count=1 ./...` | **0** | full race |
| `go test -count=20 ./internal/cli -run TestForgeRun_DualDaemonRace$` | **0** | merge tree |
| `go test -race -count=20 ./internal/cli -run TestForgeRun_DualDaemonRace$` | **0** | merge tree |
| `GOFLAGS=-count=1 make check` | **0** | fmt + vet + full tests |

Feature-branch stress (pre-merge tip):

| Command | Exit |
|---------|------|
| `go test -count=50 ./internal/cli -run TestForgeRun_DualDaemonRace$` | 0 |
| `go test -race -count=50 ./internal/cli -run 'DualDaemon\|DifferentHome\|Isolation\|ForeignHome\|DirectDaemon'` | 0 |
| `go test -count=50 ./internal/cli -run 'OwnerKill\|ChildCrash\|ColdStart\|Autostart\|StaleAutostart'` | 0 |
| `go test -count=1 ./...` + `-race -count=1 ./...` | 0 |

### Remaining risks

| Risk | Status |
|------|--------|
| T4 owner-SIGKILL before readiness → transient second same-home process | **Unchanged residual** (documented non-blocking in final re-review §9); not the Gate B failure mode |
| Sampler 3ms poll gap | Unchanged; practical under real spawn |
| Windows process-table sampling | Still disabled (`daemonProcCount` → 0); pidfile path remains |
| Generation/nonce handshake | **Not required** for this failure (Variant B); defer unless T4 is promoted to blocking |

### Safety

- No push / PR / fetch / pull / clone / ls-remote
- No remote mutation
- `main` left at pre-merge `253c9d1`
- Unrelated review docs (`M12_M13_REVIEW.md`, etc.) **not** committed
- Ephemeral merge worktree removed after verification

(End of post-rollback investigation.)

(End of local merge verification report.)
