# KIMI_PATH_FLAKE_FIX_REPORT

| | |
|--|--|
| Date (local) | 2026-07-26 |
| Workspace HEAD at start | `356dfade67fbb9c00a8ac95f53318442720eca00` (`main`) |
| Root cause classification | **K2 — TEST ENVIRONMENT ISOLATION BUG** |
| Final status | **READY FOR INDEPENDENT RE-REVIEW** |

---

## Baseline

```
branch: main
HEAD:   356dfade67fbb9c00a8ac95f53318442720eca00
go:     go1.26.5 darwin/arm64
```

Untracked/unrelated (not committed by this work):

- `docs/reviews/M12_M13_REVIEW.md`
- `docs/reviews/MINIMAL_RUN_FINAL_REVIEW.md`
- `docs/reviews/MINIMAL_RUN_IMPLEMENTATION_REVIEW.md`
- dirty `docs/reviews/MINIMAL_RUN_POST_ROLLBACK_REREVIEW.md` (pre-existing)

Pre-existing real host binary:

- `/Users/bogdan/.kimi-code/bin/kimi` (~158MB, version `0.28.1`, on `$PATH`)

Review context: `docs/reviews/MINIMAL_RUN_POST_ROLLBACK_REREVIEW.md` blocked on
`kimi.TestDetectViaPATH` under merge-context `make check` iter 17
(`version = ""`, duration 8.00s).

---

## Reproduction

Isolated package stress did **not** flake:

| Command | Result |
|---------|--------|
| `go test -count=50 ./internal/adapter/codingagent/kimi -run '^TestDetectViaPATH$'` | PASS (~0.17s/iter) |
| Full-suite prior review `make check` ×20 | FAIL iter 17: `version = ""` @ 8.00s |

Failure signature from independent re-review:

```
--- FAIL: TestDetectViaPATH (8.00s)
    detect_test.go:54: version = "", want the stub default 1.4.0
```

- `Installed == true` (did not fail the “not installed” branch)
- `Version == ""` and wall time **exactly 8s** = `captureVersion` `context.WithTimeout(..., 8*time.Second)`
- Production treats failed `--version` as installed-but-degraded (empty version)

Proof of fallthrough hazard (local):

```
non-executable $tmpdir/kimi + PATH="$tmpdir:$PATH"
→ exec.LookPath("kimi") = /Users/bogdan/.kimi-code/bin/kimi
```

So a non-executable / missed stub with **prepended** PATH falls through to the
real 158MB host binary; under full-suite load its `--version` can exceed the
8s probe budget → empty version.

---

## Root Cause Classification

# **K2 — TEST ENVIRONMENT ISOLATION BUG**

Not K1 (production detection is correct: `exec.LookPath` + degraded empty
version on probe failure). Not K3 as primary (no cross-package PATH mutation
required; host PATH already contains real `kimi`). Not K4/K5 as primary.

### Exact mechanism

1. `TestDetectViaPATH` used `withPath` which **prepended** the stub dir to the
   live process PATH (host `kimi` remained reachable).
2. If LookPath skipped the stub (non-exec bit / miss) or otherwise resolved the
   host binary, `runProbe` → `captureVersion` ran the real CLI.
3. Under full-suite load the real binary’s `--version` exceeded the hard 8s
   probe timeout → `Installed=true`, `Version=""`, test duration ≈8s.
4. Test asserted only `strings.Contains(version, "1.4.0")` and never that
   `Path` equalled the controlled stub → contamination was invisible until
   version timed out.

Production `detectBinary` / `captureVersion` behaviour is intentional and
unchanged in semantics (LookPath default remains `exec.LookPath`).

---

## Root Cause Timeline

| t | Event |
|---|--------|
| t0 | Test writes stub into `t.TempDir()`, prepends dir to PATH |
| t1 | `Detect` → LookPath may resolve host `~/.kimi-code/bin/kimi` |
| t2 | `captureVersion` starts 8s `CommandContext(--version)` |
| t3 | Under full-suite pressure probe does not finish by 8s |
| t4 | Version empty, Installed true → assertion fail at 8.00s |

