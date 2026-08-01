# Self-Hosting Acceptance — NeuroForge

Date: 2026-08-01 (UTC) · Verdict: **ACCEPTED** (independent final review, 15/15 checks)

NeuroForge implemented a small production-code change in its own repository
through its normal production pipeline, driven by the user's existing,
already-authenticated OpenCode installation and Z.ai Coding Plan login.

## Final state

- Branch: `selfhost/mission` (45 commits ahead of `main` at acceptance time)
- Final commit at acceptance: `c007670`
- Self-hosting proof level: **self-hosting accepted** (tested with authenticated
  OpenCode, Z.ai Coding Plan model `zai-coding-plan/glm-5.2`, after a real
  daemon restart)
- Self-hosting result: task `neuroforge-3`, result commit `befb02b` on
  `refs/heads/forge/result/neuroforge-3`

## Environment

- OS: Linux on WSL2 (`6.18.33.2-microsoft-standard-WSL2`, x86_64), Ubuntu
- OS user: `ogdan` (NeuroForge and OpenCode run as the same user; no sudo)
- Go: 1.26.5 (toolchain), repo requires ≥ 1.23
- `make`: not installed on this host — all Makefile steps were run directly
  (`gofmt -l .`, `go vet ./...`, `go test ./...`, `go build ./...`)
- WSL2 note: repository lives in the Linux filesystem (`~/code/neuroforge`);
  Windows-mounted paths (`/mnt/c`) are not used. Wall-clock jumps backward
  were observed under load (WSL2); lease tests were made clock-independent.

## OpenCode integration

- Executable: `/home/ogdan/.opencode/bin/opencode` (version 1.18.11),
  resolved via `PATH` lookup (adapter `Detect`), spawned by absolute path.
- Authentication: the user's **existing** OpenCode login
  (`~/.local/share/opencode/auth.json`) — no API token was requested, created,
  copied, or embedded. The adapter never reads/copies auth files; it forwards
  an allowlisted environment (incl. `HOME`) so OpenCode finds its own session.
- Resolved model: `zai-coding-plan/glm-5.2` (Z.ai Coding Plan).
- Invocation (per stage): `opencode run --format json --dir <worktree>
  --model zai-coding-plan/glm-5.2 -- <prompt>` (argv only, no shell).
- Secret protection: env allowlist + forbidden-key filters (supervisor and
  adapter), redaction of stderr/artifacts (session IDs appear as `***`),
  verification stage runs with a stripped environment (no daemon secrets).
- Preflight (2026-08-01): `opencode run` under the exact restricted
  environment the adapter forwards answered correctly with the model above.

## Canonical commands

```sh
go build ./...            # build
gofmt -l .                # formatting (empty = clean)
go vet ./...              # static analysis
go test ./...             # full suite — green at acceptance
go test -race ./...       # race suite — green at acceptance
```

Live task submission (the normal user path):

```sh
./forge run --verbose "describe the change and its acceptance criteria"
```

## Live runs (all through `forge run` → daemon pipeline)

| Task | Repo | Result | Evidence |
|---|---|---|---|
| `nf-fixture-3` | /tmp/nf-fixture | `completed-with-commit` `9c60bad` on `forge/result/nf-fixture-3` | Multiply + TestMultiply written by glm-5.2; 4-level verify passed; 3-role review REVIEWED |
| `nf-fixture-4` | /tmp/nf-fixture | `completed-with-commit` `429bba4` on `forge/result/nf-fixture-4` | **live repair loop**: verify att1 failed (`TestDivide: got (12,nil), want (5,nil)`) → repair (2nd OpenCode run) → verify att2 passed → review → finalize |
| `neuroforge-3` | **this repo** | `completed-with-commit` `befb02b` on `forge/result/neuroforge-3` | **self-hosting**: `FailureCategory.IsRetryable()` + 19-case test in `internal/pipeline`; full-suite verify passed; review REVIEWED with 2 substantive minor findings |
| `nf-fixture-5` | /tmp/nf-fixture | `completed-with-commit` `0f8eb92` on `forge/result/nf-fixture-5` | **restart acceptance**: daemon SIGKILLed mid-review (agent died via Pdeathsig); `review att1 failed (interrupted)`; restarted daemon re-drove review att2 → finalize |
| `neuroforge-1` | this repo | `failed` (`provider_timeout`) | honest failure: verify caught a stale test (`TestForgeRun_UncommittedChanges`), repair agent hit the 10-min timeout; the stale test was fixed (`e029817`) |
| `neuroforge-2` | this repo | `cancelled` | CLI 30-min wait cap cancelled a healthy run mid-repair; led to `--wait-timeout` (2h default) and the verify flake-retry guard (`29ef706`, `2aa28d8`, `c007670`) |

