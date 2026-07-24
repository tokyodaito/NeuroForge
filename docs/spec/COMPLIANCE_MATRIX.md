# Compliance matrix

Maps the spec's acceptance criteria (§35) and a selection of hard rules (§36) to
their implementation status, owning milestone, issue (see
[`../milestones/IMPLEMENTATION_PLAN.md`](../milestones/IMPLEMENTATION_PLAN.md)),
and the package(s) responsible.

Statuses:

- `done` — implemented and covered by an automated test.
- `partial` — some pieces exist; not yet acceptance-complete.
- `planned` — not started; milestone/issue assigned.
- `n/a` — not applicable at this stage.

The spec (`NEUROFORGE_SPEC.md`) is authoritative; this matrix is a tracking view.

## Acceptance criteria (spec §35)

| AC | Requirement (abridged) | Status | Milestone | Issue(s) | Package(s) |
|----|------------------------|--------|-----------|----------|------------|
| AC-1 | `forge` (no args) opens interactive TUI | partial (shell planned in M0-8; today prints a clear "not implemented" notice) | M0 | M0-8 | `internal/tui`, `internal/cli` |
| AC-2 | Manage projects/tasks without CLI | planned | M1 | M1-6 | `internal/tui`, `internal/project` |
| AC-3 | Create a task with free-form text (no template) | planned | M1 | M1-5 | `internal/task` |
| AC-4 | Attach an image to a task | planned | M1 | M1-5 | `internal/task` |
| AC-5 | Codex / Claude Code / Grok Build / Kimi Code / OpenCode / Gemini CLI | planned | M4–M5 | M4-n, M5-n | `internal/adapter/codingagent` |
| AC-6 | A 7th agent via plugin, no core changes | planned | M2 | M2-7 | `internal/adapter/codingagent` |
| AC-7 | LOCAL_REVIEW performs no Git network ops | planned (enforced by design — ADR-0008) | M0/M11 | M0-7, M11-7 | `internal/policy`, `internal/adapter/vcs` |
| AC-8 | Code saved in a separate local result branch | planned | M3 | M3-5 | `internal/workspace` |
| AC-9 | Open diff and worktree from TUI | planned | M3 | M3-5 | `internal/tui`, `internal/workspace` |
| AC-10 | Accept / reject / ask-for-changes | planned | M3/M11 | M3-5, M11-2 | `internal/workspace`, `internal/adapter/vcs` |
| AC-11 | Disable test generation | planned | M8 | M8-3 | `internal/policy`, `internal/testengine` |
| AC-12 | Disable running existing tests separately | planned | M8 | M8-3 | `internal/policy`, `internal/testengine` |
| AC-13 | Disable AI-review | planned | M8 | M8-4 | `internal/policy`, `internal/review` |
| AC-14 | Push / PR-MR / merge switchable separately | planned | M8/M11 | M8-1, M11-6 | `internal/policy`, `internal/adapter/vcs` |
| AC-15 | Quota failure after edits → continuation via fallback, checkpoint kept | planned | M7 | M7-1, M7-5 | `internal/supervisor`, `internal/adapter/codingagent` |
| AC-16 | Simple task → cheap route | planned | M6 | M6-4 | `internal/router` |
| AC-17 | Complex task → strong model | planned | M6 | M6-4 | `internal/router` |
| AC-18 | Dashboard shows exact vs ~estimated vs unknown usage distinctly | planned | M6 | M6-9 | `internal/tui`, `internal/quota` |
| AC-19 | GPT Image and Nano Banana adapters | planned | M9 | M9-3, M9-4 | `internal/adapter/imageprovider` |
| AC-20 | Generate a visual specification from text | planned | M9/M10 | M9-6 | `internal/design`, `internal/adapter/imageprovider` |
| AC-21 | Create UI implementation task from an attached image | planned | M10 | M10-8 | `internal/task`, `internal/visual` |
| AC-22 | Visual Verification captures a real screenshot | planned | M10 | M10-3 | `internal/adapter/visualharness`, `internal/visual` |
| AC-23 | Visual discrepancy triggers repair loop | planned | M10 | M10-5 | `internal/visual` |
| AC-24 | Disabled visual verification never claims UI is verified | planned | M10 | M10-7 | `internal/visual`, `internal/policy` |
| AC-25 | `forge init --dry-run` shows a plan, changes nothing | planned | M13 | M13-3 | `internal/cli`, bootstrap |
| AC-26 | `forge init` installs tools, offers official auth, runs doctor | planned | M13 | M13-1..M13-6 | bootstrap |
| AC-27 | Daemon resumes unfinished tasks after restart | planned | M0/M7 | M0-4, M7-3 | `internal/daemon`, `internal/storage` |
| AC-28 | Agent has no merge credentials | planned (enforced by design — ADR-0008) | M3/M11 | M3-4, M11-5 | `internal/supervisor`, `internal/merge` |
| AC-29 | Non-disableable security policy cannot be weakened by task override | planned (core in M0-7) | M0/M8 | M0-7, M8-1 | `internal/policy` |
| AC-30 | Full task history available in audit | planned | M0+ | M0-6 | `internal/audit` |

