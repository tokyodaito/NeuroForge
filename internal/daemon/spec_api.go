package daemon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"neuroforge/internal/task"
	"neuroforge/internal/transport"
)

// This file implements transport.SpecAPI for the production daemon (M14-03).
//
// The adapter is the daemon-mediated Task Compiler path (spec §18.1, §18.2):
// the user-facing compile operation reads a task from the backlog, runs the
// deterministic task.Compile over its description + attachment metadata, and
// durably persists the result through task.SpecificationStore (M14-01) so the
// compiled specification survives daemon restart and is reachable through the
// same transport for get/lock/versions.
//
// The adapter deliberately keeps the compile step PURE on the way in
// (task.Compile is called with exactly the task's durable fields; no clock,
// no I/O) and pushes every mutation through task.SpecificationStore so the
// storage transaction + audit-record atomicity established by M14-01 is
// preserved. Idempotency is provided at this layer (see CompileSpec).

// specAPIAdapter implements transport.SpecAPI over *Services. It mirrors
// apiAdapter's relationship to ProjectAPI/TaskAPI: DTO conversion +
// delegation to domain services + bus notifications. No business logic lives
// in the transport layer.
type specAPIAdapter struct {
	svc *Services
}

// newSpecAPIAdapter returns an adapter that implements transport.SpecAPI.
func newSpecAPIAdapter(svc *Services) *specAPIAdapter {
	return &specAPIAdapter{svc: svc}
}

// defaultLockedBy is the actor recorded on the audit trail when a compile or
// lock call does not name an actor. The daemon is the system of record for
// transport-initiated mutations; the value is intentionally a stable,
// machine-identifiable string so audit consumers can distinguish
// transport-initiated events from CLI-named user actors.
const defaultLockedBy = "daemon"

// CompileSpec implements transport.SpecAPI.CompileSpec. It is the
// daemon-mediated, durable, idempotent compile-and-save path.
//
// Idempotency contract (task brief: "Повторный запрос idempotent"):
//
//  1. The task is loaded from the backlog. A missing task surfaces as
//     task.ErrNotFound ("task not found"), which writeAPIError maps to 404.
//  2. task.Compile produces a deterministic specification from the task's
//     description + attachment metadata. Identical task state ⇒ identical
//     compiled content.
//  3. The latest existing version is fetched (if any). If its content is
//     semantically equal to the freshly compiled content, the existing
//     version is returned unchanged and no new persistence occurs
//     (Created=false). This is the idempotent re-compile case.
//  4. Otherwise the freshly compiled spec is persisted:
//     - If the latest version is UNLOCKED, it is replaced in place (same
//     version number) — this is the "task description was edited" case.
//     - If the latest version is LOCKED, a NEW version is allocated (the
//     locked snapshot is preserved by task.SpecificationStore.Save).
//
// All persistence goes through task.SpecificationStore.Save, which validates,
// opens a transaction, allocates the version atomically, and records the
// task.specification.saved audit event in the same transaction (spec §11.4).
func (a *specAPIAdapter) CompileSpec(ctx context.Context, req transport.CompileSpecRequest) (transport.CompileSpecResultDTO, error) {
	if a.svc.Specs == nil {
		return transport.CompileSpecResultDTO{}, errors.New("spec store not configured")
	}
	if req.TaskID == "" {
		return transport.CompileSpecResultDTO{}, fmt.Errorf("task_id is required")
	}

	// 1. Load the task from the backlog. A missing task is a client bug; the
	//    error message carries "not found" so writeAPIError maps it to 404.
	t, err := a.svc.Tasks.Get(ctx, req.TaskID)
	if err != nil {
		return transport.CompileSpecResultDTO{}, err
	}

	// 2. Run the pure compiler over the task's durable fields. The compiler
	//    does no I/O; identical task state ⇒ identical compiled content.
	res, err := task.Compile(task.CompileInput{
		TaskID:      t.ID,
		Title:       t.Title,
		Description: t.Description,
		Priority:    t.Priority,
		Attachments: t.Attachments,
	})
	if err != nil {
		return transport.CompileSpecResultDTO{}, fmt.Errorf("compile: %w", err)
	}

	createdBy := req.LockedBy
	if createdBy == "" {
		createdBy = defaultLockedBy
	}
	compiled := res.Specification
	compiled.CreatedBy = createdBy

	// 3. Idempotency: compare the freshly compiled content to the latest
	//    persisted version. Semantic equality (ignoring Version/CreatedAt/
	//    Locked*/CreatedBy) means the caller is repeating the same compile;
	//    return the durable version unchanged and skip Save.
	latest, latestErr := a.svc.Specs.GetLatest(ctx, req.TaskID)
	if latestErr == nil {
		if specificationsSemanticallyEqual(compiled, latest) {
			a.svc.Bus.Publish("task.specification.compiled", map[string]any{
				"task_id": req.TaskID, "version": latest.Version, "created": false,
			})
			return compileResultDTO(latest, res, false), nil
		}
		// Latest exists but content differs.
		if !latest.Locked {
			// Replace the unlocked latest in place — same version number, new
			// content. This matches the spec's "task description was edited,
			// re-compile" semantics without minting a new version per call.
			compiled.Version = latest.Version
		}
		// If latest is LOCKED and content differs, leave Version=0 so Save
		// allocates a fresh version (the locked snapshot is preserved).
	} else if !errors.Is(latestErr, task.ErrSpecificationNotFound) {
		// A real storage error (not "absent"). Surface it.
		return transport.CompileSpecResultDTO{}, fmt.Errorf("compile: load latest: %w", latestErr)
	}

	// 4. Persist. SpecificationStore.Save runs validation, opens a tx,
	//    allocates the version (if Version==0), writes the row + ACs, records
	//    the audit event atomically, and commits.
	saved, err := a.svc.Specs.Save(ctx, compiled)
	if err != nil {
		return transport.CompileSpecResultDTO{}, fmt.Errorf("compile: save: %w", err)
	}

	a.svc.Bus.Publish("task.specification.compiled", map[string]any{
		"task_id": saved.TaskID, "version": saved.Version, "created": true,
	})
	return compileResultDTO(saved, res, true), nil
}

