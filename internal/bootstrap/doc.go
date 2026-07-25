// Package bootstrap implements `forge init`, the onboarding wizard, the system
// scan, the installation planner, platform-specific installers, the
// authentication wizard, the toolchain lock, `forge update` and `forge init
// --repair` (spec §7, milestone M13).
//
// STATUS: implemented for milestone M13.
//
// Hard safety rules enforced throughout (spec §7.2/§36.17/§36.18/§36.19):
//
//   - NO silent installation: every install step is shown in the plan and
//     requires explicit confirmation (§7.2 stage 4). `--dry-run` produces a plan
//     and changes NOTHING (AC-25).
//   - NO silent privilege escalation: any step needing sudo is flagged and only
//     run after explicit confirmation (§36.18).
//   - Shell profile changes are shown as a diff before they are applied (§7.2
//     stage 4 "изменять shell profile без показа diff" is forbidden).
//   - NeuroForge NEVER asks for provider passwords directly (§7.2 stage 6):
//     authentication uses each provider's OFFICIAL mechanism; the wizard only
//     launches it and reports the outcome.
//   - A provider CLI is NEVER updated during an active task (§36.19): the
//     toolchain lock refuses an update while a run is in progress.
//   - Installer tests never install real system packages (rule §33): a fake
//     package manager is the default in CI.
package bootstrap
