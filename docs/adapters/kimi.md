# Kimi Code adapter

In-process coding-agent adapter for the **Kimi Code** CLI (`kimi`), implementing
the 13-method `codingagent.Adapter` surface (spec §12.2, AC-5). Lives in
[`internal/adapter/codingagent/kimi`](../../internal/adapter/codingagent/kimi).
It is an in-process Go adapter (ADAPTER_DEV_GUIDE "Path 3") — no self-registration;
construct it with `kimi.New(opts)` and register it explicitly if desired.

```go
import (
    "neuroforge/internal/adapter/codingagent"
    "neuroforge/internal/adapter/codingagent/kimi"
)

reg := codingagent.Default()
reg.MustRegister(kimi.New(kimi.Options{}), 50)
```

## Detection

`Detect` resolves the `kimi` executable with `exec.LookPath`, which honours
`PATHEXT` on Windows — so `kimi.exe`, `kimi.cmd`, `kimi.bat` and npm-style shims
are all found, and paths containing spaces or Unicode characters work. It then
runs `kimi --version` (with a PATH-only environment, never ambient credentials)
and parses the first `X.Y[.Z]` tuple.

| `--version` output      | parsed version |
|-------------------------|----------------|
| `Kimi Code 1.4.0`       | `1.4.0`        |
| `kimi v2.0.1`           | `2.0.1`        |
| `1.3`                   | `1.3.0`        |
| *(no version found)*    | unknown        |

An unrecognised/missing version degrades gracefully: the adapter still reports
`Installed` and attempts a run with the baseline (headless + model selection)
capability profile.

The adapter also best-effort probes `kimi --help` to record which flags the
installed version advertises (`--output`, `--model`, `--continue`, `--max-turns`).
When the probe succeeds it is authoritative; otherwise the version-gated defaults
below apply.

## Command shape

The headless argv is built deterministically by a pure function (`buildArgv`) —
**no shell, no globbing, no `/bin/sh`**. It is passed straight to `exec` as an
argv slice, so spaces/quotes/Unicode in the workspace or prompt need no escaping.

```
kimi -p <prompt>
     [--output stream-json]      # when streaming is supported
     [--model <id>]              # when a model is requested and supported
     [--max-turns <n>]           # when req.TurnLimit > 0 and supported
     [--continue <session-id>]   # on Resume, when session resume is supported
     [Options.ExtraArgs...]
```