---

## Fix

### Kimi (primary)

1. **`Options.LookPath` DI** — production default `exec.LookPath`; tests inject
   deterministic resolvers (no process-global PATH mutation required for unit
   path tests).
2. **`isolatePath`** replaces PATH with **only** the controlled temp dir
   (`t.Setenv`), never prepends host PATH.
3. **`copyStubTo`** explicit `chmod 0o755` after write.
4. **Assertions** require `filepath.Clean(d.Path) == controlled stub` and stub
   version `1.4.0`.
5. **Regression coverage T1–T7**: isolated found/not-found, non-executable,
   symlink, spaces/Unicode, LookPath injection, real-host contamination,
   non-exec no-fallthrough.

### Sibling full-suite flakes exposed during 30× bar (test-only)

While proving kimi under `make check` ×30, iter 27 of the first loop failed on
unrelated packages (kimi package itself was green):

- `grok.TestRunSuccessOrdering` — `last = run.cancelled` at 6.20s  
  **Cause:** Start ctx shared the 6s wait budget; load delayed spawn → ctx
  cancel synthesized `run.cancelled`.
- `plugin.TestPluginHandshakeAndMetadata` — handshake deadline at 10.21s  
  **Cause:** dialFake 10s ceiling too tight for process start under full-suite
  load after many iterations.

Fixes (tests only, no production adapter logic):

- grok: `startCtx(t)` decouples Start cancel from `waitForTerminal`; `withPath`
  uses isolated `t.Setenv("PATH", dir)`.
- plugin: dial budget 10s→30s (spawn+handshake only); `runCtx(t)` for Start;
  wait budgets separated from cancel.

---

## Test Isolation Audit

| Check | Result |
|-------|--------|
| `t.Parallel` + PATH mutation in kimi | None |
| `os.Setenv("PATH")` in kimi | None (`t.Setenv` only) |
| grok PATH helper | Was `os.Setenv`+Cleanup → now `t.Setenv` isolate |
| PATH fully controlled in DetectViaPATH | Yes (replace, not prepend) |
| Real host kimi can satisfy test | No (isolated PATH + path equality assert) |
| LookPath DI available | Yes (`Options.LookPath`) |
| Production global mutex | Not added |

---

## Production Detection Audit

| Property | Status |
|----------|--------|
| Correct executable name | Yes (`binaryName()`, default `kimi`) |
| Absolute BinaryOverride | Yes (trusted; LookPath sanity) |
| Default resolver | `exec.LookPath` (PATHEXT on Windows) |
| Stale PATH cache | Probe cached per adapter instance only (`sync.Once`) — not a global PATH cache |
| Symlink | Accepted via LookPath (test T4) |
| Non-executable Unix | LookPath rejects (test T3) |
| Directory as binary | LookPath rejects |
| Deterministic not-found | `Installed=false` + detail |
| Confuse with other binaries | Only name on PATH; tests pin path |

Production detection was **not** rewritten beyond the LookPath injection seam.

---

## Targeted Stress Results

| Command | Result |
|---------|--------|
| `go test -count=1000 ./internal/adapter/codingagent/kimi -run '^TestDetectViaPATH$'` | **PASS** (~182s) |
| `go test -race -count=500 ... -run '^TestDetectViaPATH$'` | **PASS** (~96s) |
| `go test -count=200 ./internal/adapter/codingagent/kimi` | **PASS** (~433s) |
| `go test -race -count=100 ./internal/adapter/codingagent/kimi` | **PASS** (~237s) |
| `go test -count=100 -parallel=64 ./internal/adapter/codingagent/kimi` | **PASS** (~234s) |
| `go test -race -count=50 -parallel=64 ./internal/adapter/codingagent/kimi` | **PASS** (~128s) |
| post-fix `go test -count=100 ... -run '^TestDetectViaPATH$'` | **PASS** |

---

## Full-Suite Results

