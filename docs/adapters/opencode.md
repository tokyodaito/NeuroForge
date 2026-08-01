# OpenCode coding-agent adapter (AC-5)

In-process NeuroForge adapter that wraps the **OpenCode** agent engine as a
Protocol-v1 `codingagent.Adapter`. It lives in
[`internal/adapter/codingagent/opencode`](../../internal/adapter/codingagent/opencode)
and is constructed via `opencode.New(opts)` — it does **not** self-register.

> OpenCode is an **agent engine**, not a model provider (spec §12.1). The model
> is supplied separately as `provider/model` via `AgentRunRequest.Model`; this
> adapter drives the OpenCode engine to run a task in an isolated worktree.

## Detection

`Detect` resolves the engine runtime in this order:

1. `Options.Binary`, if set (used verbatim — tolerates spaces and Unicode).
2. `exec.LookPath("opencode")`, which finds the binary and bare scripts
   produced by npm-style installers.

When a binary is found, `Detect` runs `opencode --version` to capture the engine
version (used to gate capabilities). A failed version probe is **non-fatal**: the
binary still reports `Installed`, just without a version.

## Command shape (headless, one-shot)

The adapter drives **one-shot headless runs** via `opencode run` — it never starts
the persistent `opencode serve` server. The argv is built deterministically from
`AgentRunRequest` (argv only — never a shell string, so paths with
spaces/Unicode are handled natively):

```
<binary> run --format json \
  --dir <workspace> \
  --model <provider/model> \      # from AgentRunRequest.Model
  --agent <profile> \             # Options.Agent, if set
  [--session <id>] \              # resume only, version-gated
  [Options.ExtraArgs...] \
  -- <prompt | prompt-file>       # `--` shields dash-leading prompts; inline preferred, else prompt file
```

Only documented OpenCode `run` flags are used. Flags that are **never** passed:

| Flag        | Reason |
|-------------|--------|
| `--share`   | NeuroForge-managed runs are **never shared**. |
| `--fork`    | Forking diverges from Protocol-v1 "resume the same session" continuation-pack semantics, so resume uses `--session` only. |
| `--attach`  | The adapter does not attach to a foreign server. |

Resume (`Resume`) is honoured only when `Capabilities.SessionResume` is true for
the detected version; otherwise `Resume` returns an explicit error (rule §36.25).
`SendMessage` is unsupported in headless mode (`LiveUserMessages` is false).

## Output contract & parsing

The adapter reads stdout **line-by-line without a 64KiB cap** (long agent lines
are accumulated), strips a leading UTF-8 BOM and tolerates CRLF. Each line is
parsed with the canonical `protocol.ParseEventLine`:

- a well-formed normalized event → forwarded to the sink;
- a malformed/unknown line → surfaced as a recoverable `warning` event **and**
  persisted (redacted) to the artifacts dir; it **never aborts the run**;
- if the process exits without emitting a terminal `run.*` event, the adapter
  synthesizes `run.completed` (exit 0) or `run.failed` (non-zero) via
  `DefaultClassify`.

To guarantee event ordering the adapter synthesises the opening `run.started`
(for `Start`) / `run.resumed` (for `Resume`) itself before forwarding the stream.

## Capabilities by version

Capabilities are derived from the detected engine version so version-gated
features degrade on older engines rather than being over-claimed (rule §36.25):

| Capability            | Value | Gating |
|-----------------------|-------|--------|
| `HeadlessMode`        | true  | `opencode run` |
| `StreamingEvents`     | true  | `--format json` |
| `StructuredOutput`    | true  | structured tool/file/command events |
| `ModelSelection`      | true  | `--model provider/model` |
| `ImageInput`          | true  | `--file` may attach images |
| `UsageReporting`      | true  | `usage.updated` events when reported |
| `CachedUsageReporting`| true  | version ≥ 0.1.0 |
| `SessionResume`       | true  | version ≥ 0.1.0 (`--session`) |
| `MCP`                 | true  | OpenCode MCP support |
| `ACP`                 | true  | `opencode acp` |
| `ToolPermissions`     | true  | OpenCode permission system |
| `LiveUserMessages`    | false | headless one-shot runs have no live channel |
| `NativeSandbox`       | false | isolation comes from the worktree (§17) |
| `InteractiveMode`     | false | this adapter drives headless runs only |

An empty/unparseable version yields a conservative baseline (headless +
streaming + model selection) and turns the version-gated flags **off**.

## Usage & quota

`usage.updated` events in the stream are normalised: the confidence is clamped to
the least-precise applicable level and downgraded to `UNKNOWN` when no real
figures are present (rule §36.10). `EXACT` is never reported — OpenCode reports
observed figures, not an authoritative remaining-quota figure, so usage is at
most `PROVIDER_REPORTED`.

`InspectQuota` returns `UNKNOWN`: the headless `run` command exposes no live
remaining-quota API. Per-run token accounting still flows through `usage.updated`
events. `ListModels` returns no descriptors: the engine serves any resolvable
`provider/model`, the core never hard-codes names (rule §36.8), and guessing a
catalogue would violate §36.25.

