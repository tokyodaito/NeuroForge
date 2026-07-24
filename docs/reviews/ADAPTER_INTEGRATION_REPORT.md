# Adapter integration report — M4/M5 coding-agent adapters

- **Integration branch:** `integration/adapters`
- **Integration owner:** production coding-agent adapters
- **Date:** 2026-07-25
- **Spec refs:** §12 (engines + adapter interface), §13 (extensibility), §13.3
  (conformance), §29.2/§36 (security), §36.25 (explicitly-not-implemented)
- **ADRs:** [ADR-0005](../adr/0005-coding-agent-adapter-protocol.md),
  [ADR-0012](../adr/0012-versioned-coding-agent-protocol.md)
- **Acceptance criteria:** AC-5 (six engines), AC-6 (7th via plugin, unchanged core)
- **Head SHA:** `84a9d70` (this report); code+docs integration tip `bd01a8a`

This report records the integration of the six first-party coding-agent adapter
branches into `integration/adapters`. The spec is the source of truth; this
document is a tracking/review artifact.

## 1. Pre-integration gates

| Gate | Result |
|------|--------|
| Read AGENTS.md, spec, ADR-0005/0012, Protocol v1, adapter dev guide | done |
| Working tree clean on `main` | clean |
| `main` baseline `go build ./...` + `go test -count=1 ./...` | green |
| Each adapter branch inspected for OPEN BLOCKER / MAJOR | none found |
| Adapter branches touch disjoint paths | confirmed (no shared-file overlap) |

Per-adapter review notes (`docs/reviews/adapters/{codex,claude,opencode}.md`)
each state "No BLOCKER/MAJOR findings". Gemini/Kimi/Grok carry no review file;
their `docs/adapters/*.md` and code were inspected and contain no blocker/major
findings (the only grep hit for "major" was the literal version token
`major.minor.patch`).

## 2. Merged branches

All six branches merged in the required order, each with `--no-ff` and a
descriptive merge commit. **Zero conflicts** (mechanical or semantic) — every
branch added files only under its own `internal/adapter/codingagent/<engine>/`
directory plus its own `docs/adapters/<engine>.md`.

| # | Branch | Merge commit | Adapter ID |
|---|--------|--------------|-----------|
| 1 | `adapter/codex` | `658822c` | `codex` |
| 2 | `adapter/claude` | `ac9bd1c` | `claude` |
| 3 | `adapter/gemini` | `cb5c8f4` | `gemini` |
| 4 | `adapter/kimi` | `651ccb1` | `kimi` |
| 5 | `adapter/grok` | `69e901d` | `grok` |
| 6 | `adapter/opencode` | `aa77d29` | `opencode` |

Per-branch tip SHAs:

| Branch | Tip |
|--------|-----|
| `adapter/codex` | `556c8da1e8a12d3a55ff17d3ae556fa7c11bda8a` |
| `adapter/claude` | `086a40ed8a4e7a27f2e4a44233f08207ece6421a` |
| `adapter/gemini` | `2d1201fa27aafd07c3643aad1684b1b559334368` |
| `adapter/kimi` | `1ce923b6e41a76533cb70fe6d558ad7fc2f7eb42` |
| `adapter/grok` | `40b439ead5a0e6a387bcb0a5e7180f85f64c78f1` |
| `adapter/opencode` | `9b2e126695e04dfe931043c9fcb9e537daccf8f8` |

### Skipped branches

None. All six met the integration criteria (no blockers) and were merged.

### Conflicts

None. Because each adapter is fully encapsulated in its own subpackage and the
Protocol v1 package was frozen by every branch, there was nothing to merge-
conflict. No mechanical conflict resolution was required and no semantic conflict
was introduced. No protocol-design decision was chosen silently.

## 3. Post-merge integration work

Two integration commits follow the merges:

