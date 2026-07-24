# Review: Claude Code adapter (AC-5, M4)

Self-review of `internal/adapter/codingagent/claude` against the task brief and
spec. Performed before commit on branch `adapter/claude`.

## Scope compliance

- **Allowed paths only.** Only `internal/adapter/codingagent/claude/**` and
  `docs/adapters/claude.md` were added. No shared/core package was modified
  (verified: `git status` shows a single untracked directory under the allowed
  path; `git diff --check` clean).
- **No `go.mod` / `go.sum` change** (stdlib + existing internal packages only).
- **No self-registration.** No `init()` / `MustRegister`; the package exposes
  `New(opts claude.Options) (*claude.Adapter, error)`.
- **Protocol v1 frozen.** No event types added to `protocol/`; the adapter
  translates Claude's own schema onto the existing §12.4 set.
- **No paid calls in tests (§36.5).** Unit + conformance tests use recorded
  byte-stream fixtures and stubbed detect/version/health probes. The real CLI is
  exercised only by the `claudesmoke` build-tagged test (skipped in normal/CI).

## Interface coverage (spec §12.2 — 13 methods)

`var _ codingagent.Adapter = (*Adapter)(nil)` enforces full coverage at compile
time: `ID, Detect, Version, Health, Capabilities, ListModels, InspectQuota,
Start, Resume, SendMessage, Cancel, ClassifyFailure`. (`SendMessage` is
explicitly unsupported and documented — rule §36.25.)

## Required capabilities (brief → evidence)

| Requirement | Evidence |
|---|---|
| Detection via `exec.LookPath` + PATHEXT/.cmd/.bat/shim/Unicode | `detect.go` `defaultLookPath`/`searchPathExt`; tests `detect_test.go` |
| Version parse; `ProtocolVersion==1` | `parseVersion`, `Version()`; `TestVersionResultProtocolOne` |
| Health via `auth status` (ok/degraded/down/unknown) | `probes.go` `Health`; `TestHealth*` |
| Version-gated capabilities | `capabilities.go`; unknown version → safe base set |
| Deterministic argv builder (no shell) | `command.go` `buildArgv`; `TestBuildArgvDeterministic` |
| Headless exec via `proctree.NewGroupCommand`; allowlisted env | `run.go` `proctreeSpawner`, `command.go` `buildEnv`; `TestBuildEnv*`, `TestProctreeSpawnerKillsRealProcess` |
| No-64k line parser; `ParseEventLine` malformed handling; artifacts | `stream.go` `lineReader`/`translate`; `TestLineReaderNo64KiBCap`, `TestRunMalformedSavedAndWarning` |
| BOM/CRLF tolerance | `stripBOM`, `trimCR`; `TestStripBOM`, `TestLineReaderHandlesCRLF` |
| Partial/malformed/unknown never fatal | `translate` → warning; `TestTranslateMalformedNeverFatal` |
| Session id capture + gated resume | `translateSystem`/`translateResult` + `SessionID()`; `TestRunSessionExtractionWhileActive`, `TestRunResumeEmitsResumed` |
| Usage incl. cached tokens + cost + confidence | `translateResult`; `TestTranslateResultSuccessUsageAndCompleted` |
| Timeout + cancellation; `run.cancelled`; group kill | `run.go` supervise; `TestRunCancellationEmitsCancelled`, `TestRunTimeoutEmitsFailedTimeout` |
| Process-tree cleanup (no orphans) | `proctree.KillGroup`; `TestProctreeSpawnerKillsRealProcess` |
| Failure classification per class; no unbounded retry | `classify.go`; `TestClassify*`, `TestClassifyNoUnboundedRetry` |
| Secret redaction | `secret.go`; `TestRedact*`, `TestRunNoSecretLeak` |
| Conformance wiring (`conformance.Suite.Run`) | `conformance_test.go`; all 9 checks pass |
| Opt-in smoke test (build-tagged, skipped in CI) | `smoke_test.go` (`//go:build claudesmoke`) |
| Windows correctness | PATHEXT/.cmd/.bat/shim, CRLF/BOM, argv-only, `os.TempDir()`, shared `proctree`; no Unix-isms |

## Conformance: honoured vs deferred

All nine §13.3 checks are **honoured** against recorded byte-stream fixtures
(no faking — the adapter genuinely translates the recorded Claude output):
handshake, version_compatibility, event_ordering, malformed_output,
cancellation, timeout, quota_failure, resume, process_crash. The real-CLI path
is covered by the opt-in `claudesmoke` test.

## Verification commands run (all green)

```
gofmt -l .                          # clean
go vet ./...                        # clean
go test -count=1 ./internal/adapter/codingagent/claude/...      # PASS
go test -count=1 ./internal/adapter/codingagent/conformance/... # PASS
go test -count=1 ./...              # all packages PASS
```

Note: `-race` could not be executed on this Windows host (no gcc/cgo); CI runs
race on Linux. Shared state is mutex-disciplined (`Adapter.mu`, `runState.causeMu`,
`runState.sessMu`, `sync.Once` for the version cache and the replay kill guard).

## Risks / follow-ups

- `claude auth status` JSON shape is based on the published CLI reference; if a
  future CLI changes the exit-code contract, `Health` maps unknown exits to
  `HealthUnknown` rather than guessing.
- Prompt delivery defaults to stdin (Windows-safe); `Options.PromptStrategy =
  PromptPositional` is available for short-prompt, single-shot use.
- The model catalogue defaults to the documented `--model` aliases
  (`sonnet`/`opus`/`haiku`) and is fully overridable via `Options.Models`.
