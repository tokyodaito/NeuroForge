# MINIMAL_RUN_POST_ROLLBACK_REREVIEW

Independent post-rollback re-review of DualDaemon / BF-F-01 fixes.
Does **not** trust prior coding-agent classification, test names, commit
messages, or claimed PASS. Evidence is specification, code, independently
reproduced process observation, and merge-context test runs.

| | |
|--|--|
| Date (local) | 2026-07-26 |
| Reviewer role | independent re-reviewer (no production fixes) |
| Feature branch | `fix/runtime-production-adapters` |
| Feature HEAD (verified) | `e8cf5a75c07a821e21e3027263be54cf8a6c969f` |
| Main (verified, unchanged at end) | `253c9d1fd68c818f50a60cfb3e115b624e42fde3` |
| Ephemeral merge SHA | `80ee2304b52febad165dd818ac1d77e64ef02083` |
| Root cause classification | **VARIANT B — CROSS-TEST PROCESS ATTRIBUTION** |
| Final verdict | **READY FOR MERGE** |

---

## Review Scope

Post-rollback commits under review:

| SHA | Subject |
|-----|---------|
| `e4f812e` | fix(autostart): scope daemon process sampler by runtime home (BF-F-01) |
| `2be8e6b` | fix(cli,tui): keep demo usage timestamps inside UTC day window |
| `e7e48ef` | test(declarative): widen run timeouts under full-suite load |
| `e8cf5a7` | docs(release): post-rollback BF-F-01 Variant B investigation |

Prior merge failure context: `docs/reviews/MINIMAL_RUN_MERGE_REPORT.md`
(Gate B `make check` DualDaemonRace: 1 start event, 1 pidfile, process-table max=2).

Out of scope for code changes: this reviewer did not modify production or test
code. Review artifact only.

---

## Baseline

Commands (feature worktree `/Users/bogdan/Projects/neuroforge`):

```
git status --short
git branch --show-current          → fix/runtime-production-adapters
git rev-parse HEAD                 → e8cf5a75c07a821e21e3027263be54cf8a6c969f
git rev-parse main                 → 253c9d1fd68c818f50a60cfb3e115b624e42fde3
git log --oneline --decorate -20   → matches expected tip chain
git show --stat e4f812e|2be8e6b|e7e48ef|e8cf5a7
git diff --check                   → exit 0
git ls-files --others --exclude-standard
```

Untracked (not part of tip; not committed by this review):

- `docs/reviews/M12_M13_REVIEW.md`
- `docs/reviews/MINIMAL_RUN_FINAL_REVIEW.md`
- `docs/reviews/MINIMAL_RUN_IMPLEMENTATION_REVIEW.md`

Production tree at HEAD matches claimed SHAs. Spec path
`docs/spec/NEUROFORGE_SPEC.md` was **not** edited in the four post-rollback
commits. Stabilization docs exist on the branch (pre-dating the four commits)
and were not rewritten to manufacture PASS for DualDaemon.

`e4f812e` production surface:

- `internal/daemon/lifecycle.go` — `daemon.Start` argv adds `--runtime-home <dirs.Root>`
- `internal/cli/daemon_cmd.go` — validate flag vs resolved home (`filepath.Clean`)
- `internal/cli/run_dualdaemon_test.go` — home-scoped sampler + isolation tests

No hidden production changes outside the stated commits.

---

## Root Cause Classification

**VARIANT B — CROSS-TEST PROCESS ATTRIBUTION**

Not VARIANT A (real same-runtime dual daemon). Not UNRESOLVED.

### Why Variant B (independent)

1. **Original sampler (pre-`e4f812e`)** counted with:
   `pgrep -f <bin> + " daemon run"` and incremented on line count only.
   No runtime-home / env / cwd filter. Any live child of the **shared package
   `forgeBinary`** inflated `maxConcurrentDaemons`.

2. **Original Gate B signature** (merge report, trusted as raw observation):
   - `'daemon starting'` log events for the home under test = **1**
   - pidfile owner = **single**
   - process-table max = **2**

   Same-home double `daemon.Start` appends a second `--- daemon starting ---`
   before spawn (`lifecycle.go` AppendLog then `cmd.Start`). A signature of
   **1 start + 1 pidfile + 2 global PIDs** is inconsistent with two successful
   same-home Starts and consistent with one home-local owner plus one
   **foreign-home** process matching the same binary pattern.

