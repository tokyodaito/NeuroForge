# MINIMAL_RUN_FINAL_REREVIEW

Independent final re-review of BF-F-01 / FR-2 / R-2.3 (`forge run` autostart dual-daemon).

Reviewer is **not** the implementer and does **not** trust prior coding-agent
reports, commit messages, test names, comments, or single green runs.

| | |
|--|--|
| Date (local) | 2026-07-26 |
| Branch | `fix/runtime-production-adapters` |
| Expected HEAD | `a6e92ae02993b0fc1d0fa4f4fe0d15228675c1cb` |
| Actual HEAD | `a6e92ae02993b0fc1d0fa4f4fe0d15228675c1cb` |
| Prior final review | `docs/reviews/MINIMAL_RUN_FINAL_REVIEW.md` (NOT READY @ `47a5769`) |
| Production code changed by this review | **No** |
| Commits / push / PR / merge by this review | **No** |
| Temporary review harness left in tree | **No** (deleted after use) |

---

## 1. Review Scope

Independent verification that BF-F-01 is closed enough for merge:

- Spec: `docs/stabilization/minimal-run/*`, ADR-0019, prior `MINIMAL_RUN_FINAL_REVIEW.md`
- Commits under review: `410c7b6` (fix), `a6e92ae` (tests)
- Code: lock, reclaim, lifecycle spawn/reap, bind claim, CLI ensureDaemon
- Reproduction: DualDaemon stress, owner-kill T4/T5, child-crash T6, direct double-start
- Gates A/B/C; Gate D not run

---

## 2. Baseline

### 2.1 Commands

```
git status --short
git branch --show-current
git rev-parse HEAD
git log --oneline --decorate -12
git show --stat --oneline 410c7b6
git show --stat --oneline a6e92ae
git diff --check
git diff --stat
git ls-files --others --exclude-standard
git diff --stat 47a5769..HEAD
git diff 47a5769..HEAD -- docs/spec/
```

### 2.2 Results

| Check | Result |
|-------|--------|
| Branch | `fix/runtime-production-adapters` — **MATCH** |
| HEAD | `a6e92ae02993b0fc1d0fa4f4fe0d15228675c1cb` — **MATCH** |
| Working tree production dirty | **No** |
| Untracked | only expected review docs: `M12_M13_REVIEW.md`, `MINIMAL_RUN_FINAL_REVIEW.md`, `MINIMAL_RUN_IMPLEMENTATION_REVIEW.md` (+ this file after write) |
| Spec / stabilization docs in fix commits | **unchanged** (only `internal/daemon/*`, `internal/cli/run_dualdaemon_test.go`) |
| `docs/spec/NEUROFORGE_SPEC.md` | **empty diff** vs prior baseline |
| Protocol freeze | **not touched** |

**Baseline finding:** none. Branch/HEAD match; no specification edits for PASS; commits scoped to autostart fix + tests.

---

## 3. Specification Interpretation

### 3.1 Exact text (authoritative)

**FR-2** (REQUIREMENTS.md):

> `forge run` connects to a running daemon if healthy; otherwise spawns one,
> waits for readiness, and never spawns a second. Stale PID files are reclaimed.

**R-2.3** (REQUIREMENTS.md §3):

> `forge run` never creates a second daemon process when one is
> **already healthy**. (A guard — e.g. file lock or health re-check after spawn —
> must close the two-CLIs-race window.)

**B-11** (TEST_PLAN.md):

> launch two `forge run` concurrently from the same cold home.
> **Pass:** exactly one daemon process owns the pidfile; both runs either
> reuse it or one starts and one reuses.

**P-12** (REVIEW_CHECKLIST.md):

> From a cold home, run **two** `forge run` concurrently. After they
> finish, assert exactly **one** daemon pid owns the pidfile and there
> is exactly one daemon process.

### 3.2 What is required

