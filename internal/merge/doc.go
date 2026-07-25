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
// Implemented in M11 (ADR-0015):
//   - The [Authority]: the SINGLE holder of merge authority. Every delivery call
//     (push, PR/MR, auto-merge, merge, revert) on a vcs.ChangeRequestProvider
//     flows through it; it re-checks the Governor decision, the resolved policy
//     and the network lock before any provider method runs. Agent processes
//     never hold an Authority reference (§28, AC-28).
//   - The [Queue]: a deterministic FIFO merge queue that re-validates branch
//     currency at execution time and falls back to a local merge in the §5.1 R5
//     local-merge mode.
//
// Boundaries: the Governor only AUTHORISES; the Authority is the only merge
// authority and the only delivery call site; the actual Git work is performed by
// adapter/vcs providers, which are unreachable without an ALLOW_* decision. This
// package performs no Git network operations and holds no credentials.
package merge
