package policy

// Profile is an autonomy profile (spec §4.1–§4.5).
type Profile string

const (
	// ProfilePlanOnly (§4.1): analyse/plan/estimate only; no code, branch, push,
	// PR/MR or merge.
	ProfilePlanOnly Profile = "PLAN_ONLY"
	// ProfileLocalReview (§4.2): local worktree + task branch + code + local
	// checkpoints + checks + diff; no push/PR-MR/merge/external publish.
	// Structurally network-locked (ADR-0008, AC-7).
	ProfileLocalReview Profile = "LOCAL_REVIEW"
	// ProfileRemoteReview (§4.3): LOCAL_REVIEW + push task branch + PR/MR create/
	// update; no merge.
	ProfileRemoteReview Profile = "REMOTE_REVIEW"
	// ProfileAutonomous (§4.4): full pipeline incl. push, PR/MR, merge, post-merge,
	// auto-revert.
	ProfileAutonomous Profile = "AUTONOMOUS"
	// ProfileCustom (§4.5): the user controls every stage explicitly. ProfileDefaults
	// returns the canonical §5 defaults; only dependency rules are then applied.
	ProfileCustom Profile = "CUSTOM"
)

// String returns the profile identifier.
func (p Profile) String() string { return string(p) }

// IsValid reports whether p is a known profile.
func (p Profile) IsValid() bool {
	switch p {
	case ProfilePlanOnly, ProfileLocalReview, ProfileRemoteReview,
		ProfileAutonomous, ProfileCustom:
		return true
	}
	return false
}

// IsNetworkLocked reports whether the profile structurally forbids every Git
// network operation (ADR-0008). PLAN_ONLY and LOCAL_REVIEW are network-locked.
func (p Profile) IsNetworkLocked() bool {
	switch p {
	case ProfilePlanOnly, ProfileLocalReview:
		return true
	}
	return false
}

// ProfileDefaults returns the canonical pre-normalisation Pipeline for a profile
// (spec §4). The returned Pipeline has NOT had the §5.1 dependency rules applied
// yet; call [Normalize] to normalise it.
func ProfileDefaults(p Profile) Pipeline {
	switch p {
	case ProfilePlanOnly:
		// Analyse/plan/estimate only. No implementation, no delivery.
		return Pipeline{
			Specification:  true,
			Planning:       true,
			Design:         DesignConfig{VisualVerification: true},
			Implementation: false,
			Tests: TestsConfig{
				ModifyExisting:        false,
				RunExisting:           true,
				RequiredForCompletion: true,
				RequireForRemoteMerge: true,
			},
			Review:        ReviewConfig{AIReview: true, SecurityReview: TriAuto, ArchitectureReview: TriAuto},
			Git:           GitConfig{LocalCheckpointCommits: false, FinalLocalCommit: false, Push: false},
			ChangeRequest: ChangeRequestConfig{Create: false, UpdateExisting: false},
			Merge:         false,
			PostMerge:     PostMergeConfig{Enabled: false, AutoRevert: false},
		}

	case ProfileLocalReview:
		// Local worktree + code + checkpoints + checks + diff. No delivery.
		return Pipeline{
			Specification:  true,
			Planning:       true,
			Design:         DesignConfig{VisualVerification: true},
			Implementation: true,
			Tests: TestsConfig{
				Generate:              true,
				ModifyExisting:        true,
				RunExisting:           true,
				RunGenerated:          true,
				RequiredForCompletion: true,
				RequireForRemoteMerge: true,
			},
			Review:        ReviewConfig{AIReview: true, SecurityReview: TriAuto, ArchitectureReview: TriAuto},
			Git:           GitConfig{LocalCheckpointCommits: true, FinalLocalCommit: true, Push: false},
			ChangeRequest: ChangeRequestConfig{Create: false, UpdateExisting: false},
			Merge:         false,
			PostMerge:     PostMergeConfig{Enabled: false, AutoRevert: false},
		}

	case ProfileRemoteReview:
		// LOCAL_REVIEW + push + PR/MR create/update. No merge.
		base := ProfileDefaults(ProfileLocalReview)
		base.Git.Push = true
		base.ChangeRequest = ChangeRequestConfig{Create: true, UpdateExisting: true}
		return base

	case ProfileAutonomous:
		// Full pipeline incl. merge + post-merge + auto-revert.
		base := ProfileDefaults(ProfileRemoteReview)
		base.Merge = true
		base.PostMerge = PostMergeConfig{Enabled: true, AutoRevert: true}
		return base

	default: // CUSTOM — canonical §5 defaults, user controls everything.
		return Pipeline{
			Specification:  true,
			Planning:       true,
			Design:         DesignConfig{VisualVerification: true},
			Implementation: true,
			Tests: TestsConfig{
				Generate:              true,
				ModifyExisting:        true,
				RunExisting:           true,
				RunGenerated:          true,
				RequiredForCompletion: true,
				RequireForRemoteMerge: true,
			},
			Review:        ReviewConfig{AIReview: true, SecurityReview: TriAuto, ArchitectureReview: TriAuto},
			Git:           GitConfig{LocalCheckpointCommits: true, FinalLocalCommit: true, Push: false},
			ChangeRequest: ChangeRequestConfig{Create: false, UpdateExisting: false},
			Merge:         false,
			PostMerge:     PostMergeConfig{Enabled: false, AutoRevert: false},
		}
	}
}
