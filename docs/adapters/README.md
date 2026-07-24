# Coding-agent adapters

NeuroForge supports six first-party coding engines (spec §12, AC-5), all driven
through a single versioned adapter protocol
([ADR-0005](../adr/0005-coding-agent-adapter-protocol.md),
[ADR-0012](../adr/0012-versioned-coding-agent-protocol.md)). Adding an engine is
purely additive (spec §13.3, AC-6): no change to the scheduler, database schema,
TUI/dashboard, or routing core.

| Engine | Adapter ID | Package | Status | Guide |
|--------|-----------|---------|--------|-------|
| Codex CLI | `codex` | `internal/adapter/codingagent/codex` | integrated | [codex.md](codex.md) |
| Claude Code | `claude` | `internal/adapter/codingagent/claude` | integrated | [claude.md](claude.md) |
| Gemini CLI | `gemini` | `internal/adapter/codingagent/gemini` | integrated | [gemini.md](gemini.md) |
| Kimi Code | `kimi` | `internal/adapter/codingagent/kimi` | integrated | [kimi.md](kimi.md) |
| Grok Build | `grok` | `internal/adapter/codingagent/grok` | integrated | [grok.md](grok.md) |
| OpenCode | `opencode` | `internal/adapter/codingagent/opencode` | integrated | [opencode.md](opencode.md) |

In addition, the **fake coding agent** (`internal/adapter/codingagent/fake`,
spec §33.1) is registered by default and drives all orchestration/conformance
tests without any network or paid call (rule §36.5).

## How they are wired

Every engine adapter is a self-contained in-process Go adapter ("Path 3" of the
[adapter dev guide](../architecture/ADAPTER_DEV_GUIDE.md)). Each exposes a
`New(opts)` constructor and is **not** self-registered. The single wiring site is
[`internal/adapter/codingagent/builtin`](../../internal/adapter/codingagent/builtin),
which constructs all six with default options and registers them into a
[`codingagent.Registry`](../../internal/adapter/codingagent/registry.go):

```go
import (
    "neuroforge/internal/adapter/codingagent"
    "neuroforge/internal/adapter/codingagent/builtin"
)

reg := codingagent.NewRegistry()
if err := builtin.RegisterAll(reg); err != nil { /* ... */ }
```

`builtin` contains **no provider-specific logic** (spec §13.3): it only calls
each engine's constructor and registers it. The canonical engine ids live there
as the integration contract and are verified against each adapter's `ID()`.

## The contract they share

All six implement the 13-method `codingagent.Adapter` surface (spec §12.2) at
**protocol version 1** (`protocol.ProtocolVersion`). The protocol package is
frozen — no engine adapter adds event types or shared types to it. The
supervisor (`internal/supervisor`) is the only core consumer and addresses every
engine uniformly via `Registry.Lookup(engine)`.

## Conformance

Each adapter passes the full §13.3 conformance suite offline (handshake, version
compatibility, event ordering, malformed output, cancellation, timeout, quota
failure, resume, process crash) against recorded byte-stream fixtures — no paid
calls. Run the shared suite with:

```sh
go test ./internal/adapter/codingagent/conformance/...
```

Behaviour that requires a real, authenticated engine binary (model enumeration,
live quota, live health, an actual run) is covered by each adapter's opt-in
build-tagged smoke test, which is skipped in normal/CI runs (rule §36.5).

## Windows notes

All adapters are Windows-correct: PATHEXT-aware discovery (`.exe`/`.cmd`/`.bat`/
npm shims; `.ps1` skipped where not spawnable via `CreateProcess`), paths with
spaces and Unicode, CRLF + UTF-8 BOM tolerance in stream parsers, argv-only
command builders (never a shell string), and process-group cancellation via the
shared `proctree` package. See the [Windows platform guide](../platforms/WINDOWS.md).

## Review artifacts

- Per-adapter review notes: [`../reviews/adapters/`](../reviews/adapters/)
- Integration report: [`../reviews/ADAPTER_INTEGRATION_REPORT.md`](../reviews/ADAPTER_INTEGRATION_REPORT.md)
