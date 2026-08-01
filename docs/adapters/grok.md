# Grok Build coding-agent adapter (AC-5, M5)

This documents the in-process NeuroForge adapter for **Grok Build**, implemented
at `internal/adapter/codingagent/grok/` as a "Path 3 — in-process Go adapter"
(see [ADAPTER_DEV_GUIDE.md](../architecture/ADAPTER_DEV_GUIDE.md)). It implements
every method of `codingagent.Adapter` at coding-agent **protocol version 1**
without modifying the shared protocol package.

The adapter never makes paid API calls itself; all billing happens inside the
spawned Grok process against the resolved account (rule §36.5). It is **not**
self-registered — expose it via `grok.New(opts)` and register with the daemon's
registry at wiring time.

## Detection

`Detect` resolves the Grok binary via `os/exec.LookPath` and probes
`grok --version`:

- Bare names, absolute paths, and paths with spaces or Unicode
  (`café ☕ dir`) all resolve.
- The parsed version is cached on the adapter so `Capabilities`, `Version` and
  `Health` stay consistent for the process.
- A missing binary reports `Installed: false` (never an error); a binary that
  runs but exits non-zero is still "installed" but `Health` reports `degraded`.

## Command shape (deterministic argv builder)

`Start` spawns the headless CLI in its own process group
(`proctree.NewGroupCommand`) with `cmd.Dir = workspace` and an allowlisted
environment. The argv is built deterministically from the request + capabilities
(argv-only — never a shell string, never `/bin/sh`, so spaces/Unicode in the
workspace or prompt are passed verbatim):

```
<grok>
  --no-auto-update                       # rule §36.19: never update mid-run
  -p                                      # headless / non-interactive print mode
  --output-format streaming-json          # incremental structured output
  [--model <model>]                       # when ModelSelection && req.Model
  [--resume <session-id>]                 # when SessionResume && req.SessionID
  [--max-turns <n>]                       # ONLY when Options.EnableTurnLimit
  [<prompt-file> | <inline-prompt>]       # positional; PromptFile wins
```

The workspace is mapped to the child's working directory (`cmd.Dir`), which is
the universal, cross-platform mapping of the isolated worktree (spec §17).

## Environment (spec §29.2, AC-28)

The child receives **only**:

- the allowlisted base keys: `PATH`, `HOME`, `USERPROFILE`, `USER`, `LANG`,
  `LC_ALL`, `TERM`, `SystemRoot`, `TEMP`, `TMP`;
- the caller's per-request `AllowlistEnv` entries (`KEY` copied from the current
  env, or `KEY=VAL` verbatim);
- `Options.ExtraEnv` (a **test-only** hook the daemon must never populate with
  secrets).

VCS merge tokens, the daemon auth token, production credentials and unrelated
API keys are never in the allowlist and are therefore never inherited.

## Streaming parser

The adapter reads stdout line-by-line with a scanner that has **no 64 KiB cap**
(long message deltas are reassembled across fragments), tolerates CRLF and strips
a leading UTF-8 BOM. Each line is mapped from Grok's `streaming-json` item shape
to the §12.4 normalized event set:

| Grok streaming-json item                                  | Normalized event(s)                  |
|-----------------------------------------------------------|--------------------------------------|
| `{"type":"system","subtype":"init","session_id":...}`     | — (session metadata; `run.started` is synthesized by the adapter) |
| `{"type":"message","delta":...}` / `{"type":"text",...}`  | `message.delta`                      |
| `{"type":"message","text":...}`                           | `message.completed`                  |
| `{"type":"tool","status":"running\|completed",...}`       | `tool.started` / `tool.completed`    |
| `{"type":"command","status":"running\|completed",...}`    | `command.started` / `command.completed` |
| `{"type":"file","path":...,"action":...}`                 | `file.changed` (in_scope from `Scope`) |
| `{"type":"usage",...}`                                    | `usage.updated`                      |
| `{"type":"checkpoint",...}`                               | `checkpoint.created`                 |
| `{"type":"approval",...}`                                 | `approval.requested`                 |
| `{"type":"result","status":"completed",...}`              | `message.completed` + `run.completed`|
| `{"type":"result","status":"failed","error_code":...}`    | `run.failed` (class from `error_code`) |
| `{"type":"error","fatal":true,...}`                       | `run.failed`                         |
| `{"type":"error",...}` (non-fatal)                        | `warning`                            |

