# M2 closure report

Gap analysis for milestone M2 (Coding Agent Protocol). Source of truth:
[`docs/spec/NEUROFORGE_SPEC.md`](../spec/NEUROFORGE_SPEC.md) (§12, §13, §32,
§33.1, §35, §36) and
[`IMPLEMENTATION_PLAN.md`](IMPLEMENTATION_PLAN.md). Status is mirrored in
[`docs/spec/COMPLIANCE_MATRIX.md`](../spec/COMPLIANCE_MATRIX.md).

> Legend: **DONE** = implemented and covered by an automated test. **PARTIAL** =
> some pieces exist; an acceptance scenario is blocked on a later milestone.
> **PENDING** = not started.

---

## What is DONE in M2

### M2-1 — Core interfaces & types (§12.2/§12.3)
- **Requirement:** the `CodingAgentAdapter` contract, capabilities, run
  request/handle, engine≠model separation, credential-free requests (AC-28).
- **Decision (ADR-0012):** the stable, versioned structures live in a dedicated
  `internal/adapter/codingagent/protocol` package pinned at
  `ProtocolVersion == 1`; the interface, `EventSink`, registry and the shared
  classifier live in the parent `codingagent` package.
- **Tests:** `protocol/*_test.go` + `codingagent/invariants_test.go` (no
  credential fields, no hard-coded model names, engine≠model, exact §12.4 set,
  protocol pinned to v1).

### M2-2 — Normalized events + EventSink (§12.4)
- **Requirement:** the full normalized event set, typed and ordered; robustness
  to unknown/malformed events.
- **Done:** `NormalizedEvent` + typed payloads, `ParseEventLine` (returns a
  `warning` + `MalformedEventError` for unknown/malformed lines — never aborts),
  `SliceSink`/`ChannelSink`/`TeeSink`/`SinkFunc`.
- **Tests:** `protocol/events_test.go` (typed events, unknown-type resilience,
  malformed JSON, missing type, empty lines, round-trip).

### M2-3 — Failure classification (§32)
- **Requirement:** the §32 taxonomy, a policy per class, no infinite retry.
- **Done:** `FailureClass` (all 20 §32 classes), `FailurePolicy`, bounded
  `DefaultPolicy` (retryable ⇒ `MaxRetries > 0`), `DefaultClassify`
  (deterministic exit-code/events/stderr → class; no LLM, §22.6).
- **Tests:** table-driven taxonomy/policy/terminal/failover coverage.

### M2-4 — Declarative command adapter (§13.1)
- **Requirement:** YAML-defined CLI adapter; malformed output saved + classified;
  cancellation ends the process group; no creds to the agent.
- **Done:** `declarative` package — zero-dependency YAML-subset parser (block +
  flow sequences, comments, quoting), `{{ }}` template substitution, JSONL
  streaming, malformed-line capture to artifacts + `warning` emission, terminal
  synthesis on crash/partial exit, process-group spawn/kill via `proctree`.
- **Tests:** manifest parsing (incl. `example.yaml`), success/malformed/crash/
  partial/cancellation/detect/concurrency, `buildEnv` never leaks the daemon
  token.

### M2-5 — Native plugin JSON-RPC (§13.2)
- **Requirement:** stdin/stdout JSON-RPC 2.0, handshake + version negotiation,
  mandatory methods, process-group cancel.
- **Done:** `plugin` client (handshake with `Negotiate`, `run.event` streaming
  notifications, per-run sink routing, terminal-aware unregister, process-group
  spawn + reap-on-close) + reference server in `fake.ServeJSONRPC`.
- **Tests:** handshake/metadata, success streaming, quota classification,
  cancellation, resume, malformed resilience, usage events, group termination.

### M2-6 — Fake coding agent (§33.1)
- **Requirement:** deterministic scenarios; no real AI in CI (§36.5).
- **Done:** `fake` package (in-process adapter) + `cmd/fake-coding-agent`
  (command + jsonrpc modes); 13 scenarios — success, quota-before/after-edits,
  rate-limit, auth-failure, malformed-json, timeout, crash, partial-output,
  resume, cancellation, scope-violation, usage-events. One shared replay script
  drives all three surfaces.
- **Tests:** every scenario via the in-process adapter.

### M2-7 — Conformance suite (§13.3, AC-6)
- **Requirement:** handshake, event ordering, malformed output, cancellation,
  timeout, quota failure, resume, crash, version compatibility; a 7th agent
  passes with no core changes.
- **Done:** `conformance` package (9 checks) + `forge plugin test <exe>`
  (text + `--json`). Passes against the in-process fake adapter AND the fake
  plugin.
- **Tests:** suite vs in-process fake + fake plugin; `forge plugin test` CLI
  (text/JSON/usage/missing-exe). **AC-6 satisfied.**

### Cross-cutting invariants
- **AC-28 (no merge credentials):** enforced structurally — `Account` is
  name-only; `AgentRunRequest` has no credential fields; `declarative.buildEnv`
  is allowlisted and tested to never leak the daemon token.
- **§36.8 (no hard-coded model names in core):** `TestCoreHasNoHardcodedModelNames`
  scans protocol + codingagent source.
- **§12.1 (engine ≠ model):** distinct fields on `RunHandle`/`AgentRunRequest`,
  reflect-tested.
- **Cancellation ends the whole process group:** `proctree` setpgid/tree-kill;
  tested in declarative + plugin.
- **Unknown/malformed events never break a run:** `ParseEventLine` + conformance
  `malformed_output` check + declarative artifact capture.

## AC status after this pass

| AC | Requirement | Status | Note |
|----|-------------|--------|------|
| AC-5 | Six concrete engines | planned | protocol + conformance ready; engines land in M4/M5 (rule §36.7) |
| AC-6 | 7th agent via plugin, no core changes | **DONE** | `forge plugin test` passes all 9 checks against the fake plugin |
| AC-28 | Agent has no merge credentials | done (M2 structural) | name-only `Account`, allowlisted env, reflect-tested |

## Remaining in M2

- **M2-8 (supervisor)** and **M2-9 (end-to-end demonstrable scenario)** are
  **PENDING**: they depend on **M3 workspaces** (isolated worktrees, semantic
  leases, checkpoints). They are not faked (rule §36.25). The M2 acceptance
  surface they consume — the adapter protocol, registry and conformance suite —
  is complete and tested, so M2-8 becomes pure supervisor wiring once M3 lands.

## Readiness verdict

`M2 PROTOCOL COMPLETE (AC-6 DONE)` — the coding-agent protocol is stabilised at
v1, both extensibility paths work, the fake agent covers every §33.1 scenario,
and the conformance suite validates a 7th plugin agent with no core changes. The
only M2 items outstanding (M2-8/M2-9) are explicitly blocked on M3 and are not
faked.

## Next permitted milestone

**M3 — Workspaces** (Git worktrees, semantic leases, checkpoints), which unblocks
M2-8 (supervisor) and AC-27 (full attempt recovery). Concrete coding engines
follow in M4/M5.
