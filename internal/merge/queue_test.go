package merge

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"neuroforge/internal/adapter/vcs"
	"neuroforge/internal/policy"
)

func TestQueue_RejectsNonAllowMergeDecision(t *testing.T) {
	t.Parallel()
	auth, _ := newTestAuthority(t)
	q := NewQueue(auth, nil, nil)
	err := q.Enqueue(QueueItem{
		TaskID: "T", Decision: Result{Decision: DecisionAllowPush},
		Provider: &fakeProvider{id: vcs.ProviderGitHub, network: true},
	})
	if err == nil {
		t.Fatal("must refuse non-ALLOW_MERGE decisions")
	}
}

func TestQueue_FIFO_Processing(t *testing.T) {
	t.Parallel()
	auth, _ := newTestAuthority(t)
	p := &fakeProvider{id: vcs.ProviderGitHub, network: true}
	res := autonomousResolved()
	q := NewQueue(auth, nil, nil)

	for _, id := range []string{"T1", "T2", "T3"} {
		if err := q.Enqueue(QueueItem{
			TaskID: id, Decision: allowMergeResult(res), Policy: res,
			Provider: p, Request: vcs.MergeRequest{TaskID: id, Number: 1, HeadSHA: "abc", Method: vcs.MergeMethodMerge},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if q.Len() != 3 {
		t.Fatalf("want 3 items, got %d", q.Len())
	}
	outs, err := q.Process(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(outs) != 3 {
		t.Fatalf("want 3 outcomes, got %d", len(outs))
	}
	for i, want := range []string{"T1", "T2", "T3"} {
		if outs[i].Item.TaskID != want {
			t.Errorf("outcome[%d].TaskID: want %s, got %s", i, want, outs[i].Item.TaskID)
		}
		if !outs[i].Merged {
			t.Errorf("outcome[%d] not merged: %v", i, outs[i].Err)
		}
	}
	if p.calls.Load() != 3 {
		t.Fatalf("want 3 provider merges, got %d", p.calls.Load())
	}
}

func TestQueue_LocalFallback_WhenRemoteFails(t *testing.T) {
	t.Parallel()
	auth, _ := newTestAuthority(t)
	// Remote provider that always fails merge.
	remote := &fakeProvider{id: vcs.ProviderGitHub, network: true, mergeErr: errors.New("server error")}
	// Local provider succeeds.
	local := &fakeProvider{id: vcs.ProviderLocalGit, network: false}

	// Build a policy in local-merge mode: merge=true, change_request.create=false
	// (§5.1 R5).
	proj := policy.NewProject(policy.ProfileAutonomous)
	proj.Pipeline.ChangeRequest.Create = false
	res, vs := policy.Resolve(proj, policy.TaskContext{})
	if policy.Blocks(vs) {
		t.Logf("violations: %+v", vs)
	}
	if !CanLocalMergeMode(res) {
		t.Fatal("expected local-merge mode to be permitted")
	}

	q := NewQueue(auth, local, nil)
	if err := q.Enqueue(QueueItem{
		TaskID: "T-fb", Decision: allowMergeResult(res), Policy: res,
		Provider: remote, Request: vcs.MergeRequest{TaskID: "T-fb", Number: 1, HeadSHA: "abc"},
		AllowLocalFallback: true,
	}); err != nil {
		t.Fatal(err)
	}
	outs, _ := q.Process(context.Background())
	if len(outs) != 1 {
		t.Fatalf("want 1 outcome, got %d", len(outs))
	}
	if !outs[0].Merged || !outs[0].FellBack {
		t.Fatalf("expected local fallback merge, got: %+v err=%v", outs[0], outs[0].Err)
	}
	if local.calls.Load() != 1 {
		t.Fatalf("local provider should have merged once, got %d", local.calls.Load())
	}
}

func TestQueue_BranchNotCurrent_NoFallback(t *testing.T) {
	t.Parallel()
	auth, _ := newTestAuthority(t)
	remote := &fakeProvider{id: vcs.ProviderGitHub, network: true,
		mergeErr: fmt.Errorf("%w: stale", vcs.ErrBranchNotCurrent)}
	local := &fakeProvider{id: vcs.ProviderLocalGit, network: false}
	res := autonomousResolved()

	q := NewQueue(auth, local, nil)
	_ = q.Enqueue(QueueItem{
		TaskID: "T", Decision: allowMergeResult(res), Policy: res,
		Provider: remote, Request: vcs.MergeRequest{TaskID: "T", Number: 1, HeadSHA: "abc"},
		AllowLocalFallback: true,
	})
	outs, _ := q.Process(context.Background())
	if outs[0].Merged {
		t.Fatal("must not merge when branch not current")
	}
	if outs[0].FellBack {
		t.Fatal("must not fall back on branch-not-current (needs rebase)")
	}
	if !errors.Is(outs[0].Err, vcs.ErrBranchNotCurrent) {
		t.Fatalf("want ErrBranchNotCurrent, got %v", outs[0].Err)
	}
	if local.calls.Load() != 0 {
		t.Fatal("local fallback must not run on branch-not-current")
	}
}

func TestQueue_NoFallbackWhenPolicyForbidsLocalMode(t *testing.T) {
	t.Parallel()
	auth, _ := newTestAuthority(t)
	remote := &fakeProvider{id: vcs.ProviderGitHub, network: true, mergeErr: errors.New("boom")}
	local := &fakeProvider{id: vcs.ProviderLocalGit, network: false}
	// AUTONOMOUS has change_request.create=true → NOT local-merge mode.
	res := autonomousResolved()
	if CanLocalMergeMode(res) {
		t.Fatal("AUTONOMOUS must not be local-merge mode")
	}

	q := NewQueue(auth, local, nil)
	_ = q.Enqueue(QueueItem{
		TaskID: "T", Decision: allowMergeResult(res), Policy: res,
		Provider: remote, Request: vcs.MergeRequest{TaskID: "T", Number: 1, HeadSHA: "abc"},
		AllowLocalFallback: true,
	})
	outs, _ := q.Process(context.Background())
	if outs[0].Merged || outs[0].FellBack {
		t.Fatal("must not fall back when policy is not in local-merge mode")
	}
	if local.calls.Load() != 0 {
		t.Fatal("local provider must not be called when policy forbids local-merge mode")
	}
}

func TestCanLocalMergeMode(t *testing.T) {
	t.Parallel()
	// AUTONOMOUS: merge=true, CR=true → NOT local mode.
	if CanLocalMergeMode(autonomousResolved()) {
		t.Fatal("AUTONOMOUS should not be local-merge mode")
	}
	// LOCAL_REVIEW: merge=false → NOT local mode (no merge at all).
	if CanLocalMergeMode(localReviewResolved()) {
		t.Fatal("LOCAL_REVIEW should not be local-merge mode (merge disabled)")
	}
	// Explicit local-merge mode: merge=true, CR=false.
	proj := policy.NewProject(policy.ProfileAutonomous)
	proj.Pipeline.ChangeRequest.Create = false
	res, _ := policy.Resolve(proj, policy.TaskContext{})
	if !CanLocalMergeMode(res) {
		t.Fatal("merge=true & CR=false should be local-merge mode")
	}
}