| Scenario | Required invariant | Source |
|----------|-------------------|--------|
| Concurrent cooperative cold start (B-11 / BF-F-01 original) | Max **one** daemon process for the home; one pidfile owner | FR-2, R-2.3, B-11, P-12 |
| Second CLI while daemon **already healthy** | Must not spawn another process | R-2.3 explicit |
| Direct double `daemon run` | One serving owner; loser exits without clobber | daemon bind.lock design + DirectDaemon test |
| Owner CLI **SIGKILL** after spawn, before child healthy | **Not** named as a B-11 case. R-2.3 conditions “when already healthy”. Transient second child while first is not healthy is a **residual** outside the closed B-11 window | R-2.3 wording |

### 3.3 Residual T4/T5 blocking rule (this review)

- If the **only** remaining dual-process window is owner-kill **before** the first child is healthy, and settle leaves **one** serving owner and **zero** orphans, that is **non-blocking** under R-2.3’s “already healthy” clause.
- If cooperative concurrent cold start still produces max concurrent daemon PID > 1 → **blocking FAIL**.
- Absolute reading of FR-2 (“never spawns a second” with no qualifier) would treat T4 as FAIL; this review privileges the **detailed** R-2.3 + B-11 definition that originally blocked merge (prior final review BF-F-01).

### 3.4 Gate D

Per TEST_PLAN §6 / REVIEW_CHECKLIST Gate D: opt-in, non-blocking if not exercised → **UNPROVEN**.

---

## 4. Root Cause Verification

### 4.1 Before (`410c7b6^` `lockFile`)

```go
for {
    if err := flockTry(f.Fd()); err == nil {
        return unlockFn, nil
    }
    if !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EWOULDBLOCK) {
        // no-op lock
        return func() {}, nil
    }
    // retry ...
}
```

In Go, `if err := flockTry(...); err == nil` scopes a **new** `err` to the if.
The following `errors.Is(err, …)` uses the **outer** `err` from `os.OpenFile`, which is **nil** after a successful open.

Therefore on the first `EAGAIN`:

1. inner `err` is EAGAIN (not taken as success),
2. outer `err` is nil → `!errors.Is(nil, EAGAIN)` is true → **no-op lock returned immediately**,
3. retry loop never runs.

**Proven:** both `autostart.lock` and `bind.lock` shared `lockFile` → both were no-ops under contention. Matches prior dual-daemon / token-clobber symptoms.

### 4.2 After (`410c7b6` / HEAD)

```go
lerr := flockTry(f.Fd())
if lerr == nil { return real unlock }
if !errors.Is(lerr, syscall.EAGAIN) && !errors.Is(lerr, syscall.EWOULDBLOCK) {
    // no-op only for non-retryable / unsupported flock
    return func() {}, nil
}
// deadline / ctx / 40ms backoff retry
```

| Property | Status |
|----------|--------|
| Retry uses real `flockTry` result | **Yes** (`lerr`) |
| EAGAIN → wait/retry | **Yes** |
| Unexpected errors → no-op (not silent success on EAGAIN) | **Yes** |
| Unlock: `LOCK_UN` + `Close` | **Yes** |
| FD closed on timeout/cancel/error paths | **Yes** |
| Context / deadline | **Yes** (ctx.Done + deadline, default 15s) |
| Busy-loop | **No** (40ms sleep) |
| Unix | `syscall.Flock(LOCK_EX\|LOCK_NB)` in `autostart_lock_unix.go` |
| Non-Unix | `flockTry` returns “not supported” → no-op; contract relies on health re-check |

Unit proof: `TestLockFile_ActuallySerializes`, `TestLockFile_ConcurrentAcquirersSerialize` (×20 + race green).

---

## 5. Reconstructed Startup Protocol

