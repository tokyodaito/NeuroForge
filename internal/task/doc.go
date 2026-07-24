// Package task implements the task model, backlog and task compiler.
//
// STATUS: scaffold — not implemented (planned for milestone M1; task compiler is
// extended in later milestones).
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §9, §18.1): accept free-form task input and
// attachments, store attachments content-addressed (§9.5), apply provider-upload
// policy/redaction (§9.6), and compile tasks into objective, acceptance criteria,
// non-goals, assumptions, constraints, risk, complexity and proposed scope.
//
// Boundaries: must not perform external network calls; external upload decisions
// are enforced by package policy.
package task
