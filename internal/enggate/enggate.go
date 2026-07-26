// Package enggate implements the NeuroForge engineering-baseline gate (see
// docs/engineering/ENGINEERING_BASELINE.md).
//
// It is a META concern: it governs engineering work on the NeuroForge repository
// itself (milestone tasks, fixes, refactors). It is intentionally separate from
// the runtime Verification Evidence system of the product (spec §27, package
// internal/evidence), which links product acceptance criteria to runtime test
// artifacts.
//
// The package is a pure domain model: it validates task-lifecycle state
// transitions against a machine-readable evidence manifest, enforces the
// evidence-level rules (unit / integration / blackbox / live) and the
// actor-separation rule, and gates successor-task startup on a predecessor's
// ACCEPTED state. It does not run tests, call agents, or touch Git.
package enggate

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Active versions. A manifest whose versions differ is rejected, so a stale
// baseline cannot be satisfied silently.
const (
	SchemaVersion      = 1
	BaselineVersion    = "1"
	ActiveBaselinePath = "docs/engineering/ENGINEERING_BASELINE.md"
)

// State is a task-lifecycle state.
type State string

const (
	StateStarted           State = "STARTED"
	StateImplementedTested State = "IMPLEMENTED_TESTED"
	StateChangesRequested  State = "CHANGES_REQUESTED"
	StateReviewApproved    State = "REVIEW_APPROVED"
	StateAccepted          State = "ACCEPTED"
	StateBlocked           State = "BLOCKED"
	StateFailed            State = "FAILED"
	StateRejected          State = "REJECTED"
	StateAcceptanceFailed  State = "ACCEPTANCE_FAILED"
)

// EvidenceLevel is the weight of a piece of evidence (baseline §2).
type EvidenceLevel string

const (
	LevelUnit        EvidenceLevel = "unit"
	LevelIntegration EvidenceLevel = "integration"
	LevelBlackBox    EvidenceLevel = "blackbox"
	LevelLive        EvidenceLevel = "live" // opt-in; never satisfies a mandatory criterion
)

// eligible reports whether the level may satisfy a mandatory criterion.
func (l EvidenceLevel) eligible() bool {
	return l == LevelUnit || l == LevelIntegration || l == LevelBlackBox
}

// EvidenceStatus is the pass/fail status of evidence or a command.
type EvidenceStatus string

const (
	StatusPassed EvidenceStatus = "passed"
	StatusFailed EvidenceStatus = "failed"
)

// Criterion is one acceptance criterion for the task.
type Criterion struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Mandatory   bool   `json:"mandatory"`
}

// Evidence links a criterion to one verifiable artifact.
type Evidence struct {
	Criterion string         `json:"criterion"`
	Level     EvidenceLevel  `json:"level"`
	Reference string         `json:"reference"`
	Status    EvidenceStatus `json:"status"`
	Automated bool           `json:"automated"`
}

// CommandResult is the outcome of a mandatory command (e.g. "make check").
type CommandResult struct {
	Label  string         `json:"label"`
	Status EvidenceStatus `json:"status"`
}

// Actors are the three signing roles (baseline §6).
type Actors struct {
	Implementer string `json:"implementer"`
	Reviewer    string `json:"reviewer"`
	Acceptor    string `json:"acceptor"`
}

// BlackBox records the black-box scenario (baseline §3).
type BlackBox struct {
	CompiledBinary string         `json:"compiled_binary"`
	Scenario       string         `json:"scenario"`
	Status         EvidenceStatus `json:"status"`
	Exempt         bool           `json:"exempt"`
	ExemptReason   string         `json:"exempt_reason"`
}

// Reports are the human-readable report paths.
type Reports struct {
	Implementation string `json:"implementation"`
	Review         string `json:"review"`
	Acceptance     string `json:"acceptance"`
}

// Manifest is the machine-readable evidence manifest (baseline §4).
type Manifest struct {
	SchemaVersion     int             `json:"schema_version"`
	BaselineVersion   string          `json:"baseline_version"`
	TaskID            string          `json:"task_id"`
	PredecessorTaskID string          `json:"predecessor_task_id"`
	PreviousState     State           `json:"previous_state"`
	State             State           `json:"state"`
	Criteria          []Criterion     `json:"criteria"`
	Evidence          []Evidence      `json:"evidence"`
	Commands          []CommandResult `json:"commands"`
	Actors            Actors          `json:"actors"`
	Reports           Reports         `json:"reports"`
	BlackBox          BlackBox        `json:"blackbox"`
	RemediationNote   string          `json:"remediation_note"`
}

// ValidationError is the accumulated set of violations for a transition.
type ValidationError struct {
	TaskID  string
	From    State
	To      State
	Reasons []string
}

func (e *ValidationError) Error() string {
	if e == nil {
		return ""
	}
	hdr := fmt.Sprintf("enggate: transition %s -> %s rejected for task %q", e.From, e.To, e.TaskID)
	if strings.TrimSpace(hdr) == "" {
		hdr = fmt.Sprintf("enggate: transition %s -> %s rejected", e.From, e.To)
	}
	return hdr + ":\n  - " + strings.Join(e.Reasons, "\n  - ")
}