// GetSpecification implements transport.SpecAPI.GetSpecification. version<=0
// returns the latest version; a positive version returns that exact version.
func (a *specAPIAdapter) GetSpecification(ctx context.Context, taskID string, version int) (transport.SpecificationDTO, error) {
	if a.svc.Specs == nil {
		return transport.SpecificationDTO{}, errors.New("spec store not configured")
	}
	if taskID == "" {
		return transport.SpecificationDTO{}, fmt.Errorf("task_id is required")
	}
	var (
		spec task.Specification
		err  error
	)
	if version <= 0 {
		spec, err = a.svc.Specs.GetLatest(ctx, taskID)
	} else {
		spec, err = a.svc.Specs.Get(ctx, taskID, version)
	}
	if err != nil {
		return transport.SpecificationDTO{}, err
	}
	return specificationToDTO(spec), nil
}

// ListSpecificationVersions implements transport.SpecAPI.ListSpecificationVersions.
func (a *specAPIAdapter) ListSpecificationVersions(ctx context.Context, taskID string) ([]int, error) {
	if a.svc.Specs == nil {
		return nil, errors.New("spec store not configured")
	}
	if taskID == "" {
		return nil, fmt.Errorf("task_id is required")
	}
	return a.svc.Specs.ListVersions(ctx, taskID)
}

// LockSpecification implements transport.SpecAPI.LockSpecification. The lock
// is recorded atomically with the storage change (spec §11.4, §29.4).
func (a *specAPIAdapter) LockSpecification(ctx context.Context, req transport.LockSpecRequest) (transport.SpecificationDTO, error) {
	if a.svc.Specs == nil {
		return transport.SpecificationDTO{}, errors.New("spec store not configured")
	}
	if req.TaskID == "" {
		return transport.SpecificationDTO{}, fmt.Errorf("task_id is required")
	}
	if req.Version <= 0 {
		return transport.SpecificationDTO{}, fmt.Errorf("version is required")
	}
	lockedBy := req.LockedBy
	if lockedBy == "" {
		lockedBy = defaultLockedBy
	}
	spec, err := a.svc.Specs.Lock(ctx, req.TaskID, req.Version, lockedBy)
	if err != nil {
		return transport.SpecificationDTO{}, err
	}
	a.svc.Bus.Publish("task.specification.locked", map[string]any{
		"task_id": spec.TaskID, "version": spec.Version, "by": lockedBy,
	})
	return specificationToDTO(spec), nil
}

// ---- DTO conversion ----
//
// These converters mirror projectToDTO / taskToDTO in api.go: pure,
// side-effect-free mappers between domain types and wire DTOs. Time fields are
// rendered as RFC3339 so JSON consumers can parse them uniformly.

