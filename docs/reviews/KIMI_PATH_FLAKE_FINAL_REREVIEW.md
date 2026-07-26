# KIMI_PATH_FLAKE_FINAL_REREVIEW

Independent final re-review. Author report, commit messages, claimed stress
counts, and prior “30/30 PASS” claims were **not** trusted. Only code inspection
and commands executed in this session are evidence.

| | |
|--|--|
| Date (local) | 2026-07-27 |
| Reviewer role | Independent final re-reviewer (not implementer) |
| Workspace HEAD | `06cba17f69ff41004d9d63438d8b3ad0eae37c56` |
| Branch observed | `main` (not `fix/runtime-production-adapters`) |
| Root cause | **K2 — TEST ENVIRONMENT ISOLATION BUG** |
| Verdict | **READY FOR MERGE** |

---

## Review Scope

- `0adef9d` fix(kimi): isolate DetectViaPATH from host kimi on PATH
- `f6c3a59` test(grok,plugin): decouple Start/dial cancel from wait budgets
- `06cba17` docs(review): record kimi PATH flake fix and 30/30 make check
- Ancestor `e7e48ef` test(declarative): widen run timeouts under full-suite load
- Claimed K2 mechanism, LookPath API, production detection semantics
- Independent stress, full gates, 30× `make check`, clean-cache final, regression

No production code or tests were modified by this reviewer.

---

## Baseline

```
branch:  main
HEAD:    06cba17f69ff41004d9d63438d8b3ad0eae37c56
commits: 06cba17 → f6c3a59 → 0adef9d → 356dfad (parent of fix)
```

| Check | Result |
|-------|--------|
| Expected branch `fix/runtime-production-adapters` | **Mismatch** — work is on `main`; tip of that branch is older (`f42134a`) |
| Expected commits present | **Yes** (`0adef9d`, `f6c3a59`, `06cba17`) |
| Spec / milestones changed | **No** (`docs/spec/**`, `docs/milestones/**` untouched in 356dfad..HEAD) |
| Production files in fix commits | `kimi/detect.go`, `kimi/options.go` only (+ tests/docs) |
| Unrelated dirty tree | Pre-existing: `M docs/reviews/MINIMAL_RUN_POST_ROLLBACK_REREVIEW.md`, untracked M12/MINIMAL_RUN review docs |
| `git diff --check` | Trailing whitespace **only** in dirty pre-existing review md (not in fix commits) |
| Host `kimi` | **Present**: `/Users/bogdan/.kimi-code/bin/kimi` (~158MB, reports `0.28.1`) |

Author report `docs/reviews/KIMI_PATH_FLAKE_FIX_REPORT.md` matches the actual diff themes; stress numbers were re-run independently (not trusted).

---

## Original Failure Reconstruction

### Pre-fix test (parent `356dfad` / `0adef9d^`)

From `detect_test.go` before the fix:

1. `withPath` **prepended** stub dir to live `PATH` via `t.Setenv("PATH", dir+sep+cur)`.
2. Host `~/.kimi-code/bin/kimi` remained on PATH after the stub dir.
3. Assertions checked `Installed` and `strings.Contains(version, "1.4.0")` only — **no** exact controlled path.
4. Production `captureVersion` uses `context.WithTimeout(..., 8*time.Second)`.
5. On `--version` failure, `runProbe` keeps `installed=true`, leaves `versionStr` empty, sets degraded detail.

### Independently proven hazard (this session)

```
non-executable $tmpdir/kimi + PATH="$tmpdir:$PATH"
→ exec.LookPath("kimi") = /Users/bogdan/.kimi-code/bin/kimi   (equalHost=true)

PATH="$tmpdir" only
→ LookPath miss (no host fallthrough)

executable stub + prepend
→ LookPath returns stub (host not selected)
```

Host `--version` under light load: ~0.3–0.7s (does not always hit 8s). Failure mode is therefore **load-sensitive contamination**, not a guaranteed timeout every run.

### Signature consistency

Prior blocking failure: `TestDetectViaPATH` @ **8.00s**, `version = ""`, `Installed` still true — matches `captureVersion` 8s budget + degraded installed path, **not** a “not found” path.

### Isolated pre-fix stress

Worktree at `0adef9d^`:

`go test -count=200 ./internal/adapter/codingagent/kimi -run '^TestDetectViaPATH$'` → **PASS**

