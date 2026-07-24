# Gemini CLI coding-agent adapter

Status: **implemented** (AC-5, M4). Package:
[`internal/adapter/codingagent/gemini`](../../internal/adapter/codingagent/gemini).

This is an in-process Go adapter ("Path 3" of the
[Adapter Dev Guide](../architecture/ADAPTER_DEV_GUIDE.md)). It implements the
full 13-method [`codingagent.Adapter`](../../internal/adapter/codingagent/adapter.go)
surface for the [Google Gemini CLI](https://github.com/google-gemini/gemini-cli)
without self-registering; callers construct it with
[`gemini.New`](../../internal/adapter/codingagent/gemini/options.go) and register
it into a [`codingagent.Registry`](../../internal/adapter/codingagent/registry.go)
at the daemon wiring site.

Protocol version: **1** (`protocol.ProtocolVersion`). The adapter never modifies
shared protocol/adapter code.

---

## Detection

`Detect` resolves the Gemini CLI on `PATH` via a PATHEXT-aware search
(`gemini.lookPath`) that:

- searches each `PATH` directory, appending each `PATHEXT` extension on Windows
  (`.COM;.EXE;.BAT;.CMD;…`) in declared order;
- tolerates spaces and Unicode in `PATH` entries (Go paths are UTF-8);
- **skips `.ps1`** (PowerShell-only) shims: npm installs both `gemini.cmd` and
  `gemini.ps1`, but only the `.cmd` shim is spawnable via `CreateProcess`;
- on Unix, checks the executable bit rather than extensions.

It then probes `<binary> --version` to record the engine version. A binary that
exists but fails `--version` is still reported `Installed` with the error in
`Detail` (so `forge agent doctor` surfaces the diagnostic rather than treating
the engine as absent).

`Health`: `down` when the CLI is absent, **`unknown`** when installed. The Gemini
CLI exposes no offline, reliable account/auth signal; determining OAuth vs API
key vs Vertex would require a paid call (rule §36.5) or brittle config sniffing,
so the adapter never guesses (§36.10).

## Version

`gemini --version` prints a bare semver (observed: `0.23.0`). `parseGeminiVersion`
extracts the first `major.minor.patch` token anywhere in the output, tolerating
package-name prefixes (`@google/gemini-cli/0.23.0`) and surrounding whitespace.
`Version().ProtocolVersion` is always `1`.

## Command shape (deterministic, argv-only)

The headless invocation is built by `buildRunSpec` — a pure function of the
[`AgentRunRequest`](../../internal/adapter/codingagent/protocol/request.go). It
is **argv-only** (never a shell string, never `/bin/sh`):

```
<binary> -p "<prompt>" -o json [-m <model>] [Options.ExtraArgs...]
```

Safe-default profile (never weakened by the adapter):

- `--output-format json` (a single final JSON response document).
- Model `-m` only when `req.Model` is set (rule §36.8 — never hard-codes a model).
- **Never** adds `--yolo` / `-y` / `--approval-mode yolo` / `--all-files` or any
  unrestricted mode. Any scope expansion must come from the request, not the
  adapter defaults (spec §29, task constraint).
- `TurnLimit` is not mapped: the Gemini CLI has no stable headless turn-limit
  flag (explicitly not implemented, §36.25).

**Prompt source:**

- `req.Prompt` (inline) → passed via `-p`.
- `req.PromptFile` → piped to the child's **stdin** (the `-p` flag is omitted) so
  large context packs never overflow argv and never touch a shell.

## Capabilities (version-gated)

Derived by `capabilitiesFor`. The adapter drives the one-shot
`-p … --output-format json` mode, which emits a single final JSON response. The
profile is conservative — only what the adapter actually exercises:

| Capability            | Value | Note |
|-----------------------|-------|------|
| `HeadlessMode`        | true  | `gemini -p` non-interactive one-shot |
| `ModelSelection`      | true  | `-m/--model` |
| `UsageReporting`      | true  | parsed from the JSON response |
| `CachedUsageReporting`| true  | `cachedContentTokenCount` (when version known) |
| `StructuredOutput`    | true  | JSON response carries text + usage |
| `StreamingEvents`     | **false** | final JSON only; `stream-json` translation not implemented (§36.25) |
| `ImageInput`          | false | image attachment not wired through `-p` |
| `SessionResume`       | false | `--resume` is index-based; not mapped (§36.25) |
| `LiveUserMessages`    | false | headless `-p` has no live channel |
| `ToolPermissions`     | false | `--allowed-tools` not wired to the permission system |
| `NativeSandbox`       | false | `--sandbox` exists but is not invoked |
| `MCP`                 | false | `gemini mcp` subcommand not wired |
| `ACP`                 | false | `--experimental-acp` is experimental; not wired |
| `InteractiveMode`     | false | |

An undetectable version yields the least-capable profile (no `CachedUsageReporting`
claim).

## Execution & environment

`Start` spawns the CLI via [`proctree.NewGroupCommand`](../../internal/adapter/codingagent/proctree)
so the whole process tree runs in its own group; `Cancel`/timeout terminate it
via `proctree.KillGroup` (`taskkill /T /F` on Windows, `kill -PGID` on Unix).
The child never orphans descendants.

- `cmd.Dir = req.Workspace` (the isolated worktree, never the primary checkout).
- The environment is a **positive allowlist** (`buildEnv`, spec §29.2, AC-28):
  `PATH`, `HOME`, `USERPROFILE`, `USER`, `LANG`, `LC_ALL`, `TERM`, `SystemRoot`,
  `TEMP`, `TMP` + `req.AllowlistEnv`. Merge tokens, unrelated API keys and the
  daemon auth token are structurally excluded (no whole-env dump).
- `req.Timeout` becomes the run's hard deadline; expiry is classified as
  `TIMEOUT` (distinct from an explicit `Cancel` → `CANCELLED`).

## Output parsing

`parseStream` reads stdout with an **unbounded line scanner** (no `bufio.Scanner`
64KiB cap), strips a leading **UTF-8 BOM**, and tolerates **CRLF**. It then
dispatches by sniffing the first non-empty line:

- **Gemini document mode** (the real `--output-format json` shape): the whole
  stdout is one JSON response document, decoded into `geminiResponse` and
  translated into body events (`message.completed`, `usage.updated`).
- **Protocol-JSONL mode** (forward-compatible): the first line is a JSON object
  with a `"type"` field. Each line is parsed with `protocol.ParseEventLine`;
  unknown/malformed lines become recoverable warnings, never fatal.

The supervise loop always emits `run.started`/`run.resumed` (frame open),
forwards body events, persists malformed lines as artifacts (redacted) and emits
`warning`s, then synthesizes exactly one terminal (`run.completed` on exit 0,
`run.failed` classified via `ClassifyFailure` on non-zero). Malformed, partial or
unknown-future output **never aborts a run**.

## Usage mapping

`mapUsage` translates Gemini's usage metadata to
[`protocol.UsagePayload`](../../internal/adapter/codingagent/protocol/events.go)
at **`PROVIDER_REPORTED`** confidence (counts come straight from the provider):

| Gemini field                 | UsagePayload field   |
|------------------------------|----------------------|
| `promptTokenCount`           | `InputTokens`        |
| `candidatesTokenCount`       | `OutputTokens`       |
| `cachedContentTokenCount`    | `CacheReadTokens`    |

`thoughtsTokenCount` and `toolUseTokenCount` have **no dedicated protocol field**.
They are NOT summed into `OutputTokens` (the Gemini API may already include them
in `candidatesTokenCount`; summing risks double-counting), so they are omitted
rather than overstated (§36.10). `totalTokenCount` is validated internally but
has no UsagePayload field. `Cost` is not reported by the CLI, so it stays 0
(omitted) rather than estimated. Absent usage → no `usage.updated` event.

## Failure classification

`ClassifyFailure` layers Gemini/Google-API-specific stderr signals on top of
[`codingagent.DefaultClassify`](../../internal/adapter/codingagent/failure.go),
always via `protocol.DefaultPolicy` (no unbounded retry, rule §32):

| Signal (case-insensitive)                          | Class                  |
|----------------------------------------------------|------------------------|
| `RESOURCE_EXHAUSTED`, `quota`, `billing`, `exhausted` | `PROVIDER_QUOTA`    |
| `429`, `rate limit`, `too many requests`           | `PROVIDER_RATE_LIMIT`  |
| `API key not valid`, `PERMISSION_DENIED`, `401`, `auth` | `PROVIDER_AUTH`   |
| `503`, `UNAVAILABLE`, `overloaded`                 | `PROVIDER_CAPACITY`    |
| `model` + `not found`/`not supported`              | `MODEL_NOT_AVAILABLE`  |
| signal death (exit ≥ 128, no stderr signal)        | `ENGINE_CRASH`         |
| exit 124 / 137                                     | `TIMEOUT`              |
| else                                               | `DefaultClassify`      |

Ambiguous quota-vs-rate-limit text maps to `PROVIDER_QUOTA` (failover, no
auto-retry), since a wrong rate-limit retry could burn quota.

## Secret redaction

Captured stderr and malformed artifacts are passed through `redact` before being
stored or forwarded, masking `AIza…` API keys, `ya29.…` OAuth tokens,
`key=/token=` assignments, the daemon token, and long opaque blobs (≥32 chars).
The allowlisted environment is the primary defence (§29.2); redaction is
defence-in-depth.

## Windows correctness

- PATHEXT-respecting `lookPath`; `.exe`/`.cmd`/`.bat` + npm shims; `.ps1`
  skipped; spaces and Unicode paths; argv-only (no shell quoting); case-
  insensitive env keys; cancellation via `taskkill /T /F` (no orphaned
  descendants). No Unix signals, no negative PIDs, no hardcoded `/tmp` (uses
  `os.TempDir`). Process handling is delegated to the shared `proctree`.

## Conformance

Wired into the §13.3 suite in `conformance_test.go` via a scenario-aware stub
host (offline recorded byte streams; no paid calls). Honoured checks:
`handshake`, `version_compatibility`, `event_ordering`, `malformed_output`,
`cancellation`, `timeout`, `quota_failure`, `process_crash` (8/9).

Deferred: `resume` — see "Explicitly not implemented" below.

A build-tagged real-CLI smoke test lives in `smoke_test.go` (`//go:build
geminismoke`); it is excluded from normal/CI runs and additionally gated on
`GEMINI_SMOKE=1` + installed auth.

## Explicitly not implemented (§36.25)

These are reported `false`/erroring honestly, never disguised as finished stubs:

- **Session resume** (`Resume` → `ErrSessionResumeNotSupported`,
  `SessionResume=false`). The CLI's `--resume latest|N` is index-based and
  cannot be mapped to NeuroForge's arbitrary-session-id continuation packs
  without a paid call or fragile `--list-sessions` parsing.
- **Streaming events** (`StreamingEvents=false`). Translating the `stream-json`
  output format to incremental normalized events is not wired; the adapter
  synthesizes events from the final JSON response.
- **Live user messages** (`SendMessage` → `ErrLiveMessagesNotSupported`,
  `LiveUserMessages=false`). Headless `-p` has no live message channel.
- **Image input**, **tool permissions** (`--allowed-tools`), **native sandbox**
  (`--sandbox`), **MCP**, **ACP** — not wired through the headless path.
- **Model catalogue** (`ListModels` returns empty). Enumerating models requires
  a paid API call; model ids are provider-supplied via the request/catalogue
  (rule §36.8).
- **Quota inspection** (`InspectQuota` → `UNKNOWN`). The CLI exposes no quota
  figure without a paid call (§36.10).
- **TurnLimit** mapping — no stable headless flag exists.