func specificationToDTO(s task.Specification) transport.SpecificationDTO {
	dto := transport.SpecificationDTO{
		TaskID:             s.TaskID,
		Version:            s.Version,
		Objective:          s.Objective,
		Risk:               string(s.Risk),
		Complexity:         string(s.Complexity),
		Locked:             s.Locked,
		LockedBy:           s.LockedBy,
		CreatedBy:          s.CreatedBy,
		AcceptanceCriteria: make([]transport.AcceptanceCriterionDTO, len(s.AcceptanceCriteria)),
		NonGoals:           sliceOrNilEmpty(s.NonGoals),
		Assumptions:        sliceOrNilEmpty(s.Assumptions),
		Constraints:        sliceOrNilEmpty(s.Constraints),
		ProposedScope:      sliceOrNilEmpty(s.ProposedScope),
		VisualRequirements: visualRequirementsToDTO(s.VisualRequirements),
	}
	for i, ac := range s.AcceptanceCriteria {
		dto.AcceptanceCriteria[i] = transport.AcceptanceCriterionDTO{ID: ac.ID, Statement: ac.Statement}
	}
	if !s.LockedAt.IsZero() {
		dto.LockedAt = s.LockedAt.Format(time.RFC3339Nano)
	}
	if !s.CreatedAt.IsZero() {
		dto.CreatedAt = s.CreatedAt.Format(time.RFC3339Nano)
	}
	return dto
}

func visualRequirementsToDTO(v task.VisualRequirements) transport.VisualRequirementsDTO {
	return transport.VisualRequirementsDTO{
		Required:   v.Required,
		Viewport:   v.Viewport,
		Theme:      v.Theme,
		Locale:     v.Locale,
		Density:    v.Density,
		References: sliceOrNilEmpty(v.References),
	}
}

// sliceOrNilEmpty returns nil for an empty slice so the JSON encoder omits the
// field via omitempty. This keeps the wire form stable and compact.
func sliceOrNilEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

// compileResultDTO rebuilds the wire result from a saved spec + the original
// compile diagnostics. Confidence, Clarifications and reason lists come from
// the compile step (they are advisory and not persisted); the Specification
// comes from storage so the returned Version/Locked/CreatedAt are the durable
// values.
func compileResultDTO(spec task.Specification, res task.CompileResult, created bool) transport.CompileSpecResultDTO {
	out := transport.CompileSpecResultDTO{
		Specification:      specificationToDTO(spec),
		Confidence:         string(res.Confidence),
		UncertaintyReasons: sliceOrNilEmpty(res.UncertaintyReasons),
		RiskReasons:        sliceOrNilEmpty(res.RiskReasons),
		ComplexityReasons:  sliceOrNilEmpty(res.ComplexityReasons),
		Created:            created,
	}
	out.Clarifications = make([]transport.ClarificationDTO, len(res.Clarifications))
	for i, c := range res.Clarifications {
		out.Clarifications[i] = transport.ClarificationDTO{
			Question: c.Question,
			Reason:   c.Reason,
			Options:  sliceOrNilEmpty(c.Options),
		}
	}
	return out
}

// specificationsSemanticallyEqual reports whether two specifications carry the
// same compiled content, ignoring Version, lock state, timestamps and
// provenance. It is the heart of the compile-and-save idempotency rule: the
// compiler is deterministic, so two compiles of the same task produce
// semantically equal specifications, and the second call must NOT mint a new
// version.
func specificationsSemanticallyEqual(a, b task.Specification) bool {
	if a.Objective != b.Objective {
		return false
	}
	if a.Risk != b.Risk || a.Complexity != b.Complexity {
		return false
	}
	if !stringSliceEqual(a.NonGoals, b.NonGoals) ||
		!stringSliceEqual(a.Assumptions, b.Assumptions) ||
		!stringSliceEqual(a.Constraints, b.Constraints) ||
		!stringSliceEqual(a.ProposedScope, b.ProposedScope) {
		return false
	}
	if !visualRequirementsEqual(a.VisualRequirements, b.VisualRequirements) {
		return false
	}
	if len(a.AcceptanceCriteria) != len(b.AcceptanceCriteria) {
		return false
	}
	for i := range a.AcceptanceCriteria {
		if a.AcceptanceCriteria[i] != b.AcceptanceCriteria[i] {
			return false
		}
	}
	return true
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func visualRequirementsEqual(a, b task.VisualRequirements) bool {
	if a.Required != b.Required ||
		a.Viewport != b.Viewport ||
		a.Theme != b.Theme ||
		a.Locale != b.Locale ||
		a.Density != b.Density {
		return false
	}
	return stringSliceEqual(a.References, b.References)
}