Confirms: isolated package stress can stay green while full-suite load + host contamination remains the hazard. Targeted green does **not** disprove the flake.

### Mechanism verdict

| Claim | Proven? |
|-------|---------|
| Test prepended fake dir to live PATH | **Yes** (pre-fix source) |
| Real `~/.kimi-code/bin/kimi` reachable after prepend | **Yes** (LookPath harness) |
| Detection runs `kimi --version` | **Yes** (`captureVersion`) |
| Timeout ≈ 8s | **Yes** (hard-coded) |
| Timeout → `Installed=true`, `Version=""` | **Yes** (`runProbe` degraded branch) |
| Full-suite load can push real binary past 8s | **Plausible**; signature matches; not re-hit under light load alone |
| Isolated targeted test can stay green | **Yes** (200× pre-fix PASS) |

Primary root cause is **test isolation**, not broken production LookPath semantics.

---

## Root Cause Classification

# **K2 — TEST ENVIRONMENT ISOLATION BUG**

| Code | Ruled out because |
|------|-------------------|
| K1 | Production intentionally treats found-but-unversioned as installed-degraded; default resolver remains `exec.LookPath` |
| K3 | No evidence sibling packages must mutate PATH for this failure; host PATH already contains real `kimi` |
| K4 | No FS race required; fallthrough is deterministic given non-exec/miss + prepend |
| K5 | Reproduced hazard on darwin/arm64 with stock Go toolchain |
| K6 | Mechanism closed by code + harness |

---

## Options.LookPath API Audit

| Check | Result |
|-------|--------|
| Default | `(*Options).lookPath` → `exec.LookPath` when `LookPath == nil` |
| nil / zero `Options` | Safe (`o != nil && o.LookPath != nil` guard; `New(Options{})` used by builtin) |
| Injected fn scope | Used only in `detectBinary` for lookup (override + PATH name) |
| Public API | New optional field on exported `Options`; composite literals remain valid (zero value = default) |
| Production callers must set LookPath | **No** — `builtin` uses `kimi.New(kimi.Options{})` |
| Global state / cache of PATH | None added; probe still `sync.Once` per adapter instance |
| Absolute path / symlink / PATHEXT | Still delegated to `exec.LookPath` on default path |
| Errors | Deterministic not-found when LookPath errs and no override |
| Production detection semantics | Unchanged aside from injection seam |

**No API/behavior regression found** for production default path.

---

## Kimi Test-Isolation Audit

| Check | Result |
|-------|--------|
| Controlled fake path | `isolatePath` + `copyStubTo` + exact `filepath.Clean` path assert |
| No host PATH dependency for T1 | PATH replaced with only temp dir |
| No real Kimi required | Stub only; T7 documents host ignore when isolated |
| `t.Parallel` + env mutation | **None** in kimi detect tests |
| `os.Setenv` leak | **None** (`t.Setenv` only) |
| Exact fake path assertion | **Yes** in `TestDetectViaPATH` and related |
| Deterministic not-found | `TestDetectNotOnPath`, non-exec, unknown name |
| Executable bit | Explicit `Chmod 0o755`; T3 rejects non-exec |
| Retry/sleep-as-fix | **No** in detect tests |
| Timeout increased | **No** — production still 8s |
| Assertions weakened | **No** — strengthened (path equality + isolation cases T1–T7) |
| Default LookPath path | Exercised when `LookPath` nil (production path in PATH tests) |
| Injected LookPath | Separate `TestDetectLookPathInjection` |

---

## Kimi Stress Results

Host kimi left installed (not modified).

| Command | Result |
|---------|--------|
| `go test -count=1000 .../kimi -run '^TestDetectViaPATH$'` | **PASS** (~186s) |
| `go test -race -count=500 .../kimi -run '^TestDetectViaPATH$'` | **PASS** (~120s) |
| `go test -count=200 ./internal/adapter/codingagent/kimi` | **PASS** (~443s) |
| `go test -race -count=100 ./internal/adapter/codingagent/kimi` | **PASS** (~230s) |
| `go test -count=100 -parallel=64 ./.../kimi` | **PASS** (~227s) |
| `go test -race -count=50 -parallel=64 ./.../kimi` | **PASS** (~110s) |

---

## Grok Changes Review (`f6c3a59`)

**Files:** `grok/adapter_test.go`, `grok/detect_test.go` — test-only.