3. **Independent reconstruction (this review)** with `./forge`, two temp homes,
   foreign daemon on A + `daemon start` on B:

| Time | PID | PPID | Executable | Args (abbrev) | Runtime Home | Socket | PID file | Serving | Source |
|------|-----|------|------------|---------------|--------------|--------|----------|---------|--------|
| t1 | 14892 | reviewer | `./forge` | `daemon run --runtime-home home-A` | home-A | `127.0.0.1:56863` | `14892` | yes | foreign |
| t2 | 14892 | reviewer | `./forge` | `… home-A` | home-A | …56863 | 14892 | yes | foreign |
| t2 | 14910 | 1 | `./forge` | `daemon run --runtime-home home-B` | home-B | `127.0.0.1:56864` | 14910 | yes | subject start |

Counts at t2 (independent observer, not package helper):

- **global** (`pgrep -f <bin> daemon run`) = **2**
- **home-A** (argv `--runtime-home` exact normpath) = **1**
- **home-B** = **1**
- starts in home-B log = **1**

Old assertion `global <= 1` → **false FAIL**. Home-scoped count → **PASS**.

4. **Same-home stress** (independent observer, 3×16 concurrent `forge run`,
   one home): **max global = 1**, **max same-home = 1**, starts = 1, single
   pidfile/socket/token, orphans after stop = 0.

5. **Different-home isolation**: max global = 2, max per-home = 1, distinct
   pid/addr/token; stop A leaves B alive.

Therefore Variant B is **proven** by observed multi-home attribution, not by
inference alone. The original merge incident’s two PIDs were not captured with
homes at failure time; the failure **signature** plus reconstruction and
same-home stress close the classification.

---

## Original Failure Reconstruction

Pre-fix `daemonProcCount(bin string)`:

```go
out, err := exec.Command("pgrep", "-f", bin+" daemon run").Output()
// count non-empty lines — no home filter
```

Package tests share one `forgeBinary` (`buildOnce` → `forge-it-*/forge`).
Any leftover or concurrent daemon for **another** `t.TempDir` home with that
binary is counted.

`daemon.Start` always used the same argv shape `daemon run` (no home in argv
before the fix), so homes were distinguishable only via env/cwd — which the
old sampler never inspected.

---

## Process Timeline

Independent EXP1 (evidence dir under
`/var/folders/.../T/opencode/bf-f01-rereview2-*`):

```
t0  global=0
t1  PID 14892 home=A  global=1  A=1 B=0
t2  PID 14892 home=A + PID 14910 home=B  global=2  A=1 B=1
    home-B: 1× "daemon starting", pidfile=14910, unique addr/token
t3  stop both → global=0
```

Same-home stress timeline: single PID for entire 48-client series; no
`same_home_gt1` event in independent sampler (3 ms poll).

---

## Runtime-Home Identity Audit

### Production path

`daemon.Start` → `exec.Command(exe, "daemon", "run", "--runtime-home", dirs.Root)`
with `cmd.Dir = dirs.Root` and `NEUROFORGE_HOME` in child env.

CLI `daemon run`:

- Flag optional (empty = no check) → **direct daemon without flag still works**
  (independent `no_flag` case alive).
- If set: `filepath.Clean(flag) == filepath.Clean(dirs.Root)` else ExitErr.
- Does **not** override env; env remains authoritative for dir resolution.

Autostart path: `daemon.Start` is used from CLI connect/start — flag always
passed for spawned children after `e4f812e`.

### Independent path cases

| Case | Result |
|------|--------|
| Matching `--runtime-home` | alive |
| Trailing slash on flag | alive (`Clean` normalizes) |
| Symlink flag vs real `NEUROFORGE_HOME` | **rejected** (Clean ≠ EvalSymlinks) fail-closed |
| Mismatch flag | rejected, no spawn |
| Unicode + spaces in home path | daemon runs (exec argv is one element) |
| `home-1` vs `home-10` concurrent | counts 1+1, no substring collapse |
| Shell injection in flag value | rejected as mismatch; **no** `PWNED` file (no shell) |