// LoadManifest reads and JSON-decodes a manifest from path.
func LoadManifest(path string) (Manifest, error) {
	var m Manifest
	data, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("enggate: parse %s: %w", path, err)
	}
	return m, nil
}

// ValidateTransition checks whether transitioning from -> to is legal given m.
// Returns *ValidationError listing every violation, or a plain error for
// structural problems (schema/baseline version mismatch).
func ValidateTransition(from, to State, m Manifest) error {
	// Structural checks: always enforced, even at STARTED.
	var reasons []string
	if m.SchemaVersion != SchemaVersion {
		return fmt.Errorf("enggate: schema_version %d != active %d (re-emit the manifest)", m.SchemaVersion, SchemaVersion)
	}
	if m.BaselineVersion != BaselineVersion {
		return fmt.Errorf("enggate: baseline_version %q != active %q (baseline is stale)", m.BaselineVersion, BaselineVersion)
	}
	if strings.TrimSpace(m.TaskID) == "" {
		reasons = append(reasons, "task_id is empty")
	}

	if !legalTransition(from, to) {
		reasons = append(reasons, fmt.Sprintf("illegal transition %s -> %s (see baseline §3 state machine)", from, to))
	}

	switch to {
	case StateImplementedTested, StateChangesRequested:
		reasons = append(reasons, checkImplemented(m)...)
		if to == StateChangesRequested {
			// CHANGES_REQUESTED is itself a reviewer verdict, not an impl verdict.
			// The legal re-entry is CHANGES_REQUESTED -> IMPLEMENTED_TESTED, handled
			// when to == StateImplementedTested. Reaching here with to ==
			// StateChangesRequested means the manifest claims to *become*
			// CHANGES_REQUESTED, which requires a reviewer note.
			if strings.TrimSpace(m.RemediationNote) == "" {
				reasons = append(reasons, "-> CHANGES_REQUESTED requires a non-empty remediation_note describing the review findings")
			}
		}
	case StateReviewApproved:
		reasons = append(reasons, checkReview(from, m)...)
	case StateAccepted:
		reasons = append(reasons, checkAcceptance(from, m)...)
	case StateBlocked, StateFailed, StateRejected, StateAcceptanceFailed:
		// Terminal-failure verdicts: no evidence required, but must carry a note
		// explaining the failure (never silently terminal).
		if strings.TrimSpace(m.RemediationNote) == "" {
			reasons = append(reasons, fmt.Sprintf("terminal verdict %s requires a non-empty remediation_note explaining the failure", to))
		}
	case StateStarted:
		// Initial state: nothing further to prove.
	}

	if len(reasons) > 0 {
		return &ValidationError{TaskID: m.TaskID, From: from, To: to, Reasons: reasons}
	}
	return nil
}

func legalTransition(from, to State) bool {
	switch to {
	case StateStarted:
		return from == "" || from == StateStarted
	case StateImplementedTested:
		return from == StateStarted || from == StateChangesRequested
	case StateChangesRequested:
		return from == StateImplementedTested
	case StateReviewApproved:
		return from == StateImplementedTested
	case StateAccepted:
		return from == StateReviewApproved
	case StateBlocked, StateFailed:
		return from == StateStarted || from == StateImplementedTested
	case StateRejected:
		return from == StateImplementedTested || from == StateReviewApproved
	case StateAcceptanceFailed:
		return from == StateReviewApproved
	}
	return false
}

func checkImplemented(m Manifest) []string {
	var reasons []string
	hasUnit := false
	for _, c := range m.Criteria {
		if !c.Mandatory {
			continue
		}
		if !m.hasPassingEligible(c.ID) {
			reasons = append(reasons, fmt.Sprintf("mandatory criterion %q has no passing automated unit/integration/blackbox evidence (live is not eligible)", c.ID))
		}
	}
	for _, e := range m.Evidence {
		if e.Level == LevelUnit && e.Status == StatusPassed {
			hasUnit = true
			break
		}
	}
	if !hasUnit {
		reasons = append(reasons, "no passing unit-level evidence present (a unit test is always required)")
	}
	if m.BlackBox.Exempt {
		if strings.TrimSpace(m.BlackBox.ExemptReason) == "" {
			reasons = append(reasons, "blackbox.exempt=true but exempt_reason is empty")
		}
		if !m.hasLevel(StatusPassed, LevelIntegration) {
			reasons = append(reasons, "blackbox exemption requires at least one passing integration-level evidence covering the wiring")
		}
	} else if !m.hasBlackBoxEvidence(StatusPassed) {
		reasons = append(reasons, "no passing blackbox-level evidence present (production wiring is unproven — baseline rule 8)")
	}
	if !m.commandPassed("make check") {
		reasons = append(reasons, "command \"make check\" is not recorded as passed")
	}
	return reasons
}