| Question | Finding |
|----------|---------|
| Exact failure claimed | `TestRunSuccessOrdering` → `last = run.cancelled` ~6.2s under full-suite |
| Mechanism | Start used the same short `WithTimeout` as wait; production maps `runCtx` cancel → `EventRunCancelled` (`cancelTimeoutTerminal`) |
| Production changed? | **No** |
| “Decouple Start/dial cancel from wait budgets” | `startCtx(t)` = cancel-on-cleanup background ctx; `waitForTerminal` keeps its own bound (typically 6s) |
| Assertions changed? | Success/failure class assertions **unchanged** |
| Timeout budgets increased? | **No** for wait paths; Start no longer shares the short deadline |
| Deadlock hidden? | **No** evidence; waits still bounded |
| Cancellation guarantee weakened? | **No** — `TestRunCancellationKillsGroup` still calls `Cancel` and expects `run.cancelled` |
| Bounded? | **Yes** |
| PATH helper | `os.Setenv`+Cleanup → `t.Setenv("PATH", dir)` isolate (valid hygiene) |

### Classification: **VALID TEST-ISOLATION FIX**

Not TEST WEAKENING / FLAKE MASKING: cancels were false positives from shared test deadlines, not from loosened success criteria.

---

## Plugin Changes Review (`f6c3a59`)

**File:** `plugin/adapter_test.go` — test-only.

| Question | Finding |
|----------|---------|
| Exact failure claimed | Handshake / dial deadline ~10.2s under load |
| Mechanism | `dialFake` Start+handshake shared a 10s ceiling; OS exec delay under full-suite pressure |
| Production changed? | **No** |
| runCtx decoupling | Same pattern as grok — **valid** |
| dial budget | 10s → **30s** (spawn+handshake only) |
| waitForTerminal | 2–3s → **10s** on several tests |
| Assertions | Unchanged (event types, quota class, cancel terminal, etc.) |
| Cancellation | Still explicit `Cancel` + expect `run.cancelled` |
| Bounded? | **Yes** (30s dial, 10s waits) |
| Deadlock hidden? | No production dial logic change; residual risk that slow deadlock could sit under 30s is low and unproven |

### Classification: **VALID TEST-ISOLATION FIX**

Primary fix is cancel/wait decoupling. Budget widens are load-tolerance for process start, still bounded, assertions intact — **not** classified as TEST WEAKENING or FLAKE MASKING under the acceptance bar.

Non-blocking note: dial 10→30 is coarser than kimi’s isolation fix; acceptable residual.

---

## e7e48ef Review

| Question | Finding |
|----------|---------|
| Old timeout | Start **5s** / wait **4s** |
| New timeout | Start **30s** / wait **30s** |
| Exact pre-change failure | Claimed zero events under parallel package load — **not independently re-failed** this session |
| Production changed? | **No** |
| Assertions weakened? | **No** |
| Bounded? | **Yes** (30s) |
| Starvation evidence this session | **Absent** |
| Deadlock/race hidden? | Residual risk remains (shared Start ctx still couples cancel to wait, unlike grok/plugin fix) |
| Still required after kimi fix? | **Unknown** — unrelated package; not exercised as kimi root cause |

### Classification: **UNPROVEN**

Does **not** block merge of the kimi/grok/plugin work: pre-existing, test-only, assertions intact, bounded, orthogonal to K2. Residual non-blocking risk: timeout widen without captured root cause (prefer future decouple like grok `startCtx`).

---

## Adapter-Wide Results

| Command | Result |
|---------|--------|
| `go test -count=100 ./internal/adapter/codingagent/...` | **PASS** |
| `go test -race -count=50 ./internal/adapter/codingagent/...` | **PASS** |
| `go test -count=200 ./.../grok` | **PASS** |
| `go test -race -count=100 ./.../grok` | **PASS** |
| `go test -count=200 ./.../plugin` | **PASS** |
| `go test -race -count=100 ./.../plugin` | **PASS** |
| Named cancel/success/handshake (count=50 / race count=20) | **PASS** |

---

## Full Gate Results

| Gate | Result |
|------|--------|
| A. `gofmt -l .` | **empty** (clean) |
| A. `go vet ./...` | **PASS** |
| B. `go test -count=1 ./...` | **PASS** |
| C. `go test -race -count=1 ./...` | **PASS** |
| `GOFLAGS=-count=1 make check` (single) | **PASS** |
| `git diff --check` on fix commits | clean; dirty only pre-existing review md |