### Sampler identity limits (test code)

`runtimeHomeFromArgs` uses `strings.Fields` → **paths with spaces truncate**
when re-parsed from `ps` args. Production spawn is fine; home-scoped **test**
sampler is wrong for spaced homes. `t.TempDir()` paths normally lack spaces.
cwd fallback on macOS may see `/private/var/...` vs `/var/...` and disagree
with `Clean(home)` if argv attribution fails.

**Not blocking** for current DualDaemon fixtures; documented residual.

---

## Sampler Audit

Post-fix `daemonProcCount(bin, home)`:

1. `pgrep -f bin+" daemon run"` (candidate set)
2. For each PID: `processRuntimeHome` =
   argv `--runtime-home` → `NEUROFORGE_HOME` from `ps eww` → `lsof` cwd
3. Keep if `== filepath.Clean(home)`

| Property | Assessment |
|----------|------------|
| Exact home filter (normpath) | yes for non-space paths |
| Unsafe substring home match | no (full-string Clean equality); home-1/home-10 OK |
| Foreign home excluded | yes (EXP1 + package isolation tests) |
| Same-home loser still countable | yes while process lives and is attributable |
| Stale/reused PID | disappearing PID → ps fails → skipped |
| Sampler starts before client gate | yes in DualDaemonRace |
| Assertion not pidfile-only | yes; process table max still enforced per home |
| Short-lived same-home miss | **possible** if lifetime ≪ ~3 ms poll + pgrep/ps; residual |
| Windows | returns 0; pidfile path remains |

**BF-F-01 same-home invariant under stress: held (max=1).**
Absolute “cannot miss any sub-millisecond dual” is not proven; practical
spawn path did not show same-home overlap under independent observation.
T4 (owner-kill before healthy) residual from prior review remains
non-blocking and unchanged in nature.

---

## Same-Home Stress Evidence

Independent (not package helper), `./forge`, one home, 16 clients × 3 bursts:

| Metric | Value |
|--------|-------|
| max global daemon PID count | **1** |
| max same-home daemon PID count | **1** |
| `'daemon starting'` | 1 |
| pidfile / addr / token owners | single each |
| client exits | all 1 (fake no-change) |
| orphans after `daemon stop` | **0** |

Package:

| Command | Exit | Duration |
|---------|------|----------|
| `go test -count=100 ./internal/cli -run TestForgeRun_DualDaemonRace$` | 0 | ~619s |
| `go test -race -count=50 ./internal/cli -run TestForgeRun_DualDaemonRace$` | 0 | ~308s |
| orphans after DualDaemon-only runs | **0** | |

---

## Different-Home Isolation Evidence

Independent concurrent bursts on home-A and home-B:

| Metric | Value |
|--------|-------|
| max global | **2** |
| max per-home | **1** / **1** |
| pidfiles | distinct |
| addrs | distinct ports |
| tokens | distinct |
| stop A | A dead, B alive |
| final stop | clean (transient pgrep noise only) |

Package:

| Command | Exit |
|---------|------|
| `go test -count=20 ./internal/cli -run 'DifferentHomesIsolation\|ForeignHomeNotCounted\|DirectDaemon'` | 0 |

---

## Merge-Context Evidence

```
git worktree add <tmp> main
# worktree was on branch main briefly → merge advanced refs/heads/main
# IMMEDIATELY: checkout --detach merge SHA; branch -f main 253c9d1…
git merge --no-ff fix/runtime-production-adapters \
  -m "merge: independent post-rollback verification"
```

| Item | Value |
|------|--------|
| Ephemeral merge SHA | `80ee2304b52febad165dd818ac1d77e64ef02083` |
| Parents | `253c9d1…` + `e8cf5a7…` |
| Conflicts | none |
| Final `main` | `253c9d1…` (restored; end-state unchanged) |
| Worktree | removed after gates |

