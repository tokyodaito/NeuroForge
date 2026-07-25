// Package vcs implements the change-request provider abstraction (spec §17.6).
//
// STATUS: implemented for milestone M11.
//
// Scope: the ChangeRequestProvider interface that Local Git, GitHub PRs and
// GitLab MRs all implement. Every delivery operation (PushBranch,
// CreateChangeRequest, UpdateChangeRequest, GetChecks, EnableAutoMerge, Merge,
// Revert) flows through exactly one chokepoint — the Merge Governor Authority
// (internal/merge) — which authorises each call against a Governor decision and
// the resolved policy before any provider method runs.
//
// Security invariants (spec §28, §29, AC-7, AC-28, ADR-0008/0015):
//   - No provider method is reachable in LOCAL_REVIEW: the Authority refuses
//     every delivery action when policy.Allows(...) is false, and the policy
//     resolver structurally disables push/change_request/merge for network-
//     locked profiles. Providers therefore perform ZERO Git network operations
//     in LOCAL_REVIEW by construction (AC-7).
//   - Credentials never cross into agent processes. A provider holds the
//     credential resolver the DAEMON injected; agent subprocesses get only the
//     allowlisted environment (§29.2, AC-28) and never see a provider handle.
//   - Only the Authority may call Merge — there is no second path to merge
//     authority (§28: "Agent process does not have merge credentials").
package vcs

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// ProviderID identifies a change-request provider implementation.
type ProviderID string

const (
	// ProviderLocalGit is the local-only provider (§17.5 accept-into-branch:
	// merge/squash/cherry-pick/patch into the user's checkout). It performs NO
	// Git network operations.
	ProviderLocalGit ProviderID = "local-git"
	// ProviderGitHub is the GitHub Pull Request provider (§17.6).
	ProviderGitHub ProviderID = "github"
	// ProviderGitLab is the GitLab Merge Request provider (§17.6).
	ProviderGitLab ProviderID = "gitlab"
)

// MergeMethod selects how a change is integrated (§17.5).
type MergeMethod string

const (
	MergeMethodMerge      MergeMethod = "merge"  // create a merge commit
	MergeMethodSquash     MergeMethod = "squash" // squash all commits into one
	MergeMethodRebase     MergeMethod = "rebase" // fast-forward / rebase
	MergeMethodCherryPick MergeMethod = "cherry-pick"
	MergeMethodPatch      MergeMethod = "patch" // apply a diff/patch
)

// Capabilities advertises which operations a provider supports. Local Git
// supports local merge/revert but not remote push/CR/auto-merge; GitHub and
// GitLab support the full remote surface. The Authority and the merge queue
// consult this to pick the right provider (e.g. local-merge fallback).
type Capabilities struct {
	// PushBranch: push a task branch to the remote.
	PushBranch bool
	// CreateChangeRequest: open a PR/MR.
	CreateChangeRequest bool
	// UpdateChangeRequest: amend a PR/MR (title/body/state).
	UpdateChangeRequest bool
	// GetChecks: read CI/required-check status.
	GetChecks bool
	// EnableAutoMerge: request the platform auto-merge when checks pass.
	EnableAutoMerge bool
	// Merge: integrate the change. Both local and remote providers set this;
	// the MECHANISM differs (local git merges into the checkout, remote calls
	// the platform merge API).
	Merge bool
	// Revert: undo a merged change.
	Revert bool
	// IsNetwork reports whether the provider performs Git network operations.
	// Local Git is NOT a network provider (AC-7). The Authority double-checks
	// this before any call in a network-locked profile.
	IsNetwork bool
}

// PushBranchRequest pushes a local task branch to the provider's remote.
type PushBranchRequest struct {
	// TaskID scopes the operation for audit.
	TaskID string
	// LocalBranch is the branch to push (e.g. forge/result/<task>).
	LocalBranch string
	// RemoteBranch is the destination ref on the remote.
	RemoteBranch string
	// HeadSHA is the commit being pushed (for audit + branch-currency checks).
	HeadSHA string
	// Force is honoured only when policy explicitly permits force-push (never
	// in LOCAL_REVIEW). Providers MUST refuse when Force is set but the
	// Authority did not authorise it.
	Force bool
}

// PushResult is the outcome of PushBranch.
type PushResult struct {
	RemoteBranch string
	HeadSHA      string
	PushedAt     string // RFC3339 when known
}

// CreateChangeRequestRequest opens a PR/MR (or, for local-git, records the
// local result as the reviewable artifact).
type CreateChangeRequestRequest struct {
	TaskID     string
	Title      string
	Body       string
	HeadBranch string // source branch (already pushed)
	BaseBranch string // target branch
	Draft      bool
	// ReviewerIDs is the platform-native list of reviewer handles/ids.
	ReviewerIDs []string
}

// UpdateChangeRequestRequest amends an existing PR/MR.
type UpdateChangeRequestRequest struct {
	TaskID string
	Number int // PR/MR number (platform-native)
	Title  string
	Body   string
	State  string // "open"/"closed" — empty means unchanged
}

// ChangeRequest is a PR (GitHub) or MR (GitLab).
type ChangeRequest struct {
	Provider   ProviderID
	Number     int
	Title      string
	Body       string
	HeadBranch string
	BaseBranch string
	State      string // "open"/"merged"/"closed"
	WebURL     string
	Mergeable  *bool // tri-state: nil unknown
}

// GetChecksRequest reads the CI/required-check status for a PR/MR head.
type GetChecksRequest struct {
	TaskID     string
	Number     int
	HeadBranch string
	HeadSHA    string
}

