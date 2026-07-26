package enggate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goodManifest returns a manifest that satisfies every rule for the full
// STARTED -> IMPLEMENTED_TESTED -> REVIEW_APPROVED -> ACCEPTED flow. Each
// negative test clones it and breaks exactly one invariant.
func goodManifest() Manifest {
	return Manifest{
		SchemaVersion:   SchemaVersion,
		BaselineVersion: BaselineVersion,
		TaskID:          "M14-TEST",
		PreviousState:   StateStarted,
		State:           StateImplementedTested,
		Criteria: []Criterion{
			{ID: "AC1", Description: "criterion one", Mandatory: true},
			{ID: "AC2", Description: "criterion two (optional)", Mandatory: false},
		},
		Evidence: []Evidence{
			{Criterion: "AC1", Level: LevelUnit, Reference: "pkg.TestUnit", Status: StatusPassed, Automated: true},
			{Criterion: "AC1", Level: LevelBlackBox, Reference: "pkg.TestBlackBox", Status: StatusPassed, Automated: true},
		},
		Commands: []CommandResult{
			{Label: "make check", Status: StatusPassed},
		},
		Actors: Actors{Implementer: "impl-session", Reviewer: "review-session", Acceptor: "accept-session"},
		Reports: Reports{
			Implementation: "docs/reviews/m14/M14-TEST_IMPLEMENTATION.md",
			Review:         "docs/reviews/m14/M14-TEST_REVIEW.md",
			Acceptance:     "docs/reviews/m14/M14-TEST_ACCEPTANCE.md",
		},
		BlackBox: BlackBox{
			CompiledBinary: "forge",
			Scenario:       "forge gate validate --manifest M14-TEST.manifest.json",
			Status:         StatusPassed,
		},
	}
}

func TestValidateTransitionPositiveFullFlow(t *testing.T) {
	m := goodManifest()
	if err := ValidateTransition(StateStarted, StateImplementedTested, m); err != nil {
		t.Fatalf("STARTED -> IMPLEMENTED_TESTED: unexpected error: %v", err)
	}

	m.PreviousState = StateImplementedTested
	m.State = StateReviewApproved
	if err := ValidateTransition(StateImplementedTested, StateReviewApproved, m); err != nil {
		t.Fatalf("IMPLEMENTED_TESTED -> REVIEW_APPROVED: unexpected error: %v", err)
	}

	m.PreviousState = StateReviewApproved
	m.State = StateAccepted
	if err := ValidateTransition(StateReviewApproved, StateAccepted, m); err != nil {
		t.Fatalf("REVIEW_APPROVED -> ACCEPTED: unexpected error: %v", err)
	}
}

func TestValidateTransitionStartedAllowed(t *testing.T) {
	m := goodManifest()
	m.State = StateStarted
	if err := ValidateTransition("", StateStarted, m); err != nil {
		t.Fatalf("STARTED initial: unexpected error: %v", err)
	}
}

func wantReason(t *testing.T, err error, needle string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", needle)
	}
	if !strings.Contains(err.Error(), needle) {
		t.Fatalf("expected error containing %q, got:\n%s", needle, err.Error())
	}
}

// Negative: mandatory criterion has no passing (eligible, automated) evidence.
func TestNegativeMissingTestEvidence(t *testing.T) {
	m := goodManifest()
	m.Evidence = []Evidence{
		{Criterion: "AC1", Level: LevelBlackBox, Reference: "x", Status: StatusPassed, Automated: true},
		// unit removed; AC1 still has blackbox, so this targets the unit-requirement path.
	}
	// To specifically target "mandatory criterion has no passing eligible evidence",
	// drop all eligible evidence for AC1.
	m.Evidence = []Evidence{
		{Criterion: "AC1", Level: LevelLive, Reference: "live-only", Status: StatusPassed, Automated: true},
	}
	err := ValidateTransition(StateStarted, StateImplementedTested, m)
	wantReason(t, err, `mandatory criterion "AC1" has no passing`)
	// live-only must never satisfy a mandatory criterion.
}

// Negative: no black-box proof, not exempt.
func TestNegativeMissingBlackBoxProof(t *testing.T) {
	m := goodManifest()
	m.Evidence = []Evidence{
		{Criterion: "AC1", Level: LevelUnit, Reference: "pkg.TestUnit", Status: StatusPassed, Automated: true},
		// blackbox removed
	}
	err := ValidateTransition(StateStarted, StateImplementedTested, m)
	wantReason(t, err, "no passing blackbox-level evidence present")
}

// Negative: implementation and review by the same actor.
func TestNegativeSelfReview(t *testing.T) {
	m := goodManifest()
	m.Actors.Reviewer = m.Actors.Implementer
	m.PreviousState = StateImplementedTested
	m.State = StateReviewApproved
	err := ValidateTransition(StateImplementedTested, StateReviewApproved, m)
	wantReason(t, err, "actors.reviewer == actors.implementer")
}