| Step | File / function | Owner process | Lock | Crash point / cleanup | Orphan risk |
|------|-----------------|---------------|------|----------------------|-------------|
| 1. `forge run` | `internal/cli/run_cmd.go` | CLI | — | — | — |
| 2. `ensureDaemon` | `daemon_connect.go` `ensureDaemonDirs` | CLI | — | — | — |
| 3. Fast `Connect` | `daemon.Connect` | CLI | none | if healthy → return | none |
| 4. `LockAutostart` | `autostart_lock.go` → `lockFile` on `autostart.lock` | CLI | **autostart flock** | held via `defer unlock` until end of ensure | if CLI SIGKILL: OS drops flock |
| 5. Health re-check | `connectRetried` | CLI | held | reuse if other CLI finished start | none |
| 6. `daemon.Start` | `lifecycle.go` | CLI (under lock) | held | — | — |
| 7. Health retry | `isReachableAndHealthyRetried` | CLI | held | ErrAlreadyRunning | none |
| 8. `reclaimStaleRuntime` | `runtime.go` | CLI | held | only dead/corrupt PID | must not touch live PID |
| 9. Spawn `forge daemon run` | `lifecycle.Start` + `detach` (Setsid) | CLI → child | held | child detached | parent kill leaves child |
| 10. Pre-spawn health again | Start | CLI | held | bail if healthy | none |
| 11. `waitForReady` | Start | CLI | **still held** | on fail: **Kill+Wait child** (I4) then return | fixed vs prior orphan-on-timeout |
| 12. `Release` child | Start success only | CLI | held | intentional detach | none if ready |
| 13. Daemon `Run` early health | `daemon.go` | child | — | exit if other healthy | loser exits |
| 14. `bind.lock` + re-check | `daemon.go` `lockFile` | child | **bind flock** | loser exits without bind | second line of defence |
| 15. Listen + `writeRuntimeFiles` | daemon.go | child | bind held then released | metadata PID/token/addr | clobber prevented by bind lock |
| 16. Parent Connect | ensureDaemonDirs | CLI | held until return | then unlock | waiting CLIs re-check |
| 17. Waiting CLIs | ensureDaemonDirs | other CLIs | acquire after unlock | Connect reuse | none if single owner |

### Lock held until readiness?

| Path | Autostart lock held through readiness? |
|------|----------------------------------------|
| Success | **Yes** (`defer unlock` after Start+Connect) |
| Child exit / ready fail | **Yes** until Start returns (after Kill+Wait) |
| Context cancel during wait | **Yes** until return |
| Parent **SIGKILL** | **No** — OS releases flock; child may still be pre-ready → **T4 residual** |
| Direct `daemon run` | N/A (bind.lock only) |
| Stale reclaim | Under lock inside Start |

---

## 6. Commands Executed

| Command | Exit | Duration | Notes |
|---------|------|----------|-------|
| Baseline git suite | 0 | <1s | HEAD match |
| `go test -count=10 ./internal/cli -run DualDaemon` | 0 | ~64s | PASS |
| `go test -race -count=10 ./internal/cli -run DualDaemon` | 0 | ~64s | PASS |
| `for i in 1..100; go test -count=1 … DualDaemonRace` | 0 | **795s** | **100/100** |
| `go test -count=20 ./internal/cli -run 'DualDaemon\|Autostart\|ColdStart\|DirectDaemonDoubleStart'` | 0 | ~245s | PASS |
| same `-race -count=20` | 0 | ~248s | PASS |
| `go test -count=20 ./internal/daemon -run 'LockFile\|ReclaimStale\|…'` | 0 | ~13s | PASS |
| same `-race -count=20` | 0 | ~14s | PASS |
| `go test -count=50 ./internal/daemon -run 'SingleInstance\|Start\|Runtime\|Lock\|Cleanup\|Reclaim'` | 0 | ~79s | PASS |
| same `-race -count=50` | 0 | ~87s | PASS |
| `go test -race -count=20 ./internal/cli -run DirectDaemonDoubleStart` | 0 | ~15s | PASS |
| Owner-kill T4/T5/T6 (temp `ownerkill_review_test.go`, deleted) | 0 | ~5s | see §9 |
| `go test -count=5 ./internal/cli -run 'Restart\|DaemonCrash\|…'` | 0 | ~258s | PASS |
| same `-race -count=5` | 0 | ~260s | PASS |
| `go test -count=10 ./internal/runapp -run 'Recovery\|Concurrent\|Conflict\|Finalize'` | 0 | ~67s | PASS |
| same `-race -count=10` | 0 | ~85s | PASS |
| `gofmt -l .` | 0 | empty | Gate A |
| `go vet ./...` | 0 | — | Gate A |
| `git diff --check` | 0 | — | Gate A |
| `go test -count=1 ./...` | 0 | ~145s | Gate B |
| `go test -race -count=1 ./...` | 0 | ~143s | Gate B |
| `make check` | 0 | ~139s | Gate B |
| `go test -race -count=1 -run Reliability ./internal/cli` | 0 | ~22s | Gate C |
| `go test -race -count=3 -run Reliability ./internal/cli` | 0 | ~60s | Gate C ×3 |
| Gate D smoke | — | — | **not run** |
| `git ls-remote` | — | — | **not run** (see §13) |

