package merge

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"

	"neuroforge/internal/adapter/vcs"
	"neuroforge/internal/audit"
	"neuroforge/internal/policy"
	"neuroforge/internal/storage"
)

// fakeProvider is a vcs.ChangeRequestProvider that records every call and is
// configurable as network/local and supports every capability.
type fakeProvider struct {
	id       vcs.ProviderID
	network  bool
	calls    atomic.Int32
	mergeErr error
}

func (f *fakeProvider) ID() vcs.ProviderID { return f.id }
func (f *fakeProvider) Capabilities() vcs.Capabilities {
	return vcs.Capabilities{
		PushBranch: true, CreateChangeRequest: true, UpdateChangeRequest: true,
		GetChecks: true, EnableAutoMerge: true, Merge: true, Revert: true,
		IsNetwork: f.network,
	}
}
func (f *fakeProvider) PushBranch(context.Context, vcs.PushBranchRequest) (vcs.PushResult, error) {
	f.calls.Add(1)
	return vcs.PushResult{RemoteBranch: "feat"}, nil
}
func (f *fakeProvider) CreateChangeRequest(context.Context, vcs.CreateChangeRequestRequest) (vcs.ChangeRequest, error) {
	f.calls.Add(1)
	return vcs.ChangeRequest{Provider: f.id, Number: 1, State: "open"}, nil
}
func (f *fakeProvider) UpdateChangeRequest(context.Context, vcs.UpdateChangeRequestRequest) (vcs.ChangeRequest, error) {
	f.calls.Add(1)
	return vcs.ChangeRequest{Provider: f.id, Number: 1, State: "open"}, nil
}
func (f *fakeProvider) GetChecks(context.Context, vcs.GetChecksRequest) (vcs.CheckStatus, error) {
	f.calls.Add(1)
	return vcs.CheckStatus{AllPassed: true, RequiredPassed: true}, nil
}
func (f *fakeProvider) EnableAutoMerge(context.Context, vcs.EnableAutoMergeRequest) error {
	f.calls.Add(1)
	return nil
}
func (f *fakeProvider) Merge(context.Context, vcs.MergeRequest) (vcs.MergeResult, error) {
	f.calls.Add(1)
	if f.mergeErr != nil {
		return vcs.MergeResult{}, f.mergeErr
	}
	return vcs.MergeResult{Merged: true, CommitSHA: "merged-sha"}, nil
}
func (f *fakeProvider) Revert(context.Context, vcs.RevertRequest) (vcs.RevertResult, error) {
	f.calls.Add(1)
	return vcs.RevertResult{Reverted: true}, nil
}

func newTestAuthority(t *testing.T) (*Authority, *storage.DB) {
	t.Helper()
	db := newTestDB(t)
	rec := audit.NewRecorder(db, nil)
	return NewAuthority(rec, nil), db
}