// Negative: acceptor equals reviewer.
func TestNegativeAcceptorEqualsReviewer(t *testing.T) {
	m := goodManifest()
	m.Actors.Acceptor = m.Actors.Reviewer
	m.PreviousState = StateReviewApproved
	m.State = StateAccepted
	err := ValidateTransition(StateReviewApproved, StateAccepted, m)
	wantReason(t, err, "actors.acceptor == actors.reviewer")
}

// Negative: acceptor equals implementer.
func TestNegativeSelfAcceptance(t *testing.T) {
	m := goodManifest()
	m.Actors.Acceptor = m.Actors.Implementer
	m.PreviousState = StateReviewApproved
	m.State = StateAccepted
	err := ValidateTransition(StateReviewApproved, StateAccepted, m)
	wantReason(t, err, "actors.acceptor == actors.implementer")
}

// Negative: invalid transition (skip implementation).
func TestNegativeInvalidTransitionSkip(t *testing.T) {
	m := goodManifest()
	// STARTED -> ACCEPTED is never legal.
	err := ValidateTransition(StateStarted, StateAccepted, m)
	wantReason(t, err, "illegal transition STARTED -> ACCEPTED")
}

// Negative: invalid transition (IMPLEMENTED_TESTED -> ACCEPTED skips review).
func TestNegativeInvalidTransitionSkipReview(t *testing.T) {
	m := goodManifest()
	err := ValidateTransition(StateImplementedTested, StateAccepted, m)
	wantReason(t, err, "illegal transition IMPLEMENTED_TESTED -> ACCEPTED")
}

// Negative: next task starts before predecessor is ACCEPTED.
func TestNegativeCanStartNextBeforeAccepted(t *testing.T) {
	cases := []State{StateStarted, StateImplementedTested, StateReviewApproved, StateChangesRequested}
	for _, s := range cases {
		m := goodManifest()
		m.State = s
		if err := CanStartNext(m); err == nil {
			t.Fatalf("CanStartNext with predecessor state %s must fail", s)
		}
	}
	m := goodManifest()
	m.State = StateAccepted
	if err := CanStartNext(m); err != nil {
		t.Fatalf("CanStartNext with ACCEPTED must succeed, got: %v", err)
	}
}

// Negative: missing unit-level evidence.
func TestNegativeMissingUnit(t *testing.T) {
	m := goodManifest()
	m.Evidence = []Evidence{
		{Criterion: "AC1", Level: LevelIntegration, Reference: "x", Status: StatusPassed, Automated: true},
		{Criterion: "AC1", Level: LevelBlackBox, Reference: "y", Status: StatusPassed, Automated: true},
	}
	err := ValidateTransition(StateStarted, StateImplementedTested, m)
	wantReason(t, err, "no passing unit-level evidence present")
}

// Negative: make check not recorded as passed.
func TestNegativeMissingMakeCheck(t *testing.T) {
	m := goodManifest()
	m.Commands = []CommandResult{{Label: "make check", Status: StatusFailed}}
	err := ValidateTransition(StateStarted, StateImplementedTested, m)
	wantReason(t, err, `command "make check" is not recorded as passed`)
}

// Negative: blackbox exemption with empty reason.
func TestNegativeExemptNoReason(t *testing.T) {
	m := goodManifest()
	m.BlackBox.Exempt = true
	m.BlackBox.ExemptReason = ""
	// remove blackbox evidence so the exemption path is taken
	m.Evidence = []Evidence{
		{Criterion: "AC1", Level: LevelUnit, Reference: "u", Status: StatusPassed, Automated: true},
		{Criterion: "AC1", Level: LevelIntegration, Reference: "i", Status: StatusPassed, Automated: true},
	}
	err := ValidateTransition(StateStarted, StateImplementedTested, m)
	wantReason(t, err, "blackbox.exempt=true but exempt_reason is empty")
}

// Negative: blackbox exemption without integration evidence.
func TestNegativeExemptWithoutIntegration(t *testing.T) {
	m := goodManifest()
	m.BlackBox.Exempt = true
	m.BlackBox.ExemptReason = "no production wiring in this task"
	m.Evidence = []Evidence{
		{Criterion: "AC1", Level: LevelUnit, Reference: "u", Status: StatusPassed, Automated: true},
	}
	err := ValidateTransition(StateStarted, StateImplementedTested, m)
	wantReason(t, err, "blackbox exemption requires at least one passing integration-level evidence")
}

// Positive: valid blackbox exemption path.
func TestPositiveExemptPath(t *testing.T) {
	m := goodManifest()
	m.BlackBox.Exempt = true
	m.BlackBox.ExemptReason = "pure-doc task: no production wiring"
	m.Evidence = []Evidence{
		{Criterion: "AC1", Level: LevelUnit, Reference: "u", Status: StatusPassed, Automated: true},
		{Criterion: "AC1", Level: LevelIntegration, Reference: "i", Status: StatusPassed, Automated: true},
	}
	if err := ValidateTransition(StateStarted, StateImplementedTested, m); err != nil {
		t.Fatalf("exempt path should pass: %v", err)
	}
}