func checkReview(from State, m Manifest) []string {
	var reasons []string
	if from != StateImplementedTested {
		reasons = append(reasons, fmt.Sprintf("REVIEW_APPROVED requires previous_state IMPLEMENTED_TESTED, got %s", from))
	}
	if strings.TrimSpace(m.Reports.Review) == "" {
		reasons = append(reasons, "reports.review (review report path) is empty")
	}
	reasons = append(reasons, checkActorSeparation(m.Actors, roleReviewer)...)
	for _, c := range m.Criteria {
		if c.Mandatory && !m.hasPassingEligible(c.ID) {
			reasons = append(reasons, fmt.Sprintf("mandatory criterion %q has no passing evidence at review time", c.ID))
		}
	}
	return reasons
}

func checkAcceptance(from State, m Manifest) []string {
	var reasons []string
	if from != StateReviewApproved {
		reasons = append(reasons, fmt.Sprintf("ACCEPTED requires previous_state REVIEW_APPROVED, got %s", from))
	}
	if strings.TrimSpace(m.Reports.Acceptance) == "" {
		reasons = append(reasons, "reports.acceptance (acceptance report path) is empty")
	}
	reasons = append(reasons, checkActorSeparation(m.Actors, roleAcceptor)...)
	if m.BlackBox.Status != StatusPassed {
		reasons = append(reasons, "blackbox.status != passed at acceptance (no exemption allowed at acceptance)")
	}
	if strings.TrimSpace(m.BlackBox.Scenario) == "" {
		reasons = append(reasons, "blackbox.scenario is empty (acceptance must record the observable black-box scenario)")
	}
	if strings.TrimSpace(m.BlackBox.CompiledBinary) == "" {
		reasons = append(reasons, "blackbox.compiled_binary is empty")
	}
	if !m.commandPassed("make check") {
		reasons = append(reasons, "command \"make check\" is not recorded as passed at acceptance")
	}
	for _, c := range m.Criteria {
		if c.Mandatory && !m.hasPassingEligible(c.ID) {
			reasons = append(reasons, fmt.Sprintf("mandatory criterion %q has no passing evidence at acceptance", c.ID))
		}
	}
	return reasons
}

type role int

const (
	roleReviewer role = iota
	roleAcceptor
)

func checkActorSeparation(a Actors, r role) []string {
	var reasons []string
	switch r {
	case roleReviewer:
		if strings.TrimSpace(a.Reviewer) == "" {
			reasons = append(reasons, "actors.reviewer is empty (independent review required)")
		}
		if a.Reviewer != "" && a.Reviewer == a.Implementer {
			reasons = append(reasons, "actors.reviewer == actors.implementer (self-review is forbidden — baseline §6)")
		}
	case roleAcceptor:
		if strings.TrimSpace(a.Acceptor) == "" {
			reasons = append(reasons, "actors.acceptor is empty (independent acceptance required)")
		}
		if a.Acceptor != "" && a.Acceptor == a.Implementer {
			reasons = append(reasons, "actors.acceptor == actors.implementer (self-acceptance is forbidden — baseline §6)")
		}
		if a.Acceptor != "" && a.Acceptor == a.Reviewer {
			reasons = append(reasons, "actors.acceptor == actors.reviewer (acceptor must differ from reviewer)")
		}
	}
	return reasons
}

func (m Manifest) hasPassingEligible(criterion string) bool {
	for _, e := range m.Evidence {
		if e.Criterion == criterion && e.Status == StatusPassed && e.Automated && e.Level.eligible() {
			return true
		}
	}
	return false
}

func (m Manifest) hasLevel(status EvidenceStatus, level EvidenceLevel) bool {
	for _, e := range m.Evidence {
		if e.Status == status && e.Level == level {
			return true
		}
	}
	return false
}

func (m Manifest) hasBlackBoxEvidence(status EvidenceStatus) bool {
	for _, e := range m.Evidence {
		if e.Level == LevelBlackBox && e.Status == status && e.Automated {
			return true
		}
	}
	return false
}

func (m Manifest) commandPassed(label string) bool {
	for _, c := range m.Commands {
		if c.Label == label && c.Status == StatusPassed {
			return true
		}
	}
	return false
}

// CanStartNext returns nil only if the predecessor task's state is ACCEPTED
// (baseline §3: no successor task may start until its predecessor is ACCEPTED).
func CanStartNext(predecessor Manifest) error {
	if predecessor.State == StateAccepted {
		return nil
	}
	return fmt.Errorf("enggate: cannot start successor task %q: predecessor %q state is %s, not ACCEPTED",
		strings.TrimSpace(predecessor.TaskID), predecessor.TaskID, predecessor.State)
}

// ActiveVersions exposes the active schema/baseline versions for callers (CLI,
// docs checks) that need to print or compare them.
func ActiveVersions() (schema int, baseline string, docPath string) {
	return SchemaVersion, BaselineVersion, ActiveBaselinePath
}
