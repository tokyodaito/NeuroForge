# V0.1.0 Self-Host Release Report

Local-only release finalization and first real self-hosting canary for
NeuroForge **v0.1.0**. No remote mutation.

| | |
|--|--|
| Date (local) | 2026-07-27 |
| Role | release / self-hosting agent |
| Final result | **V0.1.0 TAGGED LOCALLY — SELF-HOSTING CANARY PASS** |

---

## Baseline

| Item | Value |
|------|-------|
| Initial main SHA | `06cba17f69ff41004d9d63438d8b3ad0eae37c56` |
| Branch | `main` |
| Expected commits present | `0adef9d`, `f6c3a59`, `06cba17` |
| Merge/rebase/cherry-pick in progress | None |
| Uncommitted production changes | None |
| Final re-review artifact present | `docs/reviews/KIMI_PATH_FLAKE_FINAL_REREVIEW.md` (untracked at baseline) |
| Tag `v0.1.0` at baseline | Did not exist |
| Pre-existing dirty (not committed) | `M docs/reviews/MINIMAL_RUN_POST_ROLLBACK_REREVIEW.md` |
| Pre-existing untracked (not committed) | `M12_M13_REVIEW.md`, `MINIMAL_RUN_FINAL_REVIEW.md`, `MINIMAL_RUN_IMPLEMENTATION_REVIEW.md` |
| Leftover worktrees (pre-existing) | `forge/neuroforge-{1,2,5}/…` under `~/.neuroforge/workspaces/` |

Final-review artifact commit (this flow):

| | |
|--|--|
| **FINAL_REVIEW_COMMIT** | `d143678e0add857c0e71adc5ba9ab87613b336ea` |
| Message | `docs(review): record final Kimi path flake approval` |
| Files | exactly `docs/reviews/KIMI_PATH_FLAKE_FINAL_REREVIEW.md` |

Artifact content confirmed: verdict **READY FOR MERGE**; root cause **K2**;
production bug **No**; Kimi stress **PASS**; adapter-wide **PASS**; Grok/plugin
**VALID TEST-ISOLATION FIX**; `make check` 30/30 **PASS**; clean-cache **PASS**;
blockers **none**; reviewer did not change production code; no push/PR/merge/network.

---

## Pre-Self-Host Gates

Run on `FINAL_REVIEW_COMMIT` (`d143678`).

| Command | Exit | Duration | Notes |
|---------|------|----------|-------|
| `git diff --check` | 2 | <1s | **Only** pre-existing dirty `MINIMAL_RUN_POST_ROLLBACK_REREVIEW.md` trailing whitespace (known at baseline; not production; not committed). Committed tree clean. |
| `test -z "$(gofmt -l .)"` | 0 | <1s | clean |
| `go vet ./...` | 0 | ~0.6s | PASS |
| `go test -count=1 ./...` | 0 | 146s | PASS |
| `go test -race -count=1 ./...` | 0 | 147s | PASS |
| `GOFLAGS=-count=1 make check` | 0 | 142s | PASS |

### Build release candidate

| Command | Exit | Result |
|---------|------|--------|
| `make build` | 0 | `./forge` produced |
| `./forge version` | 0 | `forge minimal-run-v1-4-gd143678-dirty` / commit `d143678` / `darwin/arm64` |
| `./forge help` | 0 | contains `forge run` |

Binary built from HEAD `d143678`. `-dirty` suffix reflects pre-existing uncommitted review md only.

---

## Self-Host Invocation

Exact command:

```sh
./forge run \
  --engine opencode \
  --model zai-coding-plan/glm-5.2 \
  --timeout 30m \
  --json \
  "Приведи README.md и docs/milestones/IMPLEMENTATION_PLAN.md в соответствие с текущим фактическим состоянием проекта. ..." \
  > /tmp/neuroforge-v0.1-selfhost-result.json
```

| Field | Value |
|-------|-------|
| engine | `opencode` |
| model | `zai-coding-plan/glm-5.2` |
| timeout | `30m` |
| CLI exit | 0 |
| JSON path | `/tmp/neuroforge-v0.1-selfhost-result.json` |
| outcome | `completed-with-commit` |
| task_id | `neuroforge-8` |
| workspace_id | `ws-neuroforge-8-main-1` |
| run_id | `run-1785099954380445000` |
| workspace_path | `/Users/bogdan/.neuroforge/workspaces/neuroforge/neuroforge-8/main/attempt-1` |
| base_sha | `d143678e0add857c0e71adc5ba9ab87613b336ea` |
| actual_head_sha | `308c6f259abc943e307fb89bf45971cf0500c682` |
| commit_sha | `308c6f259abc943e307fb89bf45971cf0500c682` |
| result_branch | `refs/heads/forge/result/neuroforge-8` |
| changed_files | `README.md`, `docs/milestones/IMPLEMENTATION_PLAN.md` |
| error | `null` |
| error_class | `null` |

JSON validated with `python3 -m json.tool`.

Contract checks:

| Check | Result |
|-------|--------|
| `outcome == completed-with-commit` | PASS |
| `actual_head_sha != base_sha` | PASS |
| `commit_sha == actual_head_sha` | PASS |
| `result_branch` non-empty | PASS |
| `error` / `error_class` null | PASS |
| `git rev-parse result_branch` == actual_head_sha | PASS (`308c6f2…`) |
| Primary checkout HEAD unchanged | PASS (still `d143678` before merge) |
| Primary tracked tree unchanged by canary | PASS (only pre-existing dirty/untracked review docs) |

