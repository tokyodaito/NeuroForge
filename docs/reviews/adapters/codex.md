# Review — Codex adapter (AC-5, M4)

Review of `internal/adapter/codingagent/codex/` against the task acceptance
criteria and the spec. This documents what is honoured, what is deferred, and the
test mapping. It is a review artifact, not a stub: deferred items are explicit
(rule §36.25).

## Scope & boundaries

- **Only** `internal/adapter/codingagent/codex/**` and `docs/adapters/codex*.md`
  and `docs/reviews/adapters/codex.md` were added. No shared/core package was
  modified. `go.mod`/`go.sum` untouched (stdlib + existing internal packages
  only).
- The adapter does **not** self-register (no `init`/`MustRegister`); it exposes
  `New(opts codex.Options) *codex.Adapter`.
- Protocol v1 is frozen; no event types were added to `protocol/`.

## AC-5 acceptance criteria → implementation map

| Requirement                                        | Where                                                    | Test                                                |
|----------------------------------------------------|----------------------------------------------------------|-----------------------------------------------------|
| Implement all 13 `Adapter` methods                 | `adapter.go`                                             | `var _ codingagent.Adapter = (*Adapter)(nil)`; suite |
| Detection via `exec.LookPath` (PATHEXT/.exe/.cmd/.bat/shim, spaces, Unicode) | `detect.go`, `version.go`        | `detect_test.go`                                    |
| Version parse; `Version().ProtocolVersion == 1`    | `version.go`, `adapter.go` `Version`                     | `version_test.go`                                   |
| Health/auth: installed vs not-authed               | `adapter.go` `Health`                                    | `detect_test.go` `TestHealthStatuses`               |
| Version-gated capabilities; ignore unknown fields  | `version.go` `deriveCapabilities`                        | `version_test.go`                                   |
| Deterministic command builder (argv, no shell)     | `command.go`                                             | `command_test.go`                                   |
| Headless exec via `proctree.NewGroupCommand`       | `runner.go` `proctreeRunner`                             | `adapter_test.go` (via fake runner seam)            |
| Allowlisted env; never VCS/CI/daemon tokens        | `env.go`                                                 | `env_test.go`, `adapter_test.go`                    |
| Streaming parser, no 64KiB cap, `ParseEventLine`   | `parse.go` `lineScanner`, `parseCodexLine`               | `parse_test.go`                                     |
| Malformed/unknown → recoverable warning, persisted | `adapter.go` `supervise`, `saveMalformed`                | `adapter_test.go`, `conformance_test.go`            |
| CRLF + UTF-8 BOM tolerance                          | `parse.go` `parseCodexLine`, `stripBOM`                  | `parse_test.go`                                     |
| Partial output never aborts                         | `adapter.go` `supervise` (synthesize terminal)           | `conformance_test.go` (partial-output)              |
| Session id extraction; gate on resume support       | `parse.go` `extractSessionID`, `adapter.go`              | `adapter_test.go`                                   |
| Usage mapping (input/cached/output/reasoning/cost) + confidence | `usage.go`                          | `usage_test.go`                                     |
| Timeout (`run.failed`/TIMEOUT) + Cancel (group kill) | `adapter.go` `run`, `supervise`, `Cancel`              | `adapter_test.go`                                   |
| Process-tree cleanup (no orphans)                  | `proctree` (Windows: `CREATE_NEW_PROCESS_GROUP`+`taskkill /T /F`) | `adapter_test.go` `TestCancellationKillsGroup` |
| Failure classification (§32), bounded retry        | `classify.go`                                            | `classify_test.go`                                  |
| Secret redaction (events/stderr/artifacts/logs)    | `redact.go`, `runner.go`, `saveMalformed`               | `usage_test.go`, `adapter_test.go`                  |
| Conformance suite wiring (offline, no paid call)   | `conformance_test.go`                                    | `TestConformanceSuiteAgainstCodexAdapter`           |
| Opt-in smoke test, skipped in CI                    | `smoke_test.go` (`//go:build codexsmoke`)                | compiles + skips                                    |

## Codex-specific behaviour

- Headless entrypoint: `codex exec` (no interactive-mode assumption).
- The JSON event schema is **not** pinned to one Codex version. `mapCodexEvent`
  probes a union of field/type names across releases; unmappable JSON is
  forwarded as a `warning` with the raw bytes.
- Structured final output is consumed when present, never required.
- Session/thread id extracted when present; `SessionResume` gated on a detected
  version (documented assumption in `docs/adapters/codex.md`).
- Usage: input, cached-input, output, reasoning (recognized, not double-counted),
  cost. Absent fields → 0/UNKNOWN, never fabricated (§36.10).
- Sandbox/permission mapping: Codex `--sandbox`/`--ask-for-approval` map to
  `NativeSandbox`/`ToolPermissions`. No bypass/YOLO mode is ever enabled.
- Quota vs rate-limit vs auth vs capacity vs model-not-available vs engine-crash
  are distinguished from stderr/exit/events and classified into §32.

## Offline conformance — honoured vs deferred

Honoured (deterministic, via recorded byte-stream fixtures through the real
adapter code): `handshake`, `version_compatibility`, `event_ordering`,
`malformed_output`, `cancellation`, `timeout`, `quota_failure`, `resume`,
`process_crash`.

Deferred to the opt-in smoke test (require a real, authenticated Codex and would
make a paid call — rule §36.5): authenticated model enumeration, live quota
probe, authenticated health. Nothing in the offline suite fakes these.

## Risk / deviations

1. **Session-resume version gate** is an assumption (resume available ≥ 0.1),
   documented in `docs/adapters/codex.md` rather than verified offline. The smoke
   test would surface a regression.
2. **Health** is an offline heuristic; it cannot prove authentication without a
   paid probe. Marked explicitly not implemented.
3. **ListModels** returns empty (no hard-coded model names; §36.8). Marked
   explicitly not implemented.

## Verification

```
gofmt -l ./internal/adapter/codingagent/codex/        # clean
go vet ./internal/adapter/codingagent/codex/...        # clean
go test -count=1 ./internal/adapter/codingagent/codex/...        # green
go test -count=1 ./internal/adapter/codingagent/conformance/...  # green
git diff --check                                       # clean
```