## CLI surface (spec §30)

| Command | Status | Issue |
|---------|--------|-------|
| `forge version` | done | M0-2 |
| `forge help` | done | M0-2 |
| `forge` (TUI) | partial (notice only) | M0-8 |
| `forge project ...` | planned | M1-1..M1-4 |
| `forge task ...` | planned | M1-5 |
| `forge agent ...` / `forge model ...` / `forge route ...` | planned | M2, M6 |
| `forge image-provider ...` | planned | M9 |
| `forge quota` / `usage` / `cost` | planned | M6 |
| `forge plugin ...` | planned | M2-7 |
| `forge audit` | planned | M0-6 |
| `forge emergency-stop` / `forge cleanup` | planned | M0 |
| `forge init` / `doctor` / `update` | planned | M13 |

## Hard rules (spec §36) — current enforcement

| # | Rule | Status / mechanism |
|---|------|--------------------|
| 1–2 | Modular monolith, not microservices / one giant package | done — ADR-0001 + package layout |
| 3 | No Kubernetes | done — not used |
| 4 | No web UI before TUI | done — TUI-first in plan |
| 5 | No real paid models in CI | planned — fake agents (M2-6), fake image provider (M9-2) |
| 6 | Fake coding agent first | planned — M2-6 |
| 7 | Stabilise adapter protocol, then adapters | planned — M2 before M4/M5 |
| 8 | No hard-coded model names in core | planned — catalog M6-1 (enforced by review) |
| 9 | Separate coding agents from image providers | done (structure) — ADR-0005/0006 |
| 10 | Quota not reported as exact unless provider says so | planned — M6-6 |
| 11 | No full repo in prompt | planned — M12-3 |
| 12 | No LLM for Git/policy/quota/budget arithmetic | done (policy) — ADR-0009; code-only |
| 13 | No push in LOCAL_REVIEW | planned (design-enforced) — ADR-0008 |
| 14 | Never modify primary checkout | planned — ADR-0007 (M3-1) |
| 15 | Agent cannot change project security policy | planned — M0-7 |
| 16 | Agent cannot disable checks that validate its output | planned — M0-7 |
| 17–18 | No silent install / privilege escalation | planned — M13-3 |
| 19 | No provider CLI update during active run | planned — M13-5 |
| 20 | App builds + demonstrable scenario after each milestone | enforced — `make check` gate |
| 21 | Record deviations as ADR | done — `docs/adr/` |
| 22 | Every AC has an automated/integration test | enforced — per-issue Checks |
| 23 | Spec is source of truth | done — referenced everywhere |
| 24 | Agent may not self-reduce project scope | planned — M11-5 (scope_valid) |
| 25 | Unimplemented requirements explicitly marked | done — scaffold `doc.go` markers, this matrix, help text |

## Bootstrap (this change set) — what is actually done

- `forge version` / `forge help` implemented + unit-tested (`internal/cli`,
  `internal/version`).
- Modular-monolith package skeleton created (25 packages build & vet clean); every
  not-yet-implemented package carries a `STATUS: scaffold — not implemented` doc
  comment.
- `make build` / `test` / `lint` / `check` configured and green; zero external
  dependencies (`go.mod` clean, no `go.sum`).
- ADRs 0001–0009, architecture docs (COMPONENTS / DATA_FLOW / STATE_MACHINES),
  AGENTS/README/CONTRIBUTING, and this matrix + implementation plan in place.
- `docs/spec/NEUROFORGE_SPEC.md` **untouched**.