---

## 7. Exact Reproduction Results

### 7.1 Original BF-F-01 command

```
go test -count=10 ./internal/cli -run DualDaemon   → PASS (exit 0)
go test -race -count=10 ./internal/cli -run DualDaemon → PASS (exit 0)
```

Prior final review: same command **FAIL**ed with dual PIDs / 401 token clobber.

### 7.2 Stress

```
100× TestForgeRun_DualDaemonRace count=1 → 100/100 PASS (~795s)
20× DualDaemon|Autostart|ColdStart|DirectDaemonDoubleStart ± race → PASS
```

**Normal concurrent cold start: max concurrent daemon PID = 1** (asserted by test process-table sampler + pidfile uniqueness).

---

## 8. Process Timeline Evidence

| Scenario | Observed PIDs | Max Concurrent | Orphans (after settle) | Result |
|----------|---------------|----------------|------------------------|--------|
| B-11 DualDaemon 8 clients ×3 repeats ×100 loops | single pidfile owner per home | **1** (test-enforced) | **0** | **PASS** |
| DirectDaemonDoubleStart ×20 + race | winner only after loser exit | **1** serving | **0** | **PASS** |
| T4 owner unlock after spawn, child delayed 2.5s pre-bind | childA + childB | **2** transient | **0** (1 live daemon) | **RESIDUAL** (non-blocking) |
| T5 owner unlock after bind+healthy | single | **1** | **0**; no metadata clobber | **PASS** |
| T6 crash child then real start | crash reaped + one owner | **1** | **0** | **PASS** |

T4 independent harness result (deleted after run):

```
max_concurrent=2
all=[21961 21967]
liveDaemons=[21967]
owner=21967
spawnedB=true
orphans=0
status=running
```

T5:

```
max_concurrent=1
spawnedB=false
clobber=false
pid_before == pid_after
```

---

## 9. Owner-Kill T4/T5 Verification

### 9.1 Method

Temporary `internal/daemon/ownerkill_review_test.go` (not committed; removed after).
Used real `LockAutostart`, delayed child binary (`sleep` then `exec` real `forge daemon run`), process-table sampling @ 15ms, SIGKILL-equivalent of lock holder = **drop flock without reaping detached child** (same OS effect as CLI SIGKILL on flock; child already Setsid-detached).

### 9.2 T4 — kill/drop owner after spawn, before bind

| Check | Result |
|-------|--------|
| Simultaneous live daemon-ish PIDs | **2** |
| CLI B spawns second child | **Yes** (`spawnedB=true`) — Start/health not ready |
| Serving owner after settle | **1** (bind.lock + health re-check) |
| Metadata | single owner PID/addr |
| Orphans | **0** |
| Automated regression in tree for T4 | **No** |

### 9.3 T5 — after bind / healthy

| Check | Result |
|-------|--------|
| B spawns second | **No** (reuses healthy) |
| Clobber pid/addr/token | **No** |
| Max concurrent | **1** |

### 9.4 T6 — child crash before ready

Parent Wait/reap pattern + subsequent real start → running, orphans 0. Production `Start` Kill+Wait on `waitForReady` failure matches I4.

### 9.5 Verdict on residual

- Coding-agent residual claim is **true**: T4 can create a **transient second process**.
- Under §3.3 interpretation → **non-blocking**.
- Not covered by DualDaemon tests (they keep parent alive until ready).
- Does **not** re-open B-11 cooperative dual-daemon failure.

---

## 10. reclaimStaleRuntime Audit

