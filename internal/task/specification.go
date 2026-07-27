package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"neuroforge/internal/audit"
	"neuroforge/internal/storage"
)

// This file implements the domain model of the compiled task specification
// (spec §18.1, §9, §26, §27, §28) and its durable repository. The compiler that
// PRODUCES a specification from free-form input lands in a later milestone;
// M14-01 delivers only the durable, versioned substrate + lock semantics so the
// Merge Governor's `specification_locked` gate (§28) has something concrete to
// bind to.
//
// Layering: package task owns the domain types + validation + the
// SpecificationStore service; package storage owns the data-only rows. The
// store wraps storage and records every mutation to the audit trail
// atomically (spec §11.4, §29.4).

// Risk is the risk classification (spec §26: R0..R4).
type Risk string

const (
	RiskR0 Risk = "R0" // documentation and mechanical changes
	RiskR1 Risk = "R1" // local UI, analytics, simple logic
	RiskR2 Risk = "R2" // public API, provider integration, background jobs
	RiskR3 Risk = "R3" // migrations, concurrency, subscriptions
	RiskR4 Risk = "R4" // auth, payments, permissions, destructive changes
)

// IsValid reports whether r is a known risk class. The empty string is valid
// (unspecified) so the compiler can leave risk unset until it is known.
func (r Risk) IsValid() bool {
	switch r {
	case "", RiskR0, RiskR1, RiskR2, RiskR3, RiskR4:
		return true
	}
	return false
}

// Complexity is the complexity classification (spec §19 model tiers map to
// C0..C3; the cheap-classifier cascade §18.2 picks the tier).
type Complexity string

const (
	ComplexityC0 Complexity = "C0" // trivial / mechanical
	ComplexityC1 Complexity = "C1" // standard
	ComplexityC2 Complexity = "C2" // involved
	ComplexityC3 Complexity = "C3" // heavy
)

// IsValid reports whether c is a known complexity class (empty = unspecified).
func (c Complexity) IsValid() bool {
	switch c {
	case "", ComplexityC0, ComplexityC1, ComplexityC2, ComplexityC3:
		return true
	}
	return false
}

// AcceptanceCriterion is one acceptance criterion with a stable identifier
// (spec §27). The ID (e.g. "AC-1") is the primary handle for evidence linkage
// and Merge Governor accounting; it is durable across re-saves and reorders.
type AcceptanceCriterion struct {
	ID        string
	Statement string
}

// VisualRequirements captures the visual/UX requirements distilled from the
// task (spec §15, §16). It is the task-spec view of what the visual pipeline
// must satisfy; the locked visual specification (design.Specification, §15.6)
// is produced separately by the design pipeline.
type VisualRequirements struct {
	// Required signals whether visual verification is mandatory for this task.
	Required bool
	// Viewport is the target viewport label or "WxH" (e.g. "390x844").
	Viewport string
	// Theme is the target theme ("dark"/"light"/"").
	Theme string
	// Locale is the target locale tag (e.g. "en").
	Locale string
	// Density is the target screen density (e.g. "xxhdpi").
	Density string
	// References are content-addressed attachment hashes used as visual
	// references.
	References []string
}

// specPayload is the JSON-encoded portion of the specification row: the
// list-shaped and structured fields that do not need their own columns. It is
// an internal type; callers always go through [Specification].
type specPayload struct {
	NonGoals           []string           `json:"non_goals,omitempty"`
	Assumptions        []string           `json:"assumptions,omitempty"`
	Constraints        []string           `json:"constraints,omitempty"`
	ProposedScope      []string           `json:"proposed_scope,omitempty"`
	VisualRequirements VisualRequirements `json:"visual_requirements,omitempty"`
}

