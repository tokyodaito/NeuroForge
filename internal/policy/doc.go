// Package policy encodes the Factory Security Policy and pipeline toggles.
//
// STATUS: scaffold — not implemented (planned for milestone M0/M8).
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §5, §29): the immutable pipeline switches
// (specification/planning/design/implementation/tests/review/git/change_request/
// merge/post_merge), their dependency rules (e.g. push=false forces
// change_request.create=false, merge=false, post_merge=false), and the prompt
// injection priority order (Factory Policy > Constitution > Task Spec > Repo docs
// > Source comments > External attachments).
//
// Boundaries: project security policy cannot be weakened by a task override (rule
// §29, AC-29) and an agent may not disable checks that validate its own output
// (rule §36.16).
package policy