`cmd.Dir` is the run's isolated worktree; `cmd.Env` is the allowlisted set (see
[Security](#security)).

## Capabilities by version

Derived from the detected version (see `versionProfile`). The thresholds are
conservative defaults; override per-deploy with `Options.Capabilities` (merged as
a union — a caller can only *add* capability) or `Options.ForceStreaming`.

| Capability              | Earliest version | Notes                                            |
|-------------------------|------------------|--------------------------------------------------|
| `HeadlessMode`          | always           | `-p` non-interactive mode                        |
| `ModelSelection`        | always           | `--model`                                        |
| `StructuredOutput`      | always           | parseable file/command events                    |
| `UsageReporting`        | 1.1.0            | `usage.updated` events                           |
| `StreamingEvents`       | 1.2.0            | `--output stream-json`                           |
| `CachedUsageReporting`  | 1.2.0            | cache-read/write token accounting                |
| `SessionResume`         | 1.3.0            | `--continue`                                     |

Explicitly **off** (not faked): `InteractiveMode`, `ImageInput`,
`LiveUserMessages`, `ToolPermissions`, `NativeSandbox`, `MCP`, `ACP`.

## Streaming output & parsing

Stdout is read line-by-line with a scanner that has **no 64 KiB token cap**, and a
UTF-8 BOM at the start of the stream is stripped. CRLF and per-line BOMs are
tolerated. Each line is parsed as follows:

1. **Fast path:** if the line already speaks the NeuroForge normalized event
   format, `protocol.ParseEventLine` accepts it directly (engines that emit
   normalized JSONL work unchanged).
2. **Kimi translation:** otherwise the line is decoded as a Kimi stream-json item
   and mapped to a normalized event (see table below).
3. **Recoverable warning:** unknown/malformed lines become a non-fatal `warning`
   event carrying the raw bytes; the offending line is also saved to the
   artifacts dir. A run is **never** aborted by a parse problem.

| Kimi stream-json item                                       | Normalized event       |
|-------------------------------------------------------------|------------------------|
| `{"type":"system","event":"init",...}`                      | `run.started`          |
| `{"type":"system","event":"resume",...}`                    | `run.resumed`          |
| `{"type":"assistant","event":"text","text":...}`            | `message.delta`        |
| `{"type":"assistant","event":"tool_use",...}`               | `tool.started`         |
| `{"type":"user","event":"tool_result",...}`                 | `tool.completed`       |
| `{"type":"assistant","event":"command","exit_code":N}`      | `command.completed`    |
| `{"type":"file","path":...,"action":...}`                  | `file.changed`         |
| `{"type":"usage","input_tokens":...}`                      | `usage.updated`        |
| `{"type":"result","event":"success"}`                       | `run.completed`        |
| `{"type":"result","event":"error","class":...}`             | `run.failed`           |

If the engine does not support streaming, stdout plain text is wrapped into a
`run.started` → `message.completed` → terminal sequence.

### Resume

`Resume` adds `--continue <session-id>` only when the version supports it
(capability-gated). A resumed run surfaces `run.resumed` first; if the engine
emits a plain `init`, the adapter remaps the opening event to `run.resumed`.

## Usage fields & quota confidence

Usage is mapped to `protocol.UsagePayload` from Kimi's `usage`/`result` items.

- Token counts (`input_tokens`, `output_tokens`, `cache_read_tokens`,
  `cache_write_tokens`) and `cost` → `QuotaConfProviderReported`.
- **Nothing reported → `QuotaConfUnknown`**; the adapter never fabricates numbers
  or overstates precision (rule §36.10).
- **Thinking/reasoning tokens:** Kimi may report `reasoning_tokens`, but
  `UsagePayload` has no reasoning axis. These are dropped (never mislabelled); the
  confidence stays `PROVIDER_REPORTED` for the axes that *are* reported.
- `InspectQuota` returns `UNKNOWN` — there is no offline Kimi quota probe.

## Failure mapping

`ClassifyFailure` honours an explicit terminal failure event first, then refines
Kimi-specific stderr signals the shared heuristic misses, then defers to
`codingagent.DefaultClassify`. Every class maps to a **bounded** policy (rule §32:
no infinite retry).

| Signal (event class / stderr text)        | §32 class               | Policy     |
|-------------------------------------------|-------------------------|------------|
| `quota` / `exhausted` / `limit exceeded`  | `PROVIDER_QUOTA`        | failover   |
| `rate limit` / `429`                      | `PROVIDER_RATE_LIMIT`   | cooldown   |
| `overloaded` / `capacity` / `503`/`529`   | `PROVIDER_CAPACITY`     | cooldown   |
| `unauthorized` / `401` / `api key`        | `PROVIDER_AUTH`         | failover   |
| `model ... not available`                 | `MODEL_NOT_AVAILABLE`   | failover   |
| `timeout` / `timed out`                   | `TIMEOUT`               | retry      |
| exit code ≥ 128 (signal/crash)            | `ENGINE_CRASH`          | retry      |
| caller `Cancel()`                         | `CANCELLED`             | terminal   |
| `req.Timeout` exceeded                    | `TIMEOUT` (run.failed)  | retry      |
| *(unrecognized non-zero exit)*            | `MALFORMED_OUTPUT`      | retry      |

## Timeout & cancellation

- `Cancel()` terminates the **entire process group** (via the shared `proctree`
  package: Windows `CREATE_NEW_PROCESS_GROUP` + `taskkill /T /F`; unix `setpgid` +
  negative-pgid signal) and emits `run.cancelled`.
- `req.Timeout` (when > 0) is a hard wall-clock limit enforced with a timer; on
  expiry the group is killed and the run ends with `run.failed(TIMEOUT)`.
- The blocking stdout read runs in a goroutine, so cancellation/timeout always
  preempt it — a run never hangs the supervisor forever.

## Security

- **Allowlisted environment only** (spec §29.2, AC-28). The agent process
  receives just `PATH`/`HOME`/`USERPROFILE`/`USER`/`LANG`/`LC_ALL`/`TERM` and OS
  essentials (`SystemRoot`/`TEMP`/`TMP`) copied from the host, plus the caller's
  explicit `req.AllowlistEnv` and the adapter's `Options.ExtraEnv`. The **entire**
  host environment is never forwarded — so VCS merge tokens, production
  credentials, unrelated API keys and the daemon auth token can never reach the
  agent.
- **Isolated home:** Kimi's config/home is relocated to a per-run directory
  rooted in the run workspace (`.neuroforge-kimi`), via `KIMI_HOME` (override with
  `Options.HomeEnvName`). A run never reads or writes the user's global Kimi
  profile. (`Options.DisableIsolation` skips this for diagnostics — not
  recommended.)
- **Secret redaction:** captured stderr is scrubbed of credential-shaped
  substrings (`sk-…`, `Bearer …`, `Authorization: …`, `api_key=…`, `token=…`,
  …) before it appears in events or logs.
- Session archives / diagnostic logs are never written outside the workspace.

## Windows notes

- Detection uses `exec.LookPath` with `PATHEXT`, so `.exe`/`.cmd`/`.bat` and npm
  shims resolve correctly; paths with spaces and Unicode work.
- Output is argv-only (no shell quoting); CRLF JSONL and a leading UTF-8 BOM are
  handled.
- Env keys are deduplicated case-insensitively (`PATH`/`Path`).
- Cancellation uses the shared `proctree` Windows path (`taskkill /T /F`), so
  descendants are never orphaned. No Unix signals, no `/bin/sh`, no negative PIDs,
  no hardcoded `/tmp` (`os.TempDir()` is used).

## Testing

- **Unit tests** cover detection (PATH, `.cmd`/`.bat` shims, `PATHEXT` ordering,
  spaces, Unicode), version parsing, command-builder determinism, the parser
  (well-formed/malformed/unknown/CRLF/BOM/no-64KiB-cap), usage mapping + confidence,
  failure classification per class, cancellation, timeout, resume and secret
  redaction.
- **Conformance:** `conformance_test.go` wires the real adapter into the §13.3
  suite. The adapter code under test is genuine; only the CLI binary is swapped
  for the deterministic, offline `kimistub` (rule §36.5: no paid calls). All nine
  checks pass (handshake, version_compatibility, event_ordering, malformed_output,
  cancellation, timeout, quota_failure, resume, process_crash).
- **Smoke test:** `smoke_test.go` is build-tagged `//go:build kimismoke` and is
  therefore **never compiled or run** in normal/CI runs. Run it explicitly against
  a real Kimi Code install:
  `go test -tags kimismoke -run TestKimiSmoke ./internal/adapter/codingagent/kimi/...`

## Explicitly NOT implemented (spec §36.25)

These are deliberately absent (not disguised as finished stubs):

- **Dynamic model discovery** from the engine (`kimi models` or similar).
  `ListModels` returns the configured or a minimal opaque catalogue.
- **`LiveUserMessages`** — headless `-p` mode has no live message channel;
  `SendMessage` returns an explicit error.
- **Quota probe** — `InspectQuota` returns `UNKNOWN` (no offline probe); quota is
  surfaced per-run via usage events.
- **Thinking/reasoning-token accounting** — no field exists in the protocol; the
  values are dropped rather than mislabelled.
- **Session archive / diagnostic-log export** — never emitted unless the run's
  policy explicitly allows it, and never written outside the workspace.
