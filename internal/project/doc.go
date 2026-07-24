// Package project implements the project registry and project lifecycle.
//
// STATUS: scaffold — not implemented (planned for milestone M1).
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §8): register projects, run project
// onboarding (detect languages, build system, test/lint commands, AGENTS.md,
// remote provider), and drive the project state machine (§8.4). See the state
// machine in docs/architecture/STATE_MACHINES.md.
//
// Boundaries: must not modify the user's primary checkout (rule §17.1) and must
// confirm detected commands with the user before writing them to config (§8.3).
package project