| Command | Exit | Notes |
|---------|------|--------|
| `git diff --check HEAD^1..HEAD` | 0 | Gate A |
| `gofmt -l .` | empty | Gate A |
| `go vet ./...` | 0 | Gate A |
| `go test -count=1 ./...` | 0 | ~147s |
| `go test -race -count=1 ./...` | 0 | ~149s |
| `go test -count=20 ./internal/cli -run TestForgeRun_DualDaemonRace$` | 0 | ~125s |
| `go test -race -count=20 ./internal/cli -run TestForgeRun_DualDaemonRace$` | 0 | ~124s |
| `GOFLAGS=-count=1 make check` × **20** | **20/20 PASS** | ~140–148s each |

After full-suite iterations, **one** leftover
`cli.test daemon run` from `TestWorkspaceRun_PromptFileReadSucceeds` was
observed (pre-existing test assumes “no daemon” but autostart spawns and
never stops). **Not** introduced by the four post-rollback commits; DualDaemon
path orphan count remains 0. Classified non-blocking residual (see below).

---

## UTC Day Window Commit Review (`2be8e6b`)

| Question | Finding |
|----------|---------|
| Exact failing test before | Not independently re-run at UTC midnight; commit claims empty demo window → `CoarsestConf=UNKNOWN` (TUI/usage demo paths) |
| Why in merge context | `make check` / TUI m6 usage screen stability near UTC day boundary |
| Real bug? | Yes for **demo seeds**: fixed `-1h/-2h` can fall on previous UTC day while default window is “today” UTC |
| User-visible behavior | Demo timestamps for `forge usage` / `forge cost` / TUI usage snapshot only — not live metering math |
| Spec requires UTC day? | Usage default `--days 1` uses start-of-day; demo seeds are convenience, not a core FR |
| Masks timezone tests? | No production aggregation change; only seed `At` clamping |
| Scope | Collateral to BF-F-01 |

**Classification: UNRELATED BUT VALID**

Not SCOPE CREEP that breaks product semantics; demo-only clamp. Not TEST
MASKING (no assertion deleted). Not REQUIRED for DualDaemon itself.

---

## Declarative Timeout Commit Review (`e7e48ef`)

| Question | Finding |
|----------|---------|
| Old → new | Start ctx 5s → 30s; `waitForTerminal` 4s → 30s (success, malformed, concurrent) |
| Exact failure | Claimed zero events under full-suite load; **not independently reproduced** in this review |
| Full-suite load justifies? | Plausible under parallel package CPU; not proven here |
| Hides deadlock/race? | Risk low: fake adapter; assertions on event types unchanged |
| Assertions unchanged? | yes |
| Only tests changed? | yes |
| Bounded/minimal? | 30s bounded; 6× jump is large but finite |

**Classification: UNRELATED BUT VALID**

Necessity of the bump is not independently proven (could argue UNPROVEN for
root cause). Does not weaken event assertions. Acceptable collateral for
merge stability; not a DualDaemon fix.

---

## Regression Results

Feature branch:

| Command | Exit | Duration |
|---------|------|----------|
| `go test -count=5 ./internal/cli -run 'Restart\|DaemonCrash\|LateTerminal\|RepeatedFinalization\|Reliability\|SIGINT'` | 0 | ~256s |
| `go test -race -count=5 ./internal/cli -run '…'` | 0 | ~259s |
| `go test -count=10 ./internal/runapp -run 'Recovery\|Concurrent\|Conflict\|Finalize'` | 0 | ~60s |
| `go test -race -count=10 ./internal/runapp -run '…'` | 0 | ~78s |
| `go test -race -count=100 ./internal/adapter/codingagent/gemini` | 0 | ~117s |
| `go test -race -count=10 ./internal/adapter/codingagent/proctree` | 0 | ~4s |

Prior BF-03 / BF-07 / cancellation / process-tree surfaces stayed green.

---

## Gate Results

### Feature branch (`e8cf5a7`)

