# Codex coding-agent adapter (AC-5, M4)

In-process Go adapter for the **Codex CLI**, implemented at
`internal/adapter/codingagent/codex/` as a `codingagent.Adapter` (ADAPTER_DEV_GUIDE,
"Path 3"). It speaks the versioned coding-agent protocol at
`protocol.ProtocolVersion == 1` and does **not** self-register: the daemon
constructs it via `codex.New(opts)` and registers it with a
`codingagent.Registry` at startup.

```go
import "neuroforge/internal/adapter/codingagent/codex"

a := codex.New(codex.Options{ArtifactsDir: artifactsDir})
registry.MustRegister(a, 100)
```

## Design principles

- **No schema pinning.** Codex's JSONL event shape varies across releases. The
  parser (`parseCodexLine`) probes/tolerates the shape rather than hard-coding one
  version's fields. Anything it cannot map is forwarded as a recoverable
  `warning` carrying the raw bytes (spec: unknown/malformed events never abort a
  run). Unknown future fields are ignored (additive tolerance).
- **No paid calls in tests (rule §36.5).** Unit + conformance tests drive the
  adapter through a deterministic `Runner` seam with recorded byte-stream
  fixtures. A real Codex is exercised only by the opt-in smoke test
  (`//go:build codexsmoke`, env-guarded).
- **No hard-coded model names (rule §36.8).** `ListModels` returns no
  descriptors; the router supplies the model via `AgentRunRequest.Model`.
