# Writing a coding-agent adapter

This guide shows how to add a new coding agent to NeuroForge. There are three
paths, all of which land as a `codingagent.Adapter` and **none of which require
changes to the scheduler, database schema, TUI/dashboard, or routing core**
(spec §13.3, AC-6).

- **Declarative command adapter** (§13.1) — a YAML manifest. No Go code. Best for
  a CLI that already speaks JSONL.
- **Native JSON-RPC plugin** (§13.2) — an executable speaking JSON-RPC 2.0 over
  stdin/stdout. Best for rich engines with quota/usage signals.
- **In-process Go adapter** — implement `codingagent.Adapter` directly. Used by
  the fake agent and, later, by first-party engines.

The protocol contract these all satisfy lives in
[`internal/adapter/codingagent/protocol`](../../internal/adapter/codingagent/protocol)
and is **versioned at `protocol.ProtocolVersion == 1`** (stabilised in M2).

## The contract at a glance

Every adapter implements (spec §12.2):

```go
type Adapter interface {
    ID() string
    Detect(ctx) DetectionResult
    Version(ctx) VersionResult
    Health(ctx, Account) HealthResult
    Capabilities(ctx) AgentCapabilities
    ListModels(ctx, Account) ([]ModelDescriptor, error)
    InspectQuota(ctx, Account) QuotaSnapshot

    Start(ctx, AgentRunRequest, EventSink) (RunHandle, error)
    Resume(ctx, ResumeRequest, EventSink) (RunHandle, error)
    SendMessage(ctx, RunHandle, AgentMessage) error
    Cancel(ctx, RunHandle) error

    ClassifyFailure(exitCode int, events []NormalizedEvent, stderr string) FailureClassification
}
```

Invariants you must honour (enforced by tests in
[`invariants_test.go`](../../internal/adapter/codingagent/invariants_test.go)):

- **Engine ≠ model** (§12.1): `RunHandle`/`AgentRunRequest` carry `Engine` and
  `Model` as separate fields.
- **No credentials cross the boundary** (§29.2, AC-28): `AgentRunRequest` carries
  an `Account` **by name only**. Resolve credentials inside the adapter; never
  accept or emit tokens, the daemon auth token, or merge credentials.
- **No hard-coded model names** (§36.8): models come from `ListModels`; the core
  never names a specific model.
- **Unknown/malformed events never abort a run**: forward them as `warning`
  events (see `protocol.ParseEventLine`).
- **Cancellation ends the whole process group**: `Cancel` must terminate the
  agent and every descendant it spawned (use
  [`proctree`](../../internal/adapter/codingagent/proctree)).
- **Failure classification never retries infinitely** (§32): every class maps to
  a bounded policy via `protocol.DefaultPolicy`; prefer `DefaultClassify` unless
  your engine has a richer signal.

## Path 1 — Declarative command adapter (YAML)

Drop a manifest next to your CLI (spec §13.1). The loader is
`declarative.ParseManifest`; see
[`example.yaml`](../../internal/adapter/codingagent/declarative/example.yaml)
for a complete, working manifest that wraps the §33.1 fake agent.

```yaml
api_version: neuroforge/v1
kind: command-coding-agent

id: my-agent

detect:
  command: [my-agent, --version]

run:
  command:
    - my-agent
    - run
    - --cwd
    - "{{ workspace }}"
    - --model
    - "{{ model }}"
    - --output
    - jsonl
    - "{{ prompt_file }}"

capabilities:
  headless_mode: true
  streaming_events: true
  model_selection: true
  usage_reporting: true
```

Template placeholders substituted into the run command:

| Placeholder      | Replaced with |
|------------------|---------------|
| `{{ workspace }}`  | the isolated worktree path |
| `{{ model }}`      | the routed model id |
| `{{ prompt_file }}`| the compiled task prompt path |
| `{{ run_id }}`     | the run id |
| `{{ engine }}`     | the manifest id |

**Output contract.** Your CLI must emit one JSON
[`NormalizedEvent`](../../internal/adapter/codingagent/protocol/events.go) per
line on stdout (`--output jsonl`). The adapter parses each line, forwards typed
events to the sink, and:

- saves malformed/unknown lines to the artifacts dir and emits a `warning`
  (never fatal);
- synthesizes `run.completed`/`run.failed` if the process exits without a
  terminal event (e.g. a crash), classifying the exit code via
  `DefaultClassify`.

Exit non-zero with a descriptive stderr to surface a failure class (e.g. write
`quota exhausted` → `PROVIDER_QUOTA`).

## Path 2 — Native JSON-RPC plugin

Implement a standalone executable that speaks newline-delimited JSON-RPC 2.0
over stdin/stdout (spec §13.2). The daemon spawns it with an **allowlisted**
environment (never passes merge credentials or the daemon token).

Mandatory methods:

```
plugin.handshake    agent.detect      agent.health
agent.capabilities  agent.models      agent.quota
run.start           run.resume        run.message
run.cancel          failure.classify
```

**Handshake & version negotiation.** `plugin.handshake` is the first call. The
daemon sends `{protocol_min, protocol_max}`; your plugin replies with the chosen
`protocol_version` (must be `1`). If there is no version overlap, return
JSON-RPC error `-32000` (`JSONRPCProtocolError`).

**Streaming events.** During a run, stream events back as notifications using
the additive `run.event` method with a `NormalizedEvent` as params:

```json
{"jsonrpc":"2.0","method":"run.event","params":{"type":"message.delta","ts":"…","message":{"delta":"hi"}}}
```

The reference server implementation is the fake agent's
[`ServeJSONRPC`](../../internal/adapter/codingagent/fake/jsonrpc.go). The client
that drives your plugin is
[`plugin.Adapter`](../../internal/adapter/codingagent/plugin/adapter.go).

**Validate before shipping.** Run the conformance suite against your binary:

```bash
forge plugin test ./my-agent --json
```

All nine checks must pass (handshake, version, event ordering, malformed output,
cancellation, timeout, quota failure, resume, process crash). See
[`conformance`](../../internal/adapter/codingagent/conformance).

## Path 3 — In-process Go adapter

Implement `codingagent.Adapter` directly and register it:

```go
a := mypackage.New(myengine.Options{...})
codingagent.Default().MustRegister(a, 100) // priority orders listings
```

The [`fake`](../../internal/adapter/codingagent/fake) adapter is a complete,
copyable example (deterministic, no network). For `ClassifyFailure`, defer to
`codingagent.DefaultClassify` unless your engine has a more specific signal.

## Failure taxonomy

Map native errors onto the §32 classes
([`protocol.FailureClass`](../../internal/adapter/codingagent/protocol/failure.go)).
`DefaultPolicy` gives each class a disposition (retry / cooldown / failover /
escalation / pause / quarantine / terminal). Retryable classes carry a **bounded**
`MaxRetries` — never return an unbounded retry (§32).

## Running it

The supervisor (`internal/supervisor`) is the only core consumer of adapters
(M2-8 wires it). Until then, exercise your adapter through the conformance suite
and the fake-agent scenario tests.