**Robustness contract.** Unknown top-level `type` values and malformed JSON
**never abort a run**: they are surfaced as a recoverable `warning` event and the
raw bytes are persisted to the artifacts directory (`Options.ArtifactsDir`,
default `os.TempDir()`). Unknown fields inside a known item are ignored (standard
JSON decoding). If the process exits without emitting a terminal item, the
adapter synthesizes `run.completed` (exit 0) or `run.failed` (non-zero,
classified — see below). The opening `run.started` (or `run.resumed` for
`Resume`) is always synthesized first so consumers always see a well-ordered
stream even if Grok emits no such item.

> **Note (rule §36.25).** The exact Grok `streaming-json` schema is not fully
> documented upstream. The table above is the set of item shapes this adapter
> understands, derived from the headless streaming behaviour and analogous agent
> CLIs. It is an **assumption**, marked explicitly: the parser degrades
> gracefully on anything not modelled here (unknown → warning + saved artifact,
> never fatal). Tighten the mapping once the installed CLI's schema is confirmed.

## Capabilities by version

`Capabilities` derives a version-gated profile (spec §12.3). Defaults:

| Capability              | Value | Basis |
|-------------------------|-------|-------|
| `HeadlessMode`          | true  | `grok -p` headless mode |
| `StreamingEvents`       | true  | `--output-format streaming-json` |
| `StructuredOutput`      | true  | structured streaming items |
| `ModelSelection`        | true  | `--model` |
| `UsageReporting`        | true  | `usage` items |
| `CachedUsageReporting`  | version-gated (≥ `0.1.0`, **assumed**) | `cache_read_tokens` / `cache_write_tokens` |
| `SessionResume`         | version-gated (≥ `0.1.0`, **assumed**); `Options.ResumeEnabled` overrides | `--resume` |
| `ACP`                   | `Options.EnableACP` (default false) | optional; does **not** alter protocol v1 |
| `ImageInput`            | false | coding/image providers are separate (rule §36.9) |
| `LiveUserMessages`      | false | **not implemented** — headless `-p` has no stdin message channel |
| `NativeSandbox` / `ToolPermissions` / `MCP` | false | not assumed |

The version thresholds (`minVersionSessionResume`, `minVersionCachedUsage`) are
**assumed** (rule §36.25) and conservative; override or tighten once the
installed CLI's feature flags are confirmed.

## Usage mapping + confidence

`usage` items are mapped to `protocol.UsagePayload` (spec §22, §14.4, rule
§36.10: never overstate precision):

- tokens present → `PROVIDER_REPORTED` (with cost when reported, `USD` default);
- no token data → `UNKNOWN`;
- **never** `EXACT` — Grok exposes no authoritative remaining-quota figure, only
  usage tokens. A cost is never fabricated.

`InspectQuota` reports `UNKNOWN` for the same reason.

## Failure mapping (§32)

`ClassifyFailure` defers to the shared `codingagent.DefaultClassify`, refined for
Grok-specific stderr signals. Distinct §32 classes are kept distinct:

| Signal (stderr / error_code)                  | Class                  | Policy (DefaultPolicy)        |
|-----------------------------------------------|------------------------|-------------------------------|
| quota / exhausted / billing / credit          | `PROVIDER_QUOTA`       | failover                      |
| 429 / rate limit / too many requests          | `PROVIDER_RATE_LIMIT`  | cooldown + retry              |
| overloaded / capacity / 503 / 502             | `PROVIDER_CAPACITY`    | cooldown + retry/failover     |
| 401 / unauthorized / invalid api key / auth   | `PROVIDER_AUTH`        | failover (no auto-retry)      |
| model not available / not found               | `MODEL_NOT_AVAILABLE`  | failover                      |
| exit ≥ 128 (signal death, e.g. 134)           | `ENGINE_CRASH`         | bounded retry                 |
| exit 124 (timeout) / wall-clock `Timeout`     | `TIMEOUT`              | bounded retry                 |
| user `Cancel()`                               | `CANCELLED`            | terminal                      |
| non-zero exit, no signal                      | `MALFORMED_OUTPUT`     | bounded retry                 |