1. **`dcc387f` — central registry wiring.** New package
   `internal/adapter/codingagent/builtin` constructs and registers all six
   adapters into a `codingagent.Registry` with default options. It contains **no
   provider-specific logic** (spec §13.3): it only calls each engine's `New`
   constructor and registers it. Canonical engine ids live here as the
   integration contract; `RegisterAll` verifies each adapter reports the
   expected id before registration. Tests cover discovery of all six, id
   uniqueness, duplicate rejection, nil-registry guarding, and dispatch through
   the common `codingagent.Adapter` interface (the only surface the
   scheduler/supervisor core may use — ADR-0005).

2. **`bd01a8a` — docs.** `docs/adapters/README.md` index; compliance matrix
   updated in one pass (AC-5 → done, new M4/M5 section).

### Registered adapters (central registry)

`builtin.RegisterAll(reg)` registers, in priority order:

| Engine | Canonical ID | Priority | Constructor |
|--------|-------------|----------|-------------|
| Codex CLI | `codex` | 600 | `codex.New(codex.Options{})` |
| Claude Code | `claude` | 500 | `claude.New(claude.Options{})` |
| Gemini CLI | `gemini` | 400 | `gemini.New(gemini.Options{})` |
| Kimi Code | `kimi` | 300 | `kimi.New(kimi.Options{})` |
| Grok Build | `grok` | 200 | `grok.New(grok.Options{})` |
| OpenCode | `opencode` | 100 | `opencode.New(opencode.Options{})` |

The fake agent remains registered separately by the daemon (spec §33.1); it is
not owned by `builtin`.

## 4. Per-merge verification

After every merge the following was run and passed:

- `go build ./...` — OK
- targeted adapter tests `go test -count=1 ./internal/adapter/codingagent/<engine>/...` — green
- shared conformance suite `go test -count=1 ./internal/adapter/codingagent/conformance/...` — green
- `git diff --check` — clean

## 5. Test results (final, on `integration/adapters`)

| Command | Result |
|---------|--------|
| `powershell -NoProfile -File .\scripts\check.ps1` (gofmt + vet + test) | **OK** — gofmt clean, vet clean, all packages pass |
| `go test -count=1 ./...` | **PASS** — all packages green (adapters: codex, claude, gemini, kimi, grok, opencode, builtin, conformance, fake, declarative, plugin, protocol) |
| `go test -race -count=1 ./...` | **not executed locally** — see §6 |
| `git diff --check` | **clean** |

### New integration tests (`internal/adapter/codingagent/builtin`)

- `TestRegisterAll_Discovery` — registry exposes exactly the six canonical ids.
- `TestIDs_CanonicalOrder` — `IDs()` returns the six in declared priority order.
- `TestRegisteredIDs_Unique` — no two adapters share an id.
- `TestRegisteredAdapters_SatisfyInterface` — every adapter is usable purely as
  a `codingagent.Adapter`.
- `TestDispatch_ViaCommonInterface` — models the supervisor dispatch path
  (`Registry.Lookup` → `codingagent.Adapter`); proves uniform routing to all six
  with zero provider-specific code, and that an unknown engine is a clean miss.
- `TestRegisterAll_RejectsDuplicates` / `_PartialRegistrationIsObservable` /
  `_NilRegistryErrors` / `_ErrorIsNotTemporary` / `TestSortedIDsAreStable`.

## 6. Windows-specific results

Host: Windows/amd64, Go 1.26.5. The Windows-correctness requirements were
honoured by every adapter and re-validated by the integration:

| Requirement | Status | Evidence |
|-------------|--------|----------|
| `.exe`/`.cmd`/`.bat` discovery | honoured | PATHEXT-aware `lookPath` in each adapter; `Detect` exercised offline in `TestDispatch_ViaCommonInterface` |
| Paths with spaces / Unicode | honoured | argv-only command builders (never a shell string); `proctree.NewGroupCommand` carries `cmd.Dir` verbatim |
| CRLF fixtures | honoured | stream parsers trim CR and tolerate UTF-8 BOM (per-adapter `parse_test.go`/`stream_test.go`) |
| Process cancellation | honoured | `Cancel` terminates the whole group via shared `proctree` |
| Process-tree cleanup | honoured | Windows: `CREATE_NEW_PROCESS_GROUP` + `taskkill /T /F`; no orphans (per-adapter cancellation tests) |
| PowerShell scripts | honoured | `scripts/check.ps1` (the Windows `make check` equivalent) runs green; `.ps1` shims are deliberately skipped in discovery where not spawnable via `CreateProcess` |
| No Unix-only assumptions | honoured | no `/bin/sh`, no `syscall` unix-only paths in adapter code; `os.TempDir()` used for fixtures |

