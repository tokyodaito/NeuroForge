// Package project implements the project registry and project lifecycle.
//
// STATUS: M1 implemented — project registry, state machine, Git validation.
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §8): register projects, validate they are
// Git repositories, and drive the project state machine (§8.4). See the state
// machine in docs/architecture/STATE_MACHINES.md.
//
// Implemented in M1:
//   - Registry: Add / List / Get / Remove (§8.1)
//   - State machine: DISABLED / IDLE / RUNNING / PAUSED / DRAINING / ERROR (§8.4)
//   - Git repository validation (read-only, never modifies checkout §17.1)
//   - Every state change recorded in audit (§29.4)
//
// Planned for later milestones:
//   - M1-2: Project onboarding & detection (languages/build/test/lint/AGENTS.md)
//   - M2+: RUNNING / DRAINING states entered by the scheduler (require agents)
//
// Boundaries: must not modify the user's primary checkout (rule §17.1) and must
// confirm detected commands with the user before writing them to config (§8.3).
package project