- **Explicitly-marked unimplemented features (rule §36.25).** See
  [Explicitly not implemented](#explicitly-not-implemented).

## Detection

`Detect(ctx)` resolves the Codex binary with `exec.LookPath("codex")` and runs
`codex --version`. The result is cached for the adapter's lifetime.

- On Windows, `exec.LookPath` honours `PATHEXT`, so `.exe`, `.cmd`, `.bat` and
  PowerShell shims are found transparently. npm global installs surface as
  `codex.cmd` shims and are detected the same way.
- Paths containing spaces and Unicode characters are handled (argv-only process
  spawn — no shell, no quoting).
- A binary that resolves but cannot be launched (e.g. not executable) is reported
  as not-installed with a diagnostic, never as a panic.
- An unparsable version string is still reported as installed; capabilities are
  then conservative (features the adapter cannot confirm are reported `false`).

## Command shape

Fresh runs use the headless entrypoint `codex exec`. The argv is built
deterministically by `buildExecArgv` (pure, unit-tested) — it is **argv-only**,
never a shell string:

```
<codex> exec \
    --sandbox workspace-write \
    --ask-for-approval never \
    [--resume <session>]      # resume only
    [--model <model>]         # only when a model is supplied
    [<prompt>]                # final positional; omitted when no prompt
```

- `Options.ExecArgs` customises the sandbox/approval flags (inserted between
  `exec` and the model selector). The default (`DefaultExecArgs`) selects a
  workspace-write sandbox and disables interactive approval (the run is
  autonomous and cannot answer prompts).
- **The adapter never enables a privilege-bypass / "danger-full-access" / YOLO
  mode.** The default set omits it and there is no option that turns it on.
- Resume re-attaches via `--resume <session-id>` (version-gated; see below).
- The prompt is the final positional. When `AgentRunRequest.PromptFile` is set its
  contents are read and used as the prompt; otherwise `Prompt` is used. An empty
  prompt is translated faithfully (no positional) — the supervisor always
  supplies one for real runs.

## Capabilities (version-gated)

`Capabilities(ctx)` derives the profile from the detected version. Detected
versions report:

| Capability             | Value    | Notes                                                |
|------------------------|----------|------------------------------------------------------|
| `HeadlessMode`         | true     | `codex exec`                                         |
| `InteractiveMode`      | true     | Codex supports interactive mode (unused by the daemon)|
| `StreamingEvents`      | true     | JSONL event stream                                   |
| `StructuredOutput`     | true     | writes files/diffs, runs commands                    |
| `ModelSelection`       | true     | `--model`                                            |
| `NativeSandbox`        | true     | `--sandbox`                                          |
| `ToolPermissions`      | true     | `--ask-for-approval`                                 |
| `UsageReporting`       | true     | `token_count` / usage events                         |
| `CachedUsageReporting` | true     | `cached_input_tokens` when Codex emits it            |
| `SessionResume`        | gated    | true for detected versions ≥ 0.1 (see note)          |
| `LiveUserMessages`     | false    | headless exec has no live chat channel               |
| `ImageInput`           | false    | not confirmed offline                                |
| `MCP` / `ACP`          | false    | not confirmed offline                                |

> **Session-resume assumption (deviation from strict offline confirmation).**
> `SessionResume` is reported `true` for any positively-detected version, on the
> assumption that `codex exec --resume` has been available since 0.1. The adapter
> cannot confirm this without a real run; the assumption is documented here
> rather than disguised as verified (rule §36.25). If a future Codex removes
> `--resume`, the smoke test will surface it. `RunHandle.SessionID` is populated
> only when this capability is `true` and Codex actually emits a session id.

## Session handling

When `SessionResume` is `true`, `Start` briefly observes the live stream (bounded
by `bootstrapTimeout`, 3s) to capture the session/thread id Codex emits and sets
`RunHandle.SessionID`. If no id appears, `SessionID` stays empty. `Resume`
re-attaches to the supplied `SessionID` via `--resume`.

## Usage accounting

`mapUsage` maps Codex usage/token events onto `protocol.UsagePayload`:

- `input_tokens` / `prompt_tokens` → `InputTokens`
- `output_tokens` / `completion_tokens` → `OutputTokens`
- `cached_input_tokens` / `cached_read_tokens` → `CacheReadTokens`
- `cache_write_tokens` → `CacheWriteTokens`
- `cost_usd` / `cost` → `Cost` (Currency `USD` when present)
- `reasoning_tokens` is recognized but **not** folded into `OutputTokens` (there
  is no reasoning field on the payload and the adapter must not double-count or
  overstate — rule §36.10). It is surfaced as part of the raw event for
  diagnostics.

Confidence (rule §36.10 — never overstate precision):

- Any reported token count → `PROVIDER_REPORTED`.
- A usage event with no numeric field → `UNKNOWN`, all counters zero (never
  fabricated).

## Failure mapping (§32)

`ClassifyFailure` prefers `codingagent.DefaultClassify` and refines Codex-specific
signals `DefaultClassify` does not cover:

| Signal (stderr/exit)                                | Class                       |
|-----------------------------------------------------|-----------------------------|
| `overloaded`, `capacity`, `503`                     | `PROVIDER_CAPACITY`         |
| `not logged in`, `codex login`, `no api key`        | `PROVIDER_AUTH`             |
| `billing`, `insufficient_quota`, `plan limit`       | `PROVIDER_QUOTA`            |
| `deprecat`, `model does not exist`, `no such model` | `MODEL_NOT_AVAILABLE`       |
| `quota`, `exhausted`, `limit exceeded`              | `PROVIDER_QUOTA`            |
| `429`, `rate limit`, `too many requests`            | `PROVIDER_RATE_LIMIT`       |
| `unauthorized`, `401`, `invalid api key`            | `PROVIDER_AUTH`             |
| exit `124` / `137`                                  | `TIMEOUT`                   |
| exit ≥ `128`                                        | `ENGINE_CRASH`              |
| typed `run.failed` event                            | honours the event's class   |
| otherwise non-zero                                  | `MALFORMED_OUTPUT` (default)|

Every class maps to a bounded policy via `protocol.DefaultPolicy` — no class
produces an unbounded retry (rule §32). Timeout (via `req.Timeout`) emits
`run.failed(TIMEOUT)`; caller `Cancel()` emits `run.cancelled`.

## Process execution & cancellation

- Codex is spawned in its own process group via `proctree.NewGroupCommand`. On
  Windows this uses `CREATE_NEW_PROCESS_GROUP`; `Cancel` tears the whole tree
  down via `taskkill /T /F` (spec: cancellation ends the whole process group,
  never orphaning descendants).
- The agent process receives an **allowlisted** environment only (§29.2, AC-28):
  `PATH/HOME/USER/USERPROFILE/LANG/LC_ALL/TERM/SystemRoot/TEMP/TMP/...` plus
  `AgentRunRequest.AllowlistEnv`. Forbidden prefixes (`GITHUB_TOKEN`,
  `OPENAI_API_KEY`, `NEUROFORGE_DAEMON_TOKEN`, …) are rejected even if they appear
  in the allowlist by mistake (defence-in-depth; mirrors
  `internal/supervisor.ForbiddenEnvPrefixes`).
- stdout is read line-by-line with no 64KiB cap (large diff events are handled).
- Malformed/unknown lines are redacted and persisted to `Options.ArtifactsDir`
  (default `os.TempDir()`) as recoverable artifacts.

## Secret redaction

Captured stderr and persisted artifacts are passed through `redact`, which
strips `sk-…`, `ghp_…`, `bearer <token>`, and `…KEY=…` / `…TOKEN=…` shapes,
replacing them with `[REDACTED]`. This is defence-in-depth on top of the
allowlisted environment.

## Windows notes

- Detection: `exec.LookPath` + `PATHEXT`; `.exe`/`.cmd`/`.bat` and npm shims.
- Spawn: `CREATE_NEW_PROCESS_GROUP`; cancellation via `taskkill /T /F`.
- Stream parsing tolerates CRLF line endings and a leading UTF-8 BOM.
- argv-only: no shell quoting, no `/bin/sh`; spaces and Unicode paths are safe.
- Env keys are de-duplicated case-insensitively.
- Temp paths use `os.TempDir()` (no hard-coded `/tmp`); artifact files are joined
  with `filepath.Join`.
- No Unix signals, no negative PIDs, no assumptions about Unix permission bits.
- Process-tree management is delegated entirely to the shared `proctree` package.

## Explicitly not implemented

These are **not** provided and are marked rather than disguised (rule §36.25):

- **Model catalogue.** `ListModels` returns an empty slice. The Codex catalogue
  is account/provider-dependent and cannot be enumerated offline without a paid
  call (§36.5) or hard-coding model names (§36.8). Routing uses provider-configured
  ids via `AgentRunRequest.Model`.
- **Live quota.** `InspectQuota` reports `UNKNOWN`. Codex exposes no offline quota
  signal; quota is observed via `usage.updated` events during runs.
- **Authenticated health.** `Health` is an offline heuristic: it reports `down`
  when not installed, `degraded` when an auth signal appears in the version probe,
  else `ok`. A genuine authenticated-state check is deferred to the opt-in smoke
  test.
- **Live user messages.** `SendMessage` is unsupported (`LiveUserMessages=false`);
  a headless `codex exec` run is autonomous.
- **Image input / MCP / ACP** capabilities are not claimed (version/provider-
  dependent; not confirmable offline).
- **Interactive approval.** The default approval policy is `never` for autonomy;
  the adapter does not drive an interactive approval UI.

## Testing

- Unit tests cover every capability: detection (missing / `.cmd` / `.bat` / `.exe`
  / shim / PATHEXT / Unicode / spaces / caching), version parse, command-builder
  determinism, parser (well-formed / malformed / partial-line / unknown-event /
  CRLF / BOM / no-64KiB-cap), usage mapping + confidence, failure classification
  per class, cancellation kills the group, timeout, session extraction, secret
  redaction, Windows path handling, env allowlist + AC-28.
- `conformance_test.go` runs the full §13.3 conformance suite through the
  deterministic `Runner` seam (all nine checks pass offline; no real Codex, no
  paid call).
- `smoke_test.go` (`//go:build codexsmoke`, env-guarded) exercises a real Codex.