// Specification is the compiled, versioned task specification (spec §18.1). A
// specification is immutable once Locked (§28). New versions are new snapshots;
// previous versions remain queryable for audit/history.
type Specification struct {
	TaskID             string
	Version            int
	Objective          string
	AcceptanceCriteria []AcceptanceCriterion
	NonGoals           []string
	Assumptions        []string
	Constraints        []string
	Risk               Risk
	Complexity         Complexity
	ProposedScope      []string
	VisualRequirements VisualRequirements

	// Lock state. A locked version cannot be mutated by Save (§28).
	Locked   bool
	LockedAt time.Time
	LockedBy string

	CreatedAt time.Time
	CreatedBy string
}

// ErrInvalidSpecification is returned when a specification fails domain
// validation.
var ErrInvalidSpecification = errors.New("invalid specification")

// ErrSpecificationLocked is returned when a caller tries to mutate a locked
// specification version. It wraps the storage-level error so callers can use
// errors.Is.
var ErrSpecificationLocked = storage.ErrSpecificationLocked

// ErrSpecificationNotFound wraps the storage-level not-found error.
var ErrSpecificationNotFound = storage.ErrSpecificationNotFound

// ValidateSpecification validates the structural invariants of a compiled
// specification (spec §9.2, §27, §26). It does NOT touch storage. Returns the
// normalised specification (defaults applied) and an error joining every
// violation, so a caller can report all problems at once.
//
// Invariants:
//   - TaskID is non-empty.
//   - Objective is non-empty (a spec must state what it wants).
//   - At least one acceptance criterion (§27 ties evidence to ACs).
//   - Every AC has a non-empty, unique ID and a non-empty statement.
//   - Risk and Complexity, when set, are known classes.
func ValidateSpecification(s Specification) (Specification, error) {
	var errs []error

	if s.TaskID == "" {
		errs = append(errs, fmt.Errorf("%w: task_id is required", ErrInvalidSpecification))
	}
	if trim(s.Objective) == "" {
		errs = append(errs, fmt.Errorf("%w: objective is required", ErrInvalidSpecification))
	}
	if len(s.AcceptanceCriteria) == 0 {
		errs = append(errs, fmt.Errorf("%w: at least one acceptance criterion is required", ErrInvalidSpecification))
	}
	seen := make(map[string]bool, len(s.AcceptanceCriteria))
	for i, ac := range s.AcceptanceCriteria {
		if trim(ac.ID) == "" {
			errs = append(errs, fmt.Errorf("%w: acceptance criterion #%d has no id", ErrInvalidSpecification, i+1))
			continue
		}
		id := trim(ac.ID)
		if seen[id] {
			errs = append(errs, fmt.Errorf("%w: duplicate acceptance criterion id %q", ErrInvalidSpecification, id))
		}
		seen[id] = true
		if trim(ac.Statement) == "" {
			errs = append(errs, fmt.Errorf("%w: acceptance criterion %q has no statement", ErrInvalidSpecification, id))
		}
	}
	if !s.Risk.IsValid() {
		errs = append(errs, fmt.Errorf("%w: unknown risk class %q", ErrInvalidSpecification, s.Risk))
	}
	if !s.Complexity.IsValid() {
		errs = append(errs, fmt.Errorf("%w: unknown complexity class %q", ErrInvalidSpecification, s.Complexity))
	}

	if len(errs) > 0 {
		return s, errors.Join(errs...)
	}

	// Normalise: trim whitespace on the scalar fields; keep slice order.
	s.Objective = trim(s.Objective)
	acs := make([]AcceptanceCriterion, len(s.AcceptanceCriteria))
	for i, ac := range s.AcceptanceCriteria {
		acs[i] = AcceptanceCriterion{ID: trim(ac.ID), Statement: trim(ac.Statement)}
	}
	s.AcceptanceCriteria = acs
	return s, nil
}

