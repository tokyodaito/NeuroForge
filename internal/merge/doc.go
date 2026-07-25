// Package merge implements the deterministic Merge Governor.
//
// STATUS: implemented for milestone M8 (ADR-0009).
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §28): a deterministic decision engine that
// checks specification lock, scope validity, required checks, acceptance
// evidence, findings, target policy, branch currency, budget and visual policy
// before emitting one of ALLOW_LOCAL_RESULT/ALLOW_PUSH/ALLOW_CHANGE_REQUEST/
// ALLOW_MERGE/REQUIRE_REBASE/REQUIRE_REPAIR/POLICY_BLOCKED/QUARANTINE. See
// ADR-0009.
//
// Implemented in M8:
//   - The [Decide] function: a pure, deterministic gate evaluation that never
//     calls an LLM (rule §22.6/§36.6).
//   - The §24.5 enforcement: a task override that disables tests cannot bypass
//     the mandatory merge rule (tests_required_for_merge gate). When tests ran
//     but failed, the decision is REQUIRE_REPAIR; when tests were disabled
//     entirely, the decision is POLICY_BLOCKED.
//   - The AC-29 enforcement: mandatory project reviews producing blocker
//     findings block delivery.
//   - The §24.4/§25.1 local-result labels (LocalResultLabels): IMPLEMENTED /
//     NOT TESTED / NOT REVIEWED / LOCAL BRANCH ONLY.
//
// Boundaries: the Governor only AUTHORISES; the actual delivery is performed by
// adapter/vcs (M11), which is the only holder of merge credentials and is
// unreachable without an ALLOW_* decision. This package does not perform Git
// network operations or hold credentials.
package merge
