package policy

import "testing"

func TestNormalize_PushFalseCascades(t *testing.T) {
	t.Parallel()
	// A pathological pipeline: push off but CR/merge/post_merge on.
	p := Pipeline{
		Implementation: true,
		Git:            GitConfig{Push: false},
		ChangeRequest:  ChangeRequestConfig{Create: true, UpdateExisting: true},
		Merge:          true,
		PostMerge:      PostMergeConfig{Enabled: true, AutoRevert: true},
	}
	out, adj := Normalize(p)
	if out.ChangeRequest.Create || out.Merge || out.PostMerge.Enabled {
		t.Fatalf("push=false must force CR/merge/post_merge off: %+v", out)
	}
	// Exactly R1, R2, R3 fired (R4 is a no-op since post_merge already off).
	if len(adj) != 3 {
		t.Fatalf("expected 3 adjustments, got %d: %+v", len(adj), adj)
	}
	fields := map[string]bool{}
	for _, a := range adj {
		fields[a.Field] = true
	}
	for _, want := range []string{"change_request.create", "merge.enabled", "post_merge.enabled"} {
		if !fields[want] {
			t.Errorf("missing adjustment for %s", want)
		}
	}
}

func TestNormalize_MergeFalseForcesPostMerge(t *testing.T) {
	t.Parallel()
	// push on, merge off, post_merge on → R4 only.
	p := Pipeline{
		Git:       GitConfig{Push: true},
		Merge:     false,
		PostMerge: PostMergeConfig{Enabled: true, AutoRevert: true},
	}
	out, adj := Normalize(p)
	if out.PostMerge.Enabled {
		t.Fatal("merge=false must force post_merge off (R4)")
	}
	if len(adj) != 1 || adj[0].Field != "post_merge.enabled" {
		t.Fatalf("expected single R4 adjustment, got %+v", adj)
	}
}

func TestNormalize_LocalMergeModeAllowed(t *testing.T) {
	t.Parallel()
	// change_request.create=false & merge=true & push=true → local merge mode.
	p := Pipeline{
		Git:           GitConfig{Push: true},
		ChangeRequest: ChangeRequestConfig{Create: false},
		Merge:         true,
		PostMerge:     PostMergeConfig{Enabled: false},
	}
	out, adj := Normalize(p)
	// merge stays on (push on, R2 doesn't fire); R5 info recorded.
	if !out.Merge {
		t.Fatal("merge should remain enabled in local merge mode")
	}
	found := false
	for _, a := range adj {
		if a.Field == "merge.mode" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected local-merge-mode info adjustment, got %+v", adj)
	}
}

func TestNormalize_Idempotent(t *testing.T) {
	t.Parallel()
	p := ProfileDefaults(ProfileLocalReview)
	first, _ := Normalize(p)
	second, adj2 := Normalize(first)
	if second != first {
		t.Fatal("normalising a normalised pipeline must be a no-op")
	}
	if len(adj2) != 0 {
		t.Fatalf("second normalise must produce no adjustments, got %+v", adj2)
	}
}

func TestNormalize_AutonomousIsStable(t *testing.T) {
	t.Parallel()
	p, _ := Normalize(ProfileDefaults(ProfileAutonomous))
	// Autonomous: push+CR+merge+post_merge on, and consistent → unchanged.
	if !p.Git.Push || !p.ChangeRequest.Create || !p.Merge || !p.PostMerge.Enabled {
		t.Fatalf("autonomous should normalise to all-on delivery: %+v", p)
	}
}

func TestBlocks(t *testing.T) {
	t.Parallel()
	if Blocks(nil) {
		t.Fatal("nil violations must not block")
	}
	if Blocks([]Violation{{Severity: SeverityWarn}}) {
		t.Fatal("warn must not block")
	}
	if !Blocks([]Violation{{Severity: SeverityBlock}}) {
		t.Fatal("block must block")
	}
}