// CheckRun is one CI check on a PR/MR head.
type CheckRun struct {
	Name       string
	Status     string // "queued"/"in_progress"/"completed"
	Conclusion string // "success"/"failure"/"neutral"/"" when not completed
	Mandatory  bool
}

// CheckStatus aggregates the checks for a head.
type CheckStatus struct {
	AllPassed      bool
	RequiredPassed bool
	Pending        bool
	Checks         []CheckRun
}

// EnableAutoMergeRequest asks the platform to merge once checks pass.
type EnableAutoMergeRequest struct {
	TaskID string
	Number int
	Method MergeMethod
}

// MergeRequest integrates a change.
type MergeRequest struct {
	TaskID        string
	Number        int // 0 for a local merge (no PR/MR)
	HeadBranch    string
	BaseBranch    string
	HeadSHA       string
	Method        MergeMethod
	CommitMessage string
}

// MergeResult is the outcome of Merge.
type MergeResult struct {
	Merged     bool
	Method     MergeMethod
	CommitSHA  string // the resulting merge commit / squashed commit
	BaseBranch string
}

// RevertRequest reverts a previously merged change.
type RevertRequest struct {
	TaskID     string
	Number     int
	CommitSHA  string
	BaseBranch string
}

// RevertResult is the outcome of Revert.
type RevertResult struct {
	Reverted  bool
	RevertSHA string
}

// ChangeRequestProvider is the §17.6 abstraction. Every method is reachable
// ONLY through the Merge Governor Authority, which has already verified the
// Governor decision and the resolved policy permit the action.
//
// Implementations must be safe for concurrent use. They must NOT read process
// environment directly for credentials (the daemon injects a resolver, AC-28),
// and network providers must perform zero Git network operations when the
// Authority has not authorised them (AC-7 — the Authority guarantees this, but
// providers defend in depth by checking their own authorisation token).
type ChangeRequestProvider interface {
	// ID returns the stable provider identifier.
	ID() ProviderID
	// Capabilities advertises supported operations.
	Capabilities() Capabilities

	PushBranch(ctx context.Context, req PushBranchRequest) (PushResult, error)
	CreateChangeRequest(ctx context.Context, req CreateChangeRequestRequest) (ChangeRequest, error)
	UpdateChangeRequest(ctx context.Context, req UpdateChangeRequestRequest) (ChangeRequest, error)
	GetChecks(ctx context.Context, req GetChecksRequest) (CheckStatus, error)
	EnableAutoMerge(ctx context.Context, req EnableAutoMergeRequest) error
	Merge(ctx context.Context, req MergeRequest) (MergeResult, error)
	Revert(ctx context.Context, req RevertRequest) (RevertResult, error)
}

// Sentinel errors. Providers wrap these (via %w) so the Authority and the merge
// queue can classify outcomes deterministically (rule §36.6: no LLM in policy).
var (
	// ErrUnsupported is returned when a provider is asked for a capability it
	// does not advertise (e.g. asking local-git to PushBranch).
	ErrUnsupported = errors.New("vcs: operation not supported by this provider")
	// ErrPolicyDenied is returned by the Authority when the Governor decision
	// or resolved policy forbids the action.
	ErrPolicyDenied = errors.New("vcs: action denied by policy / merge governor")
	// ErrBranchNotCurrent is returned when the branch has fallen behind the
	// target since the Governor decision (the merge queue triggers a rebase).
	ErrBranchNotCurrent = errors.New("vcs: branch is not current with the target")
	// ErrChecksFailed is returned when required CI checks have not passed.
	ErrChecksFailed = errors.New("vcs: required checks did not pass")
	// ErrAuthFailed is returned when the provider credentials are missing or
	// rejected.
	ErrAuthFailed = errors.New("vcs: authentication failed")
	// ErrNetworkLocked is returned when a network operation is attempted in a
	// network-locked profile (defense-in-depth; the Authority prevents this).
	ErrNetworkLocked = errors.New("vcs: network operation attempted in a network-locked profile (AC-7)")
)

// Unsupported is a helper for providers to uniformly reject unsupported ops.
func Unsupported(id ProviderID, op string) error {
	return fmt.Errorf("%w: %s does not implement %s", ErrUnsupported, id, op)
}

// Registry maps provider ids to implementations (mirrors the image-provider
// registry shape, ADR-0006/0015). Adding a provider is purely additive: the
// Authority and merge queue learn it only through this registry.
type Registry struct {
	mu  sync.RWMutex
	all map[ProviderID]ChangeRequestProvider
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{all: map[ProviderID]ChangeRequestProvider{}}
}

// Register adds a provider. It errors on duplicate id (no silent override).
func (r *Registry) Register(p ChangeRequestProvider) error {
	if p == nil {
		return fmt.Errorf("vcs: cannot register a nil provider")
	}
	id := p.ID()
	if id == "" {
		return fmt.Errorf("vcs: provider has empty ID")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.all[id]; ok {
		return fmt.Errorf("vcs: provider %q already registered", id)
	}
	r.all[id] = p
	return nil
}

// MustRegister panics on registration error (wiring code with known-good sets).
func (r *Registry) MustRegister(p ChangeRequestProvider) {
	if err := r.Register(p); err != nil {
		panic(err)
	}
}

// Lookup returns the provider for an id.
func (r *Registry) Lookup(id ProviderID) (ChangeRequestProvider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.all[id]
	return p, ok
}

// IDs returns the registered provider ids in stable (alphabetical) order.
func (r *Registry) IDs() []ProviderID {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]ProviderID, 0, len(r.all))
	for id := range r.all {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// Len reports the number of registered providers.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.all)
}