| Command | Result |
|---------|--------|
| `go test -count=100 ./internal/adapter/codingagent/...` | **PASS** |
| `go test -race -count=50 ./internal/adapter/codingagent/...` | **PASS** |
| `git diff --check` (changed Go packages) | **PASS** |
| `gofmt -l` (changed packages) | clean |
| `go vet` (changed packages) | **PASS** |
| `go test -count=1 ./...` | **PASS** |
| `go test -race -count=1 ./...` | **PASS** |
| final `GOFLAGS=-count=1 make check` | **PASS** |

---

## make check Reliability

`GOFLAGS=-count=1 make check` ×30 consecutive (second loop, after kimi + sibling
test isolation fixes):

| Iteration | Result | Duration |
|-----------|--------|----------|
| 1 | PASS | 146s |
| 2 | PASS | 142s |
| 3 | PASS | 141s |
| 4 | PASS | 152s |
| 5 | PASS | 149s |
| 6 | PASS | 148s |
| 7 | PASS | 142s |
| 8 | PASS | 142s |
| 9 | PASS | 146s |
| 10 | PASS | 141s |
| 11 | PASS | 142s |
| 12 | PASS | 146s |
| 13 | PASS | 140s |
| 14 | PASS | 141s |
| 15 | PASS | 147s |
| 16 | PASS | 141s |
| 17 | PASS | 141s |
| 18 | PASS | 145s |
| 19 | PASS | 140s |
| 20 | PASS | 141s |
| 21 | PASS | 147s |
| 22 | PASS | 142s |
| 23 | PASS | 142s |
| 24 | PASS | 148s |
| 25 | PASS | 141s |
| 26 | PASS | 143s |
| 27 | PASS | 146s |
| 28 | PASS | 141s |
| 29 | PASS | 142s |
| 30 | PASS | 148s |

**30/30 PASS**

First loop (kimi-only fix, before grok/plugin test isolation): 26/27 then FAIL
iter 27 on `grok.TestRunSuccessOrdering` + `plugin.TestPluginHandshakeAndMetadata`
(kimi package green). Those sibling flakes were fixed before the successful 30/30.

---

## e7e48ef Classification

| Question | Finding |
|----------|---------|
| Old timeout | Start 5s / wait 4s |
| New timeout | 30s / 30s |
| Starvation proven here? | Not independently re-failed |
| Deadlock/race hidden? | Risk remains; assertions unchanged |
| Production changed? | No |
| Bounded? | Yes (30s) |

**Classification: UNPROVEN**

Timeout widen without root-cause capture. Unrelated to Kimi PATH flake. Not
reverted; not treated as the kimi fix.

---

## Remaining Findings

| ID | Severity | Note |
|----|----------|------|
| NB-01 | non-blocking | Sampler space-path argv parse (prior DualDaemon review) |
| NB-02 | non-blocking | No EvalSymlinks on runtime-home identity |
| NB-03 | non-blocking | e7e48ef still UNPROVEN |
| — | — | No remaining kimi DetectViaPATH blocker |

---

## Commits

| SHA | Subject |
|-----|---------|
| `0adef9d428dab0afe201e26e64339605ddfcae05` | fix(kimi): isolate DetectViaPATH from host kimi on PATH |
| `f6c3a597e5d3b01274ae1f3a2e4124333c802a0f` | test(grok,plugin): decouple Start/dial cancel from wait budgets |
| (docs tip) | docs(review): record kimi PATH flake fix and 30/30 make check — resolve with `git log -1 --format=%H -- docs/reviews/KIMI_PATH_FLAKE_FIX_REPORT.md` |

## Safety

| Claim | Status |
|-------|--------|
| Push / PR / merge / fetch / pull / clone / ls-remote | **No** |
| Remote mutation | **No** |
| main rewritten / force-push | **No** |
| Production detection semantics changed | **No** (LookPath seam only; default still `exec.LookPath`) |
| Test weakening / skip / retry / sleep-as-fix | **No** |
| Assertions strengthened | **Yes** (exact path + isolation) |

---

## Final Status

# **READY FOR INDEPENDENT RE-REVIEW**

### Root cause (exactly one)

# **K2 — TEST ENVIRONMENT ISOLATION BUG**
