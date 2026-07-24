// Package policy encodes the Factory Security Policy and the pipeline toggle
// model.
//
// STATUS: core implemented for milestone M0 (spec §4, §5, §5.1, §29).
//
// Implemented in M0:
//   - The typed pipeline toggle set (§5): specification, planning, design,
//     implementation, tests, review, git, change_request, merge, post_merge.
//   - Autonomy profiles (§4.1–§4.5): PLAN_ONLY, LOCAL_REVIEW, REMOTE_REVIEW,
//     AUTONOMOUS, CUSTOM, each yielding a default pipeline.
//   - Dependency normalisation (§5.1): push=false forces change_request/merge/
//     post_merge off; merge=false forces post_merge off; etc.
//   - The AC-29 invariant: a task override can only further restrict a project's
//     policy and can never disable a mandatory (non-disableable) project check.
//   - LOCAL_REVIEW structural network lock (AC-7, ADR-0008): push/change_request/
//     merge/post_merge are unreachable when the project is network-locked.
//   - The prompt-injection priority order (§29.3) as typed constants.
//   - Typed policy decisions/violations and an action gate (Allows).
//
// Boundaries: this is a pure domain package. It must not import CLI, TUI,
// SQLite, daemon, adapters, or any provider. Enforcement points (Merge Governor,
// agent env allowlist, stage wiring) consume this package in later milestones.
package policy
