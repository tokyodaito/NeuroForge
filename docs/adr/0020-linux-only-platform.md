# ADR-0020: Linux-only platform; WSL2 for Windows hosts

- **Status:** Accepted
- **Date:** 2026-08-01
- **Spec refs:** header "Целевые ОС" (target OS), §7 (bootstrap), §29 (security)
- **Amends:** the spec's original "Windows, macOS, Linux" target-OS line

## Context

NeuroForge originally targeted Windows, macOS and Linux. Native Windows support
imposed real and growing costs:

- PATHEXT-aware executable discovery, `.exe`/`.cmd`/`.bat`/`.ps1` npm-shim
  handling and case-insensitive environment-key logic duplicated in every
  coding-agent adapter.
- A second process-management implementation (CREATE_NEW_PROCESS_GROUP +
  `taskkill /T /F`) alongside the unix `setpgid` + negative-pgid path.
- Windows-only CI job, PowerShell script tree (`scripts/*.ps1`) and a parallel
  documentation surface.
- Semantics with no Unix equivalent (no permission bits, no signals, different
  home/env model) that forced skips and weakened assertions in the test suite.

Every supported coding-agent CLI and the Z.ai Coding Plan workflow run fine
under WSL2, which is now the common way to run Linux dev tooling on a Windows
host. Maintaining a native Windows path buys little and slows all other work.

## Decision

- **Linux is the canonical and only first-class platform.** NeuroForge, its
  daemon and all coding-agent adapters target Linux.
- **WSL2 is the supported way to run NeuroForge from a Windows host.**
  Everything (clone, build, daemon, coding-agent CLIs, Git/SSH credentials,
  OpenCode auth) lives inside the WSL2 Linux filesystem; see
  `docs/platforms/WSL2.md`.
- **macOS** may keep compiling/running via the generic unix code paths
  (`//go:build unix`), but receives no dedicated support or CI coverage.
- Native Windows support is **deleted, not disabled**: Windows build-tagged
  files, PATHEXT/shim machinery, Windows env-allowlist keys, the PowerShell
  scripts, the CI windows job and the Windows platform guide are removed.

## Consequences

**Positive**

- One process-management implementation (proctree unix path) and one
  executable-discovery semantic (`exec.LookPath` + executable bit).
- Adapter env allowlists shrink to unix-essential keys; no case-insensitive
  env handling anywhere.
- CI is a single Linux job; test suite drops Windows skips and asserts real
  unix guarantees (e.g. 0o600 runtime files) unconditionally.

**Negative / trade-offs**

- Users on Windows must install and use WSL2; there is no fallback.
- OpenCode (and other CLIs) must be installed and authenticated inside WSL2 as
  the same Linux user that runs NeuroForge; Windows-side installs are ignored.
- macOS regressions may go unnoticed (no CI).

## Alternatives considered

- **Keep native Windows support.** Rejected: continuing cost across every
  adapter and the CI/test surface for a platform none of the supported
  workflows require natively.
- **Support Windows only via cross-compilation.** Rejected: the blockers are
  runtime semantics (process groups, PATHEXT, env casing), not compilation.
