// Package vcs implements change-request providers (local Git, GitHub, GitLab).
//
// STATUS: scaffold — not implemented (planned for milestone M11).
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §17.6): the ChangeRequestProvider interface
// (PushBranch/CreateChangeRequest/UpdateChangeRequest/GetChecks/EnableAutoMerge/
// Merge/Revert). Every call is authorized by the Merge Governor (package merge)
// and constrained by the active autonomy profile (LOCAL_REVIEW forbids all network
// operations — AC-7).
//
// Boundaries: never invoked when push/change_request/merge are disabled by policy;
// credentials are held outside agent processes (§28, AC-28).
package vcs
