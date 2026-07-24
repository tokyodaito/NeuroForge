# Claude Code adapter

In-process NeuroForge coding-agent adapter for Anthropic's **Claude Code** CLI
(spec §12 engines, AC-5, milestone M4). It is a "Path 3" in-process Go adapter
(see [docs/architecture/ADAPTER_DEV_GUIDE.md](../architecture/ADAPTER_DEV_GUIDE.md)):
it implements the full 13-method `codingagent.Adapter` surface (spec §12.2) by
spawning the headless `claude -p` (print/SDK) mode, streaming its
`--output-format stream-json` output, and translating each Claude SDK message
into the protocol-v1 normalized event set.

- **Package:** `internal/adapter/codingagent/claude`
- **Engine id:** `claude` (spec §12.1: an engine is not a model)
- **Protocol version:** `1` ([ADR-0012](../adr/0012-versioned-coding-agent-protocol.md))
- **Construction:** `claude.New(opts)` (does **not** self-register)

## Detection

`Detect` resolves the `claude` executable and runs `claude --version`.

- Resolution uses `exec.LookPath` first, then a manual `PATH` + `PATHEXT`
  fallback ([`searchPathExt`]) that also covers **npm shims** (`.cmd` / `.bat` on
  Windows, bare scripts on unix). `PATHEXT` is honoured case-insensitively.
- Spaces and Unicode in `PATH` entries / the resolved path are tolerated.
- An explicit `Options.BinaryPath` bypasses resolution.
- `Version().ProtocolVersion == 1` always; `Version().EngineVersion` is the
  parsed `claude --version` semver (best-effort).

### Windows specifics

`.exe`, `.cmd`, `.bat` and npm shims are all found via `PATHEXT`. No Unix signals,
no `/bin/sh`, no negative PIDs, no hardcoded `/tmp` (`os.TempDir()` is used). The
shared `proctree` package is used verbatim for process handling — this adapter
never reimplements Windows process creation or termination.

## Command shape (headless argv)

Built by `buildArgv`, a pure function of `(Options, AgentRunRequest, isResume)`.
The prompt text **never** appears in argv (it is piped via stdin by default), so
argv is stable regardless of prompt size — important on Windows where
`CreateProcess` caps the command line near 32 000 characters.

```
<bin> -p --output-format stream-json --verbose
       [--bare]                         # Options.Bare (default: off, project-aware)
       --permission-mode <mode>         # always emitted; default "default"
       [--model <m>]                    # from AgentRunRequest.Model
       [--max-turns <n>]                # from AgentRunRequest.TurnLimit (>0)
       [--effort <e>]                   # Options.Effort, if set
       [--add-dir <d> ...]              # Options.AdditionalDirs
       [--resume <session-id>]          # Resume with a captured session id
       [...Options.ExtraArgs]           # validated (see Security)
```

Determinism: for the same `(Options, request, isResume)` the argv is byte-identical.
The prompt is delivered through the child's stdin (the documented
`cat file | claude -p` pattern); set `Options.PromptStrategy = PromptPositional` to
pass it as a positional argument instead.

## Streaming output → normalized events

The adapter reads stdout line-by-line with a scanner that has **no 64 KiB cap**
(Claude assistant/result lines can be large), tolerates a leading UTF-8 BOM and
CRLF line endings, and translates each Claude SDK message:

| Claude SDK message | Normalized event(s) |
|---|---|
| `system` (`subtype:"init"`) | captures `session_id`; emits nothing |
| `assistant` (`message.content[].type=="text"`) | `message.completed` |
| `assistant` (`...type=="tool_use"`) | `tool.started` |
| `user` (`...type=="tool_result"`) | `tool.completed` |
| `result` (`subtype:"success"`) | `usage.updated` then `run.completed` |
| `result` (`subtype:"error_*"`) | `usage.updated` then `run.failed` (mapped class) |
| `stream_event` (`text_delta`, only with partial messages) | `message.delta` |
| malformed JSON line | `warning` (`malformed.json`, raw persisted to artifacts) |
| unknown future `type` | `warning` (`unknown-claude-event`, raw persisted) |