Code: `internal/daemon/runtime.go` `reclaimStaleRuntime`.

| Case | Behavior | Evidence |
|------|----------|----------|
| Dead PID | reclaim | unit + R-2.4 |
| Malformed PID | reclaim | unit R-2.5 |
| Missing PID file | clean (no-op removes) | unit NoPIDFileIsNoop |
| Stale socket + dead PID | reclaim | via cleanRuntimeFiles |
| Live PID current daemon | **preserve** | unit LivePIDIsPreserved |
| Live unrelated / reused PID | **preserve** (conservative) | unit ReusedPIDIsPreserved |
| Daemon starting, health not ready, PID written | **preserve** | design I2 |
| Ready + transient health fail | **preserve** (live PID) | design; Start still may spawn if not StatusRunning — residual with T4 |
| Partial metadata + live PID | preserve | live path |
| Stale token + dead PID | reclaim | dead path |
| Active startup lock owner | lock serializes Start; reclaim under lock | protocol |

**Not proven by unit matrix:** “active startup lock owner” as a distinct reclaim input (lock is orthogonal).
**Gap (non-blocking):** `Start` still **spawns** after preserving a live-but-unhealthy PID (does not treat live PID as ErrAlreadyRunning unless `/healthz` is StatusRunning). That is exactly the T4 second-spawn mechanism; bind.lock is the backstop.

Idempotent: repeated reclaim on clean/live/dead is safe (unit).

---

## 11. Test Quality Audit

### 11.1 `TestForgeRun_DualDaemonRace` (HEAD)

| Probe | Result |
|-------|--------|
| Clients start via barrier | **Yes** (`startGate`) |
| Sampler starts before spawn | **Yes** (goroutine before `close(startGate)`) |
| Sampling interval | **3ms** — short dual could theoretically be missed; under real spawn, dual lives >>3ms; combined with 100× stress → practical confidence high, not formal zero-gap proof |
| Not only pidfile | **Yes** (`daemonProcCount` / pgrep + maxConcurrent) |
| Process match | `pgrep -f bin+" daemon run"` — fixture bin path unique |
| Cleanup before assert | sampler stopped, then asserts; fixture cleanup after |
| Race mode | **not skipped**; race DualDaemon green |
| Fixed-sleep-only proof | **No** — barrier + sampling + post conditions |
| Loser filtered out | **No** — max concurrent fails if >1 |
| Orphans after | `daemonProcCount > 1` fails |

### 11.2 Lock / reclaim / DirectDaemon tests

Strong direct regression for root cause and I2. DirectDaemon covers bind-side single owner after one is healthy.

### 11.3 Gaps

- No committed T4 owner-SIGKILL test.
- `daemonProcCount` returns 0 on Windows (pidfile path remains).
- Sampler 3ms → claim “max concurrent PID=1” is **strongly evidenced**, not mathematically continuous.

---

## 12. Previous Fix Regression Check

| Suite | Result |
|-------|--------|
| cli Restart / DaemonCrash / LateTerminal / RepeatedFinalization / Reliability / SIGINT ×5 ± race | **PASS** |
| runapp Recovery / Concurrent / Conflict / Finalize ×10 ± race | **PASS** |
| Reliability Gate C ×1 and ×3 under race | **PASS** |

No regression of BF-03 / BF-07 surfaces observed.

---

## 13. Safety / Network Process Violation

### 13.1 Production LOCAL_REVIEW

No change in this fix pair to git network paths. FR-19 wall unchanged from prior final review (allowlist + no push/fetch/ls-remote in run path).

### 13.2 Coding-agent `git ls-remote`

Prior process guidance included **no Git network operations**. Coding-agent reported running `git ls-remote`.

| Question | Answer |
|----------|--------|
| Scope of ban | Spec FR-19 / I.10 bind **production** `forge run` / LOCAL_REVIEW. Review process also asked for no network git ops as safety discipline. |
| Did this reviewer run `git ls-remote`? | **No** |
| Remote state changed by this review? | **No** |
| Credentials / side effects observed here? | **None** (not executed) |
| Impact on BF-F-01 technical evidence? | **None** |
| Process note | Coding-agent **did** perform a Git network operation; do **not** claim a clean “no Git network operations” process history for the **implementer**. This re-review did not repeat it. |

