# opencode adapter review

**Reviewer:** author self-review (staff-level)
**Date:** 2026-07-25
**Scope:** `internal/adapter/codingagent/opencode/**` — the OpenCode coding-agent
adapter (AC-5, M5). Path-3 in-process Go adapter wrapping the OpenCode engine.
**Status:** Implementation review against spec §12/§13/§17/§22/§29/§32/§36,
ADAPTER_DEV_GUIDE, ADR-0005/0012, and the hard constraints of the AC-5 task.
**Tests executed:** `gofmt -l` (clean), `go vet` (clean),
`go test -count=1 ./internal/adapter/codingagent/opencode/...` (green, includes
the §13.3 conformance suite), `go test -count=1 ./internal/adapter/codingagent/...`
(full adapter tree green). `go test -race` could not run locally (no gcc/cgo);
the locking pattern mirrors the proven `declarative` adapter and CI runs race
validation. No shared code was modified; `go.mod`/`go.sum` untouched.

**Summary:**
- All 13 `codingagent.Adapter` methods implemented in a self-contained package
  under `internal/adapter/codingagent/opencode`. No `init()`/self-registration;
  constructed via `opencode.New(opts)`.
- The §13.3 conformance suite passes in full (9/9) through the adapter's **real**
  run pipeline against recorded byte-stream fixtures — offline, no paid calls
  (rule §36.5).
- Security invariants enforced unconditionally: allowlisted env only (§29.2,
  AC-28), `--share` never passed, secret redaction in stderr/events/artifacts,
  forbidden credential keys dropped even if allowlisted.
- Cancellation/timeout terminate the whole process group via shared `proctree`
  (Windows-safe); the blocking stdout read is preemptible.
- Unimplemented features are **explicitly marked** (§36.25), never disguised.

## Constraints checklist

| Constraint | Status | Evidence |
|------------|--------|----------|
| All 13 Adapter methods | ✅ | `adapter.go`, `detect.go`, `version.go`, `usage.go`, `run.go`, `classify.go` |
| No shared/core code modified | ✅ | only `internal/adapter/codingagent/opencode/**` + this dir changed |
| `go.mod`/`go.sum` untouched | ✅ | stdlib + existing internal packages only |
| No self-registration | ✅ | `New(opts)`; no `init`/`MustRegister` |
| Protocol v1 frozen | ✅ | uses `protocol.ParseEventLine`; adds no event types |
| No paid calls in tests | ✅ | recorded byte-stream stubs; smoke test gates a real binary behind tag+env and never runs a `run` |
| Unimplemented explicitly marked | ✅ | `docs/adapters/opencode.md` "Explicitly not implemented"; `SendMessage`/`Resume` return explicit errors |

## What is honoured vs. deferred (no faking)

- **Honoured (conformance, all 9):** handshake, version_compatibility,
  event_ordering, malformed_output, cancellation, timeout, quota_failure, resume,
  process_crash — each driven through genuine adapter code; only the process
  transport is a recorded stream.
- **Deferred to the opt-in smoke test:** behaviour against a real `opencode`
  binary (detection/version/health/capabilities only — never a paid `run`).

## Findings

No BLOCKER/MAJOR findings. Notes:

| Note | Severity | Notes |
|------|----------|-------|
| Native OpenCode event → normalized-event translator | INFO / deferred | Adapter parses stdout with `protocol.ParseEventLine` (normalized JSONL). A native-schema translator is deferred pending a pinned OpenCode event schema; non-normalized lines are tolerated as recoverable warnings + artifacts (never fatal). Explicitly documented (§36.25). |
| Race detector not run locally | MINOR | No cgo/gcc in this environment; CI provides race validation. Concurrency mirrors `declarative` (mutex-guarded `runs` map; preemptible reader goroutine). |
| `InspectQuota` is UNKNOWN | BY-DESIGN | Headless `run` exposes no live quota API (§20.1, rule §36.10). Per-run usage flows via `usage.updated`. |
| `ListModels` returns nothing | BY-DESIGN | No hardcoded model names (§36.8); the engine serves any resolvable `provider/model`. Catalogue supplied by M6-1. |

## Verification commands

```sh
gofmt -l internal/adapter/codingagent/opencode          # (no output = clean)
go vet ./internal/adapter/codingagent/opencode/...      # clean
go test -count=1 ./internal/adapter/codingagent/opencode/...   # green (incl. conformance)
go test -count=1 ./internal/adapter/codingagent/conformance/...# green (shared suite intact)
go vet -tags opencodesmoke ./internal/adapter/codingagent/opencode/...  # smoke compiles
```
