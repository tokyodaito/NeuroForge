// Package merge implements the deterministic Merge Governor.
//
// STATUS: scaffold — not implemented (planned for milestone M11).
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §28): a deterministic decision engine that
// checks specification lock, scope validity, required checks, acceptance evidence,
// findings, target policy, branch currency, budget and visual policy before
// emitting one of ALLOW_LOCAL_RESULT/ALLOW_PUSH/ALLOW_CHANGE_REQUEST/ALLOW_MERGE/
// REQUIRE_REBASE/REQUIRE_REPAIR/POLICY_BLOCKED/QUARANTINE. See ADR-0009.
//
// Boundaries: must not itself perform Git network operations or hold merge
// credentials; it only authorizes delivery actions performed by adapter/vcs.
package merge