---

## Self-Host Scope Review

| Item | Result |
|------|--------|
| Result commit | `308c6f2` — `docs: align README + IMPLEMENTATION_PLAN with actual M0-M13 state` |
| Files changed | **exactly** `README.md`, `docs/milestones/IMPLEMENTATION_PLAN.md` |
| Forbidden files touched | **None** (no `docs/spec/**`, `internal/**`, `cmd/**`, `go.mod`, `go.sum`, `Makefile`, `.github/**`, `docs/reviews/**`) |
| README no longer claims only version/help | PASS |
| README no longer claims daemon/SQLite/adapters absent | PASS |
| Milestones summary reflects M0–M13 implemented | PASS |
| Honest self-hosting alpha (no absolute production readiness) | PASS |
| Spec remains immutable source of truth; matrix is tracking view | PASS |
| Markdown intact | PASS |

### Independent result-branch validation

Temporary worktree at `/tmp/neuroforge-selfhost-review.M3L8Zi` on
`refs/heads/forge/result/neuroforge-8`:

| Command | Exit | Duration |
|---------|------|----------|
| `git status --short` | 0 | clean |
| `git diff --check HEAD^..HEAD` | 0 | clean |
| `test -z "$(gofmt -l .)"` | 0 | clean |
| `go vet ./...` | 0 | PASS |
| `go test -count=1 ./...` | 0 | 146s |
| `GOFLAGS=-count=1 make check` | 0 | 146s |

Worktree removed via `git worktree remove` + `git worktree prune`.

---

## Self-Host Merge

| Item | Value |
|------|-------|
| **SELF_HOST_MERGE_SHA** | `0b04e804681303b28bee5eda115ff882e7d31803` |
| Strategy | `git merge --no-ff` (no squash, no cherry-pick) |
| Message | `docs: refresh project status through NeuroForge self-hosting` |
| Parents | `d143678e0add857c0e71adc5ba9ab87613b336ea` + `308c6f259abc943e307fb89bf45971cf0500c682` |
| Conflicts | None |

---

## Final Gates

Run on merge HEAD `0b04e80`.

| Command | Exit | Duration | Notes |
|---------|------|----------|-------|
| `git diff --check HEAD^1..HEAD` | 0 | <1s | merge commit clean |
| `test -z "$(gofmt -l .)"` | 0 | <1s | clean |
| `go vet ./...` | 0 | — | PASS |
| `go test -count=1 ./...` | 0 | 145s | PASS |
| `go test -race -count=1 ./...` | 0 | 148s | PASS |
| `GOFLAGS=-count=1 make check` | 0 | 141s | PASS |
| `make build` | 0 | — | `./forge` |
| `./forge version` | 0 | — | commit `0b04e80` |

---

## Release Tag

| Item | Value |
|------|-------|
| Tag | `v0.1.0` (annotated) |
| Message | `NeuroForge v0.1.0 — self-hosting alpha` |
| **Tagged release SHA** | `0b04e804681303b28bee5eda115ff882e7d31803` |
| `git rev-list -n 1 v0.1.0` | equals HEAD at tag time |
| Pushed | **No** |

### Report commit (after tag)

This report is committed **after** the tag so `v0.1.0` remains on the verified
release merge; the report is the next documentation-only commit.

| Item | Value |
|------|-------|
| Report path | `docs/reviews/V0_1_0_SELF_HOST_RELEASE_REPORT.md` |
| **Report commit SHA** | *(filled at commit time — see git log after this file lands)* |
| Tag moved onto report? | **No** |

Recorded at write time (pre-report-commit):

- tagged release SHA = `0b04e804681303b28bee5eda115ff882e7d31803`
- report will be a child of that SHA on `main`

---

## Safety

| Action | Performed? |
|--------|------------|
| push | **No** |
| PR | **No** |
| fetch / pull / clone / ls-remote | **No** |
| Any Git remote mutation | **No** |
| OpenCode / model network | **Yes — only** for the explicit self-hosting canary (`forge run --engine opencode`) |
| Unrelated pre-existing review docs committed | **No** |
| Spec / code / tests / go.mod / workflows changed by canary | **No** |
| Tag force-moved | **No** |

---

## Final Result

# **V0.1.0 TAGGED LOCALLY — SELF-HOSTING CANARY PASS**

### SHA map

| Role | SHA |
|------|-----|
| Initial main | `06cba17f69ff41004d9d63438d8b3ad0eae37c56` |
| Final-review artifact | `d143678e0add857c0e71adc5ba9ab87613b336ea` |
| Self-host agent commit | `308c6f259abc943e307fb89bf45971cf0500c682` |
| Self-host merge (`v0.1.0`) | `0b04e804681303b28bee5eda115ff882e7d31803` |
| Report commit | *(next on main after tag)* |

### Self-host summary

| | |
|--|--|
| task_id | `neuroforge-8` |
| result_branch | `refs/heads/forge/result/neuroforge-8` |
| result SHA | `308c6f259abc943e307fb89bf45971cf0500c682` |
| changed files | `README.md`, `docs/milestones/IMPLEMENTATION_PLAN.md` |
| primary checkout preserved during canary | **Yes** |

(End of v0.1.0 self-host release report.)