func newTestDB(t *testing.T) *storage.DB {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := storage.Open(ctx, dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// remoteReviewResolved builds a resolved REMOTE_REVIEW policy (push + CR, no merge).
func remoteReviewResolved() policy.Resolved {
	res, _ := policy.Resolve(policy.NewProject(policy.ProfileRemoteReview), policy.TaskContext{})
	return res
}

func autonomousResolved() policy.Resolved {
	res, _ := policy.Resolve(policy.NewProject(policy.ProfileAutonomous), policy.TaskContext{})
	return res
}

func localReviewResolved() policy.Resolved {
	res, _ := policy.Resolve(policy.NewProject(policy.ProfileLocalReview), policy.TaskContext{})
	return res
}

func allowMergeResult(res policy.Resolved) Result {
	return Result{Decision: DecisionAllowMerge, Gates: []Gate{{Name: "all", Passed: true}}, Reason: "ok"}
}
func allowPushResult(res policy.Resolved) Result {
	return Result{Decision: DecisionAllowPush, Gates: []Gate{{Name: "all", Passed: true}}, Reason: "ok"}
}
func allowCRResult(res policy.Resolved) Result {
	return Result{Decision: DecisionAllowChangeRequest, Gates: []Gate{{Name: "all", Passed: true}}, Reason: "ok"}
}

func TestAuthority_Merge_AllowsWhenGovernorAndPolicyAgree(t *testing.T) {
	t.Parallel()
	auth, _ := newTestAuthority(t)
	p := &fakeProvider{id: vcs.ProviderGitHub, network: true}
	res := autonomousResolved()

	out, err := auth.Merge(context.Background(), allowMergeResult(res), res, p, vcs.MergeRequest{
		TaskID: "T1", Number: 1, HeadSHA: "abc",
	})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !out.Merged {
		t.Fatal("expected merged")
	}
	if p.calls.Load() != 1 {
		t.Fatalf("expected 1 provider call, got %d", p.calls.Load())
	}
}

func TestAuthority_Merge_RefusesWithoutAllowMerge(t *testing.T) {
	t.Parallel()
	auth, _ := newTestAuthority(t)
	p := &fakeProvider{id: vcs.ProviderGitHub, network: true}
	res := autonomousResolved()

	// Governor only allowed push, not merge.
	_, err := auth.Merge(context.Background(), allowPushResult(res), res, p, vcs.MergeRequest{TaskID: "T1"})
	if err == nil {
		t.Fatal("expected refusal when decision is not ALLOW_MERGE")
	}
	if p.calls.Load() != 0 {
		t.Fatalf("provider must not be called on refused merge, got %d", p.calls.Load())
	}
}

func TestAuthority_Merge_RefusesWhenPolicyForbids(t *testing.T) {
	t.Parallel()
	auth, _ := newTestAuthority(t)
	p := &fakeProvider{id: vcs.ProviderGitHub, network: true}
	// REMOTE_REVIEW: no merge allowed, even though we pass an ALLOW_MERGE decision.
	res := remoteReviewResolved()
	_, err := auth.Merge(context.Background(), allowMergeResult(res), res, p, vcs.MergeRequest{TaskID: "T1"})
	if err == nil {
		t.Fatal("expected policy refusal in REMOTE_REVIEW")
	}
	if p.calls.Load() != 0 {
		t.Fatalf("provider must not be called, got %d", p.calls.Load())
	}
}

func TestAuthority_NetworkProvider_RefusedInLocalReview(t *testing.T) {
	t.Parallel()
	auth, _ := newTestAuthority(t)
	p := &fakeProvider{id: vcs.ProviderGitHub, network: true}
	res := localReviewResolved()

	// Even if a stale ALLOW_MERGE decision is passed, the network lock refuses.
	_, err := auth.Merge(context.Background(), allowMergeResult(res), res, p, vcs.MergeRequest{TaskID: "T1"})
	if err == nil {
		t.Fatal("expected network-lock refusal")
	}
	if p.calls.Load() != 0 {
		t.Fatalf("network provider must not be called in LOCAL_REVIEW, got %d", p.calls.Load())
	}
}

func TestAuthority_Push_RefusedWhenPolicyForbids(t *testing.T) {
	t.Parallel()
	auth, _ := newTestAuthority(t)
	p := &fakeProvider{id: vcs.ProviderGitHub, network: true}
	res := localReviewResolved()

	_, err := auth.Push(context.Background(), allowPushResult(res), res, p, vcs.PushBranchRequest{TaskID: "T1"})
	if err == nil {
		t.Fatal("expected push refusal in LOCAL_REVIEW")
	}
	if p.calls.Load() != 0 {
		t.Fatalf("push must not happen in LOCAL_REVIEW (AC-7), got %d calls", p.calls.Load())
	}
}

func TestAuthority_CreateChangeRequest_RequiresPush(t *testing.T) {
	t.Parallel()
	auth, _ := newTestAuthority(t)
	p := &fakeProvider{id: vcs.ProviderGitHub, network: true}

	// AC-14: CR requires push. AUTONOMOUS has push+CR enabled.
	res := autonomousResolved()
	_, err := auth.CreateChangeRequest(context.Background(), allowCRResult(res), res, p,
		vcs.CreateChangeRequestRequest{TaskID: "T1", HeadBranch: "feat", BaseBranch: "main"})
	if err != nil {
		t.Fatalf("create CR should succeed in AUTONOMOUS: %v", err)
	}
	if p.calls.Load() != 1 {
		t.Fatalf("expected 1 call, got %d", p.calls.Load())
	}
}

func TestAuthority_AuditsDeliveryActions(t *testing.T) {
	t.Parallel()
	auth, db := newTestAuthority(t)
	p := &fakeProvider{id: vcs.ProviderGitHub, network: true}
	res := autonomousResolved()

	_, err := auth.Merge(context.Background(), allowMergeResult(res), res, p, vcs.MergeRequest{
		TaskID: "T-audit", Number: 1, HeadSHA: "abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	hist, err := db.ListAuditEvents(context.Background(), storage.AuditFilter{ScopeID: "T-audit"})
	if err != nil {
		t.Fatal(err)
	}
	foundMerge := false
	for _, e := range hist {
		if e.Type == "vcs.merge" {
			foundMerge = true
		}
	}
	if !foundMerge {
		t.Fatal("audit must contain vcs.merge event (§29.4)")
	}
}

func TestAuthority_AuditsDeniedDelivery(t *testing.T) {
	t.Parallel()
	auth, db := newTestAuthority(t)
	p := &fakeProvider{id: vcs.ProviderGitHub, network: true}
	res := localReviewResolved()

	_, _ = auth.Push(context.Background(), allowPushResult(res), res, p, vcs.PushBranchRequest{TaskID: "T-deny"})

	hist, err := db.ListAuditEvents(context.Background(), storage.AuditFilter{ScopeID: "T-deny"})
	if err != nil {
		t.Fatal(err)
	}
	foundDeny := false
	for _, e := range hist {
		if e.Type == "vcs.delivery.denied" {
			foundDeny = true
		}
	}
	if !foundDeny {
		t.Fatal("audit must contain a denied-delivery event for LOCAL_REVIEW push (AC-7 observability)")
	}
}

func TestAuthority_LocalProvider_NotBlockedByNetworkLock(t *testing.T) {
	t.Parallel()
	auth, _ := newTestAuthority(t)
	// Local git provider is NOT a network provider; it may merge locally even in
	// a profile that forbids REMOTE merge (the §5.1 R5 local-merge mode).
	p := &fakeProvider{id: vcs.ProviderLocalGit, network: false}
	res := autonomousResolved()
	_, err := auth.Merge(context.Background(), allowMergeResult(res), res, p, vcs.MergeRequest{TaskID: "T1"})
	if err != nil {
		t.Fatalf("local merge should be allowed: %v", err)
	}
}