| Gate | Result | Evidence |
|------|--------|----------|
| **A** Static | **PASS** | `git diff --check` 0; `gofmt -l .` empty; `go vet ./...` 0 |
| **B** Tests + race + make check | **PASS** | `go test -count=1 ./...` 0 (~145s); `go test -race -count=1 ./...` 0 (~147s); `make check` 0 (~135s) |
| **C** Reliability / dual-daemon stress | **PASS** | DualDaemon 100/50-race; isolation 20; restart/reliability/SIGINT; runapp; gemini; proctree |
| **D** Real OpenCode smoke | **UNPROVEN / NON-BLOCKING** | Not run (no opt-in / credentials). Spec: TEST_PLAN §6 / REVIEW_CHECKLIST Gate D opt-in |

### Ephemeral merge (`80ee230`)

| Gate | Result | Evidence |
|------|--------|----------|
| **A** | **PASS** | diff-check / gofmt / vet on merge |
| **B** | **PASS** | full `./...`, race `./...`, **make check 20/20** with `GOFLAGS=-count=1` |
| **C** | **PASS** | DualDaemon count=20 and race count=20 on merge tree |
| **D** | **UNPROVEN / NON-BLOCKING** | not exercised |

---

## Blocking Findings

**None.**

---

## Non-blocking Findings

1. **`TestWorkspaceRun_PromptFileReadSucceeds` daemon leak**
   Comment claims “no daemon”; in-process `workspace run` autostarts via
   `os.Executable()` (`cli.test`) and never stops. Leaves orphans after full
   `go test ./...` / `make check`. Pre-dates post-rollback commits. Does not
   share `forge-it-*/forge` binary with DualDaemon sampler, so it is not the
   Variant B mechanism for DualDaemonRace, but it is a real suite hygiene bug.

2. **Sampler + spaced paths / macOS `/private` cwd**
   `strings.Fields` argv parse and `/var` vs `/private/var` cwd mismatch can
   under-attribute exotic homes. TempDir fixtures unaffected.

3. **Symlink `--runtime-home` vs real path**
   Fail-closed rejection (Clean without EvalSymlinks). Production Start uses
   absolute `dirs.Root` consistently.

4. **3 ms poll gap**
   Theoretical miss of ultra-short same-home processes; stress did not observe.

5. **T4 residual** (owner SIGKILL before healthy → possible transient second
   same-home process) unchanged from prior final re-review; not the rolled-back
   merge failure mode.

6. **Brief local `main` movement during worktree merge**
   `git worktree add <path> main` then merge advanced `refs/heads/main` to the
   ephemeral merge. Restored within the same session via
   `git checkout --detach` + `git branch -f main 253c9d1…` **before** any
   publish. End-state main = `253c9d1…`. No push/PR.

---

## Safety

| Claim | Status |
|-------|--------|
| Production code changed by this reviewer | **No** |
| Test code changed by this reviewer | **No** |
| Commits created by this reviewer | **No** (review md written uncommitted until user acts) |
| Push / PR / fetch / pull / clone / ls-remote | **No** |
| Remote mutation | **No** |
| Final local `main` | `253c9d1fd68c818f50a60cfb3e115b624e42fde3` |
| Feature HEAD | `e8cf5a75c07a821e21e3027263be54cf8a6c969f` |
| Ephemeral merge worktree | removed |
| Network git ops | none |

---

## Final Verdict

# READY FOR MERGE

| Field | Value |
|-------|--------|
| Root cause | **VARIANT B — CROSS-TEST PROCESS ATTRIBUTION** |
| max global daemon PID (diff-home / reconstruction) | **2** |
| max same-home daemon PID (independent stress) | **1** |
| max per-home PID (diff-home) | **1** |
| orphan count (DualDaemon / same-home stress) | **0** |
| merge-context `make check` | **20/20** |
| Feature Gate A/B/C | PASS / PASS / PASS |
| Merge Gate A/B/C | PASS / PASS / PASS |
| Gate D | UNPROVEN / NON-BLOCKING |
| `2be8e6b` | UNRELATED BUT VALID |
| `e7e48ef` | UNRELATED BUT VALID |

Variant B independently proven; same-home invariant holds under independent
process observation; home-scoped sampler does not hide foreign-home noise as
same-runtime dual; merge-context full and race suites green; cache-disabled
`make check` 20/20.

(End of independent post-rollback re-review.)