### Race detector on Windows

`go test -race` could **not** be run on this host: `CGO_ENABLED=0` and no C
compiler (`gcc` not in `PATH`), so the race detector's cgo build fails
(`cgo: C compiler "gcc" not found`). This is an **environment limitation, not a
code defect** — it matches the per-adapter review notes (claude, opencode). The
new `builtin` package introduces negligible concurrency (it only constructs and
registers adapters; the `Registry` it drives is mutex-disciplined — see
`registry.go`). Race validation is provided by CI on Linux (`e9cc5bb`).

## 7. Protocol gaps

**None.** Protocol v1 (`protocol.ProtocolVersion == 1`) is frozen. No adapter
added event types, methods, or shared types to `internal/adapter/codingagent/
protocol`. No protocol-design decision was made or changed during integration;
therefore no separate gap-resolution was required (per the integration brief:
"do not change Protocol v1 without separate gap resolution").

The integration did surface the need for a **central wiring site**, which was
satisfied additively by the new `builtin` package — not a protocol change.

## 8. Explicitly not implemented (rule §36.25 — never faked)

These are documented per-adapter and in the compliance matrix; they are
**explicit gaps, not stubs disguised as finished**:

- `ListModels` returns empty for engines with no offline catalogue (no
  hard-coded model names, §36.8). Model catalogue arrives in M6-1.
- `InspectQuota` reports UNKNOWN where the headless CLI exposes no live quota
  API (§20.1, §36.10). Per-run usage flows via `usage.updated`.
- `SendMessage`/`Resume` return explicit errors where the engine has no
  headless live-message or resumable-session contract.
- Authenticated model enumeration / live quota / live health are covered by each
  adapter's opt-in build-tagged smoke test (skipped in CI, rule §36.5).

## 9. Commit graph (`main..integration/adapters`)

```
bd01a8a docs(adapters): index README + flip AC-5 to done in compliance matrix
dcc387f feat(adapter): central registry wiring for six built-in engines
aa77d29 Merge adapter/opencode into integration/adapters (AC-5)
69e901d Merge adapter/grok into integration/adapters (AC-5)
651ccb1 Merge adapter/kimi into integration/adapters (AC-5)
cb5c8f4 Merge adapter/gemini into integration/adapters (AC-5)
ac9bd1c Merge adapter/claude into integration/adapters (AC-5)
658822c Merge adapter/codex into integration/adapters (AC-5)
```

(The adapter feature commits below each merge are the original branch contents.)

## 10. Readiness verdict

**READY** for review/merge of `integration/adapters` (not into `main` by this
task — no push performed, per instructions).

- All six required engines are integrated and registered (AC-5).
- AC-6 (7th agent via plugin, no core changes) is preserved — no core package was
  modified by any adapter or by the integration; the central registry is purely
  additive.
- The app builds, the full test suite is green, `check.ps1` passes, and
  `git diff --check` is clean.
- Protocol v1 is unchanged; no silent protocol-design decisions.
- The only un-run gate is `-race` on this Windows host (no gcc/cgo); CI covers it
  on Linux. This is the sole caveat and is environmental.

Recommended follow-ups (out of scope for this integration):
- Run `go test -race ./...` in Linux CI against `integration/adapters`.
- Wire `builtin.RegisterAll` into the daemon startup once the supervisor is
  connected to the live scheduler (M2-8 / M6).
- Add per-adapter review notes for gemini/kimi/grok to match codex/claude/opencode.