---

## make check 30/30 Evidence

Command (this session):

```sh
for i in $(seq 1 30); do
  GOFLAGS=-count=1 make check || exit $?
done
```

| Iteration | Exit | Duration | Result |
|-----------|------|----------|--------|
| 1 | 0 | 142s | PASS |
| 2 | 0 | 141s | PASS |
| 3 | 0 | 148s | PASS |
| 4 | 0 | 141s | PASS |
| 5 | 0 | 142s | PASS |
| 6 | 0 | 147s | PASS |
| 7 | 0 | 141s | PASS |
| 8 | 0 | 142s | PASS |
| 9 | 0 | 145s | PASS |
| 10 | 0 | 141s | PASS |
| 11 | 0 | 141s | PASS |
| 12 | 0 | 146s | PASS |
| 13 | 0 | 141s | PASS |
| 14 | 0 | 141s | PASS |
| 15 | 0 | 146s | PASS |
| 16 | 0 | 141s | PASS |
| 17 | 0 | 142s | PASS |
| 18 | 0 | 147s | PASS |
| 19 | 0 | 142s | PASS |
| 20 | 0 | 141s | PASS |
| 21 | 0 | 146s | PASS |
| 22 | 0 | 140s | PASS |
| 23 | 0 | 140s | PASS |
| 24 | 0 | 146s | PASS |
| 25 | 0 | 140s | PASS |
| 26 | 0 | 141s | PASS |
| 27 | 0 | 149s | PASS |
| 28 | 0 | 141s | PASS |
| 29 | 0 | 141s | PASS |
| 30 | 0 | 148s | PASS |

**30/30 PASS** (independently reproduced; prior report counts not used).

---

## Clean-Cache Final Run

```
go clean -testcache
GOFLAGS=-count=1 make check
→ exit=0 duration=141s
```

**PASS**

---

## Regression Results

| Command | Result |
|---------|--------|
| `go test -count=10 ./internal/cli -run DualDaemon` | **PASS** |
| `go test -race -count=10 ./internal/cli -run DualDaemon` | **PASS** |
| `go test -count=5 ./internal/cli -run 'Restart\|DaemonCrash\|LateTerminal\|RepeatedFinalization\|Reliability\|SIGINT'` | **PASS** |
| `go test -race -count=5 ./internal/cli` (same -run) | **PASS** |
| `go test -count=10 ./internal/runapp -run 'Recovery\|Concurrent\|Conflict\|Finalize'` | **PASS** |
| `go test -race -count=10 ./internal/runapp` (same -run) | **PASS** |

---

## Blocking Findings

**None.**

---

## Non-blocking Findings

| ID | Note |
|----|------|
| NB-01 | Expected branch name was `fix/runtime-production-adapters`; commits already on `main` |
| NB-02 | `e7e48ef` remains **UNPROVEN** (timeout widen without captured root cause) |
| NB-03 | Plugin dial 10→30s is coarser load padding (still bounded; assertions intact) |
| NB-04 | Working tree has pre-existing dirty/untracked review docs unrelated to this fix |
| NB-05 | Host kimi still on developer PATH — production Detect correctly finds it; tests no longer depend on that |

---

## Safety

| Action | Performed? |
|--------|------------|
| push / PR / merge / fetch / pull / clone / ls-remote | **No** |
| Remote mutation | **No** |
| Production code edited by reviewer | **No** |
| Tests edited by reviewer | **No** |
| Review artifact only | **Yes** (this file) |

Temporary worktree at pre-fix commit was created under `/var/folders/.../T/opencode/` and removed after use.

---

## Final Verdict

# **READY FOR MERGE**

### Root cause (exactly one)

# **K2 — TEST ENVIRONMENT ISOLATION BUG**

### Summary

- Kimi flake mechanism independently reconstructed and fixed at the test boundary.
- Production detection default path remains `exec.LookPath`; optional `LookPath` is backward-compatible.
- Grok/plugin changes are valid test harness isolation, not assertion weakening.
- `e7e48ef` stays UNPROVEN and non-blocking.
- Independent stress, full gates, 30/30 `make check`, clean-cache final, and regression suites all green.

(End of independent final re-review.)
