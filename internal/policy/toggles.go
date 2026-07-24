package policy

// TriState models the three-valued review switches (§5: security_review and
// architecture_review default to "auto"). TriOn/TriOff are explicit enable/
// disable; TriAuto defers to the project's mandatory-check resolution.
type TriState int

const (
	TriAuto TriState = iota // defer to project mandatory policy
	TriOn                   // explicitly enabled
	TriOff                  // explicitly disabled
)

// String returns a stable identifier for serialisation/logging.
func (t TriState) String() string {
	switch t {
	case TriOn:
		return "on"
	case TriOff:
		return "off"
	default:
		return "auto"
	}
}

// Pipeline is the complete, typed set of pipeline toggles (spec §5). All fields
// are value types so a Pipeline can be copied freely.
type Pipeline struct {
	Specification  bool
	Planning       bool
	Design         DesignConfig
	Implementation bool
	Tests          TestsConfig
	Review         ReviewConfig
	Git            GitConfig
	ChangeRequest  ChangeRequestConfig
	Merge          bool
	PostMerge      PostMergeConfig
}

// DesignConfig holds the design-stage toggles (§5.design).
type DesignConfig struct {
	Generate           bool
	HumanSelection     bool
	VisualVerification bool
}

// TestsConfig holds the test-stage toggles (§5.tests).
type TestsConfig struct {
	Generate              bool
	RunExisting           bool
	RunGenerated          bool
	RequiredForCompletion bool
}

// ReviewConfig holds the review-stage toggles (§5.review).
type ReviewConfig struct {
	AIReview           bool
	SecurityReview     TriState
	ArchitectureReview TriState
}

// GitConfig holds the git-stage toggles (§5.git).
type GitConfig struct {
	LocalCheckpointCommits bool
	FinalLocalCommit       bool
	Push                   bool
}

// ChangeRequestConfig holds the change-request toggles (§5.change_request).
type ChangeRequestConfig struct {
	Create         bool
	UpdateExisting bool
}

// PostMergeConfig holds the post-merge toggles (§5.post_merge).
type PostMergeConfig struct {
	Enabled    bool
	AutoRevert bool
}