Capacity is **never** lumped into quota or rate-limit. No class maps to an
unbounded retry (rule §32). `error_code` values on `result`/`error` items map to
the same taxonomy. Failure reasons are run through secret redaction before
reaching events/logs.

## Timeout & cancellation

- `req.Timeout` is enforced via the run context; on expiry the whole group is
  killed and `run.failed(TIMEOUT)` is emitted.
- `Cancel()` cancels the context and terminates the entire process group via
  `proctree.KillGroup` (negative-pgid signal).
  `run.cancelled` is emitted. The blocking stdout read runs in its own
  goroutine so cancellation/timeout never block forever on a pipe read.
- Descendants are never orphaned (proctree).

## Platform notes

The adapter targets Linux: plain `PATH` lookup, spaces and Unicode in
binary/workspace paths, CRLF JSONL, UTF-8 BOM, argv-only (no shell quoting),
and process-group cancellation via the shared `proctree` package. On a Windows
host, run it inside WSL2 (see `docs/platforms/WSL2.md`).

## Secret redaction

Failure reasons and warning messages are scrubbed of common credential shapes
(Bearer tokens, `api key …`, `token=…`, `sk-…`, `ghp_…`, Slack tokens, long
hex/base64 blobs) before they reach events or logs. Captured stderr is held in
memory only for classification and is never persisted verbatim.

## Testing

- **Unit tests** (`*_test.go` in the package) cover every capability:
  detection (missing/shim/Unicode+spaces), version parse,
  command-builder determinism + gating, parser (well-formed/malformed/partial /
  unknown-event/CRLF/BOM/all item types/scope), usage mapping + confidence,
  failure classification per class (incl. capacity distinct), cancellation kills
  the group, timeout, resume, secret redaction, path handling, env
  allowlist, concurrency.
- **Conformance wiring** (`conformance_test.go`) runs `conformance.Suite.Run`
  against the adapter wired to a test-only stub binary
  (`internal/adapter/codingagent/grok/internal/stub`) that emulates Grok's
  streaming-json for each `fake.Scenario`. This exercises the adapter's real
  code paths with no paid calls; all nine checks pass. The stub is a fixture,
  not the real CLI.
- **Opt-in smoke test** (`smoke_test.go`, build tag `groksmoke`) probes a real
  installed Grok CLI and is skipped in every normal/CI run. Run with
  `go test -tags groksmoke -run TestGrokSmoke -v ./internal/adapter/codingagent/grok/...`
  (set `GROK_SMOKE_RUN=1` to also start a single short headless run).

## Explicitly not implemented / pending confirmation (rule §36.25)

- **`SendMessage` / `LiveUserMessages`**: headless `-p` mode has no stdin
  message channel. `SendMessage` returns `ErrLiveMessagesUnsupported` and the
  capability is `false`. Deferred until a Grok surface for live messages exists.
- **`ListModels`**: returns a single opaque placeholder descriptor
  (`grok/default`); no real model names are hard-coded (rule §36.8). Replace once
  a `grok models` listing surface is confirmed.
- **`InspectQuota`**: reports `UNKNOWN` — Grok exposes no authoritative quota
  probe (rule §36.10).
- **Streaming-json schema**: the item shapes in the table above are assumed,
  pending confirmation against the installed CLI; the parser degrades safely on
  unknown items.
- **Version-gated thresholds** (session resume, cached usage) and the turn-limit
  flag (`--max-turns`) are assumed / opt-in (`Options.EnableTurnLimit`, default
  off so an unknown flag cannot break a real run).