## Failure mapping (with provider provenance)

`ClassifyFailure` defers to the shared `codingagent.DefaultClassify` for the
canonical §32 mapping, then refines: it recovers the **backing provider** from
the error text (`anthropic`, `openai`, `google`, `azure`, `aws`, …) and records
it in the classification reason as `provider=<p>` so routing/failover (M6/M7) can
act on it. Every class carries a bounded `MaxRetries` (rule §32 — no infinite
retry).

Mapping of common signals:

| Signal (stderr / run.failed event)            | §32 class              | Policy     |
|-----------------------------------------------|------------------------|------------|
| quota / exhausted / limit exceeded            | `PROVIDER_QUOTA`       | failover   |
| 429 / rate limit / too many requests          | `PROVIDER_RATE_LIMIT`  | cooldown   |
| 401 / unauthorized / invalid api key / auth   | `PROVIDER_AUTH`        | failover   |
| model not available / not found               | `MODEL_NOT_AVAILABLE`  | failover   |
| exit 124 / timeout (req.Timeout)              | `TIMEOUT`              | retry      |
| exit 134 / ≥128 (signal)                      | `ENGINE_CRASH`         | retry      |
| cancel by caller                              | `CANCELLED`            | terminal   |
| abrupt exit, no terminal event                | `MALFORMED_OUTPUT`     | retry      |

## Timeout & cancellation

- `req.Timeout` (when > 0) starts a wall-clock timer; on expiry the whole process
  group is killed via `proctree.KillGroup` and a `run.failed` (`TIMEOUT`) event
  is synthesized.
- `Cancel()` cancels the supervision context and kills the group, then the
  adapter emits `run.cancelled`. The blocking stdout read runs in a goroutine so
  cancellation/timeout can always preempt it (never blocks forever).
- `proctree` gives group termination (setpgid + negative-pgid signal);
  descendants are never orphaned.

## Security invariants (unconditional)

- **Allowlisted environment only** (spec §29.2, AC-28): the agent process receives
  `PATH/HOME/USER/LANG/LC_ALL/TERM/TEMP/TMP` plus the
  caller's `AgentRunRequest.AllowlistEnv`. The environment is built from scratch
  (never `os.Environ`), so VCS merge tokens, production credentials, unrelated API
  keys and the daemon auth token can never leak. Forbidden credential keys
  (`FORGE_DAEMON_TOKEN`, `*_MERGE_TOKEN`, `GITHUB_TOKEN`, …) are dropped even if
  they appear in an allowlist (defense-in-depth).
- **`--share` is never passed.**
- **Secret redaction**: credential values are scrubbed from captured stderr,
  warning-event `Raw`/`Message`, synthesized failure reasons and malformed-output
  artifacts, while preserving signal words for classification.
- **Project plugins cannot weaken NeuroForge policy**: a workspace-level OpenCode
  project/plugin config cannot override the allowlist, no-share or redaction
  rules — they are enforced by the adapter regardless of any plugin present.

## Platform notes

Detection, env, parsing and process handling target Linux: `PATH` lookup,
spaces and Unicode paths, CRLF JSONL, UTF-8 BOM, argv-only (no shell quoting),
and cancellation + descendant cleanup via the shared `proctree`. On a Windows
host, run everything inside WSL2 (see `docs/platforms/WSL2.md`).

## Testing

- **Unit tests** cover detection (missing/shim/Unicode),
  version parsing, deterministic command building, the no-64KiB/BOM/CRLF parser,
  usage mapping + confidence, failure classification per class with provider
  provenance, cancellation, timeout, resume, secret redaction and path
  handling.
- **Conformance** (`conformance_test.go`): runs the full §13.3 suite through the
  adapter's **real** run pipeline against **recorded byte-stream fixtures**
  (offline, no paid calls — rule §36.5). All nine checks pass.
- **Opt-in smoke** (`smoke_test.go`, build tag `opencodesmoke`, env
  `OPENCODE_SMOKE=1`, skipped in `-short`): exercises only the
  detection/version/health/capabilities surface against a real installed
  OpenCode binary. It **never** starts a `run` (which would route to a real,
  paid model).

## Explicitly not implemented (§36.25)

- **Live user messages** (`SendMessage`): unsupported in headless mode; returns
  an explicit error rather than silently dropping the message.
- **Native OpenCode event → normalized-event translator**: the adapter parses
  stdout with the canonical `protocol.ParseEventLine` (normalized JSONL). A
  translator that maps OpenCode's native `--format json` event schema onto the
  Protocol-v1 set is deferred pending a pinned OpenCode event schema; until then,
  production deployments pair this adapter with an OpenCode agent/profile that
  emits normalized events, and any non-normalized line is tolerated as a
  recoverable warning + artifact (never fatal).
- **Live quota snapshot**: `InspectQuota` is `UNKNOWN` (no live API).
- **Model catalogue**: `ListModels` returns nothing (no hardcoded names, §36.8).
- **`--share` / `--fork` / `--attach`**: intentionally never used (see above).
