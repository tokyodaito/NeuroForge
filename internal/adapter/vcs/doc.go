// Package vcs implements the change-request provider abstraction (spec §17.6):
// Local Git, GitHub Pull Requests and GitLab Merge Requests.
//
// STATUS: implemented for milestone M11 (ADR-0015).
//
// The [ChangeRequestProvider] interface is the §17.6 surface
// (PushBranch/CreateChangeRequest/UpdateChangeRequest/GetChecks/
// EnableAutoMerge/Merge/Revert). Three implementations live in sub-packages:
//
//   - localgit: §17.5 accept-into-current-branch (merge/squash/cherry-pick/patch)
//     into the user's checkout. Performs NO Git network operations.
//   - github: GitHub Pull Requests over the REST API.
//   - gitlab: GitLab Merge Requests over the REST API.
//
// Every delivery call flows through the Merge Governor Authority
// (internal/merge.Authority), the single holder of merge authority. Providers
// are therefore unreachable in LOCAL_REVIEW (AC-7), never receive VCS
// credentials from agent processes (AC-28), and only merge when the Governor
// emitted ALLOW_MERGE.
//
// Boundaries: this package defines the contract + a Registry; concrete HTTP/git
// work lives in sub-packages. No core package imports a concrete provider.
package vcs