// Negative: acceptance with blackbox.status != passed (exemption not honored at acceptance).
func TestNegativeAcceptanceBlackBoxNotPassed(t *testing.T) {
	m := goodManifest()
	m.BlackBox.Status = StatusFailed
	m.PreviousState = StateReviewApproved
	m.State = StateAccepted
	err := ValidateTransition(StateReviewApproved, StateAccepted, m)
	wantReason(t, err, "blackbox.status != passed at acceptance")
}

// Negative: terminal-failure verdict without a note.
func TestNegativeTerminalWithoutNote(t *testing.T) {
	m := goodManifest()
	m.State = StateBlocked
	err := ValidateTransition(StateStarted, StateBlocked, m)
	wantReason(t, err, "terminal verdict BLOCKED requires a non-empty remediation_note")
}

// Structural: schema version mismatch -> plain error (not ValidationError).
func TestNegativeSchemaMismatch(t *testing.T) {
	m := goodManifest()
	m.SchemaVersion = 999
	if _, ok := ValidateTransition(StateStarted, StateImplementedTested, m).(*ValidationError); ok {
		t.Fatalf("schema mismatch must be a plain error, not *ValidationError")
	}
}

// Structural: baseline version mismatch -> plain error.
func TestNegativeBaselineMismatch(t *testing.T) {
	m := goodManifest()
	m.BaselineVersion = "0"
	err := ValidateTransition(StateStarted, StateImplementedTested, m)
	if err == nil || strings.Contains(err.Error(), "*ValidationError") {
		t.Fatalf("baseline mismatch must be a plain error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "stale") {
		t.Fatalf("expected 'stale' in error, got: %v", err)
	}
}

// Negative: empty task_id.
func TestNegativeEmptyTaskID(t *testing.T) {
	m := goodManifest()
	m.TaskID = ""
	err := ValidateTransition(StateStarted, StateImplementedTested, m)
	wantReason(t, err, "task_id is empty")
}

// Negative: review with empty reviewer.
func TestNegativeEmptyReviewer(t *testing.T) {
	m := goodManifest()
	m.Actors.Reviewer = ""
	m.PreviousState = StateImplementedTested
	m.State = StateReviewApproved
	err := ValidateTransition(StateImplementedTested, StateReviewApproved, m)
	wantReason(t, err, "actors.reviewer is empty")
}

// Remediation loop: CHANGES_REQUESTED -> IMPLEMENTED_TESTED requires a note and full evidence.
func TestRemediationReentry(t *testing.T) {
	m := goodManifest()
	m.PreviousState = StateChangesRequested
	m.State = StateImplementedTested
	m.RemediationNote = "addressed review findings: added regression test"
	if err := ValidateTransition(StateChangesRequested, StateImplementedTested, m); err != nil {
		t.Fatalf("remediation re-entry should pass: %v", err)
	}
	// Without a note the -> CHANGES_REQUESTED claim is rejected; covered by the
	// terminal/changes path. Here we ensure the legal re-entry is accepted.
}

// LoadManifest round-trips a manifest through JSON.
func TestLoadManifestRoundTrip(t *testing.T) {
	m := goodManifest()
	dir := t.TempDir()
	p := filepath.Join(dir, "M14-TEST.manifest.json")
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadManifest(p)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if loaded.TaskID != m.TaskID || loaded.State != m.State {
		t.Fatalf("round-trip mismatch: %+v", loaded)
	}
	if err := ValidateTransition(StateStarted, StateImplementedTested, loaded); err != nil {
		t.Fatalf("loaded manifest should validate: %v", err)
	}
}

// LoadManifest errors on a missing file.
func TestLoadManifestMissing(t *testing.T) {
	if _, err := LoadManifest(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

// Legal-transition coverage table.
func TestLegalTransitionTable(t *testing.T) {
	legal := []struct{ from, to State }{
		{"", StateStarted},
		{StateStarted, StateImplementedTested},
		{StateChangesRequested, StateImplementedTested},
		{StateImplementedTested, StateChangesRequested},
		{StateImplementedTested, StateReviewApproved},
		{StateReviewApproved, StateAccepted},
		{StateStarted, StateBlocked},
		{StateImplementedTested, StateBlocked},
		{StateImplementedTested, StateRejected},
		{StateReviewApproved, StateRejected},
		{StateReviewApproved, StateAcceptanceFailed},
	}
	for _, c := range legal {
		if !legalTransition(c.from, c.to) {
			t.Errorf("expected legal: %s -> %s", c.from, c.to)
		}
	}
	illegal := []struct{ from, to State }{
		{StateStarted, StateAccepted},
		{StateImplementedTested, StateAccepted},
		{StateReviewApproved, StateImplementedTested},
		{StateAccepted, StateImplementedTested},
		{StateStarted, StateReviewApproved},
	}
	for _, c := range illegal {
		if legalTransition(c.from, c.to) {
			t.Errorf("expected illegal: %s -> %s", c.from, c.to)
		}
	}
}