Stage timelines for accepted runs are durably recorded in
`~/.neuroforge/state.db` (`pipeline_runs`, `pipeline_stage_records`) and
evidence artifacts under `~/.neuroforge/artifacts/`.

## Self-hosting task detail (`neuroforge-3`)

- Submitted task: add `IsRetryable() bool` on `pipeline.FailureCategory`
  (true only for `FailureQuotaExceeded`, `FailureRateLimited`,
  `FailureProviderTimeout`) + a table-driven test covering all 19 categories.
- Acceptance criteria: gofmt clean; `go build ./...` passes;
  `go test ./internal/pipeline/` passes.
- Changed files: `internal/pipeline/stage.go` (+13),
  `internal/pipeline/failure_retryable_test.go` (new, 47 lines).
- Stages: compile → plan → ready → execute → verify → review → finalize,
  one attempt each; review verdict REVIEWED (3 roles, 2 minor findings);
  result commit `befb02b` by `NeuroForge <neuroforge@local>`.
- The task was implemented by authenticated OpenCode launched by the daemon;
  the lead agent did not write the change.

## Quota / usage

All live usage is durably recorded in `usage_events` (~100 rows for the
acceptance runs): provider `opencode`, model `zai-coding-plan/glm-5.2`,
per-step token counts. Reported cost is $0.00 (Z.ai Coding Plan is a
subscription plan). Live runs were executed sequentially with bounded
repair (`--max-repair 2–3`) and per-run agent timeouts (10–20 min).

## Test results at acceptance

- Unit/integration/race suites: green (`go test ./...`, `go test -race ./...`).
- Deterministic provider (fake engine) fault suite: restart at every stage,
  stale lease, duplicate suppression, provider failure matrix, verify-fail →
  repair, repair exhaustion, review rejection, cancel, estop, result-branch
  invariants — all green (`internal/daemon/pipeline_fault_*_test.go`).
- Authenticated OpenCode: phases F/G/H/I above.
- Independent reviews: security review (0 blocking), architecture review
  (0 blocking, 3 high → all remediated), remediation re-review (accepted),
  final acceptance review (15/15 YES).

## Known limitations (non-blocking for personal self-hosted use)

- The agent (and reviewer) run unsandboxed as the user; the worktree is an
  organizational boundary, not a security boundary (see README "Security model").
- Auto-commit sweeps the whole worktree (`git add -A`); build artifacts the
  agent leaves behind can be committed (observed: a compiled `fixture`
  binary in one fixture result commit).
- `--wait-timeout` expiry still cancels the run daemon-side; a true
  CLI-detach model is future work.
- First-try reliability depends on provider health: the self-hosting task
  succeeded on its third submission after honest failure/cancellation of the
  first two (infrastructure causes, fixed).
- `IsRetryable` coarsely duplicates retry-policy knowledge owned by
  `protocol.FailureClassification` (flagged by the live reviewer; the method
  is currently informational).
- Multi-tenant use unsupported; macOS untested; WSL2 not CI-covered.
- Failed/cancelled runs and their workspaces are retained by design
  (auditable history).

## Submitting the next task

From any git repository checkout:

```sh
/home/ogdan/code/neuroforge/forge run --verbose \
  "Describe the change, constraints, and objective acceptance criteria."
```

Observe: `forge pipeline status <task-id>` · Cancel: `forge pipeline cancel
<task-id>` · Emergency stop: `forge estop on|off|status`.