---

## 14. Gate Results

| Gate | Status | Evidence |
|------|--------|----------|
| **A** Static | **PASS** | gofmt empty, vet 0, diff --check 0 |
| **B** Tests + race | **PASS** | `go test -count=1 ./...`, `-race -count=1 ./...`, `make check` all 0 |
| **C** Reliability | **PASS** | race Reliability ×1 and ×3 |
| **D** Real OpenCode smoke | **UNPROVEN** | not run; **non-blocking** |
| **E** This re-review | **ACCEPT** (with residual T4 documented) | |

---

## 15. Blocking Findings

**None** for BF-F-01 / FR-2 cooperative autostart (B-11).

Prior blocker BF-F-01 (dual daemon on concurrent cold start) is **not reproducible** under independent stress (100/100 DualDaemonRace, race suites green).

---

## 16. Non-blocking Findings

| ID | Note |
|----|------|
| NB-R1 | **T4 residual:** if autostart lock holder dies after spawn and before child is healthy, a second child can be spawned; bind.lock → one serving owner; orphans 0 after settle. No committed regression test. |
| NB-R2 | `Start` does not refuse spawn solely because a live PID owns the pidfile without StatusRunning — relies on health + bind.lock. |
| NB-R3 | DualDaemon sampler 3ms interval — practical not formal continuous coverage. |
| NB-R4 | Non-Unix flock is no-op (pre-existing support contract). |
| NB-R5 | `ACCEPTANCE_MATRIX.md` still all NOT IMPLEMENTED on disk (process hygiene). |
| NB-R6 | Implementer `git ls-remote` process violation (see §13); not a product defect. |
| NB-R7 | Prior soft cancel-restart assertion gap (NB-01 from previous review) unchanged; out of BF-F-01 scope. |

---

## 17. Final Verdict

# **READY FOR MERGE**

### Summary table

| Item | Value |
|------|--------|
| Verdict | **READY FOR MERGE** |
| BF-F-01 | **PASS** (B-11 concurrent cold-start dual-daemon closed) |
| FR-2 / R-2.3 | **PASS** for cooperative path; residual T4 non-blocking per R-2.3 “already healthy” |
| Gate A | **PASS** |
| Gate B | **PASS** |
| Gate C | **PASS** |
| Gate D | **UNPROVEN (non-blocking)** |
| Gate E | **ACCEPT** |
| Max concurrent daemon PID (normal cold start) | **1** |
| Max concurrent daemon PID (owner-kill before readiness) | **2** transient |
| Orphan count after settle (all exercised scenarios) | **0** |
| Blocking issues | **none** |
| Production code modified by reviewer | **No** |
| Commits / push / PR / merge by reviewer | **No** |
| Residual T4/T5 blocks merge? | **No** (T5 clean; T4 residual non-blocking) |
| git ls-remote | Implementer did network git op; **this review did not** |

### Why READY (vs prior NOT READY)

Prior final review failed solely on BF-F-01 dual-daemon under concurrent cold start. Root cause (lock variable shadowing) is real and fixed; locks serialize; reclaim is non-destructive; ready-fail reaps child; independent DualDaemon stress is green at scale; bind-side double-start is safe; regressions hold.

---

## Appendix — Code anchors

- Lock fix: `internal/daemon/autostart_lock.go` `lockFile`
- Unix flock: `internal/daemon/autostart_lock_unix.go`
- Reclaim: `internal/daemon/runtime.go` `reclaimStaleRuntime`
- Spawn/reap: `internal/daemon/lifecycle.go` `Start`
- CLI hold lock across start: `internal/cli/daemon_connect.go` `ensureDaemonDirs`
- Bind claim: `internal/daemon/daemon.go` bind.lock + `writeRuntimeFiles`
- Tests: `internal/cli/run_dualdaemon_test.go`, `internal/daemon/autostart_lock_test.go`, `internal/daemon/reclaim_test.go`

(End of independent final re-review.)
