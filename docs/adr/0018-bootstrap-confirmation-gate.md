# ADR-0018: Bootstrap safety — explicit confirmation over the installer abstraction

- **Status:** Accepted
- **Date:** 2026-07-25
- **Spec refs:** §7 (bootstrap), §7.2 stage 3/4/5/6, §7.4 (toolchain lock),
  §7.5 (update + rollback), §36.17 (no silent install), §36.18 (no silent
  privilege escalation), §36.19 (no provider CLI update during active run),
  AC-25 (dry-run changes nothing), AC-26 (init installs + auth + doctor)

## Context

M13 delivers `forge init` and `forge update`. The spec mandates hard safety
invariants: nothing is installed silently (§36.17), no privilege is escalated
silently (§36.18), shell-profile changes are shown as a diff before applying
(§7.2 stage 4), provider passwords are never collected inside NeuroForge
(§7.2 stage 6), and a provider CLI is never updated during an active task
(§36.19). `--dry-run` must change nothing (AC-25).

## Decision

`internal/bootstrap` centralises every mutation behind one type, the
`Executor`, which is the ONLY thing that touches the system. The `Executor`
takes a confirmed `Installer` + a `Confirmer` and enforces, in order, for every
plan:

1. **Plan approval** (`Confirmer.ConfirmPlan`) — denied ⇒ `ErrNotConfirmed`,
   nothing runs (§36.17). A nil confirmer is rejected at construction.
2. **Shell-profile diff approval** (`Confirmer.ConfirmShellProfile`) — the diff
   is rendered from the plan and shown BEFORE any step runs; denied ⇒
   `ErrShellProfileNotApproved` (§7.2 stage 4).
3. **Per-step sudo approval** (`Confirmer.ConfirmSudo`) — each privilege-
   escalating step asks explicitly; denied ⇒ `ErrNotConfirmed` (§36.18).

The system is otherwise pure: `Scan` and `ComputePlan` are read-only, so
`Wizard.DryRun` (scan → plan) is provably a no-op (AC-25, tested with a
filesystem-snapshot assertion).

Supporting decisions:

- **Installer abstraction** (`Installer` + `Registry`): CI uses the
  `FakeInstaller` (rule §33: installer tests never install real system
  packages); production uses a `guidedInstaller` that prints each step and never
  escalates silently. A native installer would register into the `Registry`,
  always behind the confirmation gate.
- **Auth wizard** (`AuthWizard` + `LoginLauncher`): launches each provider's
  OFFICIAL login mechanism; NeuroForge never collects a password (tested — no
  password field crosses the boundary, §7.2 stage 6).
- **Toolchain lock** (`ToolchainLock`, §7.4): persists detected/installed
  versions; `Update` consults an `ActiveTaskGuard` and returns `ErrActiveTask`
  before any update during an active run (§36.19). `forge update` (§7.5) snapshots
  the previous lock and restores it on conformance failure
  (`ErrConformanceFailed`, rollback §7.5 step 5).

## Consequences

**Positive**

- §36.17/§36.18 are enforced by type: there is no `Installer.Install` call site
  that is not behind a confirmed `Executor`.
- AC-25 is provable: `DryRun` shares only read-only code with `Run`.
- §36.19 is structural: the lock refuses to update while a task is active.
- §7.5 rollback restores the previous working toolchain on a failed conformance
  suite.

**Negative / trade-offs**

- The production native install is guided (prints the official command) rather
  than silently invoking `sudo`/`brew`/`apt`. This is deliberate: a silent
  system mutation would violate §36.17/§36.18. A platform-specific installer
  that runs the native package manager registers into the `Registry` when the
  user opts in, still behind the confirmation gate.

## Alternatives considered

- **Direct shell-out from the wizard.** Rejected: no confirmation chokepoint, no
  test seam (rule §33), violates §36.17/§36.18.
- **Auto-approve `--yes` as silent.** Rejected: `--yes` still prints every action
  and only shortens the prompt; it does not bypass the shell-diff/sudo surfaces.
