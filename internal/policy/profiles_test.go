package policy

import "testing"

func TestProfileDefaults_CoreCapabilities(t *testing.T) {
	t.Parallel()
	cases := []struct {
		profile             Profile
		wantPush, wantMerge bool
		wantImpl            bool
	}{
		{ProfilePlanOnly, false, false, false},
		{ProfileLocalReview, false, false, true},
		{ProfileRemoteReview, true, false, true},
		{ProfileAutonomous, true, true, true},
	}
	for _, c := range cases {
		t.Run(c.profile.String(), func(t *testing.T) {
			p := ProfileDefaults(c.profile)
			if p.Git.Push != c.wantPush {
				t.Errorf("push = %v, want %v", p.Git.Push, c.wantPush)
			}
			if p.Merge != c.wantMerge {
				t.Errorf("merge = %v, want %v", p.Merge, c.wantMerge)
			}
			if p.Implementation != c.wantImpl {
				t.Errorf("implementation = %v, want %v", p.Implementation, c.wantImpl)
			}
		})
	}
}

func TestProfile_NetworkLock(t *testing.T) {
	t.Parallel()
	locked := map[Profile]bool{
		ProfilePlanOnly:     true,
		ProfileLocalReview:  true,
		ProfileRemoteReview: false,
		ProfileAutonomous:   false,
		ProfileCustom:       false,
	}
	for p, want := range locked {
		if got := p.IsNetworkLocked(); got != want {
			t.Errorf("%s.IsNetworkLocked() = %v, want %v", p, got, want)
		}
	}
}

func TestProfile_IsValid(t *testing.T) {
	t.Parallel()
	for _, p := range []Profile{ProfilePlanOnly, ProfileLocalReview, ProfileRemoteReview, ProfileAutonomous, ProfileCustom} {
		if !p.IsValid() {
			t.Errorf("%s should be valid", p)
		}
	}
	if (Profile("BOGUS")).IsValid() {
		t.Error("unknown profile should be invalid")
	}
}

func TestProfileDefaults_RemoteReviewExtendsLocal(t *testing.T) {
	t.Parallel()
	local := ProfileDefaults(ProfileLocalReview)
	remote := ProfileDefaults(ProfileRemoteReview)
	// Remote = local + push + CR, merge still off.
	if !remote.Git.Push || !remote.ChangeRequest.Create {
		t.Error("remote review must enable push + CR")
	}
	if remote.Merge {
		t.Error("remote review must not merge")
	}
	// Everything else inherited from local.
	if remote.Implementation != local.Implementation || remote.Tests.Generate != local.Tests.Generate {
		t.Error("remote review should inherit local implementation/tests")
	}
}

func TestProfileDefaults_AutonomousEnablesMergeAndPostMerge(t *testing.T) {
	t.Parallel()
	auto := ProfileDefaults(ProfileAutonomous)
	if !auto.Merge || !auto.PostMerge.Enabled || !auto.PostMerge.AutoRevert {
		t.Errorf("autonomous must enable merge+post_merge+auto_revert: %+v", auto.PostMerge)
	}
}
