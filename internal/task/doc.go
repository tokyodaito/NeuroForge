// Package task implements the task model, backlog and task state machine.
//
// STATUS: M1 implemented — local backlog, free-form tasks, attachments, state machine.
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §9): accept free-form task input and
// attachments, store attachments content-addressed (§9.5), and manage the task
// state machine (NEW / INGESTED / PAUSED / CANCELLED in M1).
//
// Implemented in M1:
//   - Backlog.Add: free-form text task with optional title/attachments (AC-3)
//   - Content-addressed attachment storage (SHA-256, §9.5, AC-4)
//   - Task state machine: NEW / INGESTED / PAUSED / CANCELLED
//   - Every state change recorded in audit (§29.4)
//
// Planned for later milestones:
//   - M2+: Task compiler (objective, ACs, scope, risk — §18.1)
//   - M2+: Additional task states (COMPILED, READY, RUNNING, VERIFIED, etc.)
//
// Boundaries: must not perform external network calls; external upload decisions
// are enforced by package policy.
package task