The run is always started with `run.started` (or `run.resumed` for `Resume`) as the
first event, and always ends with exactly one terminal event. If the process exits
without emitting a terminal event (e.g. a crash), the adapter synthesizes one via
`ClassifyFailure`. **Malformed and unknown events never abort a run** (spec: a
malformed event is saved + classified, not fatal); the raw bytes are persisted to
`Options.ArtifactsDir` for forensics.

## Capabilities (version-gated)

Derived from the detected CLI semver in `Capabilities()`. The base set (always
advertised when the CLI is detected):

| Capability | Value | Rationale |
|---|---|---|
| `HeadlessMode` | true | `-p` print mode |
| `StreamingEvents` | true | `--output-format stream-json` |
| `StructuredOutput` | true | assistant content blocks / result |
| `ModelSelection` | true | `--model` |
| `UsageReporting` | true | `result.usage` |
| `CachedUsageReporting` | true | `cache_creation_input_tokens` / `cache_read_input_tokens` |
| `ToolPermissions` | true | `--allowedTools` / `--disallowedTools` / `--permission-mode` |
| `SessionResume` | true | `--resume` / `--continue` |
| `MCP` | true | `--mcp-config` |

Capabilities the adapter **does not** wire up are reported `false` so the engine
never claims what it cannot honour (rule §36.25):

| Capability | Value | Why |
|---|---|---|
| `LiveUserMessages` | false | headless text-mode reads stdin once; `SendMessage` returns an error |
| `NativeSandbox` | false | Claude Code has a permission system, not an execution sandbox |
| `ImageInput` | false | the adapter does not surface image attachments in headless mode |
| `ACP` | false | not supported by Claude Code |

## Usage fields

Mapped from the terminal `result` message into `usage.updated` (`UsagePayload`):

| Claude field | Normalized field |
|---|---|
| `usage.input_tokens` | `InputTokens` |
| `usage.output_tokens` | `OutputTokens` |
| `usage.cache_read_input_tokens` | `CacheReadTokens` |
| `usage.cache_creation_input_tokens` | `CacheWriteTokens` |
| `total_cost_usd` | `Cost` (`Currency = "USD"`) |

`Confidence = PROVIDER_REPORTED`: the CLI reports usage authoritatively, but the
adapter does not overstate to `EXACT` (rule §36.10). When usage fields are absent,
confidence is `UNKNOWN`.

## Quota

`InspectQuota` returns `Confidence=UNKNOWN`, `State=UNKNOWN`: Claude Code exposes
no authoritative remaining-quota figure through the CLI, and **subscription quota
is distinct from API rate-limit** (spec §20, rule §36.10). The supervisor infers
quota state from observed failure signals (`INFERRED`) via `ClassifyFailure`.

## Failure mapping (spec §32)

`ClassifyFailure` honours an explicit `run.failed` event class, refines an
ambiguous `INTERNAL_ERROR` (from `result.error_during_execution`) using the
`result.errors[]` vocabulary / captured stderr, then defers to
`codingagent.DefaultClassify`. Subscription quota vs API rate-limit map to distinct
classes. Every class comes from `protocol.DefaultPolicy`, so **no class yields an
unbounded retry** (rule §32).

| Signal | Class | Policy |
|---|---|---|
| `rate_limit` / `429` / `too many requests` | `PROVIDER_RATE_LIMIT` | cooldown |
| `billing` / `quota` / `exhausted` / `limit exceeded` | `PROVIDER_QUOTA` | failover |
| `authentication_failed` / `401` / unauthorized | `PROVIDER_AUTH` | failover |
| `model_not_found` | `MODEL_NOT_AVAILABLE` | failover |
| `overloaded` / `529` / capacity | `PROVIDER_CAPACITY` | cooldown + failover |
| `invalid_request` | `MALFORMED_OUTPUT` | bounded retry |
| exit ≥ 128 | `ENGINE_CRASH` | bounded retry |
| exit 124 / 137 (timeout/kill) | `TIMEOUT` | bounded retry |
| `result.subtype = error_max_turns` | `INTERNAL_ERROR` | terminal |
| `result.subtype = error_max_budget_usd` | `BUDGET_EXCEEDED` | pause |