func trim(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// SpecificationStore is the domain service that persists compiled
// specifications. It enforces domain validation before any write and records
// every mutation to the audit trail atomically with the storage change (spec
// §11.4, §29.4).
type SpecificationStore struct {
	db     *storage.DB
	audit  *audit.Recorder
	logger *slog.Logger
	now    func() time.Time
}

// NewSpecificationStore creates a store backed by db. The recorder may be nil
// (audit disabled); the logger may be nil (a quiet default is used).
func NewSpecificationStore(db *storage.DB, rec *audit.Recorder, logger *slog.Logger) *SpecificationStore {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	}
	return &SpecificationStore{
		db:     db,
		audit:  rec,
		logger: logger,
		now:    func() time.Time { return time.Now().UTC() },
	}
}

// Save persists a specification version. If s.Version is 0 the next free
// version for the task is reserved inside the same transaction (race-free,
// spec §11.4). If s.Version is already present and unlocked, the row is
// replaced (idempotent re-save). A locked version is rejected with
// ErrSpecificationLocked (§28).
//
// Validation runs first; an invalid specification is never persisted.
func (s *SpecificationStore) Save(ctx context.Context, spec Specification) (Specification, error) {
	spec, err := ValidateSpecification(spec)
	if err != nil {
		return Specification{}, err
	}

	now := s.now()
	nowTS := now.Format(time.RFC3339Nano)

	tx, err := s.db.BeginTx(ctx)
	if err != nil {
		return Specification{}, fmt.Errorf("spec: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if spec.Version == 0 {
		v, err := tx.NextSpecificationVersion(ctx, spec.TaskID)
		if err != nil {
			return Specification{}, err
		}
		spec.Version = v
	}

	payload, err := encodePayload(spec)
	if err != nil {
		return Specification{}, err
	}

	if err := tx.SaveSpecification(ctx, storage.SpecificationRow{
		TaskID:     spec.TaskID,
		Version:    spec.Version,
		Objective:  spec.Objective,
		Risk:       string(spec.Risk),
		Complexity: string(spec.Complexity),
		Payload:    payload,
		CreatedAt:  nowTS,
		CreatedBy:  spec.CreatedBy,
	}, acRows(spec)); err != nil {
		return Specification{}, err
	}

	if s.audit != nil {
		if _, err := s.audit.RecordTx(ctx, tx, audit.Event{
			Type:    "task.specification.saved",
			Scope:   audit.ScopeTask,
			ScopeID: spec.TaskID,
			Actor:   audit.ActorUser,
			Payload: audit.Payload(
				"version", spec.Version,
				"objective", spec.Objective,
				"acceptance_criteria", len(spec.AcceptanceCriteria),
				"risk", string(spec.Risk),
				"complexity", string(spec.Complexity),
			),
		}); err != nil {
			return Specification{}, fmt.Errorf("spec: audit save: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return Specification{}, fmt.Errorf("spec: commit: %w", err)
	}

	spec.CreatedAt = now
	s.logger.Info("specification saved",
		"task", spec.TaskID, "version", spec.Version,
		"acceptance_criteria", len(spec.AcceptanceCriteria))
	return spec, nil
}

// Get returns one version of a task's specification, fully reconstructed from
// durable storage (objective, ACs with stable ids, and all list fields).
func (s *SpecificationStore) Get(ctx context.Context, taskID string, version int) (Specification, error) {
	row, acs, err := s.db.GetSpecification(ctx, taskID, version)
	if err != nil {
		return Specification{}, err
	}
	return rowToSpec(row, acs)
}

// GetLatest returns the highest-numbered version of the task's specification.
func (s *SpecificationStore) GetLatest(ctx context.Context, taskID string) (Specification, error) {
	row, acs, err := s.db.GetLatestSpecification(ctx, taskID)
	if err != nil {
		return Specification{}, err
	}
	return rowToSpec(row, acs)
}

// ListVersions returns every persisted version number for a task, ascending.
func (s *SpecificationStore) ListVersions(ctx context.Context, taskID string) ([]int, error) {
	return s.db.ListSpecificationVersions(ctx, taskID)
}

// Lock marks a specification version immutable. It is idempotent. Locking is
// recorded to the audit trail atomically with the storage change (§11.4, §29.4).
// Once locked, Save rejects any further mutation of that version (§28).
func (s *SpecificationStore) Lock(ctx context.Context, taskID string, version int, lockedBy string) (Specification, error) {
	tx, err := s.db.BeginTx(ctx)
	if err != nil {
		return Specification{}, fmt.Errorf("spec: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := tx.LockSpecification(ctx, taskID, version, lockedBy); err != nil {
		return Specification{}, err
	}

	if s.audit != nil {
		if _, err := s.audit.RecordTx(ctx, tx, audit.Event{
			Type:    "task.specification.locked",
			Scope:   audit.ScopeTask,
			ScopeID: taskID,
			Actor:   audit.ActorUser,
			Payload: audit.Payload("version", version, "by", lockedBy),
		}); err != nil {
			return Specification{}, fmt.Errorf("spec: audit lock: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return Specification{}, fmt.Errorf("spec: commit: %w", err)
	}

	// Re-read so the returned specification reflects the now-durable lock.
	return s.Get(ctx, taskID, version)
}

func encodePayload(spec Specification) (string, error) {
	p := specPayload{
		NonGoals:           spec.NonGoals,
		Assumptions:        spec.Assumptions,
		Constraints:        spec.Constraints,
		ProposedScope:      spec.ProposedScope,
		VisualRequirements: spec.VisualRequirements,
	}
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("spec: encode payload: %w", err)
	}
	return string(b), nil
}

func decodePayload(payload string) (specPayload, error) {
	var p specPayload
	if payload == "" {
		return p, nil
	}
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return specPayload{}, fmt.Errorf("spec: decode payload: %w", err)
	}
	return p, nil
}

func acRows(spec Specification) []storage.AcceptanceCriterionRow {
	acs := make([]storage.AcceptanceCriterionRow, len(spec.AcceptanceCriteria))
	// Sort by ID for deterministic storage ordering (stable ids, not positional).
	sorted := make([]AcceptanceCriterion, len(spec.AcceptanceCriteria))
	copy(sorted, spec.AcceptanceCriteria)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	for i, ac := range sorted {
		acs[i] = storage.AcceptanceCriterionRow{
			TaskID:    spec.TaskID,
			Version:   spec.Version,
			AcID:      ac.ID,
			Statement: ac.Statement,
			Ordinal:   i,
		}
	}
	return acs
}

func rowToSpec(row storage.SpecificationRow, acs []storage.AcceptanceCriterionRow) (Specification, error) {
	p, err := decodePayload(row.Payload)
	if err != nil {
		return Specification{}, err
	}
	var lockedAt time.Time
	if row.LockedAt != "" {
		lockedAt, _ = time.Parse(time.RFC3339Nano, row.LockedAt)
	}
	var createdAt time.Time
	if row.CreatedAt != "" {
		createdAt, _ = time.Parse(time.RFC3339Nano, row.CreatedAt)
	}
	criteria := make([]AcceptanceCriterion, len(acs))
	for i, ac := range acs {
		criteria[i] = AcceptanceCriterion{ID: ac.AcID, Statement: ac.Statement}
	}
	return Specification{
		TaskID:             row.TaskID,
		Version:            row.Version,
		Objective:          row.Objective,
		AcceptanceCriteria: criteria,
		NonGoals:           p.NonGoals,
		Assumptions:        p.Assumptions,
		Constraints:        p.Constraints,
		Risk:               Risk(row.Risk),
		Complexity:         Complexity(row.Complexity),
		ProposedScope:      p.ProposedScope,
		VisualRequirements: p.VisualRequirements,
		Locked:             row.Locked,
		LockedAt:           lockedAt,
		LockedBy:           row.LockedBy,
		CreatedAt:          createdAt,
		CreatedBy:          row.CreatedBy,
	}, nil
}
