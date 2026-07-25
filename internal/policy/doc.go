// Package policy encodes the Factory Security Policy and the pipeline toggle
// model.
//
// STATUS: core implemented for milestone M0 (spec §4, §5, §5.1, §29); extended
// for M8 (§24 test scope, §24.2 test-path enforcement, pipeline stage status).
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
// Implemented in M8:
//   - The §24.1 test toggles: generate, modify_existing, run_existing,
//     run_generated, require_for_local_result, require_for_remote_merge.
//   - The §24.2 test-path scope rule: when test generation is disabled, test
//     paths become forbidden (CheckTestScope / CheckFileChanges). Normalisation
//     forces modify_existing and run_generated off when generate is off (R6/R7).
//   - The pipeline stage status (StageStatus): an explicit, human-readable
//     breakdown showing which stages are active, skipped, or locked — including
//     the §24.4/§25.1 local-result labels (IMPLEMENTED / NOT TESTED / NOT
//     REVIEWED).
//   - New Actions: ActModifyExistingTests, ActRunGeneratedTests,
//     ActSecurityReview, ActArchReview.
//
// Boundaries: this is a pure domain package. It must not import CLI, TUI,
// SQLite, daemon, adapters, or any provider. Enforcement points (Merge Governor,
// agent env allowlist, stage wiring) consume this package in later milestones.
package policy