## Cancellation & timeout

- `Cancel()` marks the run cancelled, cancels its context, and kills the **whole
  process group** via `proctree.KillGroup` (Windows: `CREATE_NEW_PROCESS_GROUP` +
  `taskkill /T /F`; unix: `setpgid` + negative-pgid signal). It then emits
  `run.cancelled`. Descendants are never orphaned.
- `AgentRunRequest.Timeout` (when > 0) fires a natural wall-clock deadline: on
  expiry the group is killed and `run.failed` (`TIMEOUT`) is emitted. Explicit
  `Cancel` emits `run.cancelled`; the two causes are distinguished so the
  supervisor learns the right disposition.
- The blocking stdout read happens in a goroutine; ctx cancellation preempts it,
  so the adapter never blocks forever on a pipe read.

## Security (spec §29.2, AC-28)

- **Allowlisted environment only.** The child receives a fixed baseline
  (`PATH`, `HOME`, `USERPROFILE`, `USER`, `LANG`, `LC_ALL`, `TERM`, `SystemRoot`,
  `TEMP`, `TMP`, `PATHEXT`) plus the caller's `AgentRunRequest.AllowlistEnv`. The
  adapter never injects credentials itself; the supervisor allowlists any provider
  credential (e.g. an API key) it wants the agent to see.
- **Forbidden tokens are dropped** from `AllowlistEnv` even if the caller
  accidentally includes them: the daemon auth token and VCS/merge tokens
  (`FORGE_DAEMON_TOKEN`, `GITHUB_TOKEN`, `GITLAB_TOKEN`, `MERGE_TOKEN`, …).
  Provider credentials (`ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN`) are
  intentionally **not** dropped — the caller allowlists them on purpose.
- **No dangerous permission bypass.** `Options.PermissionMode = bypassPermissions`
  is rejected; `Options.ExtraArgs` may not include `--dangerously-skip-permissions`,
  `--permission-mode`, `--output-format`, `--model`, `--max-turns`, `--resume`,
  `--session-id`, `--bare`, etc. (adapter-owned or security-sensitive).
- **Secret redaction.** Captured stderr, spawn/start error strings and
  `ClassifyFailure` stderr are scrubbed of Anthropic keys (`sk-ant-…`), bearer
  tokens, secret-named `KEY=VALUE` entries, and long opaque token blobs before
  they are stored or emitted.

## Conformance

The §13.3 conformance suite is wired in `conformance_test.go`. It runs all nine
checks against **recorded Claude Code stream-json fixtures** (rule §36.5: no real
paid calls). The fixtures mimic the real CLI output shape; the adapter genuinely
translates them — it is not stubbed at the event layer. All nine checks pass
(handshake, version_compatibility, event_ordering, malformed_output,
cancellation, timeout, quota_failure, resume, process_crash).

The real CLI is exercised by the opt-in **smoke test**
(`smoke_test.go`, build tag `claudesmoke`), which is excluded from normal and CI
runs and auto-skips when `claude` is absent or `CLAUDE_SMOKE` is unset:

```
go test -tags claudesmoke ./internal/adapter/codingagent/claude/ -run TestSmoke -v
```

## Explicitly not implemented (rule §36.25)

- **Live user messages** (`SendMessage`): headless `-p` text mode reads stdin
  once; mid-run injection is unsupported. `SendMessage` always returns an error
  and `LiveUserMessages = false`.
- **Image input**: not surfaced in headless mode (`ImageInput = false`).
- **Native sandbox**: not applicable (`NativeSandbox = false`).
- **ACP**: not supported (`ACP = false`).
- **Authoritative quota figure**: not exposed by the CLI (`InspectQuota` is
  `UNKNOWN`).
- **Partial-message token streaming**: the adapter maps `text_delta` to
  `message.delta` when present, but does not enable `--include-partial-messages`
  by default (the `assistant` turn events already carry complete text).
